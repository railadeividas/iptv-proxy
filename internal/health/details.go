package health

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"iptv-proxy/internal/store"
)

// Last describes the most recent finished pass.
type Last struct {
	Started  int64  `json:"started"`
	Finished int64  `json:"finished"`
	Count    int    `json:"count"`
	OK       int    `json:"ok"`
	Bad      int    `json:"bad"`
	Kind     string `json:"kind"`  // of that round: auto or manual
	Label    string `json:"label"` // what it covered
}

// Counts are streams by state. Streams excluded from health checks count only in Excluded, whatever their last result.
type Counts struct {
	Streams   int `json:"streams"`
	OK        int `json:"ok"`
	Bad       int `json:"bad"`
	Unchecked int `json:"unchecked"`
	Excluded  int `json:"excluded"`
}

// countCols fills Counts (Streams, OK, Bad, Unchecked, Excluded) from streams s.
const countCols = `COUNT(*), COALESCE(SUM(NOT ` + isExcluded + ` AND s.status=1),0), COALESCE(SUM(NOT ` + isExcluded + ` AND s.status=2),0),
	COALESCE(SUM(NOT ` + isExcluded + ` AND s.status=0),0), COALESCE(SUM(` + isExcluded + `),0)`

const isExcluded = `(s.url IN (SELECT url FROM health_excluded))`

type SourceRow struct {
	ID   int64  `json:"id"` // 0 is iptv-org
	Name string `json:"name"`
	Kind string `json:"kind"` // "api" or "custom"
	Counts
}

type ProfileRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Details struct {
	Scope              string `json:"scope"`
	IntervalH          int    `json:"interval_hours"`
	SelectedHours      int    `json:"selected_interval_hours"`
	Concurrency        int    `json:"concurrency"`
	FailuresBeforeDown int    `json:"failures_before_down"`
	RetryMinutes       int    `json:"retry_minutes"`
	QuietStart         string `json:"quiet_start"`
	QuietEnd           string `json:"quiet_end"`
	TimeZone           string `json:"time_zone"`
	Quiet              bool   `json:"quiet"` // quiet hours are on right now
	Paused             bool   `json:"paused"`
	AutoExcludeDays    int    `json:"auto_exclude_days"` // days of failing before a stream is excluded; 0 = never
	Excluded           int    `json:"excluded"`          // stream addresses left out of health checks

	Running bool   `json:"running"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	PassOK  int    `json:"pass_ok"`
	PassBad int    `json:"pass_bad"`
	Started int64  `json:"started"`
	Kind    string `json:"kind"`  // of the running pass: auto or manual
	Label   string `json:"label"` // what it covers
	Last    Last   `json:"last"`
	Due     int    `json:"due"`  // streams waiting for a check right now
	Next    int64  `json:"next"` // when the next automatic check is due (unix); 0 = none planned (off or paused)

	// every channel falls into exactly one of these, so they add up to Total (the Channels page uses the same rules)
	ChannelStates struct {
		Total      int `json:"total"`
		Working    int `json:"working"`     // a working stream and none failing
		Partly     int `json:"partly"`      // working and failing streams
		NotWorking int `json:"not_working"` // every stream fails
		Pending    int `json:"pending"`     // no verdict yet, and the automatic checks will look at it
		NotChecked int `json:"not_checked"` // no verdict, and the automatic checks do not cover it (scope setting)
	} `json:"channel_states"`

	Totals Counts `json:"totals"`
	Fresh  struct {
		Hour  int `json:"hour"`
		Day   int `json:"day"`
		Older int `json:"older"`
		Never int `json:"never"`
	} `json:"fresh"`
	Profiles struct {
		Counts
		Channels     int `json:"channels"`
		ChannelsDown int `json:"channels_down"`
	} `json:"profiles"`
	Sources []SourceRow `json:"sources"`
	History struct {
		RoundsKept     int `json:"rounds_kept"` // the limits from Settings
		PerChannelKept int `json:"per_channel_kept"`
		TotalKept      int `json:"total_kept"`
		Days           int `json:"days"`
		Rounds         int `json:"rounds"` // stored now
		Checks         int `json:"checks"`
	} `json:"history"`
}

// Details gathers everything the Health page shows about stream health.
func (c *Checker) Details() Details {
	var d Details
	scope := c.set.String("health_scope")
	d.Scope, d.IntervalH, d.Concurrency = scope, c.set.Int("health_interval_hours"), c.set.Int("health_concurrency")
	d.SelectedHours = c.set.Int("health_selected_interval_hours")
	d.FailuresBeforeDown, d.RetryMinutes = c.set.Int("health_failures_before_down"), c.set.Int("health_retry_minutes")
	d.QuietStart, d.QuietEnd, d.TimeZone = c.set.String("health_quiet_start"), c.set.String("health_quiet_end"), c.set.String("time_zone")
	d.Quiet, d.Paused = c.Quiet(), c.Paused()
	d.AutoExcludeDays, d.Excluded = c.set.Int("health_auto_exclude_days"), c.st.ExcludedCount()
	c.mu.Lock()
	d.Running, d.Done, d.Total, d.PassOK, d.PassBad, d.Started, d.Kind, d.Label = c.Running, c.Done, c.Total, c.passOK, c.passBad, c.started, c.kind, c.label
	c.mu.Unlock()
	m := c.st.MetaInt
	d.Last = Last{Started: m("health_last_start"), Finished: m("health_last_end"), Count: int(m("health_last_count")), OK: int(m("health_last_ok")), Bad: int(m("health_last_bad"))}

	db := c.st.DB
	now := time.Now().Unix()
	db.QueryRow(`SELECT `+countCols+` FROM streams s`).
		Scan(&d.Totals.Streams, &d.Totals.OK, &d.Totals.Bad, &d.Totals.Unchecked, &d.Totals.Excluded)
	db.QueryRow(`SELECT COALESCE(SUM(checked_at>=?),0), COALESCE(SUM(checked_at>=? AND checked_at<?),0), COALESCE(SUM(checked_at>0 AND checked_at<?),0), COALESCE(SUM(checked_at=0),0) FROM streams`,
		now-3600, now-86400, now-3600, now-86400).Scan(&d.Fresh.Hour, &d.Fresh.Day, &d.Fresh.Older, &d.Fresh.Never)

	// streams waiting for a check under the current scope (same rule as a pass)
	if scope != "off" {
		cond, args := c.autoCond(now)
		db.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM streams s WHERE (`+cond+`) GROUP BY url)`, args...).Scan(&d.Due)
	}

	c.channelStates(&d, scope)
	d.Next = c.nextCheck(now, scope)
	db.QueryRow(`SELECT kind, label FROM health_rounds WHERE finished>0 ORDER BY id DESC LIMIT 1`).Scan(&d.Last.Kind, &d.Last.Label)

	db.QueryRow(`SELECT `+countCols+` FROM streams s WHERE s.ck IN (SELECT ck FROM profile_channels)`).
		Scan(&d.Profiles.Streams, &d.Profiles.OK, &d.Profiles.Bad, &d.Profiles.Unchecked, &d.Profiles.Excluded)
	db.QueryRow(`SELECT COUNT(DISTINCT ck) FROM profile_channels`).Scan(&d.Profiles.Channels)
	db.QueryRow(`WITH ` + store.ChannelHealthCTE + ` SELECT COUNT(*) FROM ch WHERE ck IN (SELECT ck FROM profile_channels) AND w=0 AND n>0 AND d>=n`).
		Scan(&d.Profiles.ChannelsDown)

	d.Sources = []SourceRow{}
	if rows, err := db.Query(`SELECT s.src, COALESCE(cs.name,''), ` + countCols + `
		FROM streams s LEFT JOIN custom_sources cs ON cs.id=s.src GROUP BY s.src ORDER BY s.src`); err == nil {
		for rows.Next() {
			var r SourceRow
			_ = rows.Scan(&r.ID, &r.Name, &r.Streams, &r.OK, &r.Bad, &r.Unchecked, &r.Excluded)
			r.Kind = "custom"
			if r.ID == 0 {
				r.Kind, r.Name = "api", "iptv-org"
			}
			d.Sources = append(d.Sources, r)
		}
		rows.Close()
	}

	d.History.RoundsKept, d.History.PerChannelKept = c.set.Int("health_history_rounds"), c.set.Int("health_history_per_channel")
	d.History.TotalKept, d.History.Days = c.set.Int("health_history_total"), c.set.Int("health_history_days")
	db.QueryRow(`SELECT COUNT(*) FROM health_rounds WHERE finished>0`).Scan(&d.History.Rounds)
	db.QueryRow(`SELECT COUNT(*) FROM health_checks`).Scan(&d.History.Checks)
	return d
}

// ---- needs attention ----

type DownQuery struct {
	Q, Reason     string
	Offset, Limit int
}

type DownItem struct {
	CK       string       `json:"ck"`
	Name     string       `json:"name"`
	Streams  int          `json:"streams"`
	Checked  int64        `json:"checked_at"`
	Reason   string       `json:"reason"`
	Profiles []ProfileRef `json:"profiles"`
}

