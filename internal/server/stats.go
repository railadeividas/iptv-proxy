package server

import (
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"iptv-proxy/internal/auth"
	"iptv-proxy/internal/store"
)

// maxTZ bounds the viewer's offset from UTC (seconds east), the largest real zone being UTC+14.
const maxTZ = 14 * 3600

const (
	defaultRange = 7 * 86400
	maxRange     = 3650 * 86400
	autoBuckets  = 120 // automatic step: the smallest one that gives at most this many buckets
	maxBuckets   = 500 // a requested step is raised until it gives at most this many
	weekStep     = 604800
)

// bucketSteps are the bucket sizes (seconds) a chart can use.
var bucketSteps = []int64{300, 900, 3600, 86400, weekStep}

func posMod(a, m int64) int64 {
	r := a % m
	if r < 0 {
		r += m
	}
	return r
}

// bucketGrid cuts [from, to) into buckets of step seconds aligned to the viewer's zone: a boundary b satisfies
// (b+tz) % step == 0, and weeks start on Monday (1970-01-05 was one) in that zone. The grid widens the period to
// whole buckets (start = from floored, end = to ceiled); the zone is a fixed offset, so there is no daylight saving.
func bucketGrid(from, to, step, tz int64) store.Window {
	off := tz
	if step == weekStep {
		off = tz - 4*86400
	}
	start := from - posMod(from+off, step)
	end := to
	if r := posMod(to+off, step); r != 0 {
		end = to + step - r
	}
	return store.Window{From: from, To: to, Start: start, End: end, Bucket: step, N: int((end - start) / step)}
}

// statsFilter reads the range and the other filters from the query:
//   - from, to: unix seconds, [from, to); to defaults to now (this second included) and from to a week before to. A negative from is 0;
//     a range that is empty or longer than 3650 days is a 400.
//   - shift=1: the range of the same length right before.
//   - step: bucket size in seconds (300, 900, 3600, 86400 or 604800; anything else is a 400); 0 or none picks the smallest
//     that gives at most 120 buckets. A step that would give more than 500 buckets is raised to one that does not.
//   - tz: the viewer's offset from UTC in seconds east (bounded), which aligns the buckets.
//   - profile, mode.
//
// Everything is counted over the exact range; only the bucket grid is aligned. Whoever may not see every profile (see
// owner) only ever gets their own.
func (s *Server) statsFilter(w http.ResponseWriter, r *http.Request) (store.ActivityFilter, store.Window, int64, bool) {
	q := r.URL.Query()
	f := store.ActivityFilter{Owner: owner(r)}
	var win store.Window
	var bad string
	num := func(key string, def int64) int64 {
		v := q.Get(key)
		if v == "" {
			return def
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil && bad == "" {
			bad = key + " must be a whole number of seconds"
		}
		return n
	}
	tz := min(max(num("tz", 0), -maxTZ), maxTZ)
	to := num("to", time.Now().Unix()+1) // without one, up to and including this second
	from := num("from", to-defaultRange)
	step := num("step", 0)
	switch {
	case bad != "":
		httpErr(w, 400, bad)
		return f, win, tz, false
	case step != 0 && !slices.Contains(bucketSteps, step):
		httpErr(w, 400, "step must be 300, 900, 3600, 86400 or 604800 seconds")
		return f, win, tz, false
	}
	if q.Get("shift") == "1" {
		from, to = from-(to-from), from
	}
	from = max(from, 0)
	if to <= from {
		httpErr(w, 400, "to must be after from")
		return f, win, tz, false
	}
	if to-from > maxRange {
		httpErr(w, 400, "the range is longer than 3650 days")
		return f, win, tz, false
	}
	if step == 0 {
		step = bucketSteps[len(bucketSteps)-1]
		for _, st := range bucketSteps {
			if bucketGrid(from, to, st, tz).N <= autoBuckets {
				step = st
				break
			}
		}
	} else if bucketGrid(from, to, step, tz).N > maxBuckets {
		raised := bucketSteps[len(bucketSteps)-1]
		for _, st := range bucketSteps {
			if st > step && bucketGrid(from, to, st, tz).N <= maxBuckets {
				raised = st
				break
			}
		}
		step = raised
	}
	win = bucketGrid(from, to, step, tz)
	f.Since, f.Until = from, to
	if !s.scopeProfile(w, r, &f) {
		return f, win, tz, false
	}
	if m := q.Get("mode"); m == "proxy" || m == "redirect" {
		f.Mode = m
	}
	return f, win, tz, true
}

// maxProfileSelection bounds how many profiles one request may name.
const maxProfileSelection = 200

// scopeProfile applies the profile filter of the query: one id or several separated by commas. Somebody else's profile
// is a 404 for the whole request.
func (s *Server) scopeProfile(w http.ResponseWriter, r *http.Request, f *store.ActivityFilter) bool {
	raw := strings.TrimSpace(r.URL.Query().Get("profile"))
	if raw == "" || raw == "0" {
		return true
	}
	var ids []int64
	for _, part := range strings.Split(raw, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			httpErr(w, 400, "profile must be an id or ids separated by commas")
			return false
		}
		dup := false
		for _, x := range ids {
			dup = dup || x == id
		}
		if !dup {
			ids = append(ids, id)
		}
	}
	if len(ids) > maxProfileSelection {
		httpErr(w, 400, "too many profiles selected")
		return false
	}
	for _, id := range ids {
		p, err := s.st.GetProfile(id)
		if err != nil || (f.Owner != 0 && p.OwnerID != f.Owner) {
			httpErr(w, 404, "profile not found")
			return false
		}
	}
	if len(ids) == 1 {
		f.Profile = ids[0]
	} else {
		f.Profiles = ids
	}
	return true
}

