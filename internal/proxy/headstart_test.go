package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// liveHLS is a live provider: a new segment every `every`, a playlist of the newest `window`, and for a segment that has left
// the window a 200 "not found" served as video (what a real provider does).
type liveHLS struct {
	*httptest.Server
	mu          sync.Mutex
	next        int64 // the sequence number of the segment published next
	window      int
	paused      bool
	failList    bool
	junkAlways  bool // every segment is "not found"
	playlistHit atomic.Int32
	segHits     atomic.Int32
	stop        chan struct{}
}

func segBytes(n int64) []byte {
	b := make([]byte, 188*40)
	for i := 0; i < len(b); i += 188 {
		b[i], b[i+1], b[i+2] = tsSyncByte, 0x1f, 0xff
		for j := 3; j < 188; j++ {
			b[i+j] = byte(n)
		}
	}
	return b
}

func newLiveHLS(t *testing.T, every time.Duration, window int) *liveHLS {
	t.Helper()
	l := &liveHLS{next: 100, window: window, stop: make(chan struct{})}
	l.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			l.playlistHit.Add(1)
			if l.failList {
				http.Error(w, "down", 503)
				return
			}
			var b strings.Builder
			fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:0.2\n#EXT-X-MEDIA-SEQUENCE:%d\n", l.next-int64(l.window))
			for n := l.next - int64(l.window); n < l.next; n++ {
				fmt.Fprintf(&b, "#EXTINF:0.400,\nseg%d.ts\n", n)
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Write([]byte(b.String()))
			return
		}
		var n int64
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/live/seg"), "%d.ts", &n)
		l.segHits.Add(1)
		w.Header().Set("Content-Type", "video/MP2T")
		if l.junkAlways || n < l.next-int64(l.window) || n >= l.next {
			w.Write([]byte("not found"))
			return
		}
		w.Write(segBytes(n))
	}))
	go func() {
		tk := time.NewTicker(every)
		defer tk.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-tk.C:
				l.mu.Lock()
				if !l.paused {
					l.next++
				}
				l.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() { close(l.stop); l.Close() })
	return l
}

func (l *liveHLS) newest() int64 { l.mu.Lock(); defer l.mu.Unlock(); return l.next - 1 }
func (l *liveHLS) set(f func())  { l.mu.Lock(); f(); l.mu.Unlock() }

func newHSProxy(t *testing.T, delay float64, extra map[string]any) *Proxy {
	t.Helper()
	old := liveMinPoll
	liveMinPoll = 40 * time.Millisecond
	t.Cleanup(func() { liveMinPoll = old })
	apply := map[string]any{"hls_head_start": "on", "hls_head_start_seconds": delay, "upstream_retry_delay_ms": 0.0, "playlist_cache_seconds": 0.0}
	if delay == 0 {
		apply = map[string]any{"hls_head_start": "off", "upstream_retry_delay_ms": 0.0, "playlist_cache_seconds": 0.0}
	}
	for k, v := range extra {
		apply[k] = v
	}
	px := newSharingProxy(t, apply)
	t.Cleanup(func() { // trackers must be gone before liveMinPoll is put back (they read it)
		px.set.Apply(map[string]any{"hls_head_start": "off"})
		for i := 0; i < 300 && px.Stats().HeadStartStreams != 0; i++ {
			time.Sleep(10 * time.Millisecond)
		}
	})
	return px
}

var linkRe = regexp.MustCompile(`(?m)^http://proxy(/x/[^\s]+)$`)

