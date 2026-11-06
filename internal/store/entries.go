package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Entry struct {
	CK       string   `json:"ck"`
	Name     string   `json:"name"`
	Country  string   `json:"country"`
	Cats     []string `json:"categories"`
	Langs    []string `json:"languages"`
	Logo     string   `json:"logo"`
	NSFW     bool     `json:"nsfw"`
	Closed   bool     `json:"closed"`
	Custom   bool     `json:"custom"`
	Source   string   `json:"source"`        // custom source name, empty for iptv-org
	Streams  int      `json:"streams"`       // streams that count towards the status (see status.go)
	Working  int      `json:"working"`       // of those, working
	Dead     int      `json:"dead"`          // of those, not working
	Excluded int      `json:"excluded"`      // streams excluded from health checks
	Total    int      `json:"total_streams"` // every stream of the channel
	Profiles []int64  `json:"profiles"`      // IDs of profiles that contain this channel
}

type EntryQuery struct {
	Q, Country, Category, Language string
	WorkingOnly, NSFW, InProfile   bool
	Source                         string // "" = all, "api" = iptv-org, or a custom source id
	Offset, Limit                  int
	All                            bool   // every match, no paging (for "select all matching")
	Sort                           string // see entrySorts; empty = name
	Owner                          int64  // 0 = profiles of every user count; otherwise only this user's profiles
}

// entrySorts maps the ?sort= values to an ORDER BY; anything else sorts by name.
var entrySorts = map[string]string{
	"name":      `e.name COLLATE NOCASE, e.ck`,
	"name_desc": `e.name COLLATE NOCASE DESC, e.ck`,
	"country":   `COALESCE(NULLIF(e.country,''),'~') COLLATE NOCASE, e.name COLLATE NOCASE, e.ck`,
	"streams":   `(SELECT COUNT(*) FROM streams s WHERE s.ck=e.ck) DESC, e.name COLLATE NOCASE, e.ck`,
	// working first, then partly working, then not checked yet, then not working
	"status": ChannelStateSQL + `, e.name COLLATE NOCASE, e.ck`,
}

