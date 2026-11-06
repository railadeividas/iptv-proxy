// Package sync pulls the iptv-org API into SQLite and refreshes custom sources.
package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	gosync "sync"
	"time"

	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
)

type Syncer struct {
	st  *store.Store
	set *settings.Settings

	mu      gosync.Mutex
	running bool

	// OnProfilesChanged is called when a source change altered what profiles contain.
	OnProfilesChanged func()
}

func New(st *store.Store, set *settings.Settings) *Syncer {
	return &Syncer{st: st, set: set}
}

// client is built per request so a changed timeout applies right away.
func (s *Syncer) client() *http.Client {
	return &http.Client{Timeout: s.set.Minutes("download_timeout_minutes")}
}

func (s *Syncer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Run syncs on startup when data is missing or stale, then whenever the
// sync interval (a setting) has passed.
func (s *Syncer) Run(ctx context.Context) {
	check := func() {
		age := time.Since(time.Unix(s.st.MetaInt("synced_at"), 0))
		if !s.st.Ready() || age >= s.set.Hours("sync_interval_hours") {
			if err := s.SyncNow(); err != nil {
				slog.Error("api sync failed", "err", err)
			}
		}
		s.RefreshAllCustom()
	}
	check()
	t := time.NewTicker(15 * time.Minute) // cheap staleness check; survives restarts
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}

type apiChannel struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Country    string   `json:"country"`
	Network    *string  `json:"network"`
	Categories []string `json:"categories"`
	NSFW       bool     `json:"is_nsfw"`
	Closed     *string  `json:"closed"`
	Website    *string  `json:"website"`
}
type apiFeed struct {
	Channel   string   `json:"channel"`
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	IsMain    bool     `json:"is_main"`
	Languages []string `json:"languages"`
	Format    *string  `json:"format"`
}
type apiStream struct {
	Channel   *string `json:"channel"`
	Feed      *string `json:"feed"`
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Quality   *string `json:"quality"`
	UserAgent *string `json:"user_agent"`
	Referrer  *string `json:"referrer"`
}
type apiGuide struct {
	Channel  *string `json:"channel"`
	Feed     *string `json:"feed"`
	Site     string  `json:"site"`
	SiteID   string  `json:"site_id"`
	SiteName string  `json:"site_name"`
	Lang     string  `json:"lang"`
}
type apiLogo struct {
	Channel string  `json:"channel"`
	Feed    *string `json:"feed"`
	URL     string  `json:"url"`
}
type apiCategory struct{ ID, Name string }
type apiCountry struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Flag string `json:"flag"`
}
type apiLanguage struct{ Code, Name string }

var files = []string{"channels", "streams", "feeds", "guides", "logos", "categories", "countries", "languages"}

func (s *Syncer) fetch(name string, conditional bool) (data []byte, etag string, notModified bool, err error) {
	req, _ := http.NewRequest("GET", s.set.String("api_url")+"/"+name+".json", nil)
	req.Header.Set("User-Agent", s.set.String("fetch_user_agent"))
	if conditional {
		if e := s.st.Meta("etag:" + name); e != "" {
			req.Header.Set("If-None-Match", e)
		}
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, "", true, nil
	}
	if resp.StatusCode != 200 {
		return nil, "", false, fmt.Errorf("%s.json: HTTP %d", name, resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, s.set.MB("api_max_download_mb")))
	return data, resp.Header.Get("ETag"), false, err
}

// SyncNow downloads the API data and swaps it in atomically. On any failure
// the previous data stays untouched.
func (s *Syncer) SyncNow() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("sync already running")
	}
	s.running = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()

	err := s.sync()
	if err != nil {
		s.st.SetMeta("sync_error", err.Error())
	} else {
		s.st.SetMeta("sync_error", "")
	}
	return err
}

