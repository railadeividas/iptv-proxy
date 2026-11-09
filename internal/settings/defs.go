package settings

// Kind is the type of a setting's value.
type Kind string

const (
	Int    Kind = "int"
	String Kind = "string"
	URL    Kind = "url" // http(s) address
	Enum   Kind = "enum"
	Time   Kind = "time" // time of day, "HH:MM"
	Zone   Kind = "zone" // IANA time zone name, e.g. Europe/Vilnius
)

// Option is one choice of an Enum setting.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Def describes one setting. The list below is the single source of truth: it
// drives validation, storage and the Settings pages.
type Def struct {
	Key     string   `json:"key"`
	Group   string   `json:"group"`   // Settings tab: general, iptv-org, health, proxy, stats, metrics, security
	Section string   `json:"section"` // heading inside the tab
	Label   string   `json:"label"`
	Help    string   `json:"help"`
	Kind    Kind     `json:"kind"`
	Unit    string   `json:"unit,omitempty"`
	Default any      `json:"default"`
	Min     int      `json:"min"`
	Max     int      `json:"max"`
	Options []Option `json:"options,omitempty"`
	Blank   bool     `json:"blank,omitempty"` // a String/URL may be empty
}

const (
	vlcUA = "VLC/3.0.20 LibVLC/3.0.20"
)