type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type DownPage struct {
	Total   int           `json:"total"`
	Items   []DownItem    `json:"items"`
	Reasons []ReasonCount `json:"reasons"`
}

// downCTE lists channels in profiles where every stream that counts towards the status fails (see store/status.go),
// with the latest failure reason.
const downCTE = `WITH ` + store.ChannelHealthCTE + `, down AS (
	SELECT c.ck AS ck, COALESCE(e.name,c.ck) AS name, ch.n AS streams,
	  (SELECT MAX(s.checked_at) FROM streams s WHERE s.ck=c.ck) AS checked,
	  COALESCE(NULLIF((SELECT s2.fail_reason FROM streams s2 WHERE s2.ck=c.ck AND s2.status=2 ORDER BY s2.checked_at DESC LIMIT 1),''),'Unknown') AS reason
	FROM (SELECT DISTINCT ck FROM profile_channels) c LEFT JOIN entries e ON e.ck=c.ck JOIN ch ON ch.ck=c.ck
	WHERE ch.w=0 AND ch.n>0 AND ch.d>=ch.n)`

// Down lists the channels that need attention, filtered and paged.
func (c *Checker) Down(q DownQuery) DownPage {
	page := DownPage{Items: []DownItem{}, Reasons: []ReasonCount{}}
	where, args := []string{"1=1"}, []any{}
	if t := strings.TrimSpace(q.Q); t != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(t) + "%"
		where = append(where, `(name LIKE ? ESCAPE '\' OR ck LIKE ? ESCAPE '\')`)
		args = append(args, like, like)
	}
	if q.Reason != "" {
		where = append(where, `reason=?`)
		args = append(args, q.Reason)
	}
	cond := strings.Join(where, " AND ")
	c.st.DB.QueryRow(downCTE+` SELECT COUNT(*) FROM down WHERE `+cond, args...).Scan(&page.Total)
	if rows, err := c.st.DB.Query(downCTE + ` SELECT reason, COUNT(*) FROM down GROUP BY reason ORDER BY 2 DESC, 1`); err == nil {
		for rows.Next() {
			var r ReasonCount
			_ = rows.Scan(&r.Reason, &r.Count)
			page.Reasons = append(page.Reasons, r)
		}
		rows.Close()
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 10
	}
	rows, err := c.st.DB.Query(downCTE+` SELECT ck,name,streams,checked,reason FROM down WHERE `+cond+` ORDER BY checked DESC, name COLLATE NOCASE LIMIT ? OFFSET ?`,
		append(args, q.Limit, q.Offset)...)
	if err != nil {
		return page
	}
	for rows.Next() {
		var x DownItem
		_ = rows.Scan(&x.CK, &x.Name, &x.Streams, &x.Checked, &x.Reason)
		page.Items = append(page.Items, x)
	}
	rows.Close()
	for i := range page.Items {
		page.Items[i].Profiles = c.profilesOf(page.Items[i].CK, 0)
	}
	return page
}

// profilesOf lists the profiles holding a channel; with an owner only that user's profiles (0 = all).
func (c *Checker) profilesOf(ck string, owner int64) []ProfileRef {
	out := []ProfileRef{}
	if pr, err := c.st.DB.Query(`SELECT p.id, p.name FROM profile_channels pc JOIN profiles p ON p.id=pc.profile_id WHERE pc.ck=? AND (?=0 OR p.owner_id=?) ORDER BY p.name`, ck, owner, owner); err == nil {
		for pr.Next() {
			var r ProfileRef
			_ = pr.Scan(&r.ID, &r.Name)
			out = append(out, r)
		}
		pr.Close()
	}
	return out
}

// ---- one channel ----

type StreamRow struct {
	URL       string `json:"url"`
	UserAgent string `json:"user_agent"`
	Referrer  string `json:"referrer"`
	Host      string `json:"host"`
	Title     string `json:"title"`
	Quality   string `json:"quality"`
	Source    string `json:"source"`
	Status    int    `json:"status"` // 0 unknown, 1 working, 2 not working
	Fails     int    `json:"fail_count"`
	Reason    string `json:"reason"`
	Checked   int64  `json:"checked_at"`
	Skipped   bool   `json:"skipped"`
	Excluded  bool   `json:"excluded"`      // left out of health checks
	ExclSince int64  `json:"excluded_at"`   // since when
	ExclAuto  bool   `json:"excluded_auto"` // by the checker after too many failures, not by hand
}

