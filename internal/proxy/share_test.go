package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// tsServer streams numbered MPEG-TS packets without a length, like a live provider. dropAfter > 0 makes the first
// connection end after that many packets (the server dropping the stream).
type tsServer struct {
	*httptest.Server
	conns, active atomic.Int32
}

func newTSServer(t *testing.T, every time.Duration, dropAfter int) *tsServer {
	t.Helper()
	s := &tsServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := s.conns.Add(1)
		s.active.Add(1)
		defer s.active.Add(-1)
		w.Header().Set("Content-Type", "video/mp2t")
		rc := http.NewResponseController(w)
		pkt := make([]byte, tsPktSize)
		for i := 0; ; i++ {
			if dropAfter > 0 && n == 1 && i >= dropAfter {
				return
			}
			for j := range pkt {
				pkt[j] = byte(i)
			}
			pkt[0], pkt[1], pkt[2] = tsSyncByte, 0x1f, 0xff // a null packet
			if _, err := w.Write(pkt); err != nil {
				return
			}
			rc.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(every):
			}
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func newSharingProxy(t *testing.T, apply map[string]any) *Proxy {
	t.Helper()
	set := testSettings(t)
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)
	base := map[string]any{"upstream_retry_delay_ms": 0.0, "shared_stream_linger_seconds": 0.0}
	for k, v := range apply {
		base[k] = v
	}
	if errs := set.Apply(base); errs != nil {
		t.Fatal(errs)
	}
	return px
}

func openLive(t *testing.T, px *Proxy, url string) (*Upstream, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/channel", nil).WithContext(ctx)
	up, err := px.OpenLive(ctx, req, Payload{U: url})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); up.Close() })
	return up, cancel
}

// readPackets reads at least n packets from a viewer, or fails after a while.
func readPackets(t *testing.T, up *Upstream, n int) []byte {
	t.Helper()
	var got []byte
	buf := make([]byte, 8<<10)
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < n*tsPktSize {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d packets arrived", len(got)/tsPktSize, n)
		}
		c, err := up.body.Read(buf)
		got = append(got, buf[:c]...)
		if err != nil {
			t.Fatalf("read after %d packets: %v", len(got)/tsPktSize, err)
		}
	}
	return got
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSharedStreamOneConnectionForManyViewers(t *testing.T) {
	srv := newTSServer(t, 2*time.Millisecond, 0)
	px := newSharingProxy(t, nil)
	a, cancelA := openLive(t, px, srv.URL+"/live")
	readPackets(t, a, 20)
	b, cancelB := openLive(t, px, srv.URL+"/live")
	got := readPackets(t, b, 20)
	if got[0] != tsSyncByte || got[tsPktSize] != tsSyncByte {
		t.Fatal("a joining viewer must start at a packet boundary")
	}
	if srv.conns.Load() != 1 {
		t.Fatalf("one connection to the server for two viewers, got %d", srv.conns.Load())
	}
	if st := px.Stats(); st.SharedStreams != 1 || st.SharedViewers != 2 || st.SharedJoins != 1 || st.CacheHitBytes < uint64(20*tsPktSize) {
		t.Fatalf("stats: %+v", st)
	}
	if a.resp.Header.Get("Content-Type") != "video/mp2t" || b.resp.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatal("viewers are told what the stream is")
	}

	// the first viewer leaves: the stream goes on for the other one
	cancelA()
	a.Close()
	readPackets(t, b, 30)
	if srv.conns.Load() != 1 || srv.active.Load() != 1 {
		t.Fatalf("the stream must outlive the viewer who opened it: conns=%d active=%d", srv.conns.Load(), srv.active.Load())
	}
	// the last viewer leaves: the connection to the server closes
	cancelB()
	b.Close()
	waitFor(t, "the server connection to close", func() bool { return srv.active.Load() == 0 })
	if st := px.Stats(); st.SharedStreams != 0 || st.SharedViewers != 0 {
		t.Fatalf("nothing shared any more: %+v", st)
	}
}

func TestSharedStreamLingersAfterTheLastViewer(t *testing.T) {
	srv := newTSServer(t, 2*time.Millisecond, 0)
	px := newSharingProxy(t, map[string]any{"shared_stream_linger_seconds": 2.0})
	a, cancelA := openLive(t, px, srv.URL+"/live")
	readPackets(t, a, 5)
	cancelA()
	a.Close()
	time.Sleep(50 * time.Millisecond)
	if srv.active.Load() != 1 {
		t.Fatal("the stream stays open for a while after the last viewer")
	}
	b, _ := openLive(t, px, srv.URL+"/live") // flips back to it
	readPackets(t, b, 10)
	if srv.conns.Load() != 1 {
		t.Fatalf("it found the stream still running: %d connections", srv.conns.Load())
	}
}

func TestSharedStreamOnlyMPEGTS(t *testing.T) {
	var conns atomic.Int32
	text := bytes.Repeat([]byte("this is not transport stream data. "), 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		rc := http.NewResponseController(w)
		w.Write(text[:1000])
		rc.Flush() // no length: chunked
		w.Write(text[1000:])
	}))
	t.Cleanup(srv.Close)
	px := newSharingProxy(t, nil)
	for i := 0; i < 2; i++ {
		up, _ := openLive(t, px, srv.URL+"/x")
		got, _ := io.ReadAll(up.body)
		if !bytes.Equal(got, text) {
			t.Fatalf("viewer %d: the bytes must arrive unchanged (%d of %d)", i, len(got), len(text))
		}
	}
	if conns.Load() != 2 || px.Stats().SharedStreams != 0 {
		t.Fatalf("another format is not shared: conns=%d %+v", conns.Load(), px.Stats())
	}
}

