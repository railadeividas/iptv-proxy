package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"iptv-proxy/internal/activity"
	"iptv-proxy/internal/metrics"
	"iptv-proxy/internal/store"
	"iptv-proxy/internal/version"
)

var startedAt = time.Now()

// ---- counting requests ----

type reqKey struct {
	route string
	code  int
}

// route names the kind of a request, for the request counter (few values, never a path or a token).
func route(path string) string {
	switch {
	case path == "/healthz":
		return "health"
	case path == "/metrics":
		return "metrics"
	case path == "/ws":
		return "websocket"
	case strings.HasPrefix(path, "/x/"), strings.HasPrefix(path, "/xd/"):
		return "segment"
	case strings.HasPrefix(path, "/p/"):
		switch {
		case strings.Contains(path, "/channel/"):
			return "channel"
		case strings.HasSuffix(path, "/playlist.m3u"):
			return "playlist"
		case strings.HasSuffix(path, "/epg.xml"), strings.HasSuffix(path, "/epg.xml.gz"):
			return "guide"
		}
		return "player"
	case strings.HasPrefix(path, "/api/"):
		return "api"
	}
	return "ui"
}

func (s *Server) countRequest(path string, code int) {
	s.reqMu.Lock()
	s.reqs[reqKey{route(path), code}]++
	s.reqMu.Unlock()
}

// ---- the endpoint ----

// metricsAllowed answers whether this request may read the metrics and, if not, with which status.
func (s *Server) metricsAllowed(r *http.Request) (int, bool) {
	if s.set.String("metrics_enabled") != "on" {
		return http.StatusNotFound, false
	}
	if s.set.String("metrics_access") == "open" {
		return 0, true
	}
	token := s.set.String("metrics_token")
	if token == "" { // no token set: nobody gets in, so enabling the endpoint cannot expose it by accident
		return http.StatusUnauthorized, false
	}
	got, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(token))
	if !bearer || subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		return http.StatusUnauthorized, false
	}
	return 0, true
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if code, ok := s.metricsAllowed(r); !ok {
		if code == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", code)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	w.Header().Set("Content-Type", metrics.ContentType)
	if r.Method == http.MethodHead {
		return
	}
	s.collect().WriteTo(w)
}

// ---- the numbers ----

