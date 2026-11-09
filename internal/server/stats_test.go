package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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
	if stats(adm, "?profile=12").Totals.Redirect != 1 || stats(adm, "?profile=12&from="+strconv.FormatInt(time.Now().Unix()-3000*86400, 10)).Totals.Proxy != 0 {
		t.Fatal("filter by profile")
	}
	if call(h, "GET", "/api/stats?profile=12", nil, ann).Code != 404 {
		t.Fatal("someone else's profile is a 404")
	}
	// several profiles at once: the sessions of each, one list
	both := stats(adm, "?profile=11,12")
	if both.Totals.Sessions != 2 || both.Totals.Proxy != 1 || both.Totals.Redirect != 1 || len(both.Now) != 2 || len(both.Profiles) != 2 {
		t.Fatalf("two profiles selected: %+v", both)
	}
	if stats(adm, "?profile=11,11").Totals.Sessions != 1 || stats(adm, "?profile=12,11,12").Totals.Sessions != 2 {
		t.Fatal("a repeated id counts once")
	}
	if call(h, "GET", "/api/stats?profile=11,12", nil, ann).Code != 404 {
		t.Fatal("one profile that is not yours makes the whole selection a 404")
	}
	if call(h, "GET", "/api/stats?profile=11,x", nil, adm).Code != 400 || call(h, "GET", "/api/stats?profile=-1", nil, adm).Code != 400 {
		t.Fatal("a bad id is a 400")
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
	json.Unmarshal(call(h, "GET", "/api/stats/breakdown?by=channel", nil, bob).Body.Bytes(), &list)
	if list.Total != 1 || list.Items[0].Name != "News One" {
		t.Fatalf("channels: %+v", list)
	}
	if call(h, "GET", "/api/stats/channels", nil, bob).Code == 200 {
		t.Fatal("the channels list is replaced by the breakdown")
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

// newStatsApp is a server with an administrator and two users with a profile each: ann (11) and bob (12).
func newStatsApp(t *testing.T) (h http.Handler, app *Server, st *store.Store, ann, bob, adm *http.Cookie) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := settings.New(st)
	st.SetMetaInt("synced_at", 1)
	signer := proxy.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	app = New(config.Config{DataDir: t.TempDir()}, st, set, isync.New(st, set), proxy.New(signer, set),
		epg.New(st, set, "/nonexistent", t.TempDir()), health.New(st, set), signer, events.NewHub(),
		fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	h = app.Handler()
	root := signIn(t, h)
	ann = addUser(t, h, root, "ann", "user").cookie
	bob = addUser(t, h, root, "bob", "user").cookie
	adm = addUser(t, h, root, "adm", "admin").cookie
	var a, b int64
	st.DB.QueryRow(`SELECT id FROM users WHERE username='ann'`).Scan(&a)
	st.DB.QueryRow(`SELECT id FROM users WHERE username='bob'`).Scan(&b)
	st.DB.Exec(`INSERT INTO profiles(id,owner_id,name,token,mode,created_at) VALUES(11,?,'Ann TV','tokann','proxy',1),(12,?,'Bob TV','tokbob','proxy',1)`, a, b)
	st.DB.Exec(`INSERT INTO entries(ck,name,nsfw,closed,src) VALUES('News','News One',0,0,0)`)
	return
}

func getJSON(t *testing.T, h http.Handler, path string, c *http.Cookie, into any) {
	t.Helper()
	rec := call(h, "GET", path, nil, c)
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

type timelineReply struct {
	Bucket int64 `json:"bucket"`
	Start  int64 `json:"start"`
	N      int   `json:"n"`
	Rows   []struct {
		I        int    `json:"i"`
		P        int64  `json:"p"`
		Mode     string `json:"mode"`
		Sessions int64  `json:"sessions"`
		Bytes    int64  `json:"bytes"`
		Seconds  int64  `json:"seconds"`
	} `json:"rows"`
	Peaks   []int `json:"peaks"`
	Devices []int `json:"devices"`
	Peak    struct {
		Max int   `json:"max"`
		At  int64 `json:"at"`
	} `json:"peak"`
}

func (r timelineReply) sessions() (n int64) {
	for _, x := range r.Rows {
		n += x.Sessions
	}
	return
}

func TestStatsChartRoutes(t *testing.T) {
	h, app, st, ann, bob, adm := newStatsApp(t)
	now := time.Now().Unix()
	add := func(p int64, ck, mode, host, ua string, ago, secs, bytes int64) {
		st.AddSession(store.Session{ProfileID: p, CK: ck, Mode: mode, Host: host, IP: "10.0.0.0/24", UA: ua, Started: now - ago, Last: now - ago + secs, Bytes: bytes, Requests: 1})
	}
	add(11, "News", "proxy", "cdn.a", "VLC", 1800, 900, 5000)   // 30 minutes ago
	add(11, "News", "proxy", "cdn.a", "VLC", 1700, 600, 3000)   // overlaps the first
	add(12, "Sport", "redirect", "cdn.b", "Kodi", 600, 0, 0)    // bob
	add(12, "News", "proxy", "cdn.b", "Kodi", 3*86400, 60, 100) // three days ago
	add(11, "Movie", "proxy", "cdn.a", "VLC", 10*86400, 7200, 9000)
	add(12, "Movie", "proxy", "cdn.b", "Kodi", 40*86400, 60, 1) // older than the 30 days of history

	// nothing is open to anybody not signed in
	for _, p := range []string{"timeline", "patterns", "breakdown?by=channel", "samples", "live"} {
		if call(h, "GET", "/api/stats/"+p, nil).Code != 401 {
			t.Fatalf("%s needs a session", p)
		}
		other := "/api/stats/" + p + "?profile=12"
		if strings.Contains(p, "?") {
			other = "/api/stats/" + p + "&profile=12"
		}
		if call(h, "GET", other, nil, ann).Code != 404 {
			t.Fatalf("%s: somebody else's profile is a 404", p)
		}
	}

	week := func(extra string) string { return "/api/stats/timeline?" + extra }
	since := func(days int64) string { return "from=" + strconv.FormatInt(now-days*86400, 10) }

	// the default is the last 7 days; the step is automatic: the smallest with at most 120 buckets (a week of hours is 168)
	var tl timelineReply
	getJSON(t, h, "/api/stats/timeline", adm, &tl)
	if tl.Bucket != 86400 || tl.N < 7 || tl.N > 8 || len(tl.Peaks) != tl.N || len(tl.Devices) != tl.N || tl.sessions() != 4 {
		t.Fatalf("default range: bucket=%d n=%d sessions=%d", tl.Bucket, tl.N, tl.sessions())
	}
	for secs, want := range map[int64]int64{2 * 3600: 300, 24 * 3600: 900, 4 * 86400: 3600, 100 * 86400: 86400, 1000 * 86400: 604800} {
		var a timelineReply
		getJSON(t, h, week("from="+strconv.FormatInt(now-secs, 10)), adm, &a)
		if a.Bucket != want || a.N > 120 && want != 604800 {
			t.Fatalf("auto step for %d s: bucket=%d n=%d, want %d", secs, a.Bucket, a.N, want)
		}
	}
	// an explicit step; 7 days of days
	getJSON(t, h, week(since(7)+"&step=86400"), adm, &tl)
	if tl.Bucket != 86400 || tl.N < 7 || tl.N > 8 || tl.Start%86400 != 0 || tl.sessions() != 4 {
		t.Fatalf("a week of days: %+v", tl)
	}
	if tl.Peak.Max != 2 || tl.Peak.At != now-1700 || tl.Peaks[tl.N-1] != 2 {
		t.Fatalf("two streams at once, since 1700 s ago: %+v", tl.Peak)
	}
	// every step; the grid is widened to whole buckets, the counting is over the exact range
	to := now - 60
	for _, step := range []int64{300, 900, 3600, 86400, 604800} {
		var g timelineReply
		getJSON(t, h, week("from="+strconv.FormatInt(now-2*3600, 10)+"&to="+strconv.FormatInt(to, 10)+"&step="+strconv.FormatInt(step, 10)), adm, &g)
		if g.Bucket != step || g.Start%step != 0 && step != 604800 || g.Start > now-2*3600 || g.Start+int64(g.N)*step < to || g.sessions() != 3 {
			t.Fatalf("step %d: %+v", step, g)
		}
	}
	// the exact range counts: from just after the first two sessions started leaves only bob's redirect
	var exact timelineReply
	getJSON(t, h, week("from="+strconv.FormatInt(now-1000, 10)+"&step=3600"), adm, &exact)
	if exact.sessions() != 1 || exact.Rows[0].Mode != "redirect" {
		t.Fatalf("exact range: %+v", exact.Rows)
	}
	// a range that ends before they start
	getJSON(t, h, week("from="+strconv.FormatInt(now-2*86400, 10)+"&to="+strconv.FormatInt(now-2*86400+3600, 10)), adm, &exact)
	if exact.sessions() != 0 || exact.Peak.Max != 0 || exact.Peak.At != 0 {
		t.Fatalf("empty range: %+v", exact)
	}
	// the viewer's zone moves the boundaries (days: UTC+5:30; weeks start on Monday there)
	var tz timelineReply
	getJSON(t, h, week(since(7)+"&step=86400&tz=19800"), adm, &tz)
	if (tz.Start+19800)%86400 != 0 || tz.Start%86400 == 0 || tz.sessions() != 4 {
		t.Fatalf("zone: %+v", tz)
	}
	getJSON(t, h, week(since(30)+"&step=604800&tz=19800"), adm, &tz)
	if (tz.Start+19800-4*86400)%604800 != 0 || tz.Bucket != 604800 || tz.sessions() != 5 {
		t.Fatalf("weeks start on Monday in the viewer's zone: %+v", tz)
	}
	var big, max, junk timelineReply
	getJSON(t, h, week(since(7)+"&step=86400&tz=999999"), adm, &big)
	getJSON(t, h, week(since(7)+"&step=86400&tz=50400"), adm, &max)
	getJSON(t, h, week(since(7)+"&step=86400&tz=-999999"), adm, &junk)
	if big.Start != max.Start || junk.Start == max.Start {
		t.Fatalf("tz clamp: %d %d %d", big.Start, max.Start, junk.Start)
	}
	// the range right before: same length
	var prev timelineReply
	f7, t7 := now-7*86400, now
	getJSON(t, h, week("from="+strconv.FormatInt(f7, 10)+"&to="+strconv.FormatInt(t7, 10)+"&shift=1&step=86400"), adm, &prev)
	if prev.sessions() != 1 || prev.Start > f7-7*86400 || prev.Start+int64(prev.N)*86400 < f7 {
		t.Fatalf("shift: %+v", prev)
	}
	// the longest history: 30 days of days, then 40 days back finds the old one
	var all timelineReply
	getJSON(t, h, week(since(30)+"&step=86400"), adm, &all)
	if all.N < 30 || all.N > 31 || all.sessions() != 5 {
		t.Fatalf("30 days: n=%d sessions=%d", all.N, all.sessions())
	}
	getJSON(t, h, week(since(60)), adm, &all)
	if all.Bucket != 86400 || all.sessions() != 6 {
		t.Fatalf("60 days: %+v", all)
	}
	// the bucket count: auto stays at 120 or fewer; a fine step over a long range is raised to stay within 500
	getJSON(t, h, week(since(20)+"&step=300"), adm, &all)
	if all.Bucket != 3600 || all.N > 500 {
		t.Fatalf("a step that would give too many buckets is raised to the next that fits: bucket=%d n=%d", all.Bucket, all.N)
	}
	getJSON(t, h, week(since(60)+"&step=300"), adm, &all)
	if all.Bucket != 86400 || all.N > 500 {
		t.Fatalf("raised to days: bucket=%d n=%d", all.Bucket, all.N)
	}
	getJSON(t, h, week(since(60)+"&step=3600"), adm, &all) // 1440 hours
	if all.Bucket != 86400 {
		t.Fatalf("raised to days: bucket=%d", all.Bucket)
	}
	getJSON(t, h, week(since(3000)), adm, &all)
	if all.Bucket != 604800 || all.sessions() != 6 {
		t.Fatalf("the whole time: bucket=%d %d", all.Bucket, all.sessions())
	}
	// bad requests
	for _, q := range []string{"step=60", "step=abc", "step=-300", "from=abc", "to=1.5", "tz=x",
		"from=2000&to=2000", "from=2000&to=1000", "from=0&to=" + strconv.FormatInt(3651*86400, 10), "from=" + strconv.FormatInt(now+86400, 10)} {
		for _, path := range []string{"/api/stats/timeline?", "/api/stats?", "/api/stats/patterns?", "/api/stats/breakdown?by=host&", "/api/stats/samples?", "/api/stats/sessions?", "/api/stats/clients?"} {
			rec := call(h, "GET", path+q, nil, adm)
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"error"`) {
				t.Fatalf("%s%s: %d %s", path, q, rec.Code, rec.Body)
			}
		}
	}
	var ov struct {
		Totals store.ActivityTotals `json:"totals"`
		Window store.Window         `json:"window"`
		Days   *int                 `json:"days"`
	}
	getJSON(t, h, "/api/stats?"+since(30)+"&to="+strconv.FormatInt(now, 10)+"&step=86400&tz=3600", adm, &ov)
	if ov.Totals.Sessions != 5 || ov.Window.From != now-30*86400 || ov.Window.To != now || ov.Window.Bucket != 86400 || (ov.Window.Start+3600)%86400 != 0 ||
		ov.Window.End < now || ov.Window.N != int((ov.Window.End-ov.Window.Start)/86400) || ov.Days != nil {
		t.Fatalf("overview window: %+v", ov)
	}
	getJSON(t, h, "/api/stats?"+since(3000), adm, &ov)
	if ov.Totals.Sessions != 6 {
		t.Fatalf("overview from the start: %+v", ov)
	}

	// scope
	var mine timelineReply
	getJSON(t, h, "/api/stats/timeline?"+since(30), bob, &mine)
	if mine.sessions() != 2 {
		t.Fatalf("bob: %+v", mine)
	}
	for _, r := range mine.Rows {
		if r.P != 12 {
			t.Fatalf("only bob's profile: %+v", mine.Rows)
		}
	}
	getJSON(t, h, week("profile=11"), adm, &mine)
	if mine.sessions() != 2 || mine.Peak.Max != 2 {
		t.Fatalf("one profile: %+v", mine)
	}

	// patterns
	var pat struct {
		Heat []struct {
			D, H, Sessions int
		} `json:"heat"`
		Durations []int `json:"durations"`
		Longest   *struct {
			Seconds int64
			CK      string
			Name    string
			Profile string
		} `json:"longest"`
	}
	getJSON(t, h, "/api/stats/patterns?"+since(30), adm, &pat)
	sum := 0
	for _, c := range pat.Heat {
		sum += c.Sessions
	}
	if sum != 5 || len(pat.Durations) != 6 || pat.Durations[0] != 1 || pat.Durations[1] != 1 || pat.Durations[2] != 1 || pat.Durations[5] != 1 {
		t.Fatalf("patterns: %+v", pat)
	}
	if pat.Longest == nil || pat.Longest.Seconds != 7200 || pat.Longest.Name != "Movie" || pat.Longest.Profile != "Ann TV" {
		t.Fatalf("longest: %+v", pat.Longest)
	}
	getJSON(t, h, "/api/stats/patterns?"+since(30)+"&tz=7200", adm, &pat)
	var plain struct {
		Heat []struct{ D, H, Sessions int } `json:"heat"`
	}
	getJSON(t, h, "/api/stats/patterns?"+since(30), adm, &plain)
	same := len(plain.Heat) == len(pat.Heat)
	for i := range pat.Heat {
		if same && (plain.Heat[i].H != pat.Heat[i].H || plain.Heat[i].D != pat.Heat[i].D) {
			same = false
		}
	}
	if same {
		t.Fatal("the zone moves the hours of the heatmap")
	}

	// breakdown
	var bd struct {
		Total int `json:"total"`
		Items []struct {
			Key, Name                string
			Sessions, Bytes, Viewers int64
		} `json:"items"`
	}
	getJSON(t, h, "/api/stats/breakdown?by=channel&"+since(30)+"&sort=bytes", adm, &bd)
	if bd.Total != 3 || bd.Items[0].Key != "Movie" || bd.Items[1].Key != "News" || bd.Items[1].Name != "News One" || bd.Items[1].Sessions != 3 {
		t.Fatalf("channels: %+v", bd)
	}
	getJSON(t, h, "/api/stats/breakdown?by=profile&"+since(30)+"&limit=1&offset=1", adm, &bd)
	if bd.Total != 2 || len(bd.Items) != 1 || bd.Items[0].Name != "Bob TV" {
		t.Fatalf("profiles page: %+v", bd)
	}
	getJSON(t, h, "/api/stats/breakdown?by=host&"+since(30), ann, &bd)
	if bd.Total != 1 || bd.Items[0].Key != "cdn.a" {
		t.Fatalf("ann's hosts: %+v", bd)
	}
	for _, by := range []string{"player", "network"} {
		getJSON(t, h, "/api/stats/breakdown?by="+by, adm, &bd)
		if bd.Total == 0 {
			t.Fatalf("%s: %+v", by, bd)
		}
	}
	for _, q := range []string{"", "?by=colour", "?by=channel&sort=name"} {
		if call(h, "GET", "/api/stats/breakdown"+q, nil, adm).Code != 400 {
			t.Fatalf("breakdown%s must be a 400", q)
		}
	}

	// samples: hourly history, the proxy's counters only for those who see everybody's
	hour := now / 3600 * 3600
	st.AddSamples(hour, store.ServerSample{CacheHits: 8, Retries: 2}, map[int64]store.ProfileSample{11: {Playlists: 3}, 12: {Guides: 4}})
	var sm struct {
		Bucket    int64                                `json:"bucket"`
		N         int                                  `json:"n"`
		Fetches   []struct{ I, Playlists, Guides int } `json:"fetches"`
		ByProfile []struct {
			ProfileID int64 `json:"profile_id"`
			Playlists int64 `json:"playlists"`
			Guides    int64 `json:"guides"`
		} `json:"by_profile"`
		Server []struct {
			I                       int
			CacheHits, Retries      int64
			CacheMisses, CacheBytes int64
		} `json:"server"`
	}
	sm.Server = nil
	raw := map[string]json.RawMessage{}
	getJSON(t, h, "/api/stats/samples?"+since(1), adm, &sm)
	getJSON(t, h, "/api/stats/samples?"+since(1), adm, &raw)
	if sm.Bucket != 3600 || sm.N < 24 || sm.N > 25 || len(sm.Fetches) != 1 || sm.Fetches[0].Playlists != 3 || sm.Fetches[0].Guides != 4 || len(sm.Server) != 1 || sm.Server[0].Retries != 2 {
		t.Fatalf("samples: %+v", sm)
	}
	if sm.Fetches[0].I < sm.N-2 { // the current hour is the last or the one before
		t.Fatalf("bucket of the current hour: %d", sm.Fetches[0].I)
	}
	if len(sm.ByProfile) != 2 || sm.ByProfile[0].ProfileID != 11 || sm.ByProfile[0].Playlists != 3 || sm.ByProfile[1].ProfileID != 12 || sm.ByProfile[1].Guides != 4 {
		t.Fatalf("fetches per profile: %+v", sm.ByProfile)
	}
	for _, c := range []*http.Cookie{ann, bob} {
		raw = map[string]json.RawMessage{}
		getJSON(t, h, "/api/stats/samples?"+since(1), c, &raw)
		if _, has := raw["server"]; has {
			t.Fatal("the proxy's counters are for those who see everybody's activity")
		}
	}
	getJSON(t, h, "/api/stats/samples?"+since(1), ann, &sm)
	if len(sm.Fetches) != 1 || sm.Fetches[0].Playlists != 3 || sm.Fetches[0].Guides != 0 {
		t.Fatalf("ann's fetches: %+v", sm.Fetches)
	}
	// never finer than an hour, whatever the step; a coarser step groups the hours
	getJSON(t, h, "/api/stats/samples?"+since(1)+"&step=300", adm, &sm)
	if sm.Bucket != 3600 || sm.N < 24 || sm.N > 25 {
		t.Fatalf("samples are hourly at the finest: %+v", sm)
	}
	getJSON(t, h, "/api/stats/samples?"+since(3)+"&step=86400", adm, &sm)
	if sm.Bucket != 86400 || sm.N < 3 || sm.N > 4 || len(sm.Fetches) != 1 || sm.Fetches[0].Playlists != 3 || sm.Fetches[0].I < sm.N-2 {
		t.Fatalf("daily samples: %+v", sm)
	}
	// a range that ends before the sampled hour has nothing
	getJSON(t, h, "/api/stats/samples?from="+strconv.FormatInt(hour-7200, 10)+"&to="+strconv.FormatInt(hour, 10), adm, &sm)
	if len(sm.Fetches) != 0 || len(sm.Server) != 0 {
		t.Fatalf("the hour is outside [from, to): %+v", sm)
	}
	getJSON(t, h, "/api/stats/samples?"+since(1)+"&profile=12", adm, &sm)
	if len(sm.Fetches) != 1 || sm.Fetches[0].Guides != 4 || len(sm.Server) != 1 {
		t.Fatalf("a profile filter keeps the server's numbers: %+v", sm)
	}

	// live points come from Flush and are scoped like everything else
	l := app.Activity().Begin(httptest.NewRequest("GET", "/x", nil), 11, nil, "News", "proxy", "cdn.a")
	l.Add(1234)
	l.End()
	app.Activity().Flush(false)
	var lv struct {
		Interval int `json:"interval"`
		Samples  []struct {
			T        int64
			Bytes    int64
			Watching int
		} `json:"samples"`
	}
	getJSON(t, h, "/api/stats/live?from=abc&to=1&step=7", ann, &lv) // the range does not apply to live points
	getJSON(t, h, "/api/stats/live", ann, &lv)
	if lv.Interval != 10 || len(lv.Samples) != 1 || lv.Samples[0].Bytes != 1234 || lv.Samples[0].Watching != 1 {
		t.Fatalf("ann live: %+v", lv)
	}
	getJSON(t, h, "/api/stats/live", bob, &lv)
	if len(lv.Samples) != 1 || lv.Samples[0].Bytes != 0 || lv.Samples[0].Watching != 0 {
		t.Fatalf("bob does not see ann's viewers: %+v", lv)
	}
	getJSON(t, h, "/api/stats/live?profile=11", adm, &lv)
	if lv.Samples[0].Bytes != 1234 {
		t.Fatalf("admin by profile: %+v", lv)
	}
	getJSON(t, h, "/api/stats/live?profile=12", adm, &lv)
	if lv.Samples[0].Bytes != 0 {
		t.Fatalf("admin by other profile: %+v", lv)
	}
}