func (s *Syncer) sync() error {
	start := time.Now()
	data := map[string][]byte{}
	etags := map[string]string{}
	changed := false
	var stale []string
	for _, f := range files {
		d, et, nm, err := s.fetch(f, s.st.Ready())
		if err != nil {
			return err
		}
		if nm {
			stale = append(stale, f)
			continue
		}
		changed = true
		data[f], etags[f] = d, et
	}
	if !changed {
		s.st.SetMetaInt("synced_at", time.Now().Unix())
		slog.Info("api sync: no changes")
		return nil
	}
	for _, f := range stale { // unchanged files still need re-reading for a full rebuild
		d, et, _, err := s.fetch(f, false)
		if err != nil {
			return err
		}
		data[f], etags[f] = d, et
	}

	var (
		channels   []apiChannel
		feeds      []apiFeed
		streams    []apiStream
		guides     []apiGuide
		logos      []apiLogo
		categories []apiCategory
		countries  []apiCountry
		languages  []apiLanguage
	)
	for name, dst := range map[string]any{"channels": &channels, "feeds": &feeds, "streams": &streams, "guides": &guides,
		"logos": &logos, "categories": &categories, "countries": &countries, "languages": &languages} {
		if err := json.Unmarshal(data[name], dst); err != nil {
			return fmt.Errorf("parse %s.json: %w", name, err)
		}
	}
	if len(channels) == 0 || len(streams) == 0 {
		return fmt.Errorf("API returned empty channels or streams; keeping previous data")
	}

	// Remember health across syncs.
	type health struct {
		status int
		at     int64
		fails  int
		since  int64
		reason string
	}
	prev := map[string]health{}
	if rows, err := s.st.DB.Query(`SELECT url,status,checked_at,fail_count,fail_since,fail_reason FROM streams WHERE src=0 AND checked_at>0`); err == nil {
		for rows.Next() {
			var u string
			var h health
			_ = rows.Scan(&u, &h.status, &h.at, &h.fails, &h.since, &h.reason)
			prev[u] = h
		}
		rows.Close()
	}

	tx, err := s.st.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"channels", "feeds", "logos", "guides", "categories", "countries", "languages"} {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM streams WHERE src=0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM skipped WHERE source='api'`); err != nil {
		return err
	}

	chSet := make(map[string]bool, len(channels))
	{
		st, _ := tx.Prepare(`INSERT OR REPLACE INTO channels VALUES(?,?,?,?,?,?,?,?)`)
		for _, c := range channels {
			chSet[c.ID] = true
			cats, _ := json.Marshal(nonNil(c.Categories))
			nsfw := 0
			if c.NSFW {
				nsfw = 1
			}
			if _, err := st.Exec(c.ID, c.Name, c.Country, deref(c.Network), string(cats), nsfw, deref(c.Closed), deref(c.Website)); err != nil {
				return err
			}
		}
		st.Close()
	}
	{
		st, _ := tx.Prepare(`INSERT OR REPLACE INTO feeds VALUES(?,?,?,?,?,?)`)
		for _, f := range feeds {
			langs, _ := json.Marshal(nonNil(f.Languages))
			m := 0
			if f.IsMain {
				m = 1
			}
			if _, err := st.Exec(f.Channel, f.ID, f.Name, m, string(langs), deref(f.Format)); err != nil {
				return err
			}
		}
		st.Close()
	}
	{
		st, _ := tx.Prepare(`INSERT INTO logos VALUES(?,?,?)`)
		for _, l := range logos {
			if _, err := st.Exec(l.Channel, deref(l.Feed), l.URL); err != nil {
				return err
			}
		}
		st.Close()
	}
	{
		st, _ := tx.Prepare(`INSERT INTO guides VALUES(?,?,?,?,?,?)`)
		for _, g := range guides {
			if g.Channel == nil || *g.Channel == "" {
				continue
			}
			if _, err := st.Exec(*g.Channel, deref(g.Feed), g.Site, g.SiteID, g.SiteName, g.Lang); err != nil {
				return err
			}
		}
		st.Close()
	}
	for _, c := range categories {
		_, _ = tx.Exec(`INSERT OR REPLACE INTO categories VALUES(?,?)`, c.ID, c.Name)
	}
	for _, c := range countries {
		_, _ = tx.Exec(`INSERT OR REPLACE INTO countries VALUES(?,?,?)`, c.Code, c.Name, c.Flag)
	}
	for _, l := range languages {
		_, _ = tx.Exec(`INSERT OR REPLACE INTO languages VALUES(?,?)`, l.Code, l.Name)
	}

	var nStreams, nSkipped int
	{
		ins, _ := tx.Prepare(`INSERT INTO streams(src,ck,channel,feed,title,url,quality,user_agent,referrer,status,checked_at,fail_count,fail_since,fail_reason) VALUES(0,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		skip, _ := tx.Prepare(`INSERT INTO skipped(source,ref,url,reason) VALUES('api',?,?,?)`)
		seen := map[string]bool{}
		for _, x := range streams {
			ref := x.Title
			if ref == "" {
				ref = x.URL
			}
			reason := ""
			switch {
			case !validURL(x.URL):
				reason = "invalid or non-http(s) stream URL"
			case x.Channel == nil || *x.Channel == "":
				reason = "stream has no channel ID"
			case !chSet[*x.Channel]:
				reason = "unknown channel ID " + *x.Channel
			}
			if reason != "" {
				nSkipped++
				_, _ = skip.Exec(ref, x.URL, reason)
				continue
			}
			ck := *x.Channel
			if x.Feed != nil && *x.Feed != "" {
				ck += "@" + *x.Feed
			}
			if seen[ck+"|"+x.URL] {
				continue
			}
			seen[ck+"|"+x.URL] = true
			h := prev[x.URL]
			if _, err := ins.Exec(ck, *x.Channel, deref(x.Feed), x.Title, x.URL, deref(x.Quality), deref(x.UserAgent), deref(x.Referrer), h.status, h.at, h.fails, h.since, h.reason); err != nil {
				return err
			}
			nStreams++
		}
		ins.Close()
		skip.Close()
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := s.st.RebuildEntries(); err != nil {
		return err
	}
	for f, e := range etags {
		if e != "" {
			s.st.SetMeta("etag:"+f, e)
		}
	}
	s.st.SetMetaInt("synced_at", time.Now().Unix())
	s.st.SetMetaInt("epg_dirty", 1) // guides may have changed
	slog.Info("api sync done", "channels", len(channels), "streams", nStreams, "skipped", nSkipped, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
