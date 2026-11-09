package store

import (
	"reflect"
	"testing"
)

// statsFixture: two owners, three profiles and a set of sessions with known times (bucket = 1 day, window = 3 days).
// Day 0 is 1970-01-05 00:00 UTC (a Monday), so a viewer at UTC+2 sees the sessions two hours later.
func statsFixture(t *testing.T) (*Store, Window) {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.DB.Exec(`INSERT INTO users(id,username,password,role,created_at) VALUES(1,'ann','x','user',1),(2,'bob','x','user',1)`)
	st.DB.Exec(`INSERT INTO profiles(id,owner_id,name,token,created_at) VALUES(1,1,'Ann TV','t1',1),(2,1,'Ann Phone','t2',1),(3,2,'Bob TV','t3',1)`)
	st.DB.Exec(`INSERT INTO entries(ck,name,nsfw,closed,src) VALUES('News','News One',0,0,0)`)
	const day = 86400
	d0 := int64(4 * day) // Monday 00:00 UTC
	add := func(p int64, ck, mode, host, ip, ua string, start, last, bytes int64) {
		st.AddSession(Session{ProfileID: p, CK: ck, Mode: mode, Host: host, IP: ip, UA: ua, Started: start, Last: last, Bytes: bytes, Requests: 1})
	}
	// day 0: Ann TV watches News 22:30-23:30 UTC the day before (outside), and 10:00-10:10; Bob redirected at 23:30
	add(1, "News", "proxy", "cdn.a", "10.0.0.0/24", "VLC", d0+10*3600, d0+10*3600+600, 1000)
	add(3, "Sport", "redirect", "cdn.b", "10.0.1.0/24", "Kodi", d0+23*3600+1800, d0+23*3600+1800, 0)
	// day 1: Ann Phone 10:05-10:35 overlaps nothing on day 1; Ann TV 20:00-22:00 and 21:00-21:30 (two at once)
	add(2, "News", "proxy", "cdn.a", "10.0.0.0/24", "VLC", d0+day+10*3600+300, d0+day+10*3600+2100, 2000)
	add(1, "Movie", "proxy", "cdn.b", "10.0.0.0/24", "VLC", d0+day+20*3600, d0+day+22*3600, 9000)
	add(3, "Movie", "proxy", "cdn.b", "10.0.2.0/24", "TiviMate", d0+day+21*3600, d0+day+21*3600+1800, 4000)
	// day 2: one long session ending in the next day's first second after the window
	add(1, "News", "proxy", "cdn.a", "10.0.0.0/24", "Kodi", d0+2*day+23*3600, d0+3*day+100, 500)
	return st, Window{From: d0, To: d0 + 3*day, Start: d0, End: d0 + 3*day, Bucket: day, N: 3}
}

// inWin narrows a filter to the exact period of a window.
func inWin(f ActivityFilter, w Window) ActivityFilter {
	f.Since, f.Until = w.From, w.To
	return f
}

func TestActivityTimelineBuckets(t *testing.T) {
	st, win := statsFixture(t)
	tl, err := st.ActivityTimeline(inWin(ActivityFilter{}, win), win)
	if err != nil {
		t.Fatal(err)
	}
	type k struct {
		i    int
		p    int64
		mode string
	}
	got := map[k]TimelineRow{}
	for _, r := range tl.Rows {
		got[k{r.I, r.P, r.Mode}] = r
	}
	if len(tl.Rows) != 6 || got[k{0, 1, "proxy"}].Seconds != 600 || got[k{0, 3, "redirect"}].Sessions != 1 || got[k{0, 3, "redirect"}].Seconds != 0 ||
		got[k{1, 1, "proxy"}].Bytes != 9000 || got[k{1, 1, "proxy"}].Seconds != 7200 || got[k{2, 1, "proxy"}].Sessions != 1 {
		t.Fatalf("rows: %+v", tl.Rows)
	}
	// a session that started before the window is not counted; sessions after it neither
	if got[k{0, 2, "proxy"}].Sessions != 0 {
		t.Fatal("nothing for Ann Phone on day 0")
	}
	if !reflect.DeepEqual(tl.Devices, []int{2, 3, 1}) {
		t.Fatalf("devices per bucket: %v", tl.Devices)
	}
	// streams at the same time: day 1 has Ann TV 20:00-22:00 with Bob 21:00-21:30
	if !reflect.DeepEqual(tl.Peaks, []int{1, 2, 1}) {
		t.Fatalf("peaks: %v", tl.Peaks)
	}
	if tl.Peak.Max != 2 || tl.Peak.At != win.Start+86400+21*3600 {
		t.Fatalf("peak: %+v", tl.Peak)
	}

	// scoped: bob sees only his profile; a profile filter narrows; the mode filter narrows the rows but not the peaks
	tl, _ = st.ActivityTimeline(inWin(ActivityFilter{Owner: 2}, win), win)
	if len(tl.Rows) != 2 || tl.Peak.Max != 1 {
		t.Fatalf("bob: %+v", tl)
	}
	tl, _ = st.ActivityTimeline(inWin(ActivityFilter{Owner: 1, Profile: 2}, win), win)
	if len(tl.Rows) != 1 || tl.Rows[0].I != 1 || tl.Peak.Max != 1 || tl.Peak.At != win.Start+86400+10*3600+300 {
		t.Fatalf("one profile: %+v", tl)
	}
	tl, _ = st.ActivityTimeline(inWin(ActivityFilter{Mode: "redirect"}, win), win)
	if len(tl.Rows) != 1 || tl.Peak.Max != 2 {
		t.Fatalf("mode: %+v", tl)
	}
}

