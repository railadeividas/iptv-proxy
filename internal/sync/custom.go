package sync

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"iptv-proxy/internal/playlist"
)

type CustomSource struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	LastOK    int64  `json:"last_ok"`
	LastError string `json:"last_error"`
	Streams   int    `json:"streams"`
	Channels  int    `json:"channels"`
	Working   int    `json:"working"`
	Dead      int    `json:"dead"`
	Unchecked int    `json:"unchecked"`
	Excluded  int    `json:"excluded"` // excluded from health checks; not in Working, Dead or Unchecked
	Skipped   int    `json:"skipped"`

	RefreshHours int   `json:"refresh_hours"` // 0 = no automatic refresh
	LastTry      int64 `json:"last_try"`
	NextRefresh  int64 `json:"next_refresh"` // unix time, 0 when not scheduled
}

func (s *Syncer) ListCustom() ([]CustomSource, error) {
	rows, err := s.st.DB.Query(`SELECT c.id,c.name,COALESCE(c.url,''),c.last_ok,c.last_error,
		(SELECT COUNT(*) FROM streams WHERE src=c.id),
		(SELECT COUNT(DISTINCT ck) FROM streams WHERE src=c.id),
		(SELECT COUNT(*) FROM streams WHERE src=c.id AND status=1 AND url NOT IN (SELECT url FROM health_excluded)),
		(SELECT COUNT(*) FROM streams WHERE src=c.id AND status=2 AND url NOT IN (SELECT url FROM health_excluded)),
		(SELECT COUNT(*) FROM streams WHERE src=c.id AND status=0 AND url NOT IN (SELECT url FROM health_excluded)),
		(SELECT COUNT(*) FROM streams WHERE src=c.id AND url IN (SELECT url FROM health_excluded)),
		(SELECT COUNT(*) FROM skipped WHERE source='custom:'||c.id), c.refresh_hours, c.last_try
		FROM custom_sources c ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomSource{}
	for rows.Next() {
		var c CustomSource
		if err := rows.Scan(&c.ID, &c.Name, &c.URL, &c.LastOK, &c.LastError, &c.Streams, &c.Channels, &c.Working, &c.Dead, &c.Unchecked, &c.Excluded, &c.Skipped, &c.RefreshHours, &c.LastTry); err != nil {
			return nil, err
		}
		if c.URL != "" && c.RefreshHours > 0 {
			c.NextRefresh = max(c.LastTry, c.LastOK) + int64(c.RefreshHours)*3600
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddCustom creates a source from an M3U URL or pasted/uploaded M3U text.
func (s *Syncer) AddCustom(name, srcURL, m3u string) (int64, error) {
	name = strings.TrimSpace(name)
	srcURL = strings.TrimSpace(srcURL)
	if srcURL == "" && strings.TrimSpace(m3u) == "" {
		return 0, fmt.Errorf("provide an M3U URL or playlist text")
	}
	if srcURL != "" && !validURL(srcURL) {
		return 0, fmt.Errorf("invalid URL")
	}
	if name == "" {
		name = srcURL
		if name == "" {
			name = "Custom playlist"
		}
	}
	keep := ""
	if srcURL == "" {
		keep = m3u // pasted / uploaded text is kept so it can be edited later
	}
	res, err := s.st.DB.Exec(`INSERT INTO custom_sources(name,url,created_at,m3u,refresh_hours) VALUES(?,?,?,?,?)`, name, nullIfEmpty(srcURL), time.Now().Unix(), keep, s.set.Int("custom_refresh_hours"))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	var body io.Reader
	if srcURL != "" {
		resp, err := s.getURL(srcURL)
		if err != nil {
			s.st.DB.Exec(`DELETE FROM custom_sources WHERE id=?`, id)
			return 0, err
		}
		defer resp.Body.Close()
		body = io.LimitReader(resp.Body, s.set.MB("custom_max_download_mb"))
	} else {
		body = strings.NewReader(m3u)
	}
	if err := s.importM3U(id, body); err != nil {
		s.st.DB.Exec(`DELETE FROM custom_sources WHERE id=?`, id)
		return 0, err
	}
	return id, nil
}

// CustomEdit holds the editable fields of a custom source.
type CustomEdit struct {
	Name         string
	URL          string
	M3U          string // optional replacement playlist text (sources without a URL)
	RefreshHours int
}

// UpdateCustom changes a source. A new URL or replacement text is imported
// first; if that fails nothing is changed and the old list stays.
func (s *Syncer) UpdateCustom(id int64, in CustomEdit) error {
	var oldName, oldURL string
	if err := s.st.DB.QueryRow(`SELECT name, COALESCE(url,'') FROM custom_sources WHERE id=?`, id).Scan(&oldName, &oldURL); err != nil {
		return err
	}
	name, u := strings.TrimSpace(in.Name), strings.TrimSpace(in.URL)
	var newM3U *string // set when the stored playlist text changes
	if name == "" {
		name = oldName
	}
	if u != "" && !validURL(u) {
		return fmt.Errorf("invalid URL")
	}
	if in.RefreshHours < 0 || in.RefreshHours > 24*30 {
		return fmt.Errorf("refresh interval must be between 0 and 720 hours")
	}
	switch {
	case u != "" && u != oldURL:
		resp, err := s.getURL(u)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := s.importM3U(id, io.LimitReader(resp.Body, s.set.MB("custom_max_download_mb"))); err != nil {
			return err
		}
		empty := ""
		newM3U = &empty // now a URL source: the old pasted text no longer applies
	case u == "" && strings.TrimSpace(in.M3U) != "":
		if err := s.importM3U(id, strings.NewReader(in.M3U)); err != nil {
			return err
		}
		newM3U = &in.M3U
	}
	if _, err := s.st.DB.Exec(`UPDATE custom_sources SET name=?, url=?, refresh_hours=? WHERE id=?`, name, nullIfEmpty(u), in.RefreshHours, id); err != nil {
		return err
	}
	if newM3U != nil {
		if _, err := s.st.DB.Exec(`UPDATE custom_sources SET m3u=? WHERE id=?`, *newM3U, id); err != nil {
			return err
		}
	}
	s.st.Notify2("catalog")
	return nil
}

// CustomPlaylist returns the editable playlist text of an uploaded / pasted
// source. URL sources are re-fetched, so they have no local text.
func (s *Syncer) CustomPlaylist(id int64) (string, error) {
	var url, m3u string
	if err := s.st.DB.QueryRow(`SELECT COALESCE(url,''), m3u FROM custom_sources WHERE id=?`, id).Scan(&url, &m3u); err != nil {
		return "", err
	}
	if url != "" {
		return "", nil
	}
	return m3u, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Syncer) getURL(u string) (*http.Response, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", s.set.String("fetch_user_agent"))
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func (s *Syncer) DeleteCustom(id int64) error {
	removed := s.sourceCKs(id)
	tx, err := s.st.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM streams WHERE src=?`, `DELETE FROM custom_sources WHERE id=?`} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM skipped WHERE source=?`, fmt.Sprintf("custom:%d", id)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.dropFromProfiles(removed)
	return s.st.RebuildEntries()
}

// dropFromProfiles takes vanished channels out of every profile.
func (s *Syncer) dropFromProfiles(cks []string) {
	if len(cks) == 0 {
		return
	}
	if changed, err := s.st.DropFromProfiles(cks); err != nil {
		slog.Warn("could not update profiles", "err", err)
	} else if changed && s.OnProfilesChanged != nil {
		s.OnProfilesChanged()
	}
}

// sourceCKs lists the channel keys a custom source currently provides.
func (s *Syncer) sourceCKs(id int64) []string {
	var out []string
	if rows, err := s.st.DB.Query(`SELECT DISTINCT ck FROM streams WHERE src=?`, id); err == nil {
		for rows.Next() {
			var ck string
			_ = rows.Scan(&ck)
			out = append(out, ck)
		}
		rows.Close()
	}
	return out
}

// RefreshCustom re-fetches a URL source. A failure keeps the previous list.
func (s *Syncer) RefreshCustom(id int64) error {
	var u string
	if err := s.st.DB.QueryRow(`SELECT COALESCE(url,'') FROM custom_sources WHERE id=?`, id).Scan(&u); err != nil {
		return err
	}
	if u == "" {
		return fmt.Errorf("source has no URL to refresh")
	}
	resp, err := s.getURL(u)
	if err == nil {
		defer resp.Body.Close()
		err = s.importM3U(id, io.LimitReader(resp.Body, s.set.MB("custom_max_download_mb")))
	}
	if err != nil {
		s.st.DB.Exec(`UPDATE custom_sources SET last_error=?, last_try=? WHERE id=?`, err.Error(), time.Now().Unix(), id)
		s.st.Notify2("catalog")
	}
	return err
}

func (s *Syncer) RefreshAllCustom() {
	srcs, err := s.ListCustom()
	if err != nil {
		return
	}
	for _, c := range srcs {
		if c.URL == "" || c.RefreshHours <= 0 || time.Now().Unix() < c.NextRefresh {
			continue
		}
		if err := s.RefreshCustom(c.ID); err != nil {
			slog.Warn("custom source refresh failed", "id", c.ID, "err", err)
		}
	}
}

func (s *Syncer) importM3U(id int64, r io.Reader) error {
	items, skipped, err := playlist.Parse(r)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return fmt.Errorf("playlist contains no valid streams (%d skipped)", len(skipped))
	}
	type h struct {
		st     int
		at     int64
		fails  int
		since  int64
		reason string
	}
	prev := map[string]h{}
	oldCK := map[string]bool{}      // channel keys the source has now
	oldByURL := map[string]string{} // stream URL -> its current channel key
	if rows, err := s.st.DB.Query(`SELECT url,status,checked_at,ck,fail_count,fail_since,fail_reason FROM streams WHERE src=? ORDER BY id`, id); err == nil {
		for rows.Next() {
			var u, ck string
			var x h
			_ = rows.Scan(&u, &x.st, &x.at, &ck, &x.fails, &x.since, &x.reason)
			prev[u] = x
			oldCK[ck] = true
			if _, ok := oldByURL[u]; !ok {
				oldByURL[u] = ck
			}
		}
		rows.Close()
	}
	// A channel's key comes from its tvg-id (or name). If you only retitle an
	// entry, its stream URL still identifies it, so it keeps its old key and
	// with it its place and settings in every profile.
	hashCK := func(it playlist.Item) string {
		key := it.TvgID
		if key == "" {
			key = it.Name
		}
		sum := sha1.Sum([]byte(strings.ToLower(key)))
		return fmt.Sprintf("custom:%d:%s", id, hex.EncodeToString(sum[:4]))
	}
	wanted := map[string]bool{}
	for _, it := range items {
		wanted[hashCK(it)] = true
	}
	reused := map[string]bool{}
	keyFor := func(it playlist.Item) string {
		ck := hashCK(it)
		if oldCK[ck] {
			return ck
		}
		if oc, ok := oldByURL[it.URL]; ok && !wanted[oc] && !reused[oc] {
			reused[oc] = true
			return oc
		}
		return ck
	}
	tx, err := s.st.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	src := fmt.Sprintf("custom:%d", id)
	if _, err := tx.Exec(`DELETE FROM streams WHERE src=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM skipped WHERE source=?`, src); err != nil {
		return err
	}
	seen := map[string]bool{}
	kept := map[string]bool{}
	for _, it := range items {
		ck := keyFor(it)
		kept[ck] = true
		if seen[ck+it.URL] {
			continue
		}
		seen[ck+it.URL] = true
		x := prev[it.URL]
		if _, err := tx.Exec(`INSERT INTO streams(src,ck,title,url,user_agent,referrer,logo,group_title,tvg_id,status,checked_at,fail_count,fail_since,fail_reason) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, ck, it.Name, it.URL, it.UserAgent, it.Referrer, it.Logo, it.Group, it.TvgID, x.st, x.at, x.fails, x.since, x.reason); err != nil {
			return err
		}
	}
	for _, sk := range skipped {
		_, _ = tx.Exec(`INSERT INTO skipped(source,ref,url,reason) VALUES(?,?,?,?)`, src, sk.Ref, sk.URL, sk.Reason)
	}
	if _, err := tx.Exec(`UPDATE custom_sources SET last_ok=?, last_try=?, last_error='' WHERE id=?`, time.Now().Unix(), time.Now().Unix(), id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// channels that are gone from the playlist disappear from every profile too
	var gone []string
	for ck := range oldCK {
		if !kept[ck] {
			gone = append(gone, ck)
		}
	}
	s.dropFromProfiles(gone)
	return s.st.RebuildEntries()
}