func jsonList(s string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (s *Store) QueryEntries(q EntryQuery) ([]Entry, int, error) {
	var where []string
	var args []any
	if q.Q != "" {
		where = append(where, `(e.name LIKE ? ESCAPE '\' OR e.ck LIKE ? ESCAPE '\')`)
		p := "%" + likeEscape(q.Q) + "%"
		args = append(args, p, p)
	}
	if q.Country != "" {
		where = append(where, `e.country=?`)
		args = append(args, q.Country)
	}
	if q.Category != "" {
		where = append(where, `e.categories LIKE ? ESCAPE '\'`)
		args = append(args, `%"`+likeEscape(q.Category)+`"%`)
	}
	if q.Language != "" {
		where = append(where, `e.languages LIKE ? ESCAPE '\'`)
		args = append(args, `%"`+likeEscape(q.Language)+`"%`)
	}
	if !q.NSFW {
		where = append(where, `e.nsfw=0`)
	}
	switch q.Source {
	case "":
	case "api":
		where = append(where, `e.src=0`)
	default:
		if n, err := strconv.ParseInt(q.Source, 10, 64); err == nil && n > 0 {
			where = append(where, `e.src=?`)
			args = append(args, n)
		}
	}
	if q.WorkingOnly {
		where = append(where, `COALESCE(ch.w,0)>0`) // working or partly working: something plays
	}
	if q.InProfile {
		where = append(where, `e.ck IN (SELECT pc.ck FROM profile_channels pc JOIN profiles p ON p.id=pc.profile_id WHERE ?=0 OR p.owner_id=?)`)
		args = append(args, q.Owner, q.Owner)
	}
	w := ""
	if len(where) > 0 {
		w = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	from := `WITH ` + ChannelHealthCTE + ` SELECT %s FROM entries e LEFT JOIN ch ON ch.ck=e.ck`
	if err := s.DB.QueryRow(fmt.Sprintf(from, `COUNT(*)`)+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order, ok := entrySorts[q.Sort]
	if !ok {
		order = entrySorts["name"]
	}
	if q.All {
		q.Limit, q.Offset = -1, 0 // SQLite: a negative LIMIT means no limit
	} else if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 10
	}
	qs := fmt.Sprintf(from, `e.ck,e.name,COALESCE(e.country,''),COALESCE(e.categories,'[]'),COALESCE(e.languages,'[]'),COALESCE(e.logo,''),e.nsfw,e.closed,e.src,
		COALESCE(ch.n,0), COALESCE(ch.w,0), COALESCE(ch.d,0), COALESCE(ch.x,0), COALESCE(ch.t,0),
		COALESCE((SELECT group_concat(pc.profile_id) FROM profile_channels pc JOIN profiles p ON p.id=pc.profile_id WHERE pc.ck=e.ck AND (?=0 OR p.owner_id=?)),''),
		COALESCE((SELECT name FROM custom_sources cs WHERE cs.id=e.src),'')`) + w + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	// the profile list in the select comes first, then the filters, then the page
	all := append(append([]any{q.Owner, q.Owner}, args...), q.Limit, q.Offset)
	rows, err := s.DB.Query(qs, all...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var cats, langs string
		var nsfw, closed int
		var src int64
		var profs string
		if err := rows.Scan(&e.CK, &e.Name, &e.Country, &cats, &langs, &e.Logo, &nsfw, &closed, &src, &e.Streams, &e.Working, &e.Dead, &e.Excluded, &e.Total, &profs, &e.Source); err != nil {
			return nil, 0, err
		}
		e.Cats, e.Langs = jsonList(cats), jsonList(langs)
		e.NSFW, e.Closed, e.Custom = nsfw == 1, closed == 1, src > 0
		e.Profiles = []int64{}
		for _, f := range strings.Split(profs, ",") {
			if n, err := strconv.ParseInt(f, 10, 64); err == nil {
				e.Profiles = append(e.Profiles, n)
			}
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

type Named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Flag string `json:"flag,omitempty"`
}

// Filters returns the values that actually occur in the entries table.
func (s *Store) Filters() (cats, countries, langs []Named, err error) {
	cats, countries, langs = []Named{}, []Named{}, []Named{}
	rows, err := s.DB.Query(`SELECT c.code,c.name,c.flag FROM countries c WHERE c.code IN (SELECT DISTINCT country FROM entries) ORDER BY c.name`)
	if err != nil {
		return
	}
	for rows.Next() {
		var n Named
		_ = rows.Scan(&n.ID, &n.Name, &n.Flag)
		countries = append(countries, n)
	}
	rows.Close()

	names := map[string]string{}
	rows, err = s.DB.Query(`SELECT id,name FROM categories`)
	if err != nil {
		return
	}
	for rows.Next() {
		var id, name string
		_ = rows.Scan(&id, &name)
		names[id] = name
	}
	rows.Close()
	seenCat, seenLang := map[string]bool{}, map[string]bool{}
	rows, err = s.DB.Query(`SELECT DISTINCT categories, languages FROM entries`)
	if err != nil {
		return
	}
	for rows.Next() {
		var c, l string
		_ = rows.Scan(&c, &l)
		for _, x := range jsonList(c) {
			seenCat[x] = true
		}
		for _, x := range jsonList(l) {
			seenLang[x] = true
		}
	}
	rows.Close()
	for id := range seenCat {
		n := names[id]
		if n == "" {
			n = id
		}
		cats = append(cats, Named{ID: id, Name: n})
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Name < cats[j].Name })

	lnames := map[string]string{}
	rows, err = s.DB.Query(`SELECT code,name FROM languages`)
	if err != nil {
		return
	}
	for rows.Next() {
		var id, name string
		_ = rows.Scan(&id, &name)
		lnames[id] = name
	}
	rows.Close()
	for id := range seenLang {
		n := lnames[id]
		if n == "" {
			n = id
		}
		langs = append(langs, Named{ID: id, Name: n})
	}
	sort.Slice(langs, func(i, j int) bool { return langs[i].Name < langs[j].Name })
	return
}

// EntryInfo returns playlist metadata for one channel key.
type EntryInfo struct {
	CK, Name, Logo, Group string
}

func (s *Store) EntryInfo(ck string) (EntryInfo, bool) {
	var e EntryInfo
	var cats string
	err := s.DB.QueryRow(`SELECT ck,name,COALESCE(logo,''),COALESCE(categories,'[]') FROM entries WHERE ck=?`, ck).Scan(&e.CK, &e.Name, &e.Logo, &cats)
	if err != nil {
		return e, false
	}
	if l := jsonList(cats); len(l) > 0 {
		e.Group = l[0]
	}
	return e, true
}
