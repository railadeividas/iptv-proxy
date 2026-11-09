package store

import (
	"fmt"
	"sort"
)

// Window is a period [From, To) cut into N buckets of Bucket seconds. The grid [Start, End) is the period widened to
// whole buckets; what is counted is only ever the exact period.
type Window struct {
	From   int64 `json:"from"`
	To     int64 `json:"to"`
	Start  int64 `json:"start"`
	End    int64 `json:"end"`
	Bucket int64 `json:"bucket"`
	N      int   `json:"n"`
}

// Index is the bucket a time falls in, or -1 outside the window.
func (w Window) Index(t int64) int {
	if t < w.Start || t >= w.End || w.Bucket <= 0 {
		return -1
	}
	i := int((t - w.Start) / w.Bucket)
	if i >= w.N {
		return -1
	}
	return i
}

// TimelineRow is the activity of one profile and kind in one bucket.
type TimelineRow struct {
	I        int    `json:"i"`
	P        int64  `json:"p"`
	Mode     string `json:"mode"`
	Sessions int64  `json:"sessions"`
	Bytes    int64  `json:"bytes"`
	Seconds  int64  `json:"seconds"`
}

// Timeline is the activity over time: sparse rows, the most proxied streams at once per bucket, the devices per bucket.
type Timeline struct {
	Rows    []TimelineRow `json:"rows"`
	Peaks   []int         `json:"peaks"`
	Devices []int         `json:"devices"`
	Peak    struct {
		Max int   `json:"max"`
		At  int64 `json:"at"`
	} `json:"peak"`
}

const deviceExpr = `a.profile_id||'|'||a.ip||'|'||a.ua`

// ActivityTimeline buckets the sessions that started in the filter's period [Since, Until) on the window's grid.
func (s *Store) ActivityTimeline(f ActivityFilter, win Window) (Timeline, error) {
	t := Timeline{Rows: []TimelineRow{}, Peaks: make([]int, win.N), Devices: make([]int, win.N)}
	w, args := f.where("started")
	from := ` FROM activity_sessions a JOIN profiles p ON p.id=a.profile_id` + w

	rows, err := s.DB.Query(`SELECT (a.started-?)/?, a.profile_id, a.mode, COUNT(*), COALESCE(SUM(a.bytes),0),
		COALESCE(SUM(CASE WHEN a.mode='proxy' THEN a.last-a.started END),0)`+from+` GROUP BY 1,2,3 ORDER BY 1,2,3`,
		append([]any{win.Start, win.Bucket}, args...)...)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var x TimelineRow
		if err := rows.Scan(&x.I, &x.P, &x.Mode, &x.Sessions, &x.Bytes, &x.Seconds); err != nil {
			rows.Close()
			return t, err
		}
		if x.I >= 0 && x.I < win.N {
			t.Rows = append(t.Rows, x)
		}
	}
	if err := rows.Close(); err != nil {
		return t, err
	}

	drows, err := s.DB.Query(`SELECT (a.started-?)/?, COUNT(DISTINCT `+deviceExpr+`)`+from+` GROUP BY 1`, append([]any{win.Start, win.Bucket}, args...)...)
	if err != nil {
		return t, err
	}
	for drows.Next() {
		var i, n int
		if err := drows.Scan(&i, &n); err != nil {
			drows.Close()
			return t, err
		}
		if i >= 0 && i < win.N {
			t.Devices[i] = n
		}
	}
	if err := drows.Close(); err != nil {
		return t, err
	}

	// streams at the same moment: a proxied session occupies [started, last]
	lo, hi := win.From, win.To
	pf := ActivityFilter{Owner: f.Owner, Profile: f.Profile, Profiles: f.Profiles}
	pw, pargs := pf.where("started")
	prows, err := s.DB.Query(`SELECT a.started, a.last FROM activity_sessions a JOIN profiles p ON p.id=a.profile_id`+pw+
		` AND a.mode='proxy' AND a.started<? AND a.last>=?`, append(pargs, hi, lo)...)
	if err != nil {
		return t, err
	}
	type event struct {
		at    int64
		delta int
	}
	var ev []event
	for prows.Next() {
		var a, b int64
		if err := prows.Scan(&a, &b); err != nil {
			prows.Close()
			return t, err
		}
		if b < a {
			b = a
		}
		ev = append(ev, event{a, 1}, event{b + 1, -1})
	}
	if err := prows.Close(); err != nil {
		return t, err
	}
	sort.Slice(ev, func(i, j int) bool {
		if ev[i].at != ev[j].at {
			return ev[i].at < ev[j].at
		}
		return ev[i].delta < ev[j].delta // leaving before arriving at the same second
	})
	cur := 0
	for k := 0; k < len(ev); {
		at := ev[k].at
		for k < len(ev) && ev[k].at == at {
			cur += ev[k].delta
			k++
		}
		if cur <= 0 || k >= len(ev) {
			continue
		}
		from, to := at, ev[k].at // the count holds in [from, to)
		if from < lo {
			from = lo
		}
		if to > hi {
			to = hi
		}
		if to <= from {
			continue
		}
		if cur > t.Peak.Max {
			t.Peak.Max, t.Peak.At = cur, from
		}
		for i := win.Index(from); i >= 0 && i <= win.Index(to-1); i++ {
			if cur > t.Peaks[i] {
				t.Peaks[i] = cur
			}
		}
	}
	return t, nil
}