// The sweep counts a session from its first to its last second; one leaving at the second another arrives does not overlap.
func TestActivityTimelinePeaksEdges(t *testing.T) {
	st, win := statsFixture(t)
	st.DB.Exec(`DELETE FROM activity_sessions`)
	s := win.Start + 100
	st.AddSession(Session{ProfileID: 1, CK: "A", Mode: "proxy", Started: s, Last: s + 49})        // [s, s+49]
	st.AddSession(Session{ProfileID: 1, CK: "B", Mode: "proxy", Started: s + 50, Last: s + 90})   // starts right after
	st.AddSession(Session{ProfileID: 1, CK: "C", Mode: "proxy", Started: s - 90000, Last: s + 5}) // began before the window
	tl, _ := st.ActivityTimeline(inWin(ActivityFilter{}, win), win)
	if tl.Peaks[0] != 2 || tl.Peak.Max != 2 || tl.Peak.At != s {
		t.Fatalf("edges: %+v", tl)
	}
	// the window after: a stream still running at its start counts in the bucket where it is running
	later := Window{From: win.Start + 86400, To: win.End + 86400, Start: win.Start + 86400, End: win.End + 86400, Bucket: 86400, N: 3}
	tl, _ = st.ActivityTimeline(inWin(ActivityFilter{}, later), later)
	if tl.Peak.Max != 0 || tl.Peak.At != 0 {
		t.Fatalf("no streams: %+v", tl)
	}
}

func TestActivityPatterns(t *testing.T) {
	st, win := statsFixture(t)
	f := ActivityFilter{Since: win.Start, Until: win.End}
	p, err := st.ActivityPatterns(f, 0)
	if err != nil {
		t.Fatal(err)
	}
	cell := func(p Patterns, d, h int) HeatCell {
		for _, c := range p.Heat {
			if c.D == d && c.H == h {
				return c
			}
		}
		return HeatCell{}
	}
	// day 0 is a Monday (1), day 1 a Tuesday (2)
	if cell(p, 1, 10).Sessions != 1 || cell(p, 1, 23).Sessions != 1 || cell(p, 2, 20).Bytes != 9000 || cell(p, 2, 21).Sessions != 1 || len(p.Heat) != 6 {
		t.Fatalf("heat in UTC: %+v", p.Heat)
	}
	// a viewer at UTC+2 sees Bob's Monday 23:30 on Tuesday 01:30
	p, _ = st.ActivityPatterns(f, 2*3600)
	if cell(p, 2, 1).Sessions != 1 || cell(p, 1, 12).Sessions != 1 {
		t.Fatalf("heat at UTC+2: %+v", p.Heat)
	}
	p, _ = st.ActivityPatterns(f, -5*3600) // Monday 10:00 UTC is 05:00, Monday 23:30 UTC is 18:30
	if cell(p, 1, 5).Sessions != 1 || cell(p, 1, 18).Sessions != 1 {
		t.Fatalf("heat at UTC-5: %+v", p.Heat)
	}
	// proxied sessions by length: 600 s, 1800 s twice, 3700 s, 7200 s
	if p.Durations != [6]int64{0, 1, 0, 2, 1, 1} {
		t.Fatalf("durations: %v", p.Durations)
	}
	if p.Longest == nil || p.Longest.CK != "Movie" || p.Longest.Profile != "Ann TV" || p.Longest.Seconds != 7200 {
		t.Fatalf("longest: %+v", p.Longest)
	}
	empty, _ := st.ActivityPatterns(ActivityFilter{Owner: 2, Mode: "proxy", Since: 1, Until: 2}, 0)
	if empty.Longest != nil || len(empty.Heat) != 0 || empty.Heat == nil {
		t.Fatalf("empty: %+v", empty)
	}
}

