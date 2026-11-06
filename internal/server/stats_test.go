package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

type statsReply struct {
	Enabled bool `json:"enabled"`
	Now     []struct {
		Profile string `json:"profile"`
		Name    string `json:"name"`
		Mode    string `json:"mode"`
		Host    string `json:"host"`
		Bytes   int64  `json:"bytes"`
	} `json:"now"`
	Totals   store.ActivityTotals        `json:"totals"`
	Profiles []store.ProfileActivity     `json:"profiles"`
	Conn     *struct{ InUse, Limit int } `json:"connections"`
}

// Players use the links, and the Stats page shows it: a proxied viewer (playlist fetch, channel link, segment) and a
// redirected one, each visible to their owner only, everything to an admin.
func TestStatsFollowPlayerRequests(t *testing.T) {
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
	ann := addUser(t, h, root, "ann", "user").cookie
	bob := addUser(t, h, root, "bob", "user").cookie
	adm := addUser(t, h, root, "adm", "admin").cookie

	var ids [3]int64
	st.DB.QueryRow(`SELECT id FROM users WHERE username='ann'`).Scan(&ids[0])
	st.DB.QueryRow(`SELECT id FROM users WHERE username='bob'`).Scan(&ids[1])
	st.DB.Exec(`INSERT INTO profiles(id,owner_id,name,token,mode,created_at) VALUES(11,?,'Ann TV','tokann','proxy',1),(12,?,'Bob TV','tokbob','redirect',1)`, ids[0], ids[1])
	st.DB.Exec(`INSERT INTO entries(ck,name,nsfw,closed,src) VALUES('News','News One',0,0,0)`)
	st.DB.Exec(`INSERT INTO streams(ck,url,status) VALUES('News',?,1)`, upstream.URL+"/live.m3u8")
	st.DB.Exec(`INSERT INTO profile_channels(profile_id,ck,position) VALUES(11,'News',0),(12,'News',0)`)

	get := func(path string) *httptest.ResponseRecorder { return call(h, "GET", path, nil) }
	if get("/p/tokann/playlist.m3u").Code != 200 || get("/p/tokann/playlist.m3u").Code != 200 {
		t.Fatal("playlist")
	}
	rec := get("/p/tokann/channel/News.m3u8")
	if rec.Code != 200 {
		t.Fatalf("channel: %d %s", rec.Code, rec.Body)
	}
	var seg string
	for _, l := range strings.Split(rec.Body.String(), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			u, _ := url.Parse(l)
			seg = u.Path
		}
	}
	if !strings.HasPrefix(seg, "/x/") {
		t.Fatalf("segment link: %q", rec.Body)
	}
	if got := get(seg); got.Code != 200 || got.Body.Len() != 4000 {
		t.Fatalf("segment: %d %d", got.Code, got.Body.Len())
	}
	if get("/p/tokbob/channel/News.m3u8").Code != http.StatusFound {
		t.Fatal("redirect profile")
	}
	app.Activity().Flush(false)

	stats := func(c *http.Cookie, q string) statsReply {
		t.Helper()
		rec := call(h, "GET", "/api/stats"+q, nil, c)
		if rec.Code != 200 {
			t.Fatalf("stats %s: %d %s", q, rec.Code, rec.Body)
		}
		var s statsReply
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	a := stats(ann, "")
	if !a.Enabled || len(a.Now) != 1 || a.Now[0].Mode != "proxy" || a.Now[0].Name != "News One" || a.Now[0].Bytes < 4000 ||
		a.Now[0].Host != strings.TrimPrefix(upstream.URL, "http://") {
		t.Fatalf("ann now: %+v", a.Now)
	}
	if a.Totals.Sessions != 1 || a.Totals.Proxy != 1 || a.Totals.Redirect != 0 || a.Totals.Bytes < 4000 || a.Totals.Clients != 1 {
		t.Fatalf("ann totals: %+v", a.Totals)
	}
	if len(a.Profiles) != 1 || a.Profiles[0].Profile != "Ann TV" || a.Profiles[0].Playlists != 2 {
		t.Fatalf("ann profiles: %+v", a.Profiles)
	}
	if a.Conn != nil {
		t.Fatal("server load is for those who see everybody's activity")
	}
	b := stats(bob, "")
	if b.Totals.Redirect != 1 || b.Totals.Proxy != 0 || len(b.Profiles) != 1 || b.Profiles[0].Profile != "Bob TV" {
		t.Fatalf("bob: %+v", b)
	}
	all := stats(adm, "")
	if all.Totals.Sessions != 2 || len(all.Profiles) != 2 || len(all.Now) != 2 || all.Conn == nil || all.Conn.Limit != 20 {
		t.Fatalf("admin sees everybody: %+v", all)
	}
	if stats(adm, "?profile=12").Totals.Redirect != 1 || stats(adm, "?profile=12&days=0").Totals.Proxy != 0 {
		t.Fatal("filter by profile")
	}
	if call(h, "GET", "/api/stats?profile=12", nil, ann).Code != 404 {
		t.Fatal("someone else's profile is a 404")
	}
	if call(h, "GET", "/api/stats", nil).Code != 401 {
		t.Fatal("stats need a session")
	}

	var list struct {
		Total int `json:"total"`
		Items []struct {
			Name string `json:"name"`
			IP   string `json:"ip"`
			Mode string `json:"mode"`
		} `json:"items"`
	}
	json.Unmarshal(call(h, "GET", "/api/stats/sessions?limit=5", nil, adm).Body.Bytes(), &list)
	if list.Total != 2 || list.Items[0].Name != "News One" || list.Items[0].IP != "10.0.0.0/24" {
		t.Fatalf("sessions: %+v", list)
	}
	json.Unmarshal(call(h, "GET", "/api/stats/channels", nil, bob).Body.Bytes(), &list)
	if list.Total != 1 || list.Items[0].Name != "News One" {
		t.Fatalf("channels: %+v", list)
	}
	json.Unmarshal(call(h, "GET", "/api/stats/clients", nil, ann).Body.Bytes(), &list)
	if list.Total != 1 {
		t.Fatalf("clients: %+v", list)
	}

	// turning recording off applies to the next request
	if errs := set.Apply(map[string]any{"stats_enabled": "off"}); errs != nil {
		t.Fatal(errs)
	}
	get("/p/tokann/playlist.m3u")
	get("/p/tokbob/channel/News.m3u8")
	app.Activity().Flush(false)
	if s := stats(adm, ""); s.Enabled || s.Totals.Redirect != 1 || s.Profiles[0].Playlists+s.Profiles[1].Playlists != 2 {
		t.Fatalf("nothing recorded while off: %+v", s)
	}
	// playing keeps working with recording off
	rec = get("/p/tokann/channel/News.m3u8")
	if rec.Code != 200 {
		t.Fatal("playing works with recording off")
	}
}
