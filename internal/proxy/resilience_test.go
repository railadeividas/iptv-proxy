package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func signedGet(t *testing.T, px *Proxy, pl Payload) (int, string) {
	t.Helper()
	pl.E = time.Now().Add(time.Hour).Unix()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x/"+px.signer.Encode(pl)+"/f", nil)
	px.ServeSigned(rec, req, "/x/", "http://proxy")
	b, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(b)
}

func TestRetriesFollowTheSetting(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "boom", 503)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte("seg"))
	}))
	defer up.Close()
	set := testSettings(t)
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)
	set.Apply(map[string]any{"upstream_retry_delay_ms": 0.0})
	if code, body := signedGet(t, px, Payload{U: up.URL + "/a.ts"}); code != 200 || body != "seg" || hits.Load() != 2 {
		t.Fatalf("retries on by default: %d %q hits=%d", code, body, hits.Load())
	}
	if px.Stats().Retries != 1 {
		t.Fatalf("the retry is counted: %+v", px.Stats())
	}
	hits.Store(0)
	set.Apply(map[string]any{"upstream_retries": 0.0})
	if code, _ := signedGet(t, px, Payload{U: up.URL + "/b.ts"}); code != 503 || hits.Load() != 1 {
		t.Fatalf("retries off: %d hits=%d", code, hits.Load())
	}
	// a 404 is final
	hits.Store(0)
	set.Apply(map[string]any{"upstream_retries": 3.0})
	nf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.NotFound(w, r) }))
	defer nf.Close()
	if code, _ := signedGet(t, px, Payload{U: nf.URL + "/c.ts"}); code != 404 || hits.Load() != 1 {
		t.Fatalf("404 must not be retried: %d hits=%d", code, hits.Load())
	}
}

func TestPlaylistHoldover(t *testing.T) {
	var down atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "boom", 502)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte("#EXTM3U\n#EXTINF:6,\ns1.ts\n"))
	}))
	defer up.Close()
	set := testSettings(t)
	// the playlist cache would hand out the second playlist itself; this test is about the server being down
	set.Apply(map[string]any{"upstream_retries": 0.0, "playlist_cache_seconds": 0.0})
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)
	pl := Payload{U: up.URL + "/live.m3u8"}
	if code, _ := signedGet(t, px, pl); code != 200 {
		t.Fatal(code)
	}
	down.Store(true)
	code, body := signedGet(t, px, pl)
	if code != 200 || !strings.Contains(body, "/x/") {
		t.Fatalf("held copy expected: %d %q", code, body)
	}
	if px.Stats().Holdovers != 1 {
		t.Fatalf("the held copy is counted: %+v", px.Stats())
	}
	// serving the held copy must not renew it, and the setting applies live
	set.Apply(map[string]any{"playlist_holdover_seconds": 0.0})
	if code, _ := signedGet(t, px, pl); code != 502 {
		t.Fatalf("holdover off: %d", code)
	}
	set.Apply(map[string]any{"playlist_holdover_seconds": 1.0})
	px.cache.mu.Lock()
	h := px.cache.held[cacheKey(pl)]
	h.at = time.Now().Add(-5 * time.Second)
	px.cache.held[cacheKey(pl)] = h
	px.cache.mu.Unlock()
	if code, _ := signedGet(t, px, pl); code != 502 {
		t.Fatalf("expired copy must not be served: %d", code)
	}
}

