package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The numbers behind the metrics: cache hits and misses, what the cache holds, and requests to streams by result.
func TestStats(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			http.Error(w, "no", 404)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte("0123456789"))
	}))
	defer up.Close()
	set := testSettings(t)
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)

	if s := px.Stats(); s.Limit != 20 || s.CacheLimit != 1024<<20 || s.InUse != 0 {
		t.Fatalf("start: %+v", s)
	}
	k := cacheKey(Payload{U: up.URL + "/a.ts"})
	if _, ok := px.cache.get(k); ok {
		t.Fatal("empty cache")
	}
	px.cache.put(k, cached{status: 200, data: []byte("0123456789")})
	if _, ok := px.cache.get(k); !ok {
		t.Fatal("cached")
	}
	s := px.Stats()
	if s.CacheHits != 1 || s.CacheMisses != 1 || s.CacheEntries != 1 || s.CacheBytes != 10 || s.CacheHitBytes != 10 || s.CacheStores != 1 || s.CacheRejected != 0 {
		t.Fatalf("cache: %+v", s)
	}

	r := httptest.NewRequest("GET", "/", nil)
	if _, err := px.Open(context.Background(), r, Payload{U: up.URL + "/a.ts"}); err != nil {
		t.Fatal(err)
	}
	if _, err := px.Open(context.Background(), r, Payload{U: up.URL + "/bad"}); err == nil {
		t.Fatal("a 404 fails")
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	px.Open(gone, r, Payload{U: up.URL + "/a.ts"}) // the player left: not the stream's fault
	if s := px.Stats(); s.UpstreamOK != 1 || s.UpstreamErrors != 1 {
		t.Fatalf("upstream: %+v", s)
	}

	set.Apply(map[string]any{"max_connections": 5.0})
	if px.Stats().Limit != 5 {
		t.Fatal("the limit follows the setting")
	}
}