func TestActivityBreakdown(t *testing.T) {
	st, _ := statsFixture(t)
	by := func(f ActivityFilter, by, sort string, limit, offset int) ([]BreakdownItem, int) {
		t.Helper()
		items, total, err := st.ActivityBreakdown(f, by, sort, limit, offset)
		if err != nil {
			t.Fatal(err)
		}
		return items, total
	}
	items, total := by(ActivityFilter{}, "channel", "sessions", 10, 0)
	if total != 3 || items[0].Key != "News" || items[0].Sessions != 3 || items[0].Proxy != 3 || items[0].Viewers != 3 || items[0].Bytes != 3500 || items[0].Seconds != 600+1800+3700 {
		t.Fatalf("channels: %+v", items)
	}
	// by data: Movie (13000) first; the order of equal values is stable
	items, _ = by(ActivityFilter{}, "channel", "bytes", 10, 0)
	if items[0].Key != "Movie" || items[1].Key != "News" || items[2].Key != "Sport" {
		t.Fatalf("by bytes: %+v", items)
	}
	items, _ = by(ActivityFilter{}, "channel", "seconds", 1, 1)
	if len(items) != 1 || items[0].Key != "News" {
		t.Fatalf("page: %+v", items)
	}
	items, total = by(ActivityFilter{}, "profile", "sessions", 10, 0)
	if total != 3 || items[0].Key != "1" || items[0].Name != "Ann TV" || items[0].Sessions != 3 || items[2].Name != "Ann Phone" {
		t.Fatalf("profiles: %+v", items)
	}
	items, total = by(ActivityFilter{}, "player", "sessions", 10, 0)
	if total != 3 || items[0].Key != "VLC" || items[0].Name != "VLC" || items[0].Sessions != 3 {
		t.Fatalf("players: %+v", items)
	}
	items, total = by(ActivityFilter{}, "network", "sessions", 10, 0)
	if total != 3 || items[0].Key != "10.0.0.0/24" || items[0].Sessions != 4 {
		t.Fatalf("networks: %+v", items)
	}
	items, total = by(ActivityFilter{}, "host", "bytes", 10, 0)
	if total != 2 || items[0].Key != "cdn.b" || items[0].Bytes != 13000 || items[1].Key != "cdn.a" {
		t.Fatalf("hosts: %+v", items)
	}
	// scope and filters
	if _, total = by(ActivityFilter{Owner: 2}, "channel", "sessions", 10, 0); total != 2 {
		t.Fatalf("bob sees his channels only: %d", total)
	}
	items, total = by(ActivityFilter{Mode: "redirect"}, "profile", "sessions", 10, 0)
	if total != 1 || items[0].Redirect != 1 || items[0].Proxy != 0 {
		t.Fatalf("redirect only: %+v", items)
	}
	if _, _, err := st.ActivityBreakdown(ActivityFilter{}, "nope", "sessions", 10, 0); err == nil {
		t.Fatal("unknown ranking")
	}
	if !ValidBreakdown("host") || ValidBreakdown("x") || !ValidBreakdownSort("bytes") || ValidBreakdownSort("x") {
		t.Fatal("validators")
	}
}

