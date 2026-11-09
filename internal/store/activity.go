package store

import (
	"database/sql"
	"strings"
	"time"
)

// Session is one watch session: a viewer on the proxy, or one redirect.
type Session struct {
	ID        int64  `json:"id"`
	ProfileID int64  `json:"profile_id"`
	Profile   string `json:"profile"`
	Owner     string `json:"owner"`
	CK        string `json:"ck"`
	Name      string `json:"name"`
	Mode      string `json:"mode"` // proxy | redirect
	Host      string `json:"host"` // the stream server
	IP        string `json:"ip"`
	UA        string `json:"ua"`
	Started   int64  `json:"started"`
	Last      int64  `json:"last"`
	Bytes     int64  `json:"bytes"`
	Requests  int64  `json:"requests"`
}

// Client is a device (address and player name) that used a profile.
type Client struct {
	ID        int64  `json:"id"`
	ProfileID int64  `json:"profile_id"`
	Profile   string `json:"profile"`
	Owner     string `json:"owner"`
	IP        string `json:"ip"`
	UA        string `json:"ua"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
	Playlists int64  `json:"playlists"`
	Guides    int64  `json:"guides"`
	Plays     int64  `json:"plays"`
}

// TouchClient counts one playlist, guide or play request of a device. kind is "playlist", "guide" or "play".
func (s *Store) TouchClient(profileID int64, ip, ua, kind string, now int64) {
	col := map[string]string{"playlist": "playlists", "guide": "guides", "play": "plays"}[kind]
	if col == "" {
		return
	}
	_, _ = s.DB.Exec(`INSERT INTO activity_clients(profile_id,ip,ua,first_seen,last_seen,`+col+`) VALUES(?,?,?,?,?,1)
		ON CONFLICT(profile_id,ip,ua) DO UPDATE SET last_seen=excluded.last_seen, `+col+`=`+col+`+1`, profileID, ip, ua, now, now)
	s.notify("activity")
}

// AddSession stores a new session and returns its id. The profile may have been deleted meanwhile: then nothing is stored (0).
func (s *Store) AddSession(x Session) int64 {
	res, err := s.DB.Exec(`INSERT INTO activity_sessions(profile_id,ck,mode,host,ip,ua,started,last,bytes,requests) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		x.ProfileID, x.CK, x.Mode, x.Host, x.IP, x.UA, x.Started, x.Last, x.Bytes, x.Requests)
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	s.notify("activity")
	return id
}

// UpdateSession saves the numbers of a running session.
func (s *Store) UpdateSession(id, last, bytes, requests int64) {
	_, _ = s.DB.Exec(`UPDATE activity_sessions SET last=?, bytes=?, requests=? WHERE id=?`, last, bytes, requests, id)
}

// PruneActivity enforces the history limits: nothing older than maxDays, at most maxSessions sessions and maxClients devices.
func (s *Store) PruneActivity(maxDays, maxSessions, maxClients int) {
	cutoff := time.Now().AddDate(0, 0, -maxDays).Unix()
	_, _ = s.DB.Exec(`DELETE FROM activity_sessions WHERE last < ?`, cutoff)
	_, _ = s.DB.Exec(`DELETE FROM activity_sessions WHERE id IN (SELECT id FROM activity_sessions ORDER BY started DESC, id DESC LIMIT -1 OFFSET ?)`, maxSessions)
	_, _ = s.DB.Exec(`DELETE FROM activity_clients WHERE last_seen < ?`, cutoff)
	_, _ = s.DB.Exec(`DELETE FROM stats_samples WHERE hour < ?`, cutoff)
	_, _ = s.DB.Exec(`DELETE FROM stats_profile_samples WHERE hour < ?`, cutoff)
	_, _ = s.DB.Exec(`DELETE FROM activity_clients WHERE id IN (SELECT id FROM activity_clients ORDER BY last_seen DESC, id DESC LIMIT -1 OFFSET ?)`, maxClients)
}

// ClearedActivity says how much ClearActivity removed.
type ClearedActivity struct {
	Sessions int64 `json:"sessions"`
	Devices  int64 `json:"devices"`
	Samples  int64 `json:"samples"` // hourly rows: the server's and the profiles'
}