func (s *Server) collect() *metrics.Set {
	m := metrics.New()
	st := s.st
	perProfile := s.set.String("metrics_profile_labels") == "on"

	// app
	m.Gauge("iptv_build_info", "Version of this build (always 1).", 1, "version", version.String(), "go_version", runtime.Version())
	m.Gauge("iptv_start_time_seconds", "When the app started, unix time.", float64(startedAt.Unix()))
	m.Bool("iptv_ready", "The catalogue has been loaded at least once.", st.Ready())
	m.Gauge("iptv_websocket_clients", "Browsers connected for live updates.", float64(s.hub.Clients()))
	if fi, err := os.Stat(filepath.Join(s.cfg.DataDir, "iptv.db")); err == nil {
		m.Gauge("iptv_database_size_bytes", "Size of the database file.", float64(fi.Size()))
	}
	s.reqMu.Lock()
	keys := make([]reqKey, 0, len(s.reqs))
	for k := range s.reqs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].route < keys[j].route || (keys[i].route == keys[j].route && keys[i].code < keys[j].code)
	})
	for _, k := range keys {
		m.Counter("iptv_http_requests_total", "HTTP requests by kind and status code.", float64(s.reqs[k]), "route", k.route, "code", strconv.Itoa(k.code))
	}
	s.reqMu.Unlock()

	// go runtime and process
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.Gauge("go_goroutines", "Goroutines that exist now.", float64(runtime.NumGoroutine()))
	m.Gauge("go_memstats_alloc_bytes", "Bytes of allocated heap objects in use.", float64(ms.Alloc))
	m.Gauge("go_memstats_heap_inuse_bytes", "Bytes in in-use heap spans.", float64(ms.HeapInuse))
	m.Gauge("go_memstats_sys_bytes", "Bytes of memory obtained from the system.", float64(ms.Sys))
	m.Counter("go_gc_cycles_total", "Completed garbage collections.", float64(ms.NumGC))
	m.Counter("go_gc_pause_seconds_total", "Total time paused by garbage collection.", float64(ms.PauseTotalNs)/1e9)
	m.Gauge("process_start_time_seconds", "When the process started, unix time.", float64(startedAt.Unix()))

	// users, profiles, catalogue
	cat := st.CatalogueCounts()
	roles := st.UsersByRole()
	for _, role := range []string{"superuser", "admin", "user"} {
		m.Gauge("iptv_users", "Users by role.", float64(roles[role]), "role", role)
	}
	m.Gauge("iptv_profiles", "Profiles.", float64(cat.Profiles))
	m.Gauge("iptv_catalogue_channels", "Channels in the catalogue.", float64(cat.Channels))
	m.Gauge("iptv_catalogue_streams", "Streams in the catalogue.", float64(cat.Streams))
	m.Gauge("iptv_skipped_entries", "Playlist lines the importer could not use.", float64(cat.Skipped))
	m.Gauge("iptv_custom_sources", "Custom sources.", float64(cat.CustomSources))
	for _, c := range st.CustomSourceRows() {
		id := strconv.FormatInt(c.ID, 10)
		m.Gauge("iptv_custom_source_channels", "Channels of a custom source.", float64(c.Channels), "source", c.Name, "source_id", id)
		m.Gauge("iptv_custom_source_last_success_timestamp_seconds", "When a custom source was last refreshed successfully, unix time (0 = never).", float64(c.LastOK), "source", c.Name, "source_id", id)
		m.Bool("iptv_custom_source_failed", "The last refresh of a custom source failed.", c.Failed, "source", c.Name, "source_id", id)
	}

	// catalogue sync and guide
	m.Gauge("iptv_sync_last_success_timestamp_seconds", "When the iptv-org catalogue was last synced, unix time (0 = never).", float64(st.MetaInt("synced_at")))
	m.Bool("iptv_sync_running", "A catalogue sync is running.", s.sync.Running())
	m.Bool("iptv_sync_failed", "The last catalogue sync failed.", st.Meta("sync_error") != "")
	e := s.epg.Status()
	m.Bool("iptv_epg_enabled", "The guide grabber is available.", e.Enabled)
	m.Bool("iptv_epg_running", "A guide update is running.", e.Running)
	m.Bool("iptv_epg_failed", "The last guide update failed.", e.Error != "")
	m.Gauge("iptv_epg_last_success_timestamp_seconds", "When the guide was last updated, unix time (0 = never).", float64(e.LastOK))
	m.Gauge("iptv_epg_channels", "Channels that have a guide.", float64(e.Channels))
	m.Gauge("iptv_epg_unmapped_channels", "Profile channels without a guide source.", float64(e.Unmapped))

	// proxy
	px := s.px.Stats()
	m.Gauge("iptv_proxy_connections", "Streams the proxy carries now.", float64(px.InUse))
	m.Gauge("iptv_proxy_connections_limit", "Most streams at once (setting).", float64(px.Limit))
	m.Counter("iptv_proxy_upstream_requests_total", "Requests to stream servers by result.", float64(px.UpstreamOK), "result", "ok")
	m.Counter("iptv_proxy_upstream_requests_total", "Requests to stream servers by result.", float64(px.UpstreamErrors), "result", "error")
	m.Gauge("iptv_proxy_cache_bytes", "Bytes in the segment cache.", float64(px.CacheBytes))
	m.Gauge("iptv_proxy_cache_limit_bytes", "Size of the segment cache (setting).", float64(px.CacheLimit))
	m.Gauge("iptv_proxy_cache_entries", "Segments in the cache.", float64(px.CacheEntries))
	m.Counter("iptv_proxy_cache_hits_total", "Segments served from the cache.", float64(px.CacheHits))
	m.Counter("iptv_proxy_cache_misses_total", "Segments that were not in the cache.", float64(px.CacheMisses))
	m.Counter("iptv_proxy_cache_hit_bytes_total", "Bytes served from the segment cache or from a shared live stream.", float64(px.CacheHitBytes))
	m.Gauge("iptv_proxy_shared_streams", "Live streams shared between viewers now.", float64(px.SharedStreams))
	m.Gauge("iptv_proxy_shared_viewers", "Viewers on shared live streams now.", float64(px.SharedViewers))
	m.Counter("iptv_proxy_shared_joins_total", "Viewers that joined a live stream somebody else had opened.", float64(px.SharedJoins))
	m.Counter("iptv_proxy_cache_stores_total", "Segments put in the cache.", float64(px.CacheStores))
	m.Counter("iptv_proxy_cache_rejected_total", "Segments not cached because the cache was full.", float64(px.CacheRejected))
	m.Counter("iptv_proxy_prefetched_segments_total", "Segments fetched ahead of the player.", float64(px.Prefetched))
	m.Counter("iptv_proxy_coalesced_requests_total", "Segment requests that waited for a fetch already running instead of asking the server again.", float64(px.Coalesced))
	m.Counter("iptv_proxy_upstream_retries_total", "Upstream requests repeated after a temporary failure.", float64(px.Retries))
	m.Counter("iptv_proxy_playlist_holdovers_total", "Playlists served from the last good copy because the server was down.", float64(px.Holdovers))

	s.collectHealth(m)
	s.collectActivity(m, perProfile)
	return m
}

