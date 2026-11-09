package store

import "database/sql"

// A migration changes the database from one version to the next. migrations[0] upgrades baselineVersion to baselineVersion+1,
// migrations[1] the one after, and so on; the database version is baselineVersion + the number of migrations applied.
//
// Rules (see AGENTS.md):
//   - Only ever append. A migration that has shipped is never edited, reordered or removed.
//   - Each runs in its own transaction, with foreign keys switched off and checked again before the commit, so
//     rebuilding a table (create new, copy, drop old, rename) is safe. Do not run PRAGMAs or VACUUM in it.
//   - It must work on a database with real data in it and on a brand-new one (migrations run after the baseline schema).
//   - Moving or renaming data (settings are rows in meta named setting.<key>) is a migration too, not startup code.
//   - Add a test in migrate_test.go that migrates a database holding data.
type migration struct {
	name string
	up   func(tx *sql.Tx) error
}

var migrations = []migration{
	{name: "add health_excluded: streams left out of health checks", up: func(tx *sql.Tx) error {
		// keyed by stream address like the check status, so it survives syncs and source refreshes
		_, err := tx.Exec(`CREATE TABLE health_excluded(url TEXT PRIMARY KEY, at INTEGER NOT NULL, auto INTEGER NOT NULL DEFAULT 0, reason TEXT NOT NULL DEFAULT '')`)
		return err
	}},
	{name: "add streams.fail_since: automatic exclusion counts days of failing instead of failed checks", up: func(tx *sql.Tx) error {
		for _, q := range []string{
			`ALTER TABLE streams ADD COLUMN fail_since INTEGER NOT NULL DEFAULT 0`,
			// a stream that is failing now: its run started at the latest at its last check (errs on the late side)
			`UPDATE streams SET fail_since=checked_at WHERE fail_count>0`,
			// the count setting is replaced by health_auto_exclude_days; a count does not convert to days
			`DELETE FROM meta WHERE key='setting.health_auto_exclude_after'`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}},
	{name: "convert the profile-channel check interval from minutes to hours", up: func(tx *sql.Tx) error {
		// health_selected_interval_minutes (5-1440) becomes health_selected_interval_hours (1-720), rounded to whole hours.
		// Only a value someone changed is stored; without one the new default applies.
		for _, q := range []string{
			`INSERT OR REPLACE INTO meta(key,value) SELECT 'setting.health_selected_interval_hours',
			   CAST(MIN(720, MAX(1, (CAST(value AS INTEGER)+30)/60)) AS TEXT) FROM meta WHERE key='setting.health_selected_interval_minutes'`,
			`DELETE FROM meta WHERE key='setting.health_selected_interval_minutes'`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}},
	{name: "add health_rounds.t_excluded: rounds count excluded streams apart", up: func(tx *sql.Tx) error {
		// older rounds keep their totals (excluded streams inside them) and show 0 excluded
		_, err := tx.Exec(`ALTER TABLE health_rounds ADD COLUMN t_excluded INTEGER NOT NULL DEFAULT 0`)
		return err
	}},
	{name: "add activity_clients and activity_sessions: who fetches playlists and what is watched", up: func(tx *sql.Tx) error {
		// activity_clients: one row per device (address + user agent) per profile, with lifetime counters.
		// activity_sessions: one row per watch session (a proxied viewer, or one redirect), saved while it runs.
		// Both follow their profile when it is deleted. Addresses are stored as the privacy settings allow.
		for _, q := range []string{
			`CREATE TABLE activity_clients(
				id INTEGER PRIMARY KEY,
				profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
				ip TEXT NOT NULL DEFAULT '', ua TEXT NOT NULL DEFAULT '',
				first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL,
				playlists INTEGER NOT NULL DEFAULT 0, guides INTEGER NOT NULL DEFAULT 0, plays INTEGER NOT NULL DEFAULT 0,
				UNIQUE(profile_id, ip, ua))`,
			`CREATE INDEX activity_clients_seen ON activity_clients(last_seen)`,
			`CREATE TABLE activity_sessions(
				id INTEGER PRIMARY KEY,
				profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
				ck TEXT NOT NULL, mode TEXT NOT NULL, host TEXT NOT NULL DEFAULT '',
				ip TEXT NOT NULL DEFAULT '', ua TEXT NOT NULL DEFAULT '',
				started INTEGER NOT NULL, last INTEGER NOT NULL,
				bytes INTEGER NOT NULL DEFAULT 0, requests INTEGER NOT NULL DEFAULT 0)`,
			`CREATE INDEX activity_sessions_started ON activity_sessions(started)`,
			`CREATE INDEX activity_sessions_profile ON activity_sessions(profile_id, started)`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}},
	{name: "add stats_samples and stats_profile_samples: hourly history of cache, retries and playlist/guide fetches", up: func(tx *sql.Tx) error {
		// stats_samples: one row per UTC hour for the whole server (the proxy's cache and retry counters, which are only
		// kept in memory otherwise). stats_profile_samples: playlist and guide fetches per profile and hour. Both hold the
		// increase during the hour and are pruned with the other history (stats_history_days); the second follows its profile.
		for _, q := range []string{
			`CREATE TABLE stats_samples(
				hour INTEGER PRIMARY KEY,
				cache_hits INTEGER NOT NULL DEFAULT 0, cache_misses INTEGER NOT NULL DEFAULT 0, cache_hit_bytes INTEGER NOT NULL DEFAULT 0,
				retries INTEGER NOT NULL DEFAULT 0, holdovers INTEGER NOT NULL DEFAULT 0)`,
			`CREATE TABLE stats_profile_samples(
				hour INTEGER NOT NULL,
				profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
				playlists INTEGER NOT NULL DEFAULT 0, guides INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY(hour, profile_id))`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}},
	{name: "cache settings get explicit on/off switches: a stored 0 (the old way to turn a feature off) becomes the switch set to off", up: func(tx *sql.Tx) error {
		// Before: segment_cache_mb 0 = cache off, segment_cache_ttl_seconds 0 = no sharing and no prefetch,
		// segment_prefetch_count 0 = prefetch off, hls_head_start_seconds 0 = head start off.
		// Now each has a switch (segment_cache, segment_share, segment_prefetch, hls_head_start) and the number has a minimum of 1,
		// so the stored 0 is turned into the switch and the number row is dropped (the default applies when it is switched on again).
		// Only values someone changed are stored; an install that never touched them keeps the defaults (all on).
		set := func(key, val string) error {
			_, err := tx.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, "setting."+key, val)
			return err
		}
		zero := func(key string) (bool, error) {
			var n int
			err := tx.QueryRow(`SELECT COUNT(*) FROM meta WHERE key=? AND TRIM(value) IN ('0','0.0')`, "setting."+key).Scan(&n)
			return n > 0, err
		}
		for _, c := range []struct {
			num  string
			offs []string
		}{
			{"segment_cache_mb", []string{"segment_cache"}},
			{"segment_cache_ttl_seconds", []string{"segment_share", "segment_prefetch"}},
			{"segment_prefetch_count", []string{"segment_prefetch"}},
			{"hls_head_start_seconds", []string{"hls_head_start"}},
		} {
			z, err := zero(c.num)
			if err != nil {
				return err
			}
			if !z {
				continue
			}
			for _, k := range c.offs {
				if err := set(k, `"off"`); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`DELETE FROM meta WHERE key=?`, "setting."+c.num); err != nil {
				return err
			}
		}
		return nil
	}},
}