// ClearActivity deletes stored stats in one transaction: everything when before is 0, otherwise what ended before that
// unix time (sessions by their last request, devices by when they were last seen, hourly samples by their hour).
// Profiles, settings and the running sessions in memory are not touched.
func (s *Store) ClearActivity(before int64) (ClearedActivity, error) {
	var out ClearedActivity
	tx, err := s.DB.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	del := func(q string, n *int64, extra ...any) error {
		res, err := tx.Exec(q, extra...)
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		*n += c
		return nil
	}
	steps := []struct {
		all, old string
		n        *int64
	}{
		{`DELETE FROM activity_sessions`, `DELETE FROM activity_sessions WHERE last < ?`, &out.Sessions},
		{`DELETE FROM activity_clients`, `DELETE FROM activity_clients WHERE last_seen < ?`, &out.Devices},
		{`DELETE FROM stats_samples`, `DELETE FROM stats_samples WHERE hour < ?`, &out.Samples},
		{`DELETE FROM stats_profile_samples`, `DELETE FROM stats_profile_samples WHERE hour < ?`, &out.Samples},
	}
	for _, st := range steps {
		if before == 0 {
			err = del(st.all, st.n)
		} else {
			err = del(st.old, st.n, before)
		}
		if err != nil {
			return ClearedActivity{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ClearedActivity{}, err
	}
	s.notify("activity")
	return out, nil
}

// ActivityFilter narrows the stats. Owner 0 means everybody's profiles; Profile 0 (and no Profiles) every profile; Profiles
// is a selection of several; Since 0 all time.
type ActivityFilter struct {
	Owner, Profile, Since int64
	Profiles              []int64
	Until                 int64 // exclusive; 0 = no upper bound
	Mode                  string
}

// Includes tells whether a profile is part of the filter's profile selection.
func (f ActivityFilter) Includes(id int64) bool {
	if f.Profile == 0 && len(f.Profiles) == 0 {
		return true
	}
	if f.Profile == id {
		return true
	}
	for _, p := range f.Profiles {
		if p == id {
			return true
		}
	}
	return false
}

// where builds the conditions on a query that joins profiles as p and sessions or clients as a.
// timeCol is the column Since applies to.
func (f ActivityFilter) where(timeCol string) (string, []any) {
	q := ` WHERE 1=1`
	var args []any
	if f.Owner != 0 {
		q += ` AND p.owner_id=?`
		args = append(args, f.Owner)
	}
	if f.Profile != 0 {
		q += ` AND a.profile_id=?`
		args = append(args, f.Profile)
	}
	if len(f.Profiles) > 0 {
		q += ` AND a.profile_id IN (?` + strings.Repeat(`,?`, len(f.Profiles)-1) + `)`
		for _, id := range f.Profiles {
			args = append(args, id)
		}
	}
	if f.Since != 0 {
		q += ` AND a.` + timeCol + `>=?`
		args = append(args, f.Since)
	}
	if f.Until != 0 {
		q += ` AND a.` + timeCol + `<?`
		args = append(args, f.Until)
	}
	if f.Mode != "" {
		q += ` AND a.mode=?`
		args = append(args, f.Mode)
	}
	return q, args
}

// ActivityTotals are the headline numbers of a period.
type ActivityTotals struct {
	Sessions int64 `json:"sessions"`
	Proxy    int64 `json:"proxy"`    // sessions through the proxy
	Redirect int64 `json:"redirect"` // players sent straight to the stream
	Bytes    int64 `json:"bytes"`
	Seconds  int64 `json:"seconds"` // watched through the proxy
	Clients  int64 `json:"clients"` // devices seen in the period
	Channels int64 `json:"channels"`
}

func (s *Store) ActivityTotals(f ActivityFilter) (ActivityTotals, error) {
	var t ActivityTotals
	w, args := f.where("started")
	err := s.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(a.mode='proxy'),0), COALESCE(SUM(a.mode='redirect'),0), COALESCE(SUM(a.bytes),0),
		COALESCE(SUM(CASE WHEN a.mode='proxy' THEN a.last-a.started END),0), COUNT(DISTINCT a.ck)
		FROM activity_sessions a JOIN profiles p ON p.id=a.profile_id`+w, args...).Scan(&t.Sessions, &t.Proxy, &t.Redirect, &t.Bytes, &t.Seconds, &t.Channels)
	if err != nil {
		return t, err
	}
	cf := ActivityFilter{Owner: f.Owner, Profile: f.Profile, Profiles: f.Profiles, Since: f.Since}
	cw, cargs := cf.where("last_seen")
	err = s.DB.QueryRow(`SELECT COUNT(*) FROM activity_clients a JOIN profiles p ON p.id=a.profile_id`+cw, cargs...).Scan(&t.Clients)
	return t, err
}

// ProfileActivity is one row of the per-profile table.
type ProfileActivity struct {
	ProfileID int64  `json:"profile_id"`
	Profile   string `json:"profile"`
	Owner     string `json:"owner"`
	Playlists int64  `json:"playlists"` // lifetime fetches of the playlist
	Guides    int64  `json:"guides"`
	Clients   int64  `json:"clients"`   // devices seen in the period
	LastSeen  int64  `json:"last_seen"` // any request of the profile's links
	Sessions  int64  `json:"sessions"`
	Proxy     int64  `json:"proxy"`
	Redirect  int64  `json:"redirect"`
	Bytes     int64  `json:"bytes"`
	Seconds   int64  `json:"seconds"`
}

// ActivityProfiles lists every profile in scope with its numbers, busiest first.
func (s *Store) ActivityProfiles(f ActivityFilter) ([]ProfileActivity, error) {
	rows, err := s.DB.Query(`SELECT p.id, p.name, COALESCE(u.username,''),
		COALESCE((SELECT SUM(playlists) FROM activity_clients c WHERE c.profile_id=p.id),0),
		COALESCE((SELECT SUM(guides) FROM activity_clients c WHERE c.profile_id=p.id),0),
		(SELECT COUNT(*) FROM activity_clients c WHERE c.profile_id=p.id AND c.last_seen>=?),
		COALESCE((SELECT MAX(last_seen) FROM activity_clients c WHERE c.profile_id=p.id),0),
		(SELECT COUNT(*) FROM activity_sessions a WHERE a.profile_id=p.id AND a.started>=?),
		(SELECT COUNT(*) FROM activity_sessions a WHERE a.profile_id=p.id AND a.started>=? AND a.mode='proxy'),
		(SELECT COUNT(*) FROM activity_sessions a WHERE a.profile_id=p.id AND a.started>=? AND a.mode='redirect'),
		COALESCE((SELECT SUM(bytes) FROM activity_sessions a WHERE a.profile_id=p.id AND a.started>=?),0),
		COALESCE((SELECT SUM(last-started) FROM activity_sessions a WHERE a.profile_id=p.id AND a.started>=? AND a.mode='proxy'),0)
		FROM profiles p LEFT JOIN users u ON u.id=p.owner_id WHERE ?=0 OR p.owner_id=?
		ORDER BY 8 DESC, 7 DESC, p.id`, f.Since, f.Since, f.Since, f.Since, f.Since, f.Since, f.Owner, f.Owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProfileActivity{}
	for rows.Next() {
		var x ProfileActivity
		if err := rows.Scan(&x.ProfileID, &x.Profile, &x.Owner, &x.Playlists, &x.Guides, &x.Clients, &x.LastSeen, &x.Sessions, &x.Proxy, &x.Redirect, &x.Bytes, &x.Seconds); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ActivityClients lists devices, most recently seen first.
func (s *Store) ActivityClients(f ActivityFilter, limit, offset int) ([]Client, int, error) {
	f.Mode = ""
	w, args := f.where("last_seen")
	from := ` FROM activity_clients a JOIN profiles p ON p.id=a.profile_id LEFT JOIN users u ON u.id=p.owner_id` + w
	var total int
	if err := s.DB.QueryRow(`SELECT COUNT(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(`SELECT a.id,a.profile_id,p.name,COALESCE(u.username,''),a.ip,a.ua,a.first_seen,a.last_seen,a.playlists,a.guides,a.plays`+from+
		` ORDER BY a.last_seen DESC, a.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Client{}
	for rows.Next() {
		var x Client
		if err := rows.Scan(&x.ID, &x.ProfileID, &x.Profile, &x.Owner, &x.IP, &x.UA, &x.FirstSeen, &x.LastSeen, &x.Playlists, &x.Guides, &x.Plays); err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

// ActivitySessions lists sessions, newest first.
func (s *Store) ActivitySessions(f ActivityFilter, limit, offset int) ([]Session, int, error) {
	w, args := f.where("started")
	from := ` FROM activity_sessions a JOIN profiles p ON p.id=a.profile_id LEFT JOIN users u ON u.id=p.owner_id` + w
	var total int
	if err := s.DB.QueryRow(`SELECT COUNT(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(`SELECT a.id,a.profile_id,p.name,COALESCE(u.username,''),a.ck,a.mode,a.host,a.ip,a.ua,a.started,a.last,a.bytes,a.requests`+from+
		` ORDER BY a.started DESC, a.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var x Session
		if err := rows.Scan(&x.ID, &x.ProfileID, &x.Profile, &x.Owner, &x.CK, &x.Mode, &x.Host, &x.IP, &x.UA, &x.Started, &x.Last, &x.Bytes, &x.Requests); err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

// ChannelName returns the catalogue name of a channel, or the key when it is not in the catalogue.
func (s *Store) ChannelName(ck string) string {
	var n sql.NullString
	if s.DB.QueryRow(`SELECT name FROM entries WHERE ck=?`, ck).Scan(&n) == nil && n.String != "" {
		return n.String
	}
	return ck
}