type Check struct {
	At     int64  `json:"at"`
	Host   string `json:"host"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
	MS     int    `json:"ms"`
}

// Guide is one EPG source that has a programme guide for the channel.
type Guide struct {
	Site     string `json:"site"`
	SiteID   string `json:"site_id"`
	SiteName string `json:"site_name"`
	Lang     string `json:"lang"`
}

// ChannelInfo is what the catalogue knows about a channel.
type ChannelInfo struct {
	Channel    string   `json:"channel"` // iptv-org channel id (empty for custom channels)
	Feed       string   `json:"feed"`
	Country    string   `json:"country"`
	Flag       string   `json:"flag"`
	Network    string   `json:"network"`
	Website    string   `json:"website"`
	Closed     string   `json:"closed"` // date it closed, empty when still on air
	NSFW       bool     `json:"nsfw"`
	Categories []string `json:"categories"`
	Languages  []string `json:"languages"`
	Logo       string   `json:"logo"`
	Source     string   `json:"source"`
	Custom     bool     `json:"custom"`
	Guides     []Guide  `json:"guides"`
}

type ChannelDetail struct {
	CK       string       `json:"ck"`
	Name     string       `json:"name"`
	Info     ChannelInfo  `json:"info"`
	Profiles []ProfileRef `json:"profiles"`
	Streams  []StreamRow  `json:"streams"`
	History  []Check      `json:"history"`
	Kept     int          `json:"history_kept"` // the per-channel limit from Settings
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// Channel returns every stream of a channel with its last result, and its stored check history.
// owner limits the listed profiles to one user's (0 = all).
func (c *Checker) Channel(ck string, owner int64) (ChannelDetail, bool) {
	d := ChannelDetail{CK: ck, Streams: []StreamRow{}, History: []Check{}, Kept: c.set.Int("health_history_per_channel")}
	if err := c.st.DB.QueryRow(`SELECT name FROM entries WHERE ck=?`, ck).Scan(&d.Name); err != nil {
		return d, false
	}
	d.Profiles = c.profilesOf(ck, owner)
	d.Info = c.info(ck)
	rows, err := c.st.DB.Query(`SELECT s.url, COALESCE(s.user_agent,''), COALESCE(s.referrer,''), COALESCE(s.title,''), COALESCE(s.quality,''), s.src, COALESCE(cs.name,''), s.status, s.fail_count, s.fail_reason, s.checked_at,
		  COALESCE(x.at,0), COALESCE(x.auto,0)
		FROM streams s LEFT JOIN custom_sources cs ON cs.id=s.src LEFT JOIN health_excluded x ON x.url=s.url WHERE s.ck=? ORDER BY s.status=1 DESC, s.id`, ck)
	if err == nil {
		for rows.Next() {
			var r StreamRow
			var src, auto int64
			_ = rows.Scan(&r.URL, &r.UserAgent, &r.Referrer, &r.Title, &r.Quality, &src, &r.Source, &r.Status, &r.Fails, &r.Reason, &r.Checked, &r.ExclSince, &auto)
			r.Excluded, r.ExclAuto = r.ExclSince > 0, auto == 1
			if src == 0 {
				r.Source = "iptv-org"
			}
			r.Host = hostOf(r.URL)
			d.Streams = append(d.Streams, r)
		}
		rows.Close()
	}
	if rows, err := c.st.DB.Query(`SELECT at,url,ok,reason,ms FROM health_checks WHERE ck=? ORDER BY at DESC, id DESC LIMIT 200`, ck); err == nil {
		for rows.Next() {
			var x Check
			var u string
			var ok int
			_ = rows.Scan(&x.At, &u, &ok, &x.Reason, &x.MS)
			x.Host, x.OK = hostOf(u), ok == 1
			d.History = append(d.History, x)
		}
		rows.Close()
	}
	return d, true
}

// info collects the catalogue facts about a channel: country, categories, languages, guides, …
func (c *Checker) info(ck string) ChannelInfo {
	db := c.st.DB
	in := ChannelInfo{Categories: []string{}, Languages: []string{}, Guides: []Guide{}}
	var cats, langs string
	var src int64
	_ = db.QueryRow(`SELECT COALESCE(channel,''), COALESCE(feed,''), COALESCE(country,''), COALESCE(categories,'[]'), COALESCE(languages,'[]'),
		COALESCE(nsfw,0), COALESCE(logo,''), COALESCE(src,0) FROM entries WHERE ck=?`, ck).Scan(&in.Channel, &in.Feed, &in.Country, &cats, &langs, &in.NSFW, &in.Logo, &src)
	in.Custom = src > 0
	if in.Custom {
		_ = db.QueryRow(`SELECT name FROM custom_sources WHERE id=?`, src).Scan(&in.Source)
	} else {
		in.Source = "iptv-org"
	}
	if in.Country != "" {
		var name, flag string
		if db.QueryRow(`SELECT name, flag FROM countries WHERE code=?`, in.Country).Scan(&name, &flag) == nil {
			in.Flag = flag
			in.Country = name
		}
	}
	_ = db.QueryRow(`SELECT COALESCE(network,''), COALESCE(website,''), COALESCE(closed,'') FROM channels WHERE id=?`, in.Channel).Scan(&in.Network, &in.Website, &in.Closed)
	name := func(table, id string) string {
		var n string
		if db.QueryRow(`SELECT name FROM `+table+` WHERE id=?`, id).Scan(&n) == nil && n != "" {
			return n
		}
		return id
	}
	var ids []string
	_ = json.Unmarshal([]byte(cats), &ids)
	for _, id := range ids {
		if in.Custom {
			in.Categories = append(in.Categories, id) // a custom channel's group title
		} else {
			in.Categories = append(in.Categories, name("categories", id))
		}
	}
	ids = nil
	_ = json.Unmarshal([]byte(langs), &ids)
	for _, code := range ids {
		var n string
		if db.QueryRow(`SELECT name FROM languages WHERE code=?`, code).Scan(&n) != nil || n == "" {
			n = code
		}
		in.Languages = append(in.Languages, n)
	}
	if in.Channel != "" {
		if rows, err := db.Query(`SELECT site, site_id, COALESCE(site_name,''), COALESCE(lang,'') FROM guides WHERE channel=? AND (feed=? OR feed='') ORDER BY site`, in.Channel, in.Feed); err == nil {
			for rows.Next() {
				var g Guide
				_ = rows.Scan(&g.Site, &g.SiteID, &g.SiteName, &g.Lang)
				in.Guides = append(in.Guides, g)
			}
			rows.Close()
		}
	}
	return in
}

// nextCheck is when the automatic checker will next have something to do: the earliest moment a stream
// falls due under the scope (the same rules as autoCond). Never in the past; 0 when nothing is planned.
func (c *Checker) nextCheck(now int64, scope string) int64 {
	if scope == "off" || c.Paused() {
		return 0
	}
	next := int64(-1)
	earliest := func(where string, after time.Duration, args ...any) {
		var first int64 = -1
		c.st.DB.QueryRow(`SELECT COALESCE(MIN(s.checked_at), -1) FROM streams s WHERE (`+where+`) AND `+notExcluded, args...).Scan(&first)
		if first < 0 {
			return
		}
		if t := first + int64(after.Seconds()); next < 0 || t < next {
			next = t
		}
	}
	earliest(`s.ck IN (SELECT ck FROM profile_channels)`, c.set.Hours("health_selected_interval_hours"))
	if scope == "all" {
		earliest(`1=1`, c.set.Hours("health_interval_hours"))
	}
	earliest(`s.fail_count>0 AND s.fail_count<?`, c.set.Minutes("health_retry_minutes"), c.set.Int("health_failures_before_down"))
	if next < 0 {
		return 0
	}
	if next < now {
		next = now
	}
	return next
}

// channelStates counts the channels per state (the rules are in store/status.go): working, partly working, not working.
// Anything else has no verdict yet: pending if the scope makes the checker look at it (everything, or channels in
// profiles) and not all its streams are excluded from checks, otherwise not checked.
func (c *Checker) channelStates(d *Details, scope string) {
	all, selected := 0, 0
	switch scope {
	case "all":
		all = 1
	case "selected":
		selected = 1
	}
	c.st.DB.QueryRow(`WITH `+store.ChannelHealthCTE+`,
	k AS (SELECT `+store.ChannelStateSQL+` AS st, COALESCE(ch.t,0)-COALESCE(ch.x,0) AS checked, e.ck IN (SELECT ck FROM profile_channels) AS inp
		FROM entries e LEFT JOIN ch ON ch.ck=e.ck)
	SELECT COUNT(*), COALESCE(SUM(st=0),0), COALESCE(SUM(st=1),0), COALESCE(SUM(st=3),0),
		COALESCE(SUM(st=2 AND checked>0 AND (?=1 OR (?=1 AND inp))),0) FROM k`, all, selected).
		Scan(&d.ChannelStates.Total, &d.ChannelStates.Working, &d.ChannelStates.Partly, &d.ChannelStates.NotWorking, &d.ChannelStates.Pending)
	cs := &d.ChannelStates
	cs.NotChecked = cs.Total - cs.Working - cs.Partly - cs.NotWorking - cs.Pending
}
