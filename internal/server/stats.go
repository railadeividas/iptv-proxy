package server

import (
	"net/http"
	"strconv"
	"time"

	"iptv-proxy/internal/auth"
	"iptv-proxy/internal/store"
)

// statsFilter reads the period (days, 0 = everything stored), the profile and the mode from the query.
// Whoever may not see every profile (see owner) only ever gets their own.
func (s *Server) statsFilter(w http.ResponseWriter, r *http.Request) (store.ActivityFilter, int, bool) {
	q := r.URL.Query()
	days, _ := strconv.Atoi(q.Get("days"))
	if days < 0 || days > 3650 {
		days = 7
	}
	if q.Get("days") == "" {
		days = 7
	}
	f := store.ActivityFilter{Owner: owner(r)}
	if days > 0 {
		f.Since = time.Now().AddDate(0, 0, -days).Unix()
	}
	if id, _ := strconv.ParseInt(q.Get("profile"), 10, 64); id != 0 {
		p, err := s.st.GetProfile(id)
		if err != nil || (f.Owner != 0 && p.OwnerID != f.Owner) {
			httpErr(w, 404, "profile not found")
			return f, 0, false
		}
		f.Profile = id
	}
	if m := q.Get("mode"); m == "proxy" || m == "redirect" {
		f.Mode = m
	}
	return f, days, true
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
	f, days, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	now := s.act.Now(f.Owner)
	live := now[:0]
	for _, x := range now {
		if f.Profile == 0 || x.ProfileID == f.Profile {
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
		"days":         days,
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
			"hit_bytes": ps.CacheHitBytes, "prefetched": ps.Prefetched, "coalesced": ps.Coalesced, "retries": ps.Retries, "holdovers": ps.Holdovers}
	}
	out["all_profiles"] = auth.Can(userFrom(r).Role, auth.AllProfiles)
	writeJSON(w, out)
}

func (s *Server) statsChannels(w http.ResponseWriter, r *http.Request) {
	f, _, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	limit, offset := page(r)
	items, total, err := s.st.ActivityChannels(f, limit, offset)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	for i := range items {
		items[i].Name = s.st.ChannelName(items[i].CK)
	}
	writeJSON(w, map[string]any{"total": total, "items": items})
}

func (s *Server) statsClients(w http.ResponseWriter, r *http.Request) {
	f, _, ok := s.statsFilter(w, r)
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
	f, _, ok := s.statsFilter(w, r)
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