func page(r *http.Request) (limit, offset int) {
	q := r.URL.Query()
	offset, _ = strconv.Atoi(q.Get("offset"))
	limit, _ = strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	return
}

// statsOverview: settings that matter to the page, who is watching now, the totals and the per-profile table.
func (s *Server) statsOverview(w http.ResponseWriter, r *http.Request) {
	f, win, _, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	now := s.act.Now(f.Owner)
	live := now[:0]
	for _, x := range now {
		if f.Includes(x.ProfileID) {
			x.Name = s.st.ChannelName(x.CK)
			live = append(live, x)
		}
	}
	totals, err := s.st.ActivityTotals(f)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	profiles, err := s.st.ActivityProfiles(f)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	out := map[string]any{
		"enabled":      s.set.String("stats_enabled") == "on",
		"ip_mode":      s.set.String("stats_ip_mode"),
		"keeps_agent":  s.set.String("stats_store_user_agent") == "on",
		"window":       win,
		"history_days": s.set.Int("stats_history_days"),
		"live_seconds": s.set.Int("stats_live_seconds"),
		"now":          live,
		"totals":       totals,
		"profiles":     profiles,
	}
	if f.Owner == 0 { // the load of the whole server is for those who see everybody's activity
		out["connections"] = map[string]int{"in_use": s.px.InUse(), "limit": s.set.Int("max_connections")}
		ps := s.px.Stats()
		out["cache"] = map[string]any{"entries": ps.CacheEntries, "bytes": ps.CacheBytes, "limit": ps.CacheLimit, "hits": ps.CacheHits, "misses": ps.CacheMisses,
			"hit_bytes": ps.CacheHitBytes, "prefetched": ps.Prefetched, "coalesced": ps.Coalesced, "retries": ps.Retries, "holdovers": ps.Holdovers,
			"shared_streams": ps.SharedStreams, "shared_viewers": ps.SharedViewers, "shared_joins": ps.SharedJoins, "playlist_hits": ps.PlaylistHits,
			"head_start_streams": ps.HeadStartStreams, "head_start_playlists": ps.HeadStartPlaylists}
	}
	out["all_profiles"] = auth.Can(userFrom(r).Role, auth.AllProfiles)
	writeJSON(w, out)
}

func (s *Server) statsClients(w http.ResponseWriter, r *http.Request) {
	f, _, _, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	limit, offset := page(r)
	items, total, err := s.st.ActivityClients(f, limit, offset)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"total": total, "items": items})
}

func (s *Server) statsSessions(w http.ResponseWriter, r *http.Request) {
	f, _, _, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	limit, offset := page(r)
	items, total, err := s.st.ActivitySessions(f, limit, offset)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	for i := range items {
		items[i].Name = s.st.ChannelName(items[i].CK)
	}
	writeJSON(w, map[string]any{"total": total, "items": items})
}