// HeatCell is the activity at one weekday (0 = Sunday) and hour of the day.
type HeatCell struct {
	D        int   `json:"d"`
	H        int   `json:"h"`
	Sessions int64 `json:"sessions"`
	Bytes    int64 `json:"bytes"`
	Seconds  int64 `json:"seconds"`
}

// LongestSession is the longest proxied session of a period.
type LongestSession struct {
	Seconds int64  `json:"seconds"`
	CK      string `json:"ck"`
	Name    string `json:"name"`
	Profile string `json:"profile"`
	Started int64  `json:"started"`
}

// Patterns are the busiest hours, how long sessions last and the longest one.
type Patterns struct {
	Heat      []HeatCell      `json:"heat"`
	Durations [6]int64        `json:"durations"` // <5 min, <15, <30, <60, <120, longer
	Longest   *LongestSession `json:"longest"`
}

// ActivityPatterns summarises the sessions of the filter's period; tz is the viewer's offset from UTC in seconds.
func (s *Store) ActivityPatterns(f ActivityFilter, tz int64) (Patterns, error) {
	p := Patterns{Heat: []HeatCell{}}
	w, args := f.where("started")
	from := ` FROM activity_sessions a JOIN profiles p ON p.id=a.profile_id` + w

	rows, err := s.DB.Query(`SELECT ((a.started+?)/86400+4)%7, ((a.started+?)%86400)/3600, COUNT(*), COALESCE(SUM(a.bytes),0),
		COALESCE(SUM(CASE WHEN a.mode='proxy' THEN a.last-a.started END),0)`+from+` GROUP BY 1,2 ORDER BY 1,2`, append([]any{tz, tz}, args...)...)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var x HeatCell
		if err := rows.Scan(&x.D, &x.H, &x.Sessions, &x.Bytes, &x.Seconds); err != nil {
			rows.Close()
			return p, err
		}
		p.Heat = append(p.Heat, x)
	}
	if err := rows.Close(); err != nil {
		return p, err
	}

	dur := `(a.last-a.started)`
	var d [6]int64
	err = s.DB.QueryRow(`SELECT COALESCE(SUM(`+dur+`<300),0), COALESCE(SUM(`+dur+`>=300 AND `+dur+`<900),0), COALESCE(SUM(`+dur+`>=900 AND `+dur+`<1800),0),
		COALESCE(SUM(`+dur+`>=1800 AND `+dur+`<3600),0), COALESCE(SUM(`+dur+`>=3600 AND `+dur+`<7200),0), COALESCE(SUM(`+dur+`>=7200),0)`+
		from+` AND a.mode='proxy'`, args...).Scan(&d[0], &d[1], &d[2], &d[3], &d[4], &d[5])
	if err != nil {
		return p, err
	}
	p.Durations = d

	var l LongestSession
	err = s.DB.QueryRow(`SELECT a.last-a.started, a.ck, p.name, a.started`+from+` AND a.mode='proxy' ORDER BY 1 DESC, a.id LIMIT 1`, args...).
		Scan(&l.Seconds, &l.CK, &l.Profile, &l.Started)
	if err == nil {
		p.Longest = &l
	}
	return p, nil
}

