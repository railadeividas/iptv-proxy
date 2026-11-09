package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func hlsServer(t *testing.T, targetDuration int, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ".m3u8") { // a segment (the proxy may fetch them ahead): not what is counted
			w.Header().Set("Content-Type", "video/mp2t")
			w.Write([]byte("ts"))
			return
		}
		hits.Add(1)
		time.Sleep(30 * time.Millisecond) // a request takes a moment: viewers pile up behind it
		if status != 200 {
			http.Error(w, "down", status)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:" + string(rune('0'+targetDuration)) + "\n#EXT-X-MEDIA-SEQUENCE:7\n#EXTINF:4,\nseg7.ts\n#EXTINF:4,\nseg8.ts\n"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// segmentOwner is the profile a rewritten playlist's first segment link was made for.
func segmentOwner(t *testing.T, px *Proxy, body string) int64 {
	t.Helper()
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "http://proxy/x/") {
			pl, err := px.signer.Decode(strings.Split(strings.TrimPrefix(l, "http://proxy/x/"), "/")[0])
			if err != nil {
				t.Fatal(err)
			}
			return pl.P
		}
	}
	t.Fatalf("no segment link in %q", body)
	return 0
}

func TestPlaylistIsSharedBetweenViewers(t *testing.T) {
	srv, hits := hlsServer(t, 10, 200)
	px := newSharingProxy(t, nil)
	var wg sync.WaitGroup
	owners := make([]int64, 8)
	for i := range owners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, body := signedGet(t, px, Payload{U: srv.URL + "/live/index.m3u8", P: int64(100 + i), C: "News"})
			if code != 200 {
				t.Errorf("viewer %d: %d", i, code)
				return
			}
			owners[i] = segmentOwner(t, px, body)
		}()
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("eight viewers cost the server %d playlist requests, want 1", hits.Load())
	}
	for i, o := range owners {
		if o != int64(100+i) {
			t.Fatalf("viewer %d got links made for profile %d: a shared playlist must be rewritten for each viewer", i, o)
		}
	}
	if st := px.Stats(); st.PlaylistHits != 7 {
		t.Fatalf("seven playlists came from the copy: %+v", st)
	}
}

func TestPlaylistCacheFollowsTheSettingAndThePlaylist(t *testing.T) {
	srv, hits := hlsServer(t, 2, 200) // updates every 2 s: a copy may live 1 s at most
	px := newSharingProxy(t, map[string]any{"playlist_cache_seconds": 10.0})
	get := func() { t.Helper(); signedGet(t, px, Payload{U: srv.URL + "/a.m3u8"}) }
	get()
	get()
	if hits.Load() != 1 {
		t.Fatalf("second request within the time: %d requests", hits.Load())
	}
	time.Sleep(1100 * time.Millisecond)
	get()
	if hits.Load() != 2 {
		t.Fatalf("a copy never outlives half of the playlist's target duration: %d requests", hits.Load())
	}
	// switched off while running
	if errs := px.set.Apply(map[string]any{"playlist_cache_seconds": 0.0}); errs != nil {
		t.Fatal(errs)
	}
	get()
	get()
	if hits.Load() != 4 {
		t.Fatalf("off: every request goes to the server, got %d", hits.Load())
	}
	if errs := px.set.Apply(map[string]any{"playlist_cache_seconds": 5.0}); errs != nil {
		t.Fatal(errs)
	}
	time.Sleep(1100 * time.Millisecond) // the copy made before it was switched off has expired
	get()
	get()
	if hits.Load() != 5 {
		t.Fatalf("on again, without a restart: %d requests", hits.Load())
	}
}

func TestPlaylistFetchThatFailsDoesNotStallTheOthers(t *testing.T) {
	srv, hits := hlsServer(t, 4, 503)
	px := newSharingProxy(t, map[string]any{"upstream_retries": 0.0})
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, _ := signedGet(t, px, Payload{U: srv.URL + "/down.m3u8"}); code != 503 {
				t.Errorf("a failing server is reported: %d", code)
			}
		}()
	}
	wg.Wait()
	if time.Since(start) > 3*time.Second || hits.Load() < 1 {
		t.Fatalf("waiting for a fetch that fails must not hang: %v, %d requests", time.Since(start), hits.Load())
	}
	// and nothing bad was kept
	srv2, hits2 := hlsServer(t, 4, 200)
	if code, _ := signedGet(t, px, Payload{U: srv2.URL + "/ok.m3u8"}); code != 200 || hits2.Load() != 1 {
		t.Fatal("a good playlist after a bad one")
	}
}

func TestPlaylistTTL(t *testing.T) {
	s := 2 * time.Second
	for _, c := range []struct {
		kind, body string
		want       time.Duration
	}{
		{"hls", "#EXTM3U\n#EXT-X-TARGETDURATION:6\n", 2 * time.Second}, // the setting is shorter
		{"hls", "#EXTM3U\n#EXT-X-TARGETDURATION:2\n", time.Second},     // half of the target duration is shorter
		{"hls", "#EXTM3U\n#EXT-X-TARGETDURATION:1\n", 500 * time.Millisecond},
		{"hls", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nlow.m3u8\n", s}, // a master playlist: the setting
		{"dash", `<MPD type="dynamic" minimumUpdatePeriod="PT2S">`, time.Second},
		{"dash", `<MPD type="dynamic" minimumUpdatePeriod="PT1M30S">`, s},
		{"dash", `<MPD type="static">`, s},
	} {
		if got := playlistTTL(s, c.kind, []byte(c.body)); got != c.want {
			t.Errorf("%s %q: %v, want %v", c.kind, c.body, got, c.want)
		}
	}
}

func TestPlaylistCacheKeepsTheBodyOfOneViewerFromAnother(t *testing.T) {
	srv, _ := hlsServer(t, 10, 200)
	px := newSharingProxy(t, nil)
	_, a := signedGet(t, px, Payload{U: srv.URL + "/x.m3u8", P: 1})
	_, b := signedGet(t, px, Payload{U: srv.URL + "/x.m3u8", P: 2})
	if segmentOwner(t, px, a) != 1 || segmentOwner(t, px, b) != 2 || a == b {
		t.Fatal("two viewers of one cached playlist must still get their own links")
	}
	_ = io.Discard
}
