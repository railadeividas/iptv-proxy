package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
)

func testSettings(t *testing.T) *settings.Settings {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := settings.New(st)
	// the tests of other features run without the head start (it has its own tests): it holds live HLS playlists back
	set.Apply(map[string]any{"hls_head_start": "off"})
	return set
}

func TestConnectionLimitFollowsTheSetting(t *testing.T) {
	set := testSettings(t)
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)
	if errs := set.Apply(map[string]any{"max_connections": 2.0}); errs != nil {
		t.Fatal(errs)
	}
	r1, ok1 := px.Acquire()
	r2, ok2 := px.Acquire()
	if _, ok3 := px.Acquire(); !ok1 || !ok2 || ok3 {
		t.Fatalf("limit 2: %v %v %v", ok1, ok2, ok3)
	}
	set.Apply(map[string]any{"max_connections": 3.0}) // raised while streams are running
	r3, ok := px.Acquire()
	if !ok {
		t.Fatal("a raised limit must apply immediately")
	}
	r1()
	r2()
	r3()
	if px.inUse.Load() != 0 {
		t.Fatal("slots not released")
	}
}

func TestCompatUserAgentsComeFromSettings(t *testing.T) {
	set := testSettings(t)
	if CompatUA(set, "vlc") != set.String("ua_vlc") || CompatUA(set, "") != "" || CompatUA(set, "nope") != "" {
		t.Fatal("presets")
	}
	set.Apply(map[string]any{"ua_exoplayer": "MyExo/1"})
	if CompatUA(set, "exoplayer") != "MyExo/1" {
		t.Fatal("edited preset must apply")
	}
}

func TestSegmentCacheCanBeSwitchedOff(t *testing.T) {
	set := testSettings(t)
	c := newSegCache(set)
	c.put("a", cached{data: []byte("12345")})
	if _, ok := c.get("a"); !ok {
		t.Fatal("cache on by default")
	}
	set.Apply(map[string]any{"segment_cache": "off"})
	c.put("b", cached{data: []byte("12345")})
	if _, ok := c.get("b"); ok {
		t.Fatal("size 0 turns the cache off")
	}
}

func TestManifestLimitAndDefaultUserAgentAreLive(t *testing.T) {
	set := testSettings(t)
	var gotUA string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte("#EXTM3U\n" + strings.Repeat("#EXT-X-COMMENT:xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n", 40000)))
	}))
	defer up.Close()
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)

	if _, err := px.Open(t.Context(), nil, Payload{U: up.URL + "/a.m3u8"}); err != nil {
		t.Fatalf("2.5 MB manifest under the 16 MB default: %v", err)
	}
	if gotUA != set.String("proxy_default_user_agent") {
		t.Fatalf("default UA: %q", gotUA)
	}
	set.Apply(map[string]any{"manifest_max_mb": 1.0, "proxy_default_user_agent": "Custom/9"})
	_, err := px.Open(t.Context(), nil, Payload{U: up.URL + "/a.m3u8"})
	if err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("expected the new limit to apply, got %v", err)
	}
	if gotUA != "Custom/9" {
		t.Fatalf("edited default UA: %q", gotUA)
	}
}

// New links are encrypted by default; switching the setting changes the next link without a restart, and both kinds open.
func TestLinkEncryptionFollowsTheSetting(t *testing.T) {
	set := testSettings(t)
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".ts") {
			w.Header().Set("Content-Type", "video/mp2t")
			w.Write([]byte("seg"))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:5\n#EXTINF:5,\nsecretseg.ts\n"))
	}))
	defer up.Close()
	playlist := func() string {
		tok := px.signer.Seal(Payload{U: up.URL + "/live.m3u8"})
		rec := httptest.NewRecorder()
		px.ServeSigned(rec, httptest.NewRequest("GET", "/x/"+tok+"/live.m3u8", nil), "/x/", "http://p")
		for _, l := range strings.Split(rec.Body.String(), "\n") {
			if strings.HasPrefix(l, "http://p/x/") {
				return strings.Split(strings.TrimPrefix(l, "http://p/x/"), "/")[0]
			}
		}
		t.Fatalf("no link in %q", rec.Body.String())
		return ""
	}
	on := playlist()
	if strings.Contains(on, ".") {
		t.Fatalf("default must be an encrypted link: %s", on)
	}
	if pl, err := px.signer.Open(on); err != nil || !strings.HasSuffix(pl.U, "/secretseg.ts") {
		t.Fatalf("open: %v %+v", err, pl)
	}
	set.Apply(map[string]any{"link_encrypt": "off"})
	off := playlist()
	if !strings.Contains(off, ".") {
		t.Fatalf("off must give a signed link: %s", off)
	}
	if pl, err := px.signer.Decode(off); err != nil || !strings.HasSuffix(pl.U, "/secretseg.ts") {
		t.Fatalf("decode: %v %+v", err, pl)
	}
	for _, tok := range []string{on, off} { // links of both kinds are served whatever the setting is
		rec := httptest.NewRecorder()
		px.ServeSigned(rec, httptest.NewRequest("GET", "/x/"+tok+"/s.ts", nil), "/x/", "http://p")
		if rec.Code != 200 || rec.Body.String() != "seg" {
			t.Fatalf("serve %s: %d %q", tok, rec.Code, rec.Body.String())
		}
	}
}

// Expired segments are deleted by the sweeper, valid ones are not, and a changed interval applies without a restart.
func TestCacheSweepRemovesOnlyExpiredAndFollowsTheSetting(t *testing.T) {
	set := testSettings(t)
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)
	if set.Int("segment_cache_sweep_seconds") != 30 {
		t.Fatalf("default must be 30 seconds, got %d", set.Int("segment_cache_sweep_seconds"))
	}
	c := px.cache
	c.putTTL("old", cached{status: 200, data: make([]byte, 1000)}, 50*time.Millisecond)
	c.putTTL("new", cached{status: 200, data: make([]byte, 2000)}, time.Hour)
	c.hold("stale", heldManifest{at: time.Now().Add(-2 * holdoverMaxAge)})
	time.Sleep(80 * time.Millisecond)
	if n, b := c.usage(); n != 1 || b != 2000 {
		t.Fatalf("an expired segment must not count: %d entries %d bytes", n, b)
	}
	if n := c.sweep(time.Now()); n != 1 {
		t.Fatalf("sweep removed %d", n)
	}
	if _, ok := c.peek("new"); !ok {
		t.Fatal("a valid segment must survive the sweep")
	}
	if c.size != 2000 || len(c.m) != 1 || len(c.held) != 0 {
		t.Fatalf("size %d entries %d held %d", c.size, len(c.m), len(c.held))
	}

	// the loop: a long interval leaves an expired segment alone, shortening it takes effect at once
	set.Apply(map[string]any{"segment_cache_sweep_seconds": 600.0})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go px.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	c.putTTL("short", cached{status: 200, data: make([]byte, 500)}, 20*time.Millisecond)
	time.Sleep(1500 * time.Millisecond)
	c.mu.Lock()
	_, still := c.m["short"]
	c.mu.Unlock()
	if !still {
		t.Fatal("with a 600 s interval nothing should have been swept yet")
	}
	set.Apply(map[string]any{"segment_cache_sweep_seconds": 1.0})
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		_, still = c.m["short"]
		c.mu.Unlock()
		if !still {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the new interval did not apply without a restart")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