func (s *Server) collectHealth(m *metrics.Set) {
	d := s.health.Details()
	m.Bool("iptv_health_running", "A health round is running.", d.Running)
	m.Bool("iptv_health_paused", "Automatic checks are paused.", d.Paused)
	m.Bool("iptv_health_quiet_hours", "Quiet hours are on: no automatic checks.", d.Quiet)
	m.Gauge("iptv_health_streams_due", "Streams waiting for a check.", float64(d.Due))
	m.Gauge("iptv_health_next_check_timestamp_seconds", "When the next automatic check is due, unix time (0 = none planned).", float64(d.Next))
	m.Gauge("iptv_health_round_done", "Streams checked so far in the running round.", float64(d.Done))
	m.Gauge("iptv_health_round_total", "Streams in the running round.", float64(d.Total))
	m.Gauge("iptv_health_last_round_started_timestamp_seconds", "When the last finished round started, unix time (0 = none yet).", float64(d.Last.Started))
	m.Gauge("iptv_health_last_round_finished_timestamp_seconds", "When the last round finished, unix time (0 = none yet).", float64(d.Last.Finished))
	if d.Last.Finished > 0 {
		m.Gauge("iptv_health_last_round_duration_seconds", "How long the last round took.", float64(d.Last.Finished-d.Last.Started))
	}
	m.Gauge("iptv_health_last_round_streams", "Streams the last round checked.", float64(d.Last.Count))
	m.Gauge("iptv_health_last_round_working", "Of those, working.", float64(d.Last.OK))
	m.Gauge("iptv_health_last_round_failed", "Of those, not working.", float64(d.Last.Bad))
	m.Gauge("iptv_health_history_rounds", "Finished rounds stored.", float64(d.History.Rounds))
	m.Gauge("iptv_health_history_checks", "Check results stored.", float64(d.History.Checks))

	for _, f := range []struct {
		age string
		n   int
	}{{"hour", d.Fresh.Hour}, {"day", d.Fresh.Day}, {"older", d.Fresh.Older}, {"never", d.Fresh.Never}} {
		m.Gauge("iptv_health_streams_by_last_check", "Streams by the age of their last check: within an hour, within a day, older, never.", float64(f.n), "age", f.age)
	}
	for _, f := range []struct {
		state string
		n     int
	}{{"working", d.Totals.OK}, {"not_working", d.Totals.Bad}, {"unchecked", d.Totals.Unchecked}, {"excluded", d.Totals.Excluded}} {
		m.Gauge("iptv_streams", "Streams by state. Streams excluded from health checks count only as excluded.", float64(f.n), "state", f.state)
	}
	cs := d.ChannelStates
	for _, f := range []struct {
		state string
		n     int
	}{{"working", cs.Working}, {"partly_working", cs.Partly}, {"not_working", cs.NotWorking}, {"pending", cs.Pending}, {"not_checked", cs.NotChecked}} {
		m.Gauge("iptv_channels_by_state", "Channels by state.", float64(f.n), "state", f.state)
	}
	m.Gauge("iptv_channels_in_profiles_down", "Channels in profiles where every stream fails (all profiles together).", float64(d.Profiles.ChannelsDown))
	for _, src := range d.Sources {
		for _, f := range []struct {
			state string
			n     int
		}{{"working", src.OK}, {"not_working", src.Bad}, {"unchecked", src.Unchecked}, {"excluded", src.Excluded}} {
			m.Gauge("iptv_source_streams", "Streams by source and state.", float64(f.n), "source", src.Name, "kind", src.Kind, "state", f.state)
		}
	}
}

