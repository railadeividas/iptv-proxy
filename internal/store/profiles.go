package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

type Profile struct {
	ID      int64  `json:"id"`
	OwnerID int64  `json:"owner_id"`
	Owner   string `json:"owner"` // the owner's user name
	Name    string `json:"name"`
	Token   string `json:"token"`
	Mode    string `json:"mode"`   // auto | proxy | redirect
	Compat  string `json:"compat"` // "", vlc, browser, exoplayer
	Rev     int64  `json:"rev"`
	Count   int    `json:"count"`
	Created int64  `json:"created_at"`
}

type ProfileChannel struct {
	CK        string   `json:"ck"`
	Name      string   `json:"name"`       // override
	StreamURL string   `json:"stream_url"` // preferred stream
	Mode      string   `json:"mode"`       // override
	Position  int      `json:"position"`
	Default   string   `json:"default_name"`
	Logo      string   `json:"logo"`
	Country   string   `json:"country"`
	Cats      []string `json:"categories"`
	Streams   int      `json:"streams"`       // streams that count towards the status (see status.go)
	Working   int      `json:"working"`       // of those, working
	Dead      int      `json:"dead"`          // of those, not working
	Excluded  int      `json:"excluded"`      // streams excluded from health checks
	Total     int      `json:"total_streams"` // every stream of the channel
	HasGuide  bool     `json:"has_guide"`     // an iptv-org EPG source exists
	EPGID     string   `json:"epg_id"`        // manual link to an iptv-org channel ID (e.g. "M1.hu" or "M1.hu@SD")
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// profileCols is the column list scanProfile expects, with the owner's name joined in.
const profileCols = `p.id,p.owner_id,COALESCE(u.username,''),p.name,p.token,p.mode,p.compat,p.rev,p.created_at`
const profileFrom = ` FROM profiles p LEFT JOIN users u ON u.id=p.owner_id`

// ListProfiles returns the profiles of one owner, or every profile when owner is 0.
func (s *Store) ListProfiles(owner int64) ([]Profile, error) {
	rows, err := s.DB.Query(`SELECT `+profileCols+`,
		(SELECT COUNT(*) FROM profile_channels c WHERE c.profile_id=p.id)`+profileFrom+` WHERE ?=0 OR p.owner_id=? ORDER BY p.id`, owner, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		var p Profile
		if err := rows.Scan(&p.ID, &p.OwnerID, &p.Owner, &p.Name, &p.Token, &p.Mode, &p.Compat, &p.Rev, &p.Created, &p.Count); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProfile(id int64) (*Profile, error) {
	return s.scanProfile(s.DB.QueryRow(`SELECT `+profileCols+profileFrom+` WHERE p.id=?`, id))
}

func (s *Store) ProfileByToken(token string) (*Profile, error) {
	return s.scanProfile(s.DB.QueryRow(`SELECT `+profileCols+profileFrom+` WHERE p.token=?`, token))
}

func (s *Store) scanProfile(r *sql.Row) (*Profile, error) {
	var p Profile
	if err := r.Scan(&p.ID, &p.OwnerID, &p.Owner, &p.Name, &p.Token, &p.Mode, &p.Compat, &p.Rev, &p.Created); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) CreateProfile(owner int64, name string) (*Profile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name required")
	}
	res, err := s.DB.Exec(`INSERT INTO profiles(owner_id,name,token,created_at) VALUES(?,?,?,?)`, owner, name, newToken(), time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	s.notify("profiles")
	return s.GetProfile(id)
}

func (s *Store) UpdateProfile(id int64, name, mode, compat string) error {
	if mode != "auto" && mode != "proxy" && mode != "redirect" {
		return errors.New("invalid mode")
	}
	switch compat {
	case "", "vlc", "browser", "exoplayer":
	default:
		return errors.New("invalid compat profile")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("name required")
	}
	_, err := s.DB.Exec(`UPDATE profiles SET name=?,mode=?,compat=?,rev=rev+1 WHERE id=?`, name, mode, compat, id)
	s.notify("profiles")
	return err
}

func (s *Store) RotateToken(id int64) error {
	_, err := s.DB.Exec(`UPDATE profiles SET token=? WHERE id=?`, newToken(), id)
	s.notify("profiles")
	return err
}

func (s *Store) DeleteProfile(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM profiles WHERE id=?`, id)
	s.notify("profiles")
	return err
}

func (s *Store) touch(id int64) {
	_, _ = s.DB.Exec(`UPDATE profiles SET rev=rev+1 WHERE id=?`, id)
	s.notify("profiles")
}

// DropFromProfiles removes channels from every profile that contains them
// (used when a channel disappears from a custom source) and reports whether
// any profile changed. Other entries keep their position and settings.
func (s *Store) DropFromProfiles(cks []string) (bool, error) {
	changed := map[int64]bool{}
	for _, ck := range cks {
		rows, err := s.DB.Query(`SELECT profile_id FROM profile_channels WHERE ck=?`, ck)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			changed[id] = true
		}
		rows.Close()
		if _, err := s.DB.Exec(`DELETE FROM profile_channels WHERE ck=?`, ck); err != nil {
			return false, err
		}
	}
	for id := range changed {
		s.touch(id)
	}
	return len(changed) > 0, nil
}

// ProfileChannels returns the ordered channels of a profile with browse metadata.
func (s *Store) ProfileChannels(id int64) ([]ProfileChannel, error) {
	rows, err := s.DB.Query(`WITH `+ChannelHealthCTE+`
SELECT pc.ck, pc.name, pc.stream_url, pc.mode, pc.position, pc.epg_id,
  COALESCE(e.name,''), COALESCE(e.logo,''), COALESCE(e.country,''), COALESCE(e.categories,'[]'),
  COALESCE(ch.n,0), COALESCE(ch.w,0), COALESCE(ch.d,0), COALESCE(ch.x,0), COALESCE(ch.t,0),
  EXISTS(SELECT 1 FROM guides g WHERE g.channel=CASE WHEN instr(COALESCE(NULLIF(pc.epg_id,''),(SELECT NULLIF(MAX(s2.tvg_id),'') FROM streams s2 WHERE s2.ck=pc.ck AND s2.src>0),pc.ck),'@')>0 THEN substr(COALESCE(NULLIF(pc.epg_id,''),(SELECT NULLIF(MAX(s2.tvg_id),'') FROM streams s2 WHERE s2.ck=pc.ck AND s2.src>0),pc.ck),1,instr(COALESCE(NULLIF(pc.epg_id,''),(SELECT NULLIF(MAX(s2.tvg_id),'') FROM streams s2 WHERE s2.ck=pc.ck AND s2.src>0),pc.ck),'@')-1) ELSE COALESCE(NULLIF(pc.epg_id,''),(SELECT NULLIF(MAX(s2.tvg_id),'') FROM streams s2 WHERE s2.ck=pc.ck AND s2.src>0),pc.ck) END)
FROM profile_channels pc LEFT JOIN entries e ON e.ck=pc.ck LEFT JOIN ch ON ch.ck=pc.ck
WHERE pc.profile_id=? ORDER BY pc.position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProfileChannel{}
	for rows.Next() {
		var c ProfileChannel
		var cats string
		if err := rows.Scan(&c.CK, &c.Name, &c.StreamURL, &c.Mode, &c.Position, &c.EPGID, &c.Default, &c.Logo, &c.Country, &cats, &c.Streams, &c.Working, &c.Dead, &c.Excluded, &c.Total, &c.HasGuide); err != nil {
			return nil, err
		}
		c.Cats = jsonList(cats)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ProfileChannel(id int64, ck string) (*ProfileChannel, error) {
	var c ProfileChannel
	err := s.DB.QueryRow(`SELECT ck,name,stream_url,mode,position FROM profile_channels WHERE profile_id=? AND ck=?`, id, ck).
		Scan(&c.CK, &c.Name, &c.StreamURL, &c.Mode, &c.Position)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// AddChannels appends channels (ignoring ones already present).
func (s *Store) AddChannels(id int64, cks []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pos int
	_ = tx.QueryRow(`SELECT COALESCE(MAX(position)+1,0) FROM profile_channels WHERE profile_id=?`, id).Scan(&pos)
	for _, ck := range cks {
		res, err := tx.Exec(`INSERT OR IGNORE INTO profile_channels(profile_id,ck,position)
			SELECT ?,?,? WHERE EXISTS(SELECT 1 FROM entries WHERE ck=?)`, id, ck, pos, ck)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			pos++
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.touch(id)
	return nil
}

func (s *Store) RemoveChannels(id int64, cks []string) error {
	for _, ck := range cks {
		if _, err := s.DB.Exec(`DELETE FROM profile_channels WHERE profile_id=? AND ck=?`, id, ck); err != nil {
			return err
		}
	}
	s.touch(id)
	return nil
}

// ReplaceChannels rewrites order and per-channel settings in one go.
func (s *Store) ReplaceChannels(id int64, items []ProfileChannel) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM profile_channels WHERE profile_id=?`, id); err != nil {
		return err
	}
	for i, it := range items {
		switch it.Mode {
		case "", "auto", "proxy", "redirect":
		default:
			return errors.New("invalid mode for " + it.CK)
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO profile_channels(profile_id,ck,position,name,stream_url,mode,epg_id) VALUES(?,?,?,?,?,?,?)`,
			id, it.CK, i, strings.TrimSpace(it.Name), it.StreamURL, it.Mode, strings.TrimSpace(it.EPGID)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.touch(id)
	return nil
}

// SelectedCKs returns the union of channel keys across all profiles.
func (s *Store) SelectedCKs() ([]string, error) {
	rows, err := s.DB.Query(`SELECT DISTINCT ck FROM profile_channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ck string
		if err := rows.Scan(&ck); err != nil {
			return nil, err
		}
		out = append(out, ck)
	}
	return out, rows.Err()
}

// Stream is one playable URL for a channel key.
type Stream struct {
	ID        int64  `json:"id"`
	CK        string `json:"ck"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Quality   string `json:"quality"`
	UserAgent string `json:"user_agent"`
	Referrer  string `json:"referrer"`
	Status    int    `json:"status"`
	CheckedAt int64  `json:"checked_at"`
	Src       int64  `json:"src"`
}

// Streams returns streams for a channel key, best first (working, then
// unknown, then dead; higher quality first).
func (s *Store) Streams(ck string) ([]Stream, error) {
	rows, err := s.DB.Query(`SELECT id,ck,COALESCE(title,''),url,COALESCE(quality,''),COALESCE(user_agent,''),COALESCE(referrer,''),status,checked_at,src
		FROM streams WHERE ck=?`, ck)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stream
	for rows.Next() {
		var st Stream
		if err := rows.Scan(&st.ID, &st.CK, &st.Title, &st.URL, &st.Quality, &st.UserAgent, &st.Referrer, &st.Status, &st.CheckedAt, &st.Src); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	sortStreams(out)
	return out, rows.Err()
}

func rank(st int) int {
	switch st {
	case StatusOK:
		return 0
	case StatusDead:
		return 2
	}
	return 1
}

func qualityNum(q string) int {
	n, _ := strconv.Atoi(strings.TrimRight(strings.TrimSpace(q), "pi"))
	return n
}

func sortStreams(s []Stream) {
	// insertion sort keeps this dependency-free and stable for small slices
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func less(a, b Stream) bool {
	if rank(a.Status) != rank(b.Status) {
		return rank(a.Status) < rank(b.Status)
	}
	return qualityNum(a.Quality) > qualityNum(b.Quality)
}

// SelectedEPG returns, for every selected channel key, the iptv-org channel ID
// whose guide should be used: the manual link set on a profile channel, else
// the tvg-id of a custom stream, else "" (use the key itself).
func (s *Store) SelectedEPG() (map[string]string, error) {
	rows, err := s.DB.Query(`SELECT pc.ck,
	  COALESCE(NULLIF(MAX(pc.epg_id),''), (SELECT NULLIF(MAX(s.tvg_id),'') FROM streams s WHERE s.ck=pc.ck AND s.src>0), '')
	  FROM profile_channels pc GROUP BY pc.ck`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var ck, id string
		if err := rows.Scan(&ck, &id); err != nil {
			return nil, err
		}
		out[ck] = id
	}
	return out, rows.Err()
}
