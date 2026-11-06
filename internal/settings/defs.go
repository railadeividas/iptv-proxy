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
	Group   string   `json:"group"`   // Settings tab: general, iptv-org, health, proxy, security
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
	{Key: "link_expiry_hours", Group: "proxy", Section: "Links", Label: "Proxy links are valid for", Kind: Int, Unit: "hours", Default: 12, Min: 1, Max: 720,
		Help: "The signed segment and key links inside playlists the proxy rewrites."},
	{Key: "manifest_max_mb", Group: "proxy", Section: "Limits", Label: "Largest manifest", Kind: Int, Unit: "MB", Default: 16, Min: 1, Max: 256},
	{Key: "segment_cache_mb", Group: "proxy", Section: "Segment cache", Label: "Cache size", Kind: Int, Unit: "MB", Default: 256, Min: 0, Max: 4096,
		Help: "Several viewers of the same channel share recent segments. 0 turns the cache off."},
	{Key: "segment_cache_ttl_seconds", Group: "proxy", Section: "Segment cache", Label: "Keep segments for", Kind: Int, Unit: "seconds", Default: 15, Min: 0, Max: 600},
	{Key: "segment_cache_item_max_mb", Group: "proxy", Section: "Segment cache", Label: "Largest segment to cache", Kind: Int, Unit: "MB", Default: 12, Min: 1, Max: 64},
	{Key: "proxy_default_user_agent", Group: "proxy", Section: "User-Agents", Label: "Default", Kind: String, Default: vlcUA,
		Help: "Sent when neither the stream nor the profile defines one."},
	{Key: "ua_vlc", Group: "proxy", Section: "User-Agents", Label: "Compatibility: VLC", Kind: String, Default: vlcUA},
	{Key: "ua_browser", Group: "proxy", Section: "User-Agents", Label: "Compatibility: browser", Kind: String,
		Default: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"},
	{Key: "ua_exoplayer", Group: "proxy", Section: "User-Agents", Label: "Compatibility: ExoPlayer", Kind: String, Default: "ExoPlayerLib/2.19.1"},

	// ---- Security ----
	{Key: "password_min_length", Group: "security", Section: "Sign-in", Label: "Shortest password", Kind: Int, Unit: "characters", Default: 8, Min: 4, Max: 64},
	{Key: "session_days", Group: "security", Section: "Sign-in", Label: "Stay signed in for", Kind: Int, Unit: "days", Default: 7, Min: 1, Max: 365},
	{Key: "login_max_failures", Group: "security", Section: "Sign-in", Label: "Failed sign-ins allowed", Kind: Int, Default: 8, Min: 1, Max: 1000,
		Help: "Per address, within the window below. After that sign-in is refused until the window passes."},
	{Key: "login_window_seconds", Group: "security", Section: "Sign-in", Label: "Window for failed sign-ins", Kind: Int, Unit: "seconds", Default: 60, Min: 10, Max: 86400},
}
