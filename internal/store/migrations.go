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
}