// Defs lists every setting in display order. Only the
// bootstrap paths/port (see package config) live outside this list.
var Defs = []Def{
	// ---- General ----
	{Key: "player_address", Group: "general", Section: "Player links", Label: "Address for your player", Kind: URL, Default: "", Blank: true,
		Help: "Generated playlist and guide links use the address you opened this page with. Set this if players reach the server under a different address, for example http://192.168.1.10:8810. Leave empty to detect it."},
	{Key: "custom_refresh_hours", Group: "general", Section: "Custom sources", Label: "Auto-refresh for new URL sources", Kind: Int, Unit: "hours", Default: 72, Min: 0, Max: 720,
		Help: "How often a new playlist URL is downloaded again. 0 turns it off. Each source can be changed on the Sources page."},
	{Key: "custom_max_download_mb", Group: "general", Section: "Custom sources", Label: "Largest playlist download", Kind: Int, Unit: "MB", Default: 64, Min: 1, Max: 1024,
		Help: "Bigger playlists are cut off and rejected."},
	{Key: "fetch_user_agent", Group: "general", Section: "Downloads", Label: "User-Agent for downloads", Kind: String, Default: "iptv-proxy",
		Help: "Sent when downloading the iptv-org data and your playlist URLs."},
	{Key: "download_timeout_minutes", Group: "general", Section: "Downloads", Label: "Give up a download after", Kind: Int, Unit: "minutes", Default: 3, Min: 1, Max: 120,
		Help: "For the iptv-org data and for your playlist URLs."},
	{Key: "time_zone", Group: "general", Section: "Time", Label: "Time zone", Kind: Zone, Default: "", Blank: true,
		Help: "Used for the quiet hours of the health checker, for example Europe/Vilnius. Leave empty for the server's own time zone."},
	{Key: "log_format", Group: "general", Section: "Logging", Label: "Log format", Kind: Enum, Default: "json",
		Options: []Option{{"json", "JSON (one object per line)"}, {"text", "Plain text"}},
		Help:    "Applies immediately to the container log."},

	// ---- iptv-org ----
	{Key: "api_url", Group: "iptv-org", Section: "Catalogue (iptv-org/api)", Label: "API address", Kind: URL, Default: "https://iptv-org.github.io/api",
		Help: "Where channels, streams, feeds and guide sources are downloaded from. Change it to use a mirror."},
	{Key: "sync_interval_hours", Group: "iptv-org", Section: "Catalogue (iptv-org/api)", Label: "Sync every", Kind: Int, Unit: "hours", Default: 24, Min: 1, Max: 720,
		Help: "A failed sync keeps the last good data."},
	{Key: "api_max_download_mb", Group: "iptv-org", Section: "Catalogue (iptv-org/api)", Label: "Largest file to download", Kind: Int, Unit: "MB", Default: 256, Min: 16, Max: 2048},
	{Key: "epg_interval_hours", Group: "iptv-org", Section: "TV guide (iptv-org/epg)", Label: "Update the guide every", Kind: Int, Unit: "hours", Default: 12, Min: 1, Max: 720,
		Help: "The guide is also updated shortly after you change the channels of a profile."},
	{Key: "epg_change_delay_minutes", Group: "iptv-org", Section: "TV guide (iptv-org/epg)", Label: "Wait after profile changes", Kind: Int, Unit: "minutes", Default: 1, Min: 0, Max: 60,
		Help: "Several edits in a row trigger one update. The same wait is kept between two updates."},
	{Key: "epg_max_connections", Group: "iptv-org", Section: "TV guide (iptv-org/epg)", Label: "Parallel downloads", Kind: Int, Default: 5, Min: 1, Max: 50,
		Help: "How many guide pages the grabber fetches at once."},
	{Key: "epg_days", Group: "iptv-org", Section: "TV guide (iptv-org/epg)", Label: "Days of programmes", Kind: Int, Unit: "days", Default: 0, Min: 0, Max: 30,
		Help: "0 uses the grabber's own default."},
	{Key: "epg_timeout_minutes", Group: "iptv-org", Section: "TV guide (iptv-org/epg)", Label: "Give up an update after", Kind: Int, Unit: "minutes", Default: 180, Min: 5, Max: 1440},

	// ---- Health ----
	{Key: "health_scope", Group: "health", Section: "What to check", Label: "Streams to check", Kind: Enum, Default: "selected",
		Options: []Option{{"all", "Every stream"}, {"selected", "Only channels in profiles"}, {"off", "Nothing automatically"}},
		Help:    "With \"Nothing automatically\" you can still re-check profile channels by hand on the Health page."},
	{Key: "health_interval_hours", Group: "health", Section: "What to check", Label: "Check every stream again after", Kind: Int, Unit: "hours", Default: 72, Min: 1, Max: 720},
	{Key: "health_selected_interval_hours", Group: "health", Section: "What to check", Label: "Check channels in profiles again after", Kind: Int, Unit: "hours", Default: 24, Min: 1, Max: 720},
	{Key: "health_failures_before_down", Group: "health", Section: "What counts as not working", Label: "Failed checks in a row", Kind: Int, Default: 2, Min: 1, Max: 10,
		Help: "A stream that worked is marked not working only after this many failed checks in a row, so one network hiccup does not flip it. A stream that was never checked is marked at its first failed check, and is tried again after the retry time below until it reaches this number. 1 marks every stream at the first failure."},
	{Key: "health_retry_minutes", Group: "health", Section: "What counts as not working", Label: "Try a failed stream again after", Kind: Int, Unit: "minutes", Default: 60, Min: 1, Max: 1440,
		Help: "Applies to streams that failed but are not yet marked not working."},
	{Key: "health_auto_exclude_days", Group: "health", Section: "What counts as not working", Label: "Stop checking a stream that has failed for", Kind: Int, Unit: "days", Default: 7, Min: 0, Max: 3650,
		Help: "A stream whose every check has failed for this long is excluded from health checks, so long-dead streams stop taking up rounds. One working check (or a viewer's working playback) starts the count again. An excluded stream keeps its last status and is listed under Excluded streams on the Health page, where you can include it again. 0 never excludes a stream automatically."},
	{Key: "health_quiet_start", Group: "health", Section: "Quiet hours", Label: "Do not check from", Kind: Time, Default: "00:00", Blank: true,
		Help: "No automatic checks between these two times (in the time zone set on the General tab). Leave empty for no quiet hours. A check you start by hand still runs."},
	{Key: "health_quiet_end", Group: "health", Section: "Quiet hours", Label: "Until", Kind: Time, Default: "06:00", Blank: true},
	{Key: "health_history_rounds", Group: "health", Section: "History", Label: "Check rounds to keep", Kind: Int, Default: 200, Min: 0, Max: 100000,
		Help: "How many finished rounds (when, how long, how many working) the Health page lists and charts. 0 keeps none."},
	{Key: "health_history_per_channel", Group: "health", Section: "History", Label: "Results to keep per channel", Kind: Int, Default: 50, Min: 0, Max: 1000,
		Help: "The latest check results of each channel, shown when you open a channel on the Health page. Older ones are deleted. 0 keeps none."},
	{Key: "health_history_total", Group: "health", Section: "History", Label: "Results to keep overall", Kind: Int, Default: 100000, Min: 0, Max: 10000000,
		Help: "The most check results stored for all channels together. When it is exceeded the oldest are deleted first. 0 keeps none."},
	{Key: "health_history_days", Group: "health", Section: "History", Label: "Keep history for", Kind: Int, Unit: "days", Default: 30, Min: 1, Max: 3650,
		Help: "Rounds and results older than this are deleted, even when the limits above are not reached."},
	{Key: "health_max_per_pass", Group: "health", Section: "Load", Label: "Most streams per round", Kind: Int, Default: 5000, Min: 100, Max: 100000},
	{Key: "health_concurrency", Group: "health", Section: "Load", Label: "Parallel checks", Kind: Int, Default: 5, Min: 1, Max: 200},
	{Key: "health_per_host_concurrency", Group: "health", Section: "Load", Label: "Parallel checks per server", Kind: Int, Default: 1, Min: 1, Max: 200,
		Help: "The most checks that run at once against the same server (host and port), so a provider that carries many channels is not flooded, and providers that allow one connection at a time are not refused. Also limited by Parallel checks."},
	{Key: "health_connect_timeout_seconds", Group: "health", Section: "Timeouts", Label: "Connect timeout", Kind: Int, Unit: "seconds", Default: 8, Min: 1, Max: 60},
	{Key: "health_response_timeout_seconds", Group: "health", Section: "Timeouts", Label: "Wait for a response", Kind: Int, Unit: "seconds", Default: 10, Min: 1, Max: 120},
	{Key: "health_total_timeout_seconds", Group: "health", Section: "Timeouts", Label: "Total time per stream", Kind: Int, Unit: "seconds", Default: 15, Min: 2, Max: 300},
	{Key: "health_user_agent", Group: "health", Section: "Requests", Label: "User-Agent", Kind: String, Default: vlcUA,
		Help: "Used for streams that do not define their own."},

	// ---- Proxy ----
	{Key: "max_connections", Group: "proxy", Section: "Connections", Label: "Most streams at once", Kind: Int, Default: 20, Min: 1, Max: 10000,
		Help: "Extra viewers get \"503 busy\" until a slot frees up."},
	{Key: "upstream_connect_timeout_seconds", Group: "proxy", Section: "Connections", Label: "Connect timeout", Kind: Int, Unit: "seconds", Default: 10, Min: 1, Max: 120},
	{Key: "upstream_response_timeout_seconds", Group: "proxy", Section: "Connections", Label: "Wait for a response", Kind: Int, Unit: "seconds", Default: 30, Min: 1, Max: 300},
	{Key: "upstream_per_host_requests", Group: "proxy", Section: "Connections", Label: "Requests at once per server", Kind: Int, Default: 0, Min: 0, Max: 100,
		Help: "Counts every open request to the same server (host and port): players' playlists, segments and streams, and prefetch downloads. Segment prefetch only starts a download while the server is below this number, so it never adds requests beyond it. Players are never refused because of it. Use 1 for a provider that allows one connection at a time. 0 means no limit."},
	{Key: "link_expiry_hours", Group: "proxy", Section: "Links", Label: "Proxy links are valid for", Kind: Int, Unit: "hours", Default: 12, Min: 1, Max: 720,
		Help: "The signed segment and key links inside playlists the proxy rewrites."},
	{Key: "manifest_max_mb", Group: "proxy", Section: "Limits", Label: "Largest manifest", Kind: Int, Unit: "MB", Default: 16, Min: 1, Max: 256},
	{Key: "segment_cache_mb", Group: "proxy", Section: "Segment cache", Label: "Cache size", Kind: Int, Unit: "MB", Default: 1024, Min: 0, Max: 4096,
		Help: "Several viewers of the same channel share recent segments. 0 turns the cache off."},
	{Key: "segment_cache_ttl_seconds", Group: "proxy", Section: "Segment cache", Label: "Keep segments for", Kind: Int, Unit: "seconds", Default: 120, Min: 0, Max: 600},
	{Key: "playlist_cache_seconds", Group: "proxy", Section: "Segment cache", Label: "Share a fetched playlist for", Kind: Int, Unit: "seconds", Default: 2, Min: 0, Max: 10,
		Help: "A playlist (HLS or DASH) that was just fetched is handed to the other players asking for it, so ten viewers of a channel cost the server one playlist request instead of ten. A live playlist is never kept longer than half of how often it updates, so players do not see an old list. 0 turns it off."},
	{Key: "hls_head_start_seconds", Group: "proxy", Section: "Head start (live HLS)", Label: "Head start", Kind: Int, Unit: "seconds", Default: 10, Min: 0, Max: 120,
		Help: "A live HLS channel is served this many seconds behind the stream server's live point, from segments the proxy has already downloaded. A late segment, a short stall or a reconnect of the server then happens inside the head start and never reaches the player, which no longer stops or skips. The price is the same amount of delay on live TV. 0 turns it off: players get the playlist as the server sends it. It needs the segment cache above (a size of 0 turns it off) with room for the last minute or two of every channel being watched."},
	{Key: "hls_head_start_linger_seconds", Group: "proxy", Section: "Head start (live HLS)", Label: "Keep following a channel after the last viewer", Kind: Int, Unit: "seconds", Default: 30, Min: 5, Max: 600,
		Help: "A channel is followed (its playlist asked for, its segments downloaded) while somebody watches and this long after the last request, so a player that reconnects or flips back finds it ready."},
	{Key: "segment_cache_item_max_mb", Group: "proxy", Section: "Segment cache", Label: "Largest segment to cache", Kind: Int, Unit: "MB", Default: 12, Min: 1, Max: 64},
	{Key: "shared_streams", Group: "proxy", Section: "Shared streams", Label: "Share live streams between viewers", Kind: Enum, Default: "on",
		Options: []Option{{"on", "On"}, {"off", "Off"}},
		Help:    "When several players watch the same continuous live stream (plain MPEG-TS, not HLS or DASH) through the proxy, the stream server is asked once and every player is served from a short buffer. That saves connections and bandwidth with providers that allow few connections, and the proxy connects again by itself when the server drops the stream. A player that joins starts at the live point. Off: every player gets its own connection."},
	{Key: "shared_stream_buffer_mb", Group: "proxy", Section: "Shared streams", Label: "Buffer per shared stream", Kind: Int, Unit: "MB", Default: 8, Min: 1, Max: 64,
		Help: "How much of a live stream is kept for the players watching it. A player that falls further behind than this is disconnected and reconnects at the live point."},
	{Key: "shared_stream_linger_seconds", Group: "proxy", Section: "Shared streams", Label: "Keep a stream open after the last viewer", Kind: Int, Unit: "seconds", Default: 5, Min: 0, Max: 60,
		Help: "A shared stream stays open this long after its last viewer left, so a player that reconnects or flips back finds it still running. 0 closes it at once."},
	{Key: "shared_stream_reconnects", Group: "proxy", Section: "Shared streams", Label: "Reconnects in a row", Kind: Int, Default: 3, Min: 0, Max: 10,
		Help: "How many times in a row the proxy connects again when the server ends or drops a shared stream while somebody watches. Players keep their connection meanwhile. 0 ends the stream for everybody instead."},
	{Key: "upstream_retries", Group: "proxy", Section: "Upstream problems", Label: "Retries", Kind: Int, Default: 2, Min: 0, Max: 5,
		Help: "How many more times a playlist or segment is requested when the server fails to answer or returns a 5xx error, before the player is told. Nothing is retried once bytes have reached the player. 0 turns retries off."},
	{Key: "upstream_retry_delay_ms", Group: "proxy", Section: "Upstream problems", Label: "Wait before a retry", Kind: Int, Unit: "ms", Default: 300, Min: 0, Max: 5000},
	{Key: "playlist_holdover_seconds", Group: "proxy", Section: "Upstream problems", Label: "Keep serving the last playlist for", Kind: Int, Unit: "seconds", Default: 30, Min: 0, Max: 300,
		Help: "When a playlist (HLS or DASH) cannot be fetched, players get the last good copy for this long instead of an error, so a short outage does not stop playback. 0 turns it off."},
	{Key: "segment_prefetch_count", Group: "proxy", Section: "Upstream problems", Label: "Segments to fetch ahead", Kind: Int, Default: 10, Min: 0, Max: 10,
		Help: "While someone watches, the newest segments of the HLS playlist (and of a live DASH manifest that lists its segments in a SegmentTimeline) are downloaded into the segment cache before the player asks, so a slow or stalling server is absorbed. Each one is an extra request to the server (and uses a connection slot), which providers that allow one connection at a time may refuse: lower it, or set 0, for such a provider. Needs the segment cache; keep its lifetime at least as long as the segments ahead. 0 turns it off."},
	{Key: "fix_audio_channels", Group: "proxy", Section: "Stream repair", Label: "Fix stereo audio labelled as 5.1", Kind: Enum, Default: "on",
		Options: []Option{{"on", "On"}, {"off", "Off"}},
		Help:    "Some providers send stereo AAC audio in MPEG-TS streams whose headers say 5.1. VLC plays it, but Jellyfin, Plex, Emby and other ffmpeg based players fail with audio errors. When on, the proxy corrects the label to stereo as the stream passes through (nothing is re-encoded, and audio that is labelled correctly is never touched). Applies to the next segment or stream a player opens."},
	{Key: "proxy_default_user_agent", Group: "proxy", Section: "User-Agents", Label: "Default", Kind: String, Default: vlcUA,
		Help: "Sent when neither the stream nor the profile defines one."},
	{Key: "ua_vlc", Group: "proxy", Section: "User-Agents", Label: "Compatibility: VLC", Kind: String, Default: vlcUA},
	{Key: "ua_browser", Group: "proxy", Section: "User-Agents", Label: "Compatibility: browser", Kind: String,
		Default: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"},
	{Key: "ua_exoplayer", Group: "proxy", Section: "User-Agents", Label: "Compatibility: ExoPlayer", Kind: String, Default: "ExoPlayerLib/2.19.1"},

	// ---- Stats ----
	{Key: "stats_enabled", Group: "stats", Section: "Recording", Label: "Record activity", Kind: Enum, Default: "on",
		Options: []Option{{"on", "On"}, {"off", "Off"}},
		Help:    "Who fetches playlists and guides, what is watched through the proxy and what players are sent to directly. Turning it off stops recording; what is already stored stays until it expires."},
	{Key: "stats_ip_mode", Group: "stats", Section: "Privacy", Label: "Client addresses", Kind: Enum, Default: "masked",
		Options: []Option{{"masked", "Mask the end of the address"}, {"full", "Store the full address"}, {"none", "Do not store addresses"}},
		Help:    "Masked addresses keep only the first part (set below), so devices on the same network look alike. Applies to what is recorded from now on."},
	{Key: "stats_mask_ipv4_bits", Group: "stats", Section: "Privacy", Label: "Keep of an IPv4 address", Kind: Int, Unit: "bits", Default: 24, Min: 1, Max: 32,
		Help: "24 keeps 192.168.1 and hides the last number. Used when addresses are masked."},
	{Key: "stats_mask_ipv6_bits", Group: "stats", Section: "Privacy", Label: "Keep of an IPv6 address", Kind: Int, Unit: "bits", Default: 48, Min: 1, Max: 128,
		Help: "Used when addresses are masked."},
	{Key: "stats_store_user_agent", Group: "stats", Section: "Privacy", Label: "Record the player name", Kind: Enum, Default: "on",
		Options: []Option{{"on", "On"}, {"off", "Off"}},
		Help:    "The User-Agent a player sends, for example VLC or ExoPlayer. It tells devices apart."},
	{Key: "stats_forwarded_header", Group: "stats", Section: "Privacy", Label: "Address header behind a reverse proxy", Kind: String, Default: "", Blank: true,
		Help: "Leave empty to use the address the connection comes from. Behind a reverse proxy every viewer would show the proxy's address: set the header it adds, for example X-Forwarded-For (the first address in it is used) or X-Real-IP. Only set it when the proxy always writes that header, because a client could send it itself."},
	{Key: "stats_live_seconds", Group: "stats", Section: "Watching now", Label: "Still watching for", Kind: Int, Unit: "seconds", Default: 30, Min: 5, Max: 600,
		Help: "A viewer counts as watching until nothing was requested for this long. Players ask for a new piece of video every few seconds."},
	{Key: "stats_session_idle_seconds", Group: "stats", Section: "Watching now", Label: "A session ends after", Kind: Int, Unit: "seconds", Default: 300, Min: 30, Max: 86400,
		Help: "Silence for this long ends a viewing session. The same device opening the same channel later starts a new one."},
	{Key: "stats_save_interval_seconds", Group: "stats", Section: "Watching now", Label: "Save running sessions every", Kind: Int, Unit: "seconds", Default: 10, Min: 2, Max: 300,
		Help: "How often the numbers of running sessions are written to the database. The Stats page is at most this much behind."},
	{Key: "stats_history_days", Group: "stats", Section: "History", Label: "Keep history for", Kind: Int, Unit: "days", Default: 30, Min: 1, Max: 3650,
		Help: "Sessions and devices not seen for this long are deleted, even when the limits below are not reached."},
	{Key: "stats_history_sessions", Group: "stats", Section: "History", Label: "Sessions to keep", Kind: Int, Default: 100000, Min: 0, Max: 10000000,
		Help: "The most sessions stored. When it is exceeded the oldest are deleted first. 0 keeps none: only what is watching now is shown."},
	{Key: "stats_history_clients", Group: "stats", Section: "History", Label: "Devices to keep", Kind: Int, Default: 5000, Min: 0, Max: 1000000,
		Help: "The most devices (an address and player name per profile) stored. The ones seen longest ago are deleted first. 0 keeps none."},

	// ---- Metrics ----
	{Key: "metrics_enabled", Group: "metrics", Section: "Prometheus", Label: "Metrics endpoint", Kind: Enum, Default: "off",
		Options: []Option{{"off", "Off"}, {"on", "On"}},
		Help:    "Serves /metrics for Prometheus: proxy load, who is watching, stream health, sync and guide status, per profile. While it is off, /metrics does not exist (404)."},
	{Key: "metrics_access", Group: "metrics", Section: "Prometheus", Label: "Access", Kind: Enum, Default: "token",
		Options: []Option{{"token", "Require the token below"}, {"open", "Open: no authentication"}},
		Help:    "Open means anyone who can reach this port can read profile names, the channels being watched and the stream servers. Use it only on a private network, or behind a reverse proxy that restricts /metrics."},
	{Key: "metrics_token", Group: "metrics", Section: "Prometheus", Label: "Token", Kind: String, Default: "", Blank: true, Min: 16,
		Help: "Prometheus sends it as the header Authorization: Bearer <token> (authorization: credentials: in its scrape config). At least 16 characters; for example the output of: openssl rand -hex 24. While it is empty, /metrics refuses every request."},
	{Key: "metrics_profile_labels", Group: "metrics", Section: "Detail", Label: "Series per profile", Kind: Enum, Default: "on",
		Options: []Option{{"on", "On"}, {"off", "Off"}},
		Help:    "Adds the name and owner of each profile as labels (sessions, data, devices, channels by state). Off leaves only the totals."},
	{Key: "metrics_channel_viewers", Group: "metrics", Section: "Detail", Label: "Viewers per channel", Kind: Enum, Default: "on",
		Options: []Option{{"on", "On"}, {"off", "Off"}},
		Help:    "One series for each channel somebody is watching right now (it disappears when they stop). Off leaves only the totals."},

	// ---- Security ----
	{Key: "password_min_length", Group: "security", Section: "Sign-in", Label: "Shortest password", Kind: Int, Unit: "characters", Default: 8, Min: 4, Max: 64},
	{Key: "session_days", Group: "security", Section: "Sign-in", Label: "Stay signed in for", Kind: Int, Unit: "days", Default: 7, Min: 1, Max: 365},
	{Key: "login_max_failures", Group: "security", Section: "Sign-in", Label: "Failed sign-ins allowed", Kind: Int, Default: 8, Min: 1, Max: 1000,
		Help: "Per address, within the window below. After that sign-in is refused until the window passes."},
	{Key: "login_window_seconds", Group: "security", Section: "Sign-in", Label: "Window for failed sign-ins", Kind: Int, Unit: "seconds", Default: 60, Min: 10, Max: 86400},
}
