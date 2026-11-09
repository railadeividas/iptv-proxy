package store

import (
	"database/sql"
	"log/slog"
	"time"
)

// Probe is the result of checking one stream URL.
type Probe struct {
	URL    string
	OK     bool
	Reason string // why it failed; for a working stream the HTTP status it answered with
	MS     int    // time to the first data
}

// RecordProbe stores a check result. A failure of a stream that had a result
// (working) only marks it not working after `threshold` failures in a row; until
// then its status stays and the retry counter grows. A stream that was never
// checked has no status to protect, so its first failure marks it at once.
// keepPerChannel > 0 also appends it to the history of
// every channel using that URL.
func (s *Store) RecordProbe(p Probe, threshold, keepPerChannel int) {
	now := time.Now().Unix()
	if p.OK {
		s.logExec("record probe")(s.DB.Exec(`UPDATE streams SET status=?, fail_count=0, fail_since=0, fail_reason='', checked_at=? WHERE url=?`, StatusOK, now, p.URL))
	} else {
		s.logExec("record probe")(s.DB.Exec(`UPDATE streams SET fail_count=fail_count+1, fail_reason=?, checked_at=?, `+failSince+`,
			status=CASE WHEN status=0 OR fail_count+1>=? THEN ? ELSE status END WHERE url=?`, p.Reason, now, now, threshold, StatusDead, p.URL))
	}
	if keepPerChannel > 0 {
		ok := 0
		if p.OK {
			ok = 1
		}
		s.logExec("record probe history")(s.DB.Exec(`INSERT INTO health_checks(ck,url,at,ok,reason,ms) SELECT DISTINCT ck,?,?,?,?,? FROM streams WHERE url=?`,
			p.URL, now, ok, p.Reason, p.MS, p.URL))
	}
	s.notify("streams")
}

// failSince starts a run of failures (its argument is now); a run already going keeps its start.
const failSince = `fail_since=CASE WHEN fail_count=0 OR fail_since=0 THEN ? ELSE fail_since END`

// logExec reports a failed write instead of dropping it silently.
func (s *Store) logExec(what string) func(sql.Result, error) {
	return func(_ sql.Result, err error) {
		if err != nil {
			slog.Error("health: "+what, "err", err)
		}
	}
}

// MarkPlayback records what happened when a viewer tried a stream: a failure marks it not working at once, a success marks it
// working and clears its failures. When the status itself changes (not working or not checked yet becomes working, or anything
// becomes not working) it is also put in the channel's check history, so the history explains the status; a viewer that keeps
// retrying a dead stream, or plays a working one, does not fill it. keepPerChannel is the history setting (0 = no history).
func (s *Store) MarkPlayback(url string, ok bool, keepPerChannel int) {
	now := time.Now().Unix()
	var status int
	_ = s.DB.QueryRow(`SELECT status FROM streams WHERE url=? LIMIT 1`, url).Scan(&status)
	record := func(good bool, reason string) {
		if keepPerChannel <= 0 {
			return
		}
		v := 0
		if good {
			v = 1
		}
		s.logExec("record playback history")(s.DB.Exec(`INSERT INTO health_checks(ck,url,at,ok,reason,ms) SELECT DISTINCT ck,?,?,?,?,0 FROM streams WHERE url=?`, url, now, v, reason, url))
	}
	if ok {
		if status != StatusOK {
			record(true, "Played by a viewer")
		}
		_, _ = s.DB.Exec(`UPDATE streams SET status=?, fail_count=0, fail_since=0, fail_reason='', checked_at=? WHERE url=?`, StatusOK, now, url)
	} else {
		if status != StatusDead {
			record(false, "Playback failed")
		}
		_, _ = s.DB.Exec(`UPDATE streams SET status=?, fail_count=fail_count+1, fail_reason='Playback failed', checked_at=?, `+failSince+` WHERE url=?`, StatusDead, now, now, url)
	}
	s.notify("streams")
}

// ---- rounds ----

// Round is one pass of the health checker.
type Round struct {
	ID            int64  `json:"id"`
	Started       int64  `json:"started"`
	Finished      int64  `json:"finished"` // 0 while running
	Kind          string `json:"kind"`     // "auto" or "manual"
	Label         string `json:"label"`
	Probed        int    `json:"probed"`
	OK            int    `json:"ok"`
	Bad           int    `json:"bad"`
	Stopped       bool   `json:"stopped"`
	TotalOK       int    `json:"total_ok"` // all streams when the round ended; streams excluded from checks count only in TotalExcluded
	TotalBad      int    `json:"total_bad"`
	TotalNew      int    `json:"total_unchecked"`
	TotalExcluded int    `json:"total_excluded"` // 0 for rounds stored before excluded streams were counted apart
}

