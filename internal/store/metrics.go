package store

// Queries for the metrics endpoint.

// CatalogueCounts are the sizes of the catalogue.
type CatalogueCounts struct {
	Channels, Streams, Skipped, CustomSources, Profiles int
}

func (s *Store) CatalogueCounts() CatalogueCounts {
	var c CatalogueCounts
	q := s.DB
	q.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&c.Channels)
	q.QueryRow(`SELECT COUNT(*) FROM streams`).Scan(&c.Streams)
	q.QueryRow(`SELECT COUNT(*) FROM skipped`).Scan(&c.Skipped)
	q.QueryRow(`SELECT COUNT(*) FROM custom_sources`).Scan(&c.CustomSources)
	q.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&c.Profiles)
	return c
}

// UsersByRole counts users per role.
func (s *Store) UsersByRole() map[string]int {
	out := map[string]int{}
	rows, err := s.DB.Query(`SELECT role, COUNT(*) FROM users GROUP BY role`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var n int
		if rows.Scan(&role, &n) == nil {
			out[role] = n
		}
	}
	return out
}

// CustomSourceRow is the refresh status of one custom source.
type CustomSourceRow struct {
	ID       int64
	Name     string
	LastOK   int64
	LastTry  int64
	Failed   bool // the last refresh failed
	Channels int
}

func (s *Store) CustomSourceRows() []CustomSourceRow {
	rows, err := s.DB.Query(`SELECT id, COALESCE(name,''), COALESCE(last_ok,0), last_try, COALESCE(last_error,'')<>'',
		(SELECT COUNT(DISTINCT ck) FROM streams WHERE src=custom_sources.id) FROM custom_sources ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []CustomSourceRow
	for rows.Next() {
		var r CustomSourceRow
		if rows.Scan(&r.ID, &r.Name, &r.LastOK, &r.LastTry, &r.Failed, &r.Channels) == nil {
			out = append(out, r)
		}
	}
	return out
}

// ProfileStates counts the channels of one profile by status (see status.go).
type ProfileStates struct {
	Working, Partly, NotWorking, Pending int
}

// ProfileChannelStates returns the channel status counts of every profile that has channels.
func (s *Store) ProfileChannelStates() map[int64]ProfileStates {
	out := map[int64]ProfileStates{}
	rows, err := s.DB.Query(`WITH ` + ChannelHealthCTE + `
		SELECT pc.profile_id, ` + ChannelStateSQL + `, COUNT(*) FROM profile_channels pc LEFT JOIN ch ON ch.ck=pc.ck GROUP BY pc.profile_id, 2`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var state, n int
		if rows.Scan(&id, &state, &n) != nil {
			continue
		}
		c := out[id]
		switch state {
		case ChannelWorking:
			c.Working = n
		case ChannelPartly:
			c.Partly = n
		case ChannelNotWorking:
			c.NotWorking = n
		default:
			c.Pending = n
		}
		out[id] = c
	}
	return out
}