// A finished file served without a length looks like a live stream until it ends: it must reach the viewer once,
// and must not be fetched again and again.
func TestSharedStreamFiniteResponseEndsAndIsNotRepeated(t *testing.T) {
	var conns atomic.Int32
	file := make([]byte, 188*50)
	for i := 0; i < len(file); i += 188 {
		file[i] = tsSyncByte
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { conns.Add(1); w.Write(file) }))
	t.Cleanup(srv.Close)
	px := newSharingProxy(t, nil)
	for i := 0; i < 2; i++ {
		up, _ := openLive(t, px, srv.URL+"/file.ts")
		if got, _ := io.ReadAll(up.body); !bytes.Equal(got, file) {
			t.Fatalf("viewer %d got %d bytes", i, len(got))
		}
	}
	if conns.Load() != 2 {
		t.Fatalf("each viewer fetched the file once: %d connections", conns.Load())
	}
}

func TestSharedStreamReconnectsWhenTheServerDrops(t *testing.T) {
	old := minLiveRun
	minLiveRun = 10 * time.Millisecond // the fake stream runs for milliseconds, a real one for minutes
	t.Cleanup(func() { minLiveRun = old })
	srv := newTSServer(t, time.Millisecond, 20)
	px := newSharingProxy(t, nil)
	a, _ := openLive(t, px, srv.URL+"/live")
	readPackets(t, a, 60) // more than the first connection carried
	if srv.conns.Load() != 2 {
		t.Fatalf("the proxy connected again by itself: %d connections", srv.conns.Load())
	}
	// a connection that ended at once was a finished file, not a drop: nothing to connect again for
	minLiveRun = time.Hour
	srv3 := newTSServer(t, time.Millisecond, 20)
	c, _ := openLive(t, newSharingProxy(t, nil), srv3.URL+"/live")
	if got, _ := io.ReadAll(c.body); len(got) < 20*tsPktSize || srv3.conns.Load() != 1 {
		t.Fatalf("a short run is not repeated: %d bytes, %d connections", len(got), srv3.conns.Load())
	}
	minLiveRun = 10 * time.Millisecond
	// no reconnects: the viewers see the stream end
	srv2 := newTSServer(t, time.Millisecond, 20)
	px2 := newSharingProxy(t, map[string]any{"shared_stream_reconnects": 0.0})
	b, _ := openLive(t, px2, srv2.URL+"/live")
	got, _ := io.ReadAll(b.body)
	if len(got) < 20*tsPktSize || srv2.conns.Load() != 1 {
		t.Fatalf("with reconnects off the stream ends: %d bytes, %d connections", len(got), srv2.conns.Load())
	}
}

func TestSharedStreamSlowViewerIsDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		rc := http.NewResponseController(w)
		chunk := make([]byte, tsPktSize*512)
		for i := 0; i < len(chunk); i += tsPktSize {
			chunk[i] = tsSyncByte
		}
		for i := 0; i < 40; i++ { // about 4 MB, far more than a 1 MB buffer
			if _, err := w.Write(chunk); err != nil {
				return
			}
			rc.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	px := newSharingProxy(t, map[string]any{"shared_stream_buffer_mb": 1.0})
	slow, _ := openLive(t, px, srv.URL+"/live")
	buf := make([]byte, 1024)
	if _, err := slow.body.Read(buf); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // the server has written 4 MB meanwhile
	var err error
	for i := 0; i < 5000 && err == nil; i++ {
		_, err = slow.body.Read(buf)
	}
	if err != errLagged {
		t.Fatalf("a viewer that fell behind the buffer is cut off: %v", err)
	}
}

func TestSharedStreamsFollowTheSetting(t *testing.T) {
	srv := newTSServer(t, 2*time.Millisecond, 0)
	px := newSharingProxy(t, map[string]any{"shared_streams": "off"})
	a, _ := openLive(t, px, srv.URL+"/live")
	b, _ := openLive(t, px, srv.URL+"/live")
	readPackets(t, a, 3)
	readPackets(t, b, 3)
	if srv.conns.Load() != 2 {
		t.Fatalf("off: every viewer has a connection, got %d", srv.conns.Load())
	}
	if errs := px.set.Apply(map[string]any{"shared_streams": "on"}); errs != nil {
		t.Fatal(errs)
	}
	c, _ := openLive(t, px, srv.URL+"/live")
	d, _ := openLive(t, px, srv.URL+"/live")
	readPackets(t, c, 3)
	readPackets(t, d, 3)
	if srv.conns.Load() != 3 {
		t.Fatalf("on, without a restart: two more viewers share one connection, got %d in all", srv.conns.Load())
	}
}

func TestSharedStreamNeverForRangesOrPlaylists(t *testing.T) {
	px := newSharingProxy(t, nil)
	get := func(method, path, rng string) bool {
		req := httptest.NewRequest(method, "/c", nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		return px.shareable(req, Payload{U: "http://host" + path})
	}
	if !get("GET", "/live/1.ts", "") || !get("GET", "/live/123", "") {
		t.Fatal("a plain GET of a stream is shareable")
	}
	if get("GET", "/live.m3u8", "") || get("GET", "/m.mpd", "") || get("GET", "/a.ts", "bytes=0-") || get("HEAD", "/a.ts", "") {
		t.Fatal("playlists, ranges and HEAD are never shared")
	}
}