func TestPrefetchFillsTheCache(t *testing.T) {
	var segHits atomic.Int32
	var base string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Write([]byte("#EXTM3U\n#EXTINF:6,\ns1.ts\n#EXTINF:6,\ns2.ts\n#EXTINF:6,\ns3.ts\n"))
			return
		}
		segHits.Add(1)
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte(strings.Repeat("data-"+r.URL.Path, 30))) // a segment is more than a few bytes
	}))
	defer up.Close()
	base = up.URL
	set := testSettings(t)
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)
	pl := Payload{U: base + "/live.m3u8"}

	set.Apply(map[string]any{"segment_prefetch_count": 0.0, "playlist_cache_seconds": 0.0}) // these requests must each reach the server
	signedGet(t, px, pl)
	time.Sleep(100 * time.Millisecond)
	if segHits.Load() != 0 {
		t.Fatal("0 turns prefetch off")
	}
	set.Apply(map[string]any{"segment_prefetch_count": 2.0})
	signedGet(t, px, pl)
	for i := 0; i < 200 && !(px.cache.has(cacheKey(Payload{U: base + "/s2.ts"})) && px.cache.has(cacheKey(Payload{U: base + "/s3.ts"}))); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !px.cache.has(cacheKey(Payload{U: base + "/s2.ts"})) || !px.cache.has(cacheKey(Payload{U: base + "/s3.ts"})) || px.cache.has(cacheKey(Payload{U: base + "/s1.ts"})) {
		t.Fatal("the newest two segments must be cached, and only those")
	}
	n := segHits.Load()
	if code, body := signedGet(t, px, Payload{U: base + "/s3.ts"}); code != 200 || body != strings.Repeat("data-/s3.ts", 30) || segHits.Load() != n {
		t.Fatalf("player must be served from the cache: %d %q", code, body)
	}
	for i := 0; i < 200 && px.Stats().Prefetched < 2; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if st := px.Stats(); st.Prefetched != 2 || st.CacheHitBytes != uint64(len(strings.Repeat("data-/s3.ts", 30))) {
		t.Fatalf("prefetch and cache bytes are counted: %+v", st)
	}
}

func TestPrefetchRespectsThePerServerLimit(t *testing.T) {
	hold := make(chan struct{})
	var segHits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Write([]byte("#EXTM3U\n#EXTINF:6,\ns1.ts\n#EXTINF:6,\ns2.ts\n"))
		case r.URL.Path == "/long":
			w.Header().Set("Content-Type", "video/mp2t")
			w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			<-hold
		default:
			segHits.Add(1)
			w.Header().Set("Content-Type", "video/mp2t")
			w.Write([]byte(strings.Repeat("seg", 100)))
		}
	}))
	defer up.Close()
	defer close(hold)
	set := testSettings(t)
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)
	set.Apply(map[string]any{"segment_prefetch_count": 2.0, "upstream_per_host_requests": 1.0, "playlist_cache_seconds": 0.0})

	// a player's stream holds the only request the server is allowed
	long, err := px.Open(t.Context(), nil, Payload{U: up.URL + "/long"})
	if err != nil {
		t.Fatal(err)
	}
	// players are never refused, even at the limit, and they trigger a prefetch that must yield
	if code, _ := signedGet(t, px, Payload{U: up.URL + "/live.m3u8"}); code != 200 {
		t.Fatalf("player refused: %d", code)
	}
	time.Sleep(150 * time.Millisecond)
	if segHits.Load() != 0 {
		t.Fatal("prefetch must not start while the server is at its limit")
	}
	long.Close() // the slot frees up and the limit applies live
	signedGet(t, px, Payload{U: up.URL + "/live.m3u8"})
	for i := 0; i < 200 && segHits.Load() < 1; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if segHits.Load() == 0 {
		t.Fatal("prefetch must run once the server is below the limit")
	}
	time.Sleep(100 * time.Millisecond)
	px.hosts.mu.Lock()
	leaked := len(px.hosts.n)
	px.hosts.mu.Unlock()
	if leaked != 0 {
		t.Fatal("request counts leaked")
	}
}

func TestPrefetchCountsAsUpstreamRequests(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Write([]byte("#EXTM3U\n#EXTINF:6,\ns1.ts\n#EXTINF:6,\ns2.ts\n"))
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte("data"))
	}))
	defer up.Close()
	set := testSettings(t)
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)
	set.Apply(map[string]any{"segment_prefetch_count": 2.0})
	signedGet(t, px, Payload{U: up.URL + "/live.m3u8"})
	for i := 0; i < 200 && px.Stats().UpstreamOK < 3; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if s := px.Stats(); s.UpstreamOK != 3 || s.UpstreamErrors != 0 { // the playlist and the two segments
		t.Fatalf("upstream requests: %+v", s)
	}
}