// listing is what a player sees in a playlist: the media sequence, the segment links.
func listing(t *testing.T, body string) (seq int64, links []string) {
	t.Helper()
	m := regexp.MustCompile(`#EXT-X-MEDIA-SEQUENCE:(\d+)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no media sequence in %q", body)
	}
	seq, _ = strconv.ParseInt(m[1], 10, 64)
	for _, l := range linkRe.FindAllStringSubmatch(body, -1) {
		links = append(links, l[1])
	}
	return seq, links
}

func getLink(px *Proxy, path string) (int, []byte) {
	rec := httptest.NewRecorder()
	px.ServeSigned(rec, httptest.NewRequest("GET", path, nil), "/x/", "http://proxy")
	b, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, b
}

func playlistOf(t *testing.T, px *Proxy, l *liveHLS, viewer int64) (int, string) {
	t.Helper()
	return signedGet(t, px, Payload{U: l.URL + "/live/index.m3u8", P: viewer, C: "News"})
}

// segSeq is which segment a link is for.
func segSeq(t *testing.T, px *Proxy, link string) int64 {
	t.Helper()
	tok := strings.Split(strings.TrimPrefix(link, "/x/"), "/")[0]
	pl, err := px.signer.Open(tok)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	fmt.Sscanf(pl.U[strings.LastIndex(pl.U, "/seg")+4:], "%d.ts", &n)
	return n
}

func TestHeadStartServesPlaylistBehindLive(t *testing.T) {
	l := newLiveHLS(t, 100*time.Millisecond, 4)
	px := newHSProxy(t, 1, nil) // one second behind: about ten segments
	code, body := playlistOf(t, px, l, 7)
	if code != 200 {
		t.Fatalf("first playlist: %d %s", code, body)
	}
	seq0, links := listing(t, body)
	if len(links) < 3 || len(links) > hsMaxWindow {
		t.Fatalf("a player needs a few segments to start with: %d", len(links))
	}
	time.Sleep(2500 * time.Millisecond)
	_, body = playlistOf(t, px, l, 8)
	seq1, links := listing(t, body)
	newest := segSeq(t, px, links[len(links)-1])
	if behind := l.newest() - newest; behind < 8 || behind > 14 {
		t.Fatalf("the newest listed segment is %d behind the server's, want about 10 (a second)", behind)
	}
	if seq1 < seq0 {
		t.Fatalf("the media sequence never goes back: %d then %d", seq0, seq1)
	}
	// every listed segment is served, also those the server has dropped from its window long ago
	dropped := 0
	for _, link := range links {
		code, b := getLink(px, link)
		n := segSeq(t, px, link)
		if code != 200 || len(b) != 188*40 || b[3] != byte(n) {
			t.Fatalf("segment %d: %d, %d bytes", n, code, len(b))
		}
		if n < l.newest()-int64(l.window)+1 {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("the head start must serve segments the server no longer has")
	}
	if st := px.Stats(); st.HeadStartStreams != 1 || st.HeadStartPlaylists < 1 || st.CacheHits < int64Len(links) {
		t.Fatalf("stats: %+v", st)
	}
}

func int64Len(l []string) uint64 { return uint64(len(l)) }

func TestHeadStartLinksBelongToTheViewer(t *testing.T) {
	l := newLiveHLS(t, 100*time.Millisecond, 4)
	px := newHSProxy(t, 1, nil)
	_, a := playlistOf(t, px, l, 11)
	_, b := playlistOf(t, px, l, 12)
	for _, c := range []struct {
		body string
		want int64
	}{{a, 11}, {b, 12}} {
		l := linkRe.FindStringSubmatch(c.body)
		pl, err := px.signer.Open(strings.Split(strings.TrimPrefix(l[1], "/x/"), "/")[0])
		if err != nil || pl.P != c.want {
			t.Fatalf("a shared head start still rewrites for each viewer: %+v %v", pl, err)
		}
	}
}

func TestHeadStartBridgesAStallOfTheServer(t *testing.T) {
	l := newLiveHLS(t, 100*time.Millisecond, 4)
	px := newHSProxy(t, 2, nil) // two seconds of head start
	playlistOf(t, px, l, 1)
	time.Sleep(2500 * time.Millisecond)
	_, body := playlistOf(t, px, l, 1)
	_, links := listing(t, body)
	before := segSeq(t, px, links[len(links)-1])

	// the server stops publishing and refuses the playlist for a second: inside the head start
	l.set(func() { l.paused, l.failList = true, true })
	deadline := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(deadline) {
		if code, _ := playlistOf(t, px, l, 1); code != 200 {
			t.Fatalf("a player is never told about the server's trouble: %d", code)
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, body = playlistOf(t, px, l, 1)
	_, links = listing(t, body)
	during := segSeq(t, px, links[len(links)-1])
	if during < before+4 {
		t.Fatalf("the playlist kept moving during the stall: %d -> %d", before, during)
	}
	for _, link := range links { // and the segments are all there
		if code, b := getLink(px, link); code != 200 || len(b) != 188*40 {
			t.Fatalf("segment during the stall: %d %d", code, len(b))
		}
	}
	l.set(func() { l.paused, l.failList = false, false })
	time.Sleep(700 * time.Millisecond)
	_, body = playlistOf(t, px, l, 1)
	_, links = listing(t, body)
	if after := segSeq(t, px, links[len(links)-1]); after <= during {
		t.Fatalf("it goes on when the server is back: %d -> %d", during, after)
	}
}

func TestHeadStartFollowsTheSettingAndIsOffAtZero(t *testing.T) {
	l := newLiveHLS(t, 100*time.Millisecond, 4)
	px := newHSProxy(t, 0, nil)
	_, body := playlistOf(t, px, l, 1)
	_, links := listing(t, body)
	if behind := l.newest() - segSeq(t, px, links[len(links)-1]); behind > 2 {
		t.Fatalf("off: the player gets the server's own playlist (%d behind)", behind)
	}
	if px.Stats().HeadStartStreams != 0 {
		t.Fatal("off: nothing is followed")
	}
	if errs := px.set.Apply(map[string]any{"hls_head_start": "on", "hls_head_start_seconds": 1.0}); errs != nil {
		t.Fatal(errs)
	}
	time.Sleep(300 * time.Millisecond)
	playlistOf(t, px, l, 1)
	time.Sleep(1800 * time.Millisecond)
	_, body = playlistOf(t, px, l, 1)
	_, links = listing(t, body)
	if behind := l.newest() - segSeq(t, px, links[len(links)-1]); behind < 7 || px.Stats().HeadStartStreams != 1 {
		t.Fatalf("on, without a restart: %d behind, %+v", behind, px.Stats())
	}
	if errs := px.set.Apply(map[string]any{"hls_head_start": "off"}); errs != nil {
		t.Fatal(errs)
	}
	_, body = playlistOf(t, px, l, 1)
	_, links = listing(t, body)
	if behind := l.newest() - segSeq(t, px, links[len(links)-1]); behind > 2 {
		t.Fatalf("off again at once: %d behind", behind)
	}
	waitFor(t, "the tracker to stop following", func() bool { return px.Stats().HeadStartStreams == 0 })
}

func TestHeadStartLeavesOtherPlaylistsAlone(t *testing.T) {
	for name, text := range map[string]string{
		"vod":         "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\ns1.ts\n#EXT-X-ENDLIST\n",
		"byteranges":  "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\n#EXT-X-BYTERANGE:100@0\nbig.ts\n",
		"low latency": "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES\n#EXTINF:4,\ns1.ts\n",
		"master":      "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nlow.m3u8\n",
		"no segments": "#EXTM3U\n#EXT-X-TARGETDURATION:4\n",
	} {
		if _, ok := parseMedia(text); ok {
			t.Errorf("%s must be left alone", name)
		}
	}
	vod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\ns1.ts\n#EXT-X-ENDLIST\n"))
	}))
	t.Cleanup(vod.Close)
	px := newHSProxy(t, 5, nil)
	start := time.Now()
	code, body := signedGet(t, px, Payload{U: vod.URL + "/movie.m3u8"})
	if code != 200 || !strings.Contains(body, "#EXT-X-ENDLIST") || time.Since(start) > time.Second || px.Stats().HeadStartStreams != 0 {
		t.Fatalf("a playlist that ends is served as it is, at once: %d %q %v", code, body, time.Since(start))
	}
}

func TestHeadStartNeverKeepsOrListsJunk(t *testing.T) {
	if plausibleSegment("/a.ts", "video/MP2T", []byte("not found")) || plausibleSegment("/a.ts", "text/html", make([]byte, 5000)) || plausibleSegment("/a.m4s", "video/mp4", nil) {
		t.Fatal("an error body is not a segment")
	}
	if !plausibleSegment("/a.ts", "video/MP2T", make([]byte, 188*3)) || !plausibleSegment("/a.m4s", "video/mp4", []byte("x")) {
		t.Fatal("encrypted and other segments are fine: nothing in them has to look like MPEG-TS")
	}
	l := newLiveHLS(t, 100*time.Millisecond, 4)
	l.set(func() { l.junkAlways = true })
	px := newHSProxy(t, 1, map[string]any{"upstream_response_timeout_seconds": 1.0})
	start := time.Now()
	code, body := playlistOf(t, px, l, 1)
	if code != 200 || !strings.Contains(body, "#EXT-X-MEDIA-SEQUENCE") || time.Since(start) > 3*time.Second {
		t.Fatalf("segments that cannot be kept: the player still gets the playlist: %d %v", code, time.Since(start))
	}
	again := time.Now()
	playlistOf(t, px, l, 1)
	if time.Since(again) > 500*time.Millisecond {
		t.Fatalf("and the next poll does not wait again: %v", time.Since(again))
	}
	if px.Stats().HeadStartStreams != 0 {
		t.Fatal("nothing is followed that cannot be kept")
	}
}

func TestHeadStartStopsFollowingWhenNobodyWatches(t *testing.T) {
	l := newLiveHLS(t, 100*time.Millisecond, 4)
	px := newHSProxy(t, 1, nil)
	playlistOf(t, px, l, 1)
	tr := px.hs.get(cacheKey(Payload{U: l.URL + "/live/index.m3u8"}))
	if tr == nil || px.Stats().HeadStartStreams != 1 {
		t.Fatalf("following: %+v", px.Stats())
	}
	hits := l.playlistHit.Load()
	time.Sleep(1200 * time.Millisecond)
	if l.playlistHit.Load() <= hits {
		t.Fatal("the tracker asks the server for the playlist itself")
	}
	tr.lastUsed.Store(time.Now().Add(-time.Hour).UnixNano()) // nobody asked for a long time
	waitFor(t, "the tracker to stop", func() bool { return px.Stats().HeadStartStreams == 0 })
}

func TestHeadStartKeepsKeysMapsAndDiscontinuities(t *testing.T) {
	px := newHSProxy(t, 0, nil)
	text := `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:50
#EXT-X-DISCONTINUITY-SEQUENCE:7
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-KEY:METHOD=AES-128,URI="k1"
#EXT-X-MAP:URI="init1.mp4"
#EXTINF:4,
a.m4s
#EXTINF:4,
b.m4s
#EXT-X-DISCONTINUITY
#EXT-X-KEY:METHOD=AES-128,URI="k2"
#EXT-X-MAP:URI="init2.mp4"
#EXTINF:4,
c.m4s
#EXT-X-DISCONTINUITY
#EXT-X-KEY:METHOD=NONE
#EXTINF:4,
d.m4s
`
	pp, ok := parseMedia(text)
	if !ok || len(pp.segs) != 4 || pp.header[0] != "#EXT-X-INDEPENDENT-SEGMENTS" {
		t.Fatalf("parse: %+v %v", pp, ok)
	}
	base := httptest.NewRequest("GET", "http://x/live/index.m3u8", nil).URL
	tr := &hsTracker{p: px, key: "k", pl: Payload{U: base.String()}, url: base}
	now := time.Now()
	if !tr.merge(pp, now) {
		t.Fatal("merge")
	}
	for _, s := range tr.segs {
		px.cache.put(s.ck, cached{status: 200, ct: "video/mp4", data: []byte("x")})
	}
	out, ok := tr.render(now.Add(time.Hour), 0)
	if !ok {
		t.Fatal("render")
	}
	for _, want := range []string{"#EXT-X-MEDIA-SEQUENCE:50", "#EXT-X-DISCONTINUITY-SEQUENCE:7", "#EXT-X-INDEPENDENT-SEGMENTS"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Count(out, `URI="k1"`) != 1 || strings.Count(out, `URI="k2"`) != 1 || strings.Count(out, "init1.mp4") != 1 || strings.Count(out, "init2.mp4") != 1 || strings.Count(out, "METHOD=NONE") != 1 {
		t.Fatalf("keys and maps appear where they change:\n%s", out)
	}
	if strings.Count(out, "#EXT-X-DISCONTINUITY\n") != 2 {
		t.Fatalf("discontinuities are kept:\n%s", out)
	}

	// a window that starts after the first discontinuity: the sequence numbers tell it, and the first segment needs no tag
	for i := 2; i < len(tr.segs); i++ {
		_ = i
	}
	tr.segs = tr.segs[2:]
	out, _ = tr.render(now.Add(time.Hour), 0)
	if !strings.Contains(out, "#EXT-X-MEDIA-SEQUENCE:52") || !strings.Contains(out, "#EXT-X-DISCONTINUITY-SEQUENCE:8") {
		t.Fatalf("window start:\n%s", out)
	}
	if strings.HasPrefix(strings.SplitN(out, "#EXTINF", 2)[1], "") && strings.Contains(strings.SplitN(out, "c.m4s", 2)[0], "#EXT-X-DISCONTINUITY\n") {
		t.Fatalf("the first segment of a playlist carries no discontinuity tag:\n%s", out)
	}
	if !strings.Contains(out, `URI="k2"`) || !strings.Contains(out, "init2.mp4") {
		t.Fatalf("the key and map in effect are stated for the first segment:\n%s", out)
	}
}

func TestHeadStartMergeFollowsTheServer(t *testing.T) {
	px := newHSProxy(t, 0, nil)
	base := httptest.NewRequest("GET", "http://x/live/index.m3u8", nil).URL
	tr := &hsTracker{p: px, key: "k", pl: Payload{U: base.String()}, url: base}
	list := func(from, n int) *hsParsed {
		var b strings.Builder
		fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:%d\n", from)
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "#EXTINF:2,\ns%d.ts\n", from+i)
		}
		pp, ok := parseMedia(b.String())
		if !ok {
			t.Fatal("parse")
		}
		return pp
	}
	seqs := func() []int64 {
		var o []int64
		for _, s := range tr.segs {
			o = append(o, s.seq)
		}
		return o
	}
	now := time.Now()
	tr.merge(list(10, 4), now)
	tr.merge(list(12, 4), now.Add(4*time.Second))
	if got := seqs(); len(got) != 6 || got[0] != 10 || got[5] != 15 {
		t.Fatalf("the window slides and the tracker keeps what it saw: %v", got)
	}
	if age := now.Add(4 * time.Second).Sub(tr.segs[5].first); age != 0 {
		t.Fatalf("a segment is first seen when it appears: %v", age)
	}
	if old := tr.segs[0].first; now.Sub(old) < 5*time.Second {
		t.Fatalf("the first window's segments are dated by the segments after them: %v ago", now.Sub(old))
	}
	tr.merge(list(500, 4), now.Add(6*time.Second)) // the server started numbering again
	if got := seqs(); len(got) != 4 || got[0] != 500 {
		t.Fatalf("a restart of the numbering starts over: %v", got)
	}
}

func TestRangeFromACachedSegment(t *testing.T) {
	for _, c := range []struct {
		h          string
		a, b, code int
	}{
		{"bytes=0-9", 0, 9, 206}, {"bytes=10-", 10, 99, 206}, {"bytes=-5", 95, 99, 206}, {"bytes=90-1000", 90, 99, 206},
		{"bytes=100-", 0, 0, 416}, {"bytes=-0", 0, 0, 416}, {"bytes=0-9,20-29", 0, 0, 0}, {"bytes=9-3", 0, 0, 0}, {"items=1-2", 0, 0, 0}, {"bytes=x-", 0, 0, 0},
	} {
		a, b, code := parseRange(c.h, 100)
		if code != c.code || (code == 206 && (a != c.a || b != c.b)) {
			t.Errorf("%s: %d-%d %d, want %d-%d %d", c.h, a, b, code, c.a, c.b, c.code)
		}
	}
	px := newHSProxy(t, 0, nil)
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(segBytes(5))
	}))
	t.Cleanup(up.Close)
	pl := Payload{U: up.URL + "/s.ts", E: time.Now().Add(time.Hour).Unix()}
	path := "/x/" + px.signer.Seal(pl) + "/s.ts"
	px.cache.put(cacheKey(pl), cached{status: 200, ct: "video/mp2t", data: segBytes(5)})
	do := func(rng string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Range", rng)
		px.ServeSigned(rec, req, "/x/", "http://proxy")
		return rec
	}
	whole := segBytes(5)
	rec := do("bytes=100-199")
	if rec.Code != 206 || rec.Header().Get("Content-Range") != fmt.Sprintf("bytes 100-199/%d", len(whole)) || string(rec.Body.Bytes()) != string(whole[100:200]) || hits.Load() != 0 {
		t.Fatalf("a range of a cached segment: %d %q %d server requests", rec.Code, rec.Header().Get("Content-Range"), hits.Load())
	}
	if rec := do(fmt.Sprintf("bytes=%d-", len(whole)+5)); rec.Code != 416 || hits.Load() != 0 {
		t.Fatalf("a range beyond the end: %d", rec.Code)
	}
	do("bytes=0-1,5-6") // several ranges: the server's business
	if hits.Load() != 1 {
		t.Fatalf("what is not ours to answer goes to the server: %d requests", hits.Load())
	}
}

func TestHeadStartKeepTimeFollowsTheSetting(t *testing.T) {
	px := newHSProxy(t, 10, nil)
	tr := &hsTracker{p: px, target: 5}
	base := tr.keepLocked()
	px.set.Apply(map[string]any{"hls_head_start_keep_extra_seconds": 90.0})
	if got := tr.keepLocked() - base; got != 60*time.Second {
		t.Fatalf("the extra time is a setting: %v", got)
	}
}

// The cache features are independent: each has its own switch, and only the master switch turns them all off.
func TestCacheFeaturesAreIndependent(t *testing.T) {
	px := newHSProxy(t, 5, nil)
	c := px.cache
	put := func(k string) bool { c.put(k, cached{data: []byte("abc")}); _, ok := c.peek(k); return ok }
	if !put("a") || !px.prefetchOn() || !px.headStartOn() {
		t.Fatal("all on by default")
	}
	px.set.Apply(map[string]any{"segment_share": "off"})
	if put("b") || !px.prefetchOn() || !px.headStartOn() || !c.putTTL("c", cached{data: []byte("abc")}, time.Minute) {
		t.Fatal("sharing off: only what players ask for is not kept")
	}
	px.set.Apply(map[string]any{"segment_share": "on", "segment_prefetch": "off"})
	if !put("d") || px.prefetchOn() || !px.headStartOn() {
		t.Fatal("prefetch off: the others go on")
	}
	px.set.Apply(map[string]any{"segment_prefetch": "on", "hls_head_start": "off"})
	if !put("e") || !px.prefetchOn() || px.headStartOn() {
		t.Fatal("head start off: the others go on")
	}
	px.set.Apply(map[string]any{"hls_head_start": "on", "segment_cache": "off"})
	if put("f") || px.prefetchOn() || px.headStartOn() || c.putTTL("g", cached{data: []byte("abc")}, time.Minute) || px.Stats().CacheLimit != 0 {
		t.Fatal("the master switch turns them all off")
	}
}