func (s *Server) collectActivity(m *metrics.Set, perProfile bool) {
	all, per := s.act.Counters()
	m.Counter("iptv_playlist_fetches_total", "Playlist downloads.", float64(all.Playlists))
	m.Counter("iptv_guide_fetches_total", "Guide downloads.", float64(all.Guides))
	m.Counter("iptv_sessions_started_total", "Viewing sessions started, by delivery mode.", float64(all.Proxy), "mode", activity.Proxy)
	m.Counter("iptv_sessions_started_total", "Viewing sessions started, by delivery mode.", float64(all.Redirect), "mode", activity.Redirect)
	m.Counter("iptv_bytes_sent_total", "Video sent to players through the proxy.", float64(all.Bytes))
	m.Bool("iptv_stats_recording", "Activity is being recorded (setting).", s.set.String("stats_enabled") == "on")

	now := s.act.Now(0)
	type pm struct {
		profile int64
		mode    string
	}
	active := map[pm]int{}
	byChannel := map[[3]string]int{} // channel key, name, mode
	byHost := map[[2]string]int{}    // host, mode
	for _, x := range now {
		active[pm{x.ProfileID, x.Mode}]++
		byHost[[2]string{x.Host, x.Mode}]++
		byChannel[[3]string{x.CK, "", x.Mode}]++
	}
	for _, mode := range []string{activity.Proxy, activity.Redirect} {
		n := 0
		for k, v := range active {
			if k.mode == mode {
				n += v
			}
		}
		m.Gauge("iptv_sessions_active", "Devices watching now (or sent to a stream within the live window), by delivery mode.", float64(n), "mode", mode)
	}
	for _, k := range sortedKeys2(byHost) {
		m.Gauge("iptv_stream_host_viewers", "Viewers per stream server.", float64(byHost[k]), "host", k[0], "mode", k[1])
	}
	if s.set.String("metrics_channel_viewers") == "on" {
		for _, k := range sortedKeys3(byChannel) {
			m.Gauge("iptv_channel_viewers", "Viewers per channel being watched now.", float64(byChannel[k]), "ck", k[0], "name", s.st.ChannelName(k[0]), "mode", k[2])
		}
	}

	if !perProfile {
		return
	}
	profiles, err := s.st.ListProfiles(0)
	if err != nil {
		return
	}
	rows, _ := s.st.ActivityProfiles(store.ActivityFilter{})
	seen := map[int64]struct{ clients, last int64 }{}
	for _, r := range rows {
		seen[r.ProfileID] = struct{ clients, last int64 }{r.Clients, r.LastSeen}
	}
	states := s.st.ProfileChannelStates()
	for _, p := range profiles {
		id := strconv.FormatInt(p.ID, 10)
		l := []string{"profile", p.Name, "profile_id", id, "owner", p.Owner}
		with := func(extra ...string) []string { return append(append([]string{}, l...), extra...) }
		m.Gauge("iptv_profile_info", "A profile, with its delivery mode and compatibility preset (always 1).", 1, with("mode", p.Mode, "compat", p.Compat)...)
		m.Gauge("iptv_profile_channels", "Channels in the profile.", float64(p.Count), l...)
		c := per[p.ID]
		m.Counter("iptv_profile_playlist_fetches_total", "Playlist downloads of the profile.", float64(c.Playlists), l...)
		m.Counter("iptv_profile_guide_fetches_total", "Guide downloads of the profile.", float64(c.Guides), l...)
		m.Counter("iptv_profile_sessions_started_total", "Viewing sessions started from the profile.", float64(c.Proxy), with("mode", activity.Proxy)...)
		m.Counter("iptv_profile_sessions_started_total", "Viewing sessions started from the profile.", float64(c.Redirect), with("mode", activity.Redirect)...)
		m.Counter("iptv_profile_bytes_sent_total", "Video sent through the proxy for the profile.", float64(c.Bytes), l...)
		m.Gauge("iptv_profile_sessions_active", "Devices watching the profile now.", float64(active[pm{p.ID, activity.Proxy}]), with("mode", activity.Proxy)...)
		m.Gauge("iptv_profile_sessions_active", "Devices watching the profile now.", float64(active[pm{p.ID, activity.Redirect}]), with("mode", activity.Redirect)...)
		a := seen[p.ID]
		m.Gauge("iptv_profile_clients", "Devices (address and player) that used the profile and are still stored.", float64(a.clients), l...)
		m.Gauge("iptv_profile_last_seen_timestamp_seconds", "When a device last used the profile, unix time (0 = never).", float64(a.last), l...)
		st := states[p.ID]
		for _, f := range []struct {
			state string
			n     int
		}{{"working", st.Working}, {"partly_working", st.Partly}, {"not_working", st.NotWorking}, {"pending", st.Pending}} {
			m.Gauge("iptv_profile_channels_by_state", "Channels of the profile by state.", float64(f.n), with("state", f.state)...)
		}
	}
}

func sortedKeys2(m map[[2]string]int) [][2]string {
	out := make([][2]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0]+"\x00"+out[i][1] < out[j][0]+"\x00"+out[j][1] })
	return out
}

func sortedKeys3(m map[[3]string]int) [][3]string {
	out := make([][3]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i][0]+"\x00"+out[i][2] < out[j][0]+"\x00"+out[j][2]
	})
	return out
}
