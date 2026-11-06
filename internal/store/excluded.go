package store

import (
	"log/slog"
	"strings"
	"time"
)

// Excluded streams are left out of every health check, automatic or by hand. They keep their last status.
// The list is keyed by stream address, like the status, so it survives syncs and source refreshes.

// SetExcluded excludes a stream address from health checks or includes it again. Including it clears its run of
// failures, so automatic exclusion starts counting the days afresh. It reports false when no stream has that address.
func (s *Store) SetExcluded(url string, excluded bool) bool {
	var n int
	if _ = s.DB.QueryRow(`SELECT COUNT(*) FROM streams WHERE url=?`, url).Scan(&n); n == 0 {
		return false
	}
	if excluded {
		s.logExec("exclude stream")(s.DB.Exec(`INSERT OR IGNORE INTO health_excluded(url,at,auto,reason)
			SELECT url, ?, 0, fail_reason FROM streams WHERE url=? ORDER BY checked_at DESC LIMIT 1`, time.Now().Unix(), url))
	} else {
		s.logExec("include stream")(s.DB.Exec(`DELETE FROM health_excluded WHERE url=?`, url))
		s.logExec("include stream")(s.DB.Exec(`UPDATE streams SET fail_count=0, fail_since=0 WHERE url=?`, url))
	}
	s.notify("streams")
	return true
}

// AutoExclude excludes a stream address whose failures in a row began at least `days` days ago (0 never excludes).
// It reports whether the stream was excluded now.
func (s *Store) AutoExclude(url string, days int) bool {
	if days <= 0 {
		return false
	}
	now := time.Now()
	res, err := s.DB.Exec(`INSERT OR IGNORE INTO health_excluded(url,at,auto,reason)
		SELECT url, ?, 1, fail_reason FROM streams WHERE url=? AND fail_count>0 AND fail_since>0 AND fail_since<=? ORDER BY checked_at DESC LIMIT 1`,
		now.Unix(), url, now.AddDate(0, 0, -days).Unix())
	if err != nil {
		slog.Error("health: auto exclude", "err", err)
		return false
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.notify("streams")
	}
	return n > 0
}

// ExcludedStream is one stream address left out of health checks, with a channel that uses it.
type ExcludedStream struct {
	URL          string `json:"url"`
	CK           string `json:"ck"`
	Name         string `json:"name"`
	Since        int64  `json:"since"`
	Auto         bool   `json:"auto"`   // excluded by the checker after too many failures, not by hand
	Reason       string `json:"reason"` // the failure reason when it was excluded
	Status       int    `json:"status"` // last known: 0 not checked yet, 1 working, 2 not working
	Fails        int    `json:"fail_count"`
	FailingSince int64  `json:"failing_since"` // when its current run of failures began; 0 when it is not failing
}

// excludedCTE lists excluded addresses that are still in the catalogue, each with its first stream.
const excludedCTE = `WITH x AS (
	SELECT h.url AS url, s.ck AS ck, COALESCE(e.name, s.ck) AS name, h.at AS since, h.auto AS auto, h.reason AS reason,
	  s.status AS status, s.fail_count AS fails, s.fail_since AS fsince
	FROM health_excluded h JOIN streams s ON s.id=(SELECT MIN(id) FROM streams WHERE url=h.url) LEFT JOIN entries e ON e.ck=s.ck)`

// ExcludedCount counts the excluded stream addresses that are still in the catalogue.
func (s *Store) ExcludedCount() int {
	var n int
	_ = s.DB.QueryRow(excludedCTE + ` SELECT COUNT(*) FROM x`).Scan(&n)
	return n
}

// Excluded lists excluded streams, newest first, filtered by channel name, key or address, with the total count.
func (s *Store) Excluded(q string, offset, limit int) ([]ExcludedStream, int) {
	where, args := "1=1", []any{}
	if t := strings.TrimSpace(q); t != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(t) + "%"
		where = `(name LIKE ? ESCAPE '\' OR ck LIKE ? ESCAPE '\' OR url LIKE ? ESCAPE '\')`
		args = append(args, like, like, like)
	}
	out := []ExcludedStream{}
	var total int
	_ = s.DB.QueryRow(excludedCTE+` SELECT COUNT(*) FROM x WHERE `+where, args...).Scan(&total)
	rows, err := s.DB.Query(excludedCTE+` SELECT url,ck,name,since,auto,reason,status,fails,fsince FROM x WHERE `+where+
		` ORDER BY since DESC, name COLLATE NOCASE LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return out, total
	}
	defer rows.Close()
	for rows.Next() {
		var x ExcludedStream
		var auto int
		if rows.Scan(&x.URL, &x.CK, &x.Name, &x.Since, &auto, &x.Reason, &x.Status, &x.Fails, &x.FailingSince) == nil {
			x.Auto = auto == 1
			out = append(out, x)
		}
	}
	return out, total
}
