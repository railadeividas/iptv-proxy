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
		t.Fatalf("one retry by default: %d %q hits=%d", code, body, hits.Load())
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
	set.Apply(map[string]any{"upstream_retries": 0.0})
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
		w.Write([]byte("data-" + r.URL.Path))
	}))
	defer up.Close()
	base = up.URL
	set := testSettings(t)
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)
	pl := Payload{U: base + "/live.m3u8"}

	signedGet(t, px, pl)
	time.Sleep(100 * time.Millisecond)
	if segHits.Load() != 0 {
		t.Fatal("prefetch is off by default")
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
	if code, body := signedGet(t, px, Payload{U: base + "/s3.ts"}); code != 200 || body != "data-/s3.ts" || segHits.Load() != n {
		t.Fatalf("player must be served from the cache: %d %q", code, body)
	}
}
