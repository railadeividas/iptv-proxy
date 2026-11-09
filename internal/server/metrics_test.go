package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"iptv-proxy/internal/config"
	"iptv-proxy/internal/epg"
	"iptv-proxy/internal/events"
	"iptv-proxy/internal/health"
	"iptv-proxy/internal/proxy"
	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
	isync "iptv-proxy/internal/sync"
)

var (
	helpLine   = regexp.MustCompile(`^# HELP ([a-zA-Z_:][a-zA-Z0-9_:]*) .+$`)
	typeLine   = regexp.MustCompile(`^# TYPE ([a-zA-Z_:][a-zA-Z0-9_:]*) (gauge|counter)$`)
	sampleLine = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{([a-zA-Z_][a-zA-Z0-9_]*="([^"\\]|\\.)*"(,|))*\})? (\S+)$`)
)

// checkFormat verifies the text is valid exposition format: every metric is introduced once with HELP and TYPE and
// all its samples follow together, and every value is a number.
func checkFormat(t *testing.T, body string) {
	t.Helper()
	done := map[string]bool{}
	cur := ""
	typed := false
	for _, l := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if m := helpLine.FindStringSubmatch(l); m != nil {
			if done[m[1]] {
				t.Fatalf("metric %s appears twice", m[1])
			}
			if cur != "" {
				done[cur] = true
			}
			cur, typed = m[1], false
			continue
		}
		if m := typeLine.FindStringSubmatch(l); m != nil {
			if m[1] != cur {
				t.Fatalf("TYPE %s without its HELP: %q", m[1], l)
			}
			typed = true
			continue
		}
		m := sampleLine.FindStringSubmatch(l)
		if m == nil || m[1] != cur || !typed {
			t.Fatalf("bad line %q (inside metric %q)", l, cur)
		}
		if _, err := strconv.ParseFloat(m[6], 64); err != nil && m[6] != "NaN" && m[6] != "+Inf" {
			t.Fatalf("bad value in %q", l)
		}
	}
}

func has(body, line string) bool {
	for _, l := range strings.Split(body, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

// valueOf returns the value of the first sample starting with prefix (a name, or a name with some labels).
func valueOf(t *testing.T, body, prefix string) float64 {
	t.Helper()
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, prefix) && !strings.HasPrefix(l, "#") {
			v, err := strconv.ParseFloat(l[strings.LastIndexByte(l, ' ')+1:], 64)
			if err != nil {
				t.Fatalf("value of %q: %v", l, err)
			}
			return v
		}
	}
	t.Fatalf("no sample starts with %q in:\n%s", prefix, body)
	return 0
}

func TestMetricsEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nseg1.ts\n"))
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte(strings.Repeat("x", 4000)))
	}))
	defer upstream.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set := settings.New(st)
	st.SetMetaInt("synced_at", 1)
	signer := proxy.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	app := New(config.Config{DataDir: t.TempDir()}, st, set, isync.New(st, set), proxy.New(signer, set),
		epg.New(st, set, "/nonexistent", t.TempDir()), health.New(st, set), signer, events.NewHub(),
		fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	h := app.Handler()

	root := signIn(t, h)
	addUser(t, h, root, "ann", "user")
	addUser(t, h, root, "bob", "user")
	var ann, bob int64
	st.DB.QueryRow(`SELECT id FROM users WHERE username='ann'`).Scan(&ann)
	st.DB.QueryRow(`SELECT id FROM users WHERE username='bob'`).Scan(&bob)
	st.DB.Exec(`INSERT INTO profiles(id,owner_id,name,token,mode,created_at) VALUES(11,?,'Ann "TV"','tokann','proxy',1),(12,?,'Bob TV','tokbob','redirect',1)`, ann, bob)
	st.DB.Exec(`INSERT INTO entries(ck,name,nsfw,closed,src) VALUES('News','News One',0,0,0)`)
	st.DB.Exec(`INSERT INTO streams(ck,url,status) VALUES('News',?,1)`, upstream.URL+"/live.m3u8")
	st.DB.Exec(`INSERT INTO profile_channels(profile_id,ck,position) VALUES(11,'News',0),(12,'News',0)`)

	scrape := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/metrics", nil)
		req.RemoteAddr = "10.0.0.9:1"
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	apply := func(kv map[string]any) {
		t.Helper()
		if errs := set.Apply(kv); errs != nil {
			t.Fatal(errs)
		}
	}
	const token = "0123456789abcdef-secret-token"

	// off by default: the endpoint does not exist
	if c := scrape("Bearer " + token).Code; c != 404 {
		t.Fatalf("off by default: %d", c)
	}
	// on, but no token set: nobody gets in
	apply(map[string]any{"metrics_enabled": "on"})
	if c := scrape("").Code; c != 401 {
		t.Fatalf("on without a token set: %d", c)
	}
	if c := scrape("Bearer ").Code; c != 401 {
		t.Fatalf("an empty bearer must not match an empty token: %d", c)
	}
	if errs := set.Apply(map[string]any{"metrics_token": "short"}); errs["metrics_token"] == "" {
		t.Fatal("a short token is refused")
	}
	apply(map[string]any{"metrics_token": token})
	if rec := scrape(""); rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no header: %d", rec.Code)
	}
	for _, bad := range []string{"Bearer wrong", "Bearer " + token + "x", token, "Basic " + token} {
		if c := scrape(bad).Code; c != 401 {
			t.Fatalf("%q: %d", bad, c)
		}
	}
	if rec := scrape("Bearer " + token); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("right token: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	req := httptest.NewRequest("HEAD", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if rec := httptest.NewRecorder(); func() int { h.ServeHTTP(rec, req); return rec.Code }() != 200 {
		t.Fatal("HEAD")
	}
	// open access: no authentication, and then a token is not needed (a wrong one does not matter either)
	apply(map[string]any{"metrics_access": "open"})
	if scrape("").Code != 200 {
		t.Fatal("open access")
	}
	apply(map[string]any{"metrics_access": "token"})
	if scrape("").Code != 401 {
		t.Fatal("back to a token takes effect at once")
	}

	// traffic: ann plays through the proxy, bob is redirected
	get := func(path string) *httptest.ResponseRecorder { return call(h, "GET", path, nil) }
	get("/p/tokann/playlist.m3u")
	get("/p/tokann/epg.xml")
	rec := get("/p/tokann/channel/News.m3u8")
	var seg string
	for _, l := range strings.Split(rec.Body.String(), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			u, _ := url.Parse(l)
			seg = u.Path
		}
	}
	if get(seg).Body.Len() != 4000 {
		t.Fatal("segment")
	}
	if get("/p/tokbob/channel/News.m3u8").Code != http.StatusFound {
		t.Fatal("redirect")
	}

	body := scrape("Bearer " + token).Body.String()
	checkFormat(t, body)
	if strings.Contains(body, token) || strings.Contains(body, "tokann") || strings.Contains(body, upstream.URL+"/live") {
		t.Fatal("tokens and stream addresses never appear in the metrics")
	}
	for _, line := range []string{
		`iptv_profile_sessions_started_total{profile="Ann \"TV\"",profile_id="11",owner="ann",mode="proxy"} 1`,
		`iptv_profile_sessions_started_total{profile="Bob TV",profile_id="12",owner="bob",mode="redirect"} 1`,
		`iptv_profile_playlist_fetches_total{profile="Ann \"TV\"",profile_id="11",owner="ann"} 1`,
		`iptv_profile_guide_fetches_total{profile="Ann \"TV\"",profile_id="11",owner="ann"} 1`,
		`iptv_profile_channels{profile="Bob TV",profile_id="12",owner="bob"} 1`,
		`iptv_profile_sessions_active{profile="Ann \"TV\"",profile_id="11",owner="ann",mode="proxy"} 1`,
		`iptv_profile_channels_by_state{profile="Ann \"TV\"",profile_id="11",owner="ann",state="working"} 1`,
		`iptv_profile_clients{profile="Ann \"TV\"",profile_id="11",owner="ann"} 1`,
		`iptv_channel_viewers{ck="News",name="News One",mode="proxy"} 1`,
		`iptv_stream_host_viewers{host="` + strings.TrimPrefix(upstream.URL, "http://") + `",mode="proxy"} 1`,
		`iptv_sessions_started_total{mode="proxy"} 1`,
		`iptv_sessions_started_total{mode="redirect"} 1`,
		`iptv_sessions_active{mode="proxy"} 1`,
		`iptv_playlist_fetches_total 1`,
		`iptv_proxy_connections_limit 20`,
		`iptv_proxy_upstream_requests_total{result="error"} 0`,
		`iptv_http_requests_total{route="segment",code="200"} 1`,
		`iptv_http_requests_total{route="playlist",code="200"} 1`,
		`iptv_http_requests_total{route="metrics",code="401"} 8`,
		`iptv_users{role="admin"} 0`,
		`iptv_users{role="user"} 2`,
		`iptv_profiles 2`,
		`iptv_catalogue_channels 1`,
		`iptv_streams{state="working"} 1`,
		`iptv_channels_by_state{state="working"} 1`,
		`iptv_ready 1`,
		`iptv_stats_recording 1`,
	} {
		if !has(body, line) {
			t.Errorf("missing line: %s", line)
		}
	}
	if valueOf(t, body, "iptv_bytes_sent_total") < 4000 || valueOf(t, body, `iptv_profile_bytes_sent_total{profile="Ann`) < 4000 {
		t.Error("bytes sent")
	}
	if valueOf(t, body, "iptv_proxy_upstream_requests_total{result=\"ok\"}") < 2 {
		t.Error("upstream requests")
	}
	for _, name := range []string{"iptv_build_info", "iptv_start_time_seconds", "go_goroutines", "iptv_health_running", "iptv_sync_last_success_timestamp_seconds", "iptv_epg_enabled", "iptv_proxy_cache_limit_bytes", "iptv_proxy_cache_hit_bytes_total", "iptv_proxy_shared_streams", "iptv_proxy_shared_viewers", "iptv_proxy_shared_joins_total", "iptv_proxy_cache_stores_total", "iptv_proxy_cache_rejected_total", "iptv_proxy_prefetched_segments_total", "iptv_proxy_coalesced_requests_total", "iptv_proxy_upstream_retries_total", "iptv_proxy_playlist_holdovers_total", "iptv_websocket_clients"} {
		valueOf(t, body, name)
	}

	// the detail settings apply at once
	apply(map[string]any{"metrics_channel_viewers": "off"})
	body = scrape("Bearer " + token).Body.String()
	checkFormat(t, body)
	if strings.Contains(body, "iptv_channel_viewers") || !strings.Contains(body, "iptv_stream_host_viewers") {
		t.Fatal("viewers per channel can be switched off")
	}
	apply(map[string]any{"metrics_profile_labels": "off"})
	body = scrape("Bearer " + token).Body.String()
	checkFormat(t, body)
	if strings.Contains(body, "iptv_profile_") || strings.Contains(body, "Bob TV") || !has(body, `iptv_sessions_started_total{mode="proxy"} 1`) {
		t.Fatal("without profile labels only the totals remain")
	}
	// switching the endpoint off hides it again
	apply(map[string]any{"metrics_enabled": "off"})
	if c := scrape("Bearer " + token).Code; c != 404 {
		t.Fatalf("off again: %d", c)
	}
}