func TestActivitySamplesPerBucket(t *testing.T) {
	st, win := statsFixture(t)
	h := func(day int, hour int) int64 { return win.Start + int64(day)*86400 + int64(hour)*3600 }
	st.AddSamples(h(0, 3), ServerSample{CacheHits: 10, CacheMisses: 2}, map[int64]ProfileSample{1: {Playlists: 1}, 3: {Guides: 2}})
	st.AddSamples(h(0, 4), ServerSample{CacheHits: 5, Retries: 1}, map[int64]ProfileSample{1: {Playlists: 1, Guides: 1}})
	st.AddSamples(h(2, 23), ServerSample{Holdovers: 4, CacheHitBytes: 99}, map[int64]ProfileSample{2: {Guides: 7}})
	st.AddSamples(h(3, 0), ServerSample{CacheHits: 1000}, nil) // after the window
	f, err := st.ActivityFetches(inWin(ActivityFilter{}, win), win)
	if err != nil || !reflect.DeepEqual(f, []FetchBucket{{0, 2, 3}, {2, 0, 7}}) {
		t.Fatalf("fetches: %+v %v", f, err)
	}
	f, _ = st.ActivityFetches(inWin(ActivityFilter{Owner: 2}, win), win)
	if !reflect.DeepEqual(f, []FetchBucket{{0, 0, 2}}) {
		t.Fatalf("bob's fetches: %+v", f)
	}
	f, _ = st.ActivityFetches(inWin(ActivityFilter{Owner: 1, Profile: 2}, win), win)
	if !reflect.DeepEqual(f, []FetchBucket{{2, 0, 7}}) {
		t.Fatalf("one profile's fetches: %+v", f)
	}
	tot, err := st.ActivityFetchTotals(inWin(ActivityFilter{}, win))
	if err != nil || !reflect.DeepEqual(tot, []FetchTotal{{1, 2, 1}, {2, 0, 7}, {3, 0, 2}}) {
		t.Fatalf("fetch totals: %+v %v", tot, err)
	}
	tot, _ = st.ActivityFetchTotals(inWin(ActivityFilter{Owner: 2}, win))
	if !reflect.DeepEqual(tot, []FetchTotal{{3, 0, 2}}) {
		t.Fatalf("bob's fetch totals: %+v", tot)
	}
	srv, err := st.ActivityServer(win.From, win.To, win)
	if err != nil || len(srv) != 2 || srv[0].I != 0 || srv[0].CacheHits != 15 || srv[0].CacheMisses != 2 || srv[0].Retries != 1 || srv[1].I != 2 || srv[1].Holdovers != 4 || srv[1].CacheHitBytes != 99 {
		t.Fatalf("server: %+v %v", srv, err)
	}
	// hourly buckets: the hour rows are told apart
	hourly := Window{From: h(0, 0), To: h(1, 0), Start: h(0, 0), End: h(1, 0), Bucket: 3600, N: 24}
	srv, _ = st.ActivityServer(hourly.From, hourly.To, hourly)
	if len(srv) != 2 || srv[0].I != 3 || srv[1].I != 4 || srv[1].Retries != 1 {
		t.Fatalf("hourly: %+v", srv)
	}
}

func TestWindowIndex(t *testing.T) {
	w := Window{Start: 100, End: 400, Bucket: 100, N: 3}
	for in, want := range map[int64]int{99: -1, 100: 0, 199: 0, 200: 1, 399: 2, 400: -1} {
		if got := w.Index(in); got != want {
			t.Fatalf("Index(%d)=%d want %d", in, got, want)
		}
	}
}

// Only the grid is widened to whole buckets: sessions, devices and the peak are counted inside the exact period.
func TestActivityTimelineExactRange(t *testing.T) {
	st, win := statsFixture(t)
	// 12:00 on day 1 to 21:15 on day 1, on a day grid that starts at day 0 and ends after day 2
	from, to := win.Start+86400+12*3600, win.Start+86400+21*3600+900
	w := Window{From: from, To: to, Start: win.Start + 86400, End: win.Start + 2*86400, Bucket: 86400, N: 1}
	tl, err := st.ActivityTimeline(inWin(ActivityFilter{}, w), w)
	if err != nil {
		t.Fatal(err)
	}
	// Ann TV 20:00-22:00 and Bob 21:00-21:30 start inside; Ann Phone's 10:05 is outside
	if len(tl.Rows) != 2 || tl.Rows[0].Sessions != 1 || tl.Devices[0] != 2 {
		t.Fatalf("rows: %+v devices %v", tl.Rows, tl.Devices)
	}
	// the peak is clipped to the range too: both run until 21:15 at least, so two streams, from 21:00
	if tl.Peak.Max != 2 || tl.Peak.At != win.Start+86400+21*3600 || tl.Peaks[0] != 2 {
		t.Fatalf("peak: %+v %v", tl.Peak, tl.Peaks)
	}
	// a range that ends before Bob joins only sees one stream
	w.To = win.Start + 86400 + 20*3600 + 1800
	tl, _ = st.ActivityTimeline(inWin(ActivityFilter{}, w), w)
	if tl.Peak.Max != 1 || tl.Rows[0].Sessions != 1 {
		t.Fatalf("shorter range: %+v", tl)
	}
	// a stream that began before the range is in its peak, but not among its sessions
	w = Window{From: win.Start + 86400 + 21*3600 + 1200, To: win.Start + 86400 + 23*3600, Start: win.Start + 86400, End: win.Start + 2*86400, Bucket: 86400, N: 1}
	tl, _ = st.ActivityTimeline(inWin(ActivityFilter{}, w), w)
	if tl.Peak.Max != 2 || tl.Peak.At != w.From || len(tl.Rows) != 0 {
		t.Fatalf("before the range: %+v", tl)
	}
}

