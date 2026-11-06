package health

import (
	"strings"
	"time"
)

// RoundQuery picks the results of one round: Result is "bad", "ok" or empty for all.
type RoundQuery struct {
	ID            int64
	Result, Q     string
	Offset, Limit int
}

// RoundCheck is one stored result of a round.
type RoundCheck struct {
	CK     string `json:"ck"`
	Name   string `json:"name"`
	Host   string `json:"host"`
	At     int64  `json:"at"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason"` // why it failed, or the HTTP status of a working stream
	MS     int    `json:"ms"`
}

// RoundDetail is what one round checked, as far as the history still has it.
type RoundDetail struct {
	Round  any          `json:"round"`
	Stored int          `json:"stored"` // results still kept for this round
	OK     int          `json:"ok"`
	Bad    int          `json:"bad"`
	Total  int          `json:"total"` // matching the filters
	Items  []RoundCheck `json:"items"`
}

// Round lists the stored results of one round. Results are not tagged with a round: they belong to the
// round whose start and end time contain them (rounds never overlap). Older results may have been pruned
// by the history limits, so Stored can be lower than the round's stream count.
func (c *Checker) Round(q RoundQuery) (RoundDetail, bool) {
	r, ok := c.st.GetRound(q.ID)
	if !ok {
		return RoundDetail{}, false
	}
	d := RoundDetail{Round: r, Items: []RoundCheck{}}
	end := r.Finished
	if end == 0 {
		end = time.Now().Unix()
	}
	db := c.st.DB
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ok=1),0), COALESCE(SUM(ok=0),0) FROM health_checks WHERE at BETWEEN ? AND ?`, r.Started, end).Scan(&d.Stored, &d.OK, &d.Bad)

	where, args := []string{"hc.at BETWEEN ? AND ?"}, []any{r.Started, end}
	switch q.Result {
	case "bad":
		where = append(where, "hc.ok=0")
	case "ok":
		where = append(where, "hc.ok=1")
	}
	if t := strings.TrimSpace(q.Q); t != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(t) + "%"
		where = append(where, `(COALESCE(e.name,hc.ck) LIKE ? ESCAPE '\' OR hc.ck LIKE ? ESCAPE '\' OR hc.url LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	from := ` FROM health_checks hc LEFT JOIN entries e ON e.ck=hc.ck WHERE ` + strings.Join(where, " AND ")
	db.QueryRow(`SELECT COUNT(*)`+from, args...).Scan(&d.Total)
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 10
	}
	rows, err := db.Query(`SELECT hc.ck, COALESCE(e.name,hc.ck), hc.url, hc.at, hc.ok, hc.reason, hc.ms`+from+
		` ORDER BY hc.ok, COALESCE(e.name,hc.ck) COLLATE NOCASE, hc.at LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return d, true
	}
	defer rows.Close()
	for rows.Next() {
		var x RoundCheck
		var u string
		var ok int
		if rows.Scan(&x.CK, &x.Name, &u, &x.At, &ok, &x.Reason, &x.MS) == nil {
			x.Host, x.OK = hostOf(u), ok == 1
			d.Items = append(d.Items, x)
		}
	}
	return d, true
}