// BreakdownItem is one row of a ranking.
type BreakdownItem struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Sessions int64  `json:"sessions"`
	Proxy    int64  `json:"proxy"`
	Redirect int64  `json:"redirect"`
	Bytes    int64  `json:"bytes"`
	Seconds  int64  `json:"seconds"`
	Viewers  int64  `json:"viewers"` // different devices
	Last     int64  `json:"last"`
}

// breakdownKeys maps what to rank by to the SQL expression that groups it.
var breakdownKeys = map[string]string{
	"channel": `a.ck`,
	"profile": `CAST(a.profile_id AS TEXT)`,
	"player":  `a.ua`,
	"network": `a.ip`,
	"host":    `a.host`,
}

// ValidBreakdown says whether by is a known ranking.
func ValidBreakdown(by string) bool { _, ok := breakdownKeys[by]; return ok }

var breakdownSorts = map[string]string{"sessions": "2", "bytes": "5", "seconds": "6"}

// ValidBreakdownSort says whether sort is a known order.
func ValidBreakdownSort(sort string) bool { _, ok := breakdownSorts[sort]; return ok }

// ActivityBreakdown ranks sessions by channel, profile, player, network or stream host, one page, with the number of rows.
// For a profile the name is the profile's name; otherwise it is the key (the caller names channels).
func (s *Store) ActivityBreakdown(f ActivityFilter, by, sortBy string, limit, offset int) ([]BreakdownItem, int, error) {
	key, ok := breakdownKeys[by]
	if !ok {
		return nil, 0, fmt.Errorf("unknown ranking %q", by)
	}
	col, ok := breakdownSorts[sortBy]
	if !ok {
		col = breakdownSorts["sessions"]
	}
	w, args := f.where("started")
	from := ` FROM activity_sessions a JOIN profiles p ON p.id=a.profile_id` + w
	var total int
	if err := s.DB.QueryRow(`SELECT COUNT(DISTINCT `+key+`)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(`SELECT `+key+`, COUNT(*), COALESCE(SUM(a.mode='proxy'),0), COALESCE(SUM(a.mode='redirect'),0), COALESCE(SUM(a.bytes),0),
		COALESCE(SUM(CASE WHEN a.mode='proxy' THEN a.last-a.started ELSE 0 END),0), COUNT(DISTINCT `+deviceExpr+`), MAX(a.last), MAX(p.name)`+from+
		` GROUP BY 1 ORDER BY `+col+` DESC, 2 DESC, 5 DESC, 1 LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []BreakdownItem{}
	for rows.Next() {
		var x BreakdownItem
		var pname string
		if err := rows.Scan(&x.Key, &x.Sessions, &x.Proxy, &x.Redirect, &x.Bytes, &x.Seconds, &x.Viewers, &x.Last, &pname); err != nil {
			return nil, 0, err
		}
		x.Name = x.Key
		if by == "profile" {
			x.Name = pname
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

// ---- hourly samples ----

// ServerSample is the increase of the proxy's counters over some time.
type ServerSample struct {
	CacheHits     int64 `json:"cache_hits"`
	CacheMisses   int64 `json:"cache_misses"`
	CacheHitBytes int64 `json:"cache_hit_bytes"`
	Retries       int64 `json:"retries"`
	Holdovers     int64 `json:"holdovers"`
}

func (x ServerSample) zero() bool { return x == ServerSample{} }

// ProfileSample is the increase of the playlist and guide fetches of a profile.
type ProfileSample struct {
	Playlists int64 `json:"playlists"`
	Guides    int64 `json:"guides"`
}

// AddSamples adds increases to the rows of an hour (a UTC hour start): the server's and each profile's.
// A profile deleted meanwhile is skipped.
func (s *Store) AddSamples(hour int64, srv ServerSample, per map[int64]ProfileSample) {
	if srv.zero() && len(per) == 0 {
		return
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if !srv.zero() {
		_, _ = tx.Exec(`INSERT INTO stats_samples(hour,cache_hits,cache_misses,cache_hit_bytes,retries,holdovers) VALUES(?,?,?,?,?,?)
			ON CONFLICT(hour) DO UPDATE SET cache_hits=cache_hits+excluded.cache_hits, cache_misses=cache_misses+excluded.cache_misses,
			cache_hit_bytes=cache_hit_bytes+excluded.cache_hit_bytes, retries=retries+excluded.retries, holdovers=holdovers+excluded.holdovers`,
			hour, srv.CacheHits, srv.CacheMisses, srv.CacheHitBytes, srv.Retries, srv.Holdovers)
	}
	for id, p := range per {
		if p == (ProfileSample{}) {
			continue
		}
		_, _ = tx.Exec(`INSERT INTO stats_profile_samples(hour,profile_id,playlists,guides) SELECT ?,id,?,? FROM profiles WHERE id=?
			ON CONFLICT(hour,profile_id) DO UPDATE SET playlists=playlists+excluded.playlists, guides=guides+excluded.guides`, hour, p.Playlists, p.Guides, id)
	}
	_ = tx.Commit()
}

// FetchBucket is the playlist and guide fetches in one bucket.
type FetchBucket struct {
	I         int   `json:"i"`
	Playlists int64 `json:"playlists"`
	Guides    int64 `json:"guides"`
}

// FetchTotal is the playlist and guide fetches of one profile in the filter's period.
type FetchTotal struct {
	ProfileID int64 `json:"profile_id"`
	Playlists int64 `json:"playlists"`
	Guides    int64 `json:"guides"`
}

// ActivityFetchTotals sums the stored fetches per profile (those in scope that have any) over the filter's period [Since, Until).
func (s *Store) ActivityFetchTotals(f ActivityFilter) ([]FetchTotal, error) {
	f.Mode = ""
	w, args := f.where("hour")
	rows, err := s.DB.Query(`SELECT a.profile_id, SUM(a.playlists), SUM(a.guides) FROM stats_profile_samples a JOIN profiles p ON p.id=a.profile_id`+w+
		` GROUP BY a.profile_id ORDER BY a.profile_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FetchTotal{}
	for rows.Next() {
		var x FetchTotal
		if err := rows.Scan(&x.ProfileID, &x.Playlists, &x.Guides); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ServerBucket is the proxy's cache and retry counters' increase in one bucket.
type ServerBucket struct {
	I int `json:"i"`
	ServerSample
}

// ActivityFetches sums the stored playlist and guide fetches of the profiles in scope, by hour in the filter's period
// [Since, Until), per bucket of the window.
func (s *Store) ActivityFetches(f ActivityFilter, win Window) ([]FetchBucket, error) {
	f.Mode = ""
	w, args := f.where("hour")
	rows, err := s.DB.Query(`SELECT (a.hour-?)/?, SUM(a.playlists), SUM(a.guides) FROM stats_profile_samples a JOIN profiles p ON p.id=a.profile_id`+w+
		` GROUP BY 1 ORDER BY 1`, append([]any{win.Start, win.Bucket}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FetchBucket{}
	for rows.Next() {
		var x FetchBucket
		if err := rows.Scan(&x.I, &x.Playlists, &x.Guides); err != nil {
			return nil, err
		}
		if x.I >= 0 && x.I < win.N {
			out = append(out, x)
		}
	}
	return out, rows.Err()
}

// ActivityServer sums the stored server counters of the hours in [from, to) per bucket of the window.
func (s *Store) ActivityServer(from, to int64, win Window) ([]ServerBucket, error) {
	rows, err := s.DB.Query(`SELECT (hour-?)/?, SUM(cache_hits), SUM(cache_misses), SUM(cache_hit_bytes), SUM(retries), SUM(holdovers)
		FROM stats_samples WHERE hour>=? AND hour<? GROUP BY 1 ORDER BY 1`, win.Start, win.Bucket, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServerBucket{}
	for rows.Next() {
		var x ServerBucket
		if err := rows.Scan(&x.I, &x.CacheHits, &x.CacheMisses, &x.CacheHitBytes, &x.Retries, &x.Holdovers); err != nil {
			return nil, err
		}
		if x.I >= 0 && x.I < win.N {
			out = append(out, x)
		}
	}
	return out, rows.Err()
}