// StartRound opens a history entry and returns its id.
func (s *Store) StartRound(kind, label string) int64 {
	res, err := s.DB.Exec(`INSERT INTO health_rounds(started,kind,label) VALUES(?,?,?)`, time.Now().Unix(), kind, label)
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// FinishRound completes a history entry with the results and the totals of all streams.
func (s *Store) FinishRound(id int64, probed, ok, bad int, stopped bool) {
	if id == 0 {
		return
	}
	st := 0
	if stopped {
		st = 1
	}
	_, _ = s.DB.Exec(`WITH x AS (SELECT status, url IN (SELECT url FROM health_excluded) AS ex FROM streams)
		UPDATE health_rounds SET finished=?, probed=?, ok=?, bad=?, stopped=?,
		t_ok=(SELECT COUNT(*) FROM x WHERE NOT ex AND status=1), t_bad=(SELECT COUNT(*) FROM x WHERE NOT ex AND status=2),
		t_unchecked=(SELECT COUNT(*) FROM x WHERE NOT ex AND status=0), t_excluded=(SELECT COUNT(*) FROM x WHERE ex)
		WHERE id=?`, time.Now().Unix(), probed, ok, bad, st, id)
	s.notify("health")
}

const roundCols = `id,started,finished,kind,label,probed,ok,bad,stopped,t_ok,t_bad,t_unchecked,t_excluded`

func scanRounds(rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}) []Round {
	defer rows.Close()
	out := []Round{}
	for rows.Next() {
		var r Round
		var stopped int
		if rows.Scan(&r.ID, &r.Started, &r.Finished, &r.Kind, &r.Label, &r.Probed, &r.OK, &r.Bad, &stopped, &r.TotalOK, &r.TotalBad, &r.TotalNew, &r.TotalExcluded) == nil {
			r.Stopped = stopped == 1
			out = append(out, r)
		}
	}
	return out
}

// Rounds lists history entries, newest first, with the total count.
func (s *Store) Rounds(offset, limit int) ([]Round, int) {
	var total int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM health_rounds`).Scan(&total)
	rows, err := s.DB.Query(`SELECT `+roundCols+` FROM health_rounds ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return []Round{}, total
	}
	return scanRounds(rows), total
}

// GetRound returns one history entry.
func (s *Store) GetRound(id int64) (Round, bool) {
	rows, err := s.DB.Query(`SELECT `+roundCols+` FROM health_rounds WHERE id=?`, id)
	if err != nil {
		return Round{}, false
	}
	rs := scanRounds(rows)
	if len(rs) == 0 {
		return Round{}, false
	}
	return rs[0], true
}

// Trend returns the last n finished rounds, oldest first, for the chart.
func (s *Store) Trend(n int) []Round {
	rows, err := s.DB.Query(`SELECT `+roundCols+` FROM (SELECT * FROM health_rounds WHERE finished>0 ORDER BY id DESC LIMIT ?) ORDER BY id`, n)
	if err != nil {
		return []Round{}
	}
	return scanRounds(rows)
}

// HistoryLimits bound the stored history. Rounds, PerChannel and Total of 0 keep nothing;
// MaxAgeDays removes rounds and results older than that many days.
type HistoryLimits struct {
	Rounds, PerChannel, Total, MaxAgeDays int
}

// PruneHealth enforces the history limits.
func (s *Store) PruneHealth(l HistoryLimits) {
	_, _ = s.DB.Exec(`DELETE FROM health_excluded WHERE url NOT IN (SELECT url FROM streams)`) // streams that left the catalogue
	cutoff := time.Now().AddDate(0, 0, -l.MaxAgeDays).Unix()
	_, _ = s.DB.Exec(`DELETE FROM health_rounds WHERE finished>0 AND (started < ? OR id NOT IN (SELECT id FROM health_rounds WHERE finished>0 ORDER BY id DESC LIMIT ?))`, cutoff, l.Rounds)
	if l.PerChannel <= 0 || l.Total <= 0 {
		_, _ = s.DB.Exec(`DELETE FROM health_checks`)
		return
	}
	perChannel := l.PerChannel
	_, _ = s.DB.Exec(`DELETE FROM health_checks WHERE at < ?`, cutoff)
	_, _ = s.DB.Exec(`DELETE FROM health_checks WHERE id IN (SELECT id FROM health_checks ORDER BY at DESC, id DESC LIMIT -1 OFFSET ?)`, l.Total)
	_, _ = s.DB.Exec(`DELETE FROM health_checks WHERE id IN (
		SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY ck ORDER BY at DESC, id DESC) AS rn FROM health_checks) WHERE rn > ?)`, perChannel)
	_, _ = s.DB.Exec(`DELETE FROM health_checks WHERE ck NOT IN (SELECT DISTINCT ck FROM streams)`) // channels that left the catalogue
}