// Samples are cut by their hour in the exact range.
func TestActivitySamplesExactRange(t *testing.T) {
	st, win := statsFixture(t)
	h := func(hour int) int64 { return win.Start + int64(hour)*3600 }
	st.AddSamples(h(2), ServerSample{CacheHits: 1}, map[int64]ProfileSample{1: {Playlists: 1}})
	st.AddSamples(h(3), ServerSample{CacheHits: 10}, map[int64]ProfileSample{1: {Playlists: 10}})
	st.AddSamples(h(5), ServerSample{CacheHits: 100}, map[int64]ProfileSample{1: {Playlists: 100}})
	w := Window{From: h(3), To: h(5), Start: h(0), End: h(6), Bucket: 3 * 3600, N: 2}
	f, _ := st.ActivityFetches(inWin(ActivityFilter{}, w), w)
	srv, _ := st.ActivityServer(w.From, w.To, w)
	if len(f) != 1 || f[0].I != 1 || f[0].Playlists != 10 || len(srv) != 1 || srv[0].CacheHits != 10 {
		t.Fatalf("[from, to) by hour: %+v %+v", f, srv)
	}
}

// A selection of several profiles counts exactly their sessions, whichever way the filter is built.
func TestActivityFilterProfileSelection(t *testing.T) {
	st, win := statsFixture(t)
	all, err := st.ActivityTotals(inWin(ActivityFilter{}, win))
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	for _, id := range []int64{1, 2, 3} {
		one, err := st.ActivityTotals(inWin(ActivityFilter{Profile: id}, win))
		if err != nil {
			t.Fatal(err)
		}
		viaList, _ := st.ActivityTotals(inWin(ActivityFilter{Profiles: []int64{id}}, win))
		if one.Sessions != viaList.Sessions || one.Bytes != viaList.Bytes {
			t.Fatalf("profile %d: one %+v, as a list %+v", id, one, viaList)
		}
		sum += one.Sessions
	}
	if sum != all.Sessions {
		t.Fatalf("the profiles add up to %d, everybody has %d", sum, all.Sessions)
	}
	two, _ := st.ActivityTotals(inWin(ActivityFilter{Profiles: []int64{1, 3}}, win))
	p1, _ := st.ActivityTotals(inWin(ActivityFilter{Profile: 1}, win))
	p3, _ := st.ActivityTotals(inWin(ActivityFilter{Profile: 3}, win))
	if two.Sessions != p1.Sessions+p3.Sessions || two.Bytes != p1.Bytes+p3.Bytes {
		t.Fatalf("profiles 1 and 3: %+v, parts %+v %+v", two, p1, p3)
	}
	f := ActivityFilter{Profiles: []int64{1, 3}}
	if !f.Includes(1) || f.Includes(2) || !(ActivityFilter{}).Includes(2) || !(ActivityFilter{Profile: 2}).Includes(2) || (ActivityFilter{Profile: 2}).Includes(1) {
		t.Fatal("Includes")
	}
	tl, err := st.ActivityTimeline(inWin(ActivityFilter{Profiles: []int64{1, 3}}, win), win)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range tl.Rows {
		if r.P == 2 {
			t.Fatalf("profile 2 must not appear: %+v", r)
		}
	}
}