// statsTimeline: activity per bucket (sparse rows by profile and kind), the most streams at once and the devices per bucket.
func (s *Server) statsTimeline(w http.ResponseWriter, r *http.Request) {
	f, win, _, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	t, err := s.st.ActivityTimeline(f, win)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"bucket": win.Bucket, "start": win.Start, "n": win.N, "rows": t.Rows, "peaks": t.Peaks, "devices": t.Devices, "peak": t.Peak})
}

// statsPatterns: the busiest weekday and hour (in the viewer's zone), how long sessions last, the longest session.
func (s *Server) statsPatterns(w http.ResponseWriter, r *http.Request) {
	f, _, tz, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	p, err := s.st.ActivityPatterns(f, tz)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if p.Longest != nil {
		p.Longest.Name = s.st.ChannelName(p.Longest.CK)
	}
	writeJSON(w, p)
}

// statsBreakdown ranks the sessions by channel, profile, player, network or stream host.
func (s *Server) statsBreakdown(w http.ResponseWriter, r *http.Request) {
	f, _, _, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	by, sortBy := q.Get("by"), q.Get("sort")
	if sortBy == "" {
		sortBy = "sessions"
	}
	if !store.ValidBreakdown(by) || !store.ValidBreakdownSort(sortBy) {
		httpErr(w, 400, "unknown ranking")
		return
	}
	limit, offset := page(r)
	items, total, err := s.st.ActivityBreakdown(f, by, sortBy, limit, offset)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if by == "channel" {
		for i := range items {
			items[i].Name = s.st.ChannelName(items[i].Key)
		}
	}
	writeJSON(w, map[string]any{"total": total, "items": items})
}

// statsSamples: the stored hourly history (playlist and guide fetches; the proxy's cache and retries for those who see
// the whole server) summed per bucket, and the fetches per profile over the whole range.
func (s *Server) statsSamples(w http.ResponseWriter, r *http.Request) {
	f, win, tz, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	if win.Bucket < 3600 { // hourly rows cannot be told apart any finer
		win = bucketGrid(win.From, win.To, 3600, tz)
	}
	fetches, err := s.st.ActivityFetches(f, win)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	totals, err := s.st.ActivityFetchTotals(f)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	out := map[string]any{"bucket": win.Bucket, "start": win.Start, "n": win.N, "fetches": fetches, "by_profile": totals}
	if f.Owner == 0 {
		srv, err := s.st.ActivityServer(win.From, win.To, win)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		out["server"] = srv
	}
	writeJSON(w, out)
}

// statsLive: the last hour of live points (proxied bytes so far, viewers) of the profiles in scope. In memory only.
func (s *Server) statsLive(w http.ResponseWriter, r *http.Request) {
	f := store.ActivityFilter{Owner: owner(r)} // the range does not apply: only the profile does
	if !s.scopeProfile(w, r, &f) {
		return
	}
	var in func(int64) bool
	switch {
	case f.Profile != 0 || len(f.Profiles) > 0:
		in = f.Includes
	case f.Owner != 0:
		ps, err := s.st.ListProfiles(f.Owner)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		mine := map[int64]bool{}
		for _, p := range ps {
			mine[p.ID] = true
		}
		in = func(id int64) bool { return mine[id] }
	}
	writeJSON(w, map[string]any{"interval": s.set.Int("stats_save_interval_seconds"), "samples": s.act.LiveSamples(in)})
}

// statsClear deletes the stored stats: everything, or what is older than a number of days. It changes data of every profile,
// so it needs the Settings permission like the settings it belongs to (the page for it is Settings > Stats).
func (s *Server) statsClear(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OlderThanDays *int `json:"older_than_days"` // 0 = everything
	}
	if !decode(w, r, &in) {
		return
	}
	if in.OlderThanDays == nil || *in.OlderThanDays < 0 || *in.OlderThanDays > 3650 {
		httpErr(w, 400, "older_than_days must be 0 (everything) to 3650")
		return
	}
	var before int64
	if *in.OlderThanDays > 0 {
		before = time.Now().AddDate(0, 0, -*in.OlderThanDays).Unix()
	}
	out, err := s.st.ClearActivity(before)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	slog.Info("stats cleared", "older_than_days", *in.OlderThanDays, "sessions", out.Sessions, "devices", out.Devices, "samples", out.Samples)
	writeJSON(w, out)
}
