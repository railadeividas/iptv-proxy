package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withMigrations swaps the migration list for the length of a test.
func withMigrations(t *testing.T, m []migration) {
	t.Helper()
	old := migrations
	migrations = m
	t.Cleanup(func() { migrations = old })
}

func version(t *testing.T, st *Store) int {
	t.Helper()
	var v int
	if err := st.DB.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func addColumn(table, col string) migration {
	return migration{name: "add " + col, up: func(tx *sql.Tx) error {
		_, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s TEXT NOT NULL DEFAULT ''`, table, col))
		return err
	}}
}

func TestOpenFreshAndReopen(t *testing.T) {
	withMigrations(t, []migration{addColumn("profiles", "note")})
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v := version(t, st); v != baselineVersion+1 {
		t.Fatalf("a new database ends at the latest version, got %d", v)
	}
	if _, err := st.DB.Exec(`SELECT note FROM profiles`); err != nil {
		t.Fatalf("a new database also gets the migrations: %v", err)
	}
	st.Close()
	if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("iptv.db.bak-v%d", baselineVersion))); err == nil {
		t.Fatal("a new database has nothing to back up")
	}
	st, err = Open(dir) // same version: nothing to do
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st.Close()
}

// A database made at the baseline version, with real data in it, is migrated and keeps its data.
func TestMigrateKeepsDataAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	withMigrations(t, nil)
	st, err := Open(dir) // baseline database
	if err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(`INSERT INTO users(username,password,role,created_at) VALUES('root','x','superuser',1)`)
	st.DB.Exec(`INSERT INTO profiles(owner_id,name,token,created_at) VALUES(1,'Living room','tok',1)`)
	st.DB.Exec(`INSERT INTO profile_channels(profile_id,ck,position) VALUES(1,'A@SD',0)`)
	st.Close()

	withMigrations(t, []migration{
		addColumn("profiles", "note"),
		{name: "rebuild profile_channels", up: func(tx *sql.Tx) error { // the table-rebuild recipe, with a foreign key in the way
			for _, q := range []string{
				`CREATE TABLE pc2(profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE, ck TEXT NOT NULL, position INTEGER NOT NULL, label TEXT NOT NULL DEFAULT '', PRIMARY KEY(profile_id, ck))`,
				`INSERT INTO pc2(profile_id,ck,position) SELECT profile_id,ck,position FROM profile_channels`,
				`DROP TABLE profile_channels`,
				`ALTER TABLE pc2 RENAME TO profile_channels`,
			} {
				if _, err := tx.Exec(q); err != nil {
					return err
				}
			}
			return nil
		}},
	})
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v := version(t, st); v != baselineVersion+2 {
		t.Fatalf("version %d", v)
	}
	var name, note, ck string
	if err := st.DB.QueryRow(`SELECT p.name, p.note, c.ck FROM profiles p JOIN profile_channels c ON c.profile_id=p.id`).Scan(&name, &note, &ck); err != nil || name != "Living room" || ck != "A@SD" {
		t.Fatalf("data after migrating: %q %q %v", name, ck, err)
	}
	// foreign keys are back on for normal use
	var fk int
	st.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if _, err := st.DB.Exec(`INSERT INTO profile_channels(profile_id,ck,position) VALUES(999,'X',0)`); err == nil && fk == 1 {
		t.Fatal("foreign keys must be enforced again after migrating")
	}
	// the copy from before the upgrade opens as the old version
	old, err := sql.Open("sqlite", "file:"+filepath.Join(dir, fmt.Sprintf("iptv.db.bak-v%d", baselineVersion)))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var n, v int
	old.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&n)
	old.QueryRow(`PRAGMA user_version`).Scan(&v)
	if n != 1 || v != baselineVersion {
		t.Fatalf("backup: %d profiles, version %d", n, v)
	}
}

// The real migration that adds health_excluded runs on a database holding streams and keeps them; an exclusion is
// keyed by address, so it survives the stream rows being replaced by a sync.
func TestMigrateAddsHealthExcluded(t *testing.T) {
	dir := t.TempDir()
	real := migrations
	withMigrations(t, nil)
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(`INSERT INTO streams(ck,url,status,fail_count,fail_reason) VALUES('A@SD','http://a/1',2,5,'Timed out')`)
	st.Close()

	withMigrations(t, real)
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM streams`).Scan(&n)
	if n != 1 {
		t.Fatalf("streams after migrating: %d", n)
	}
	if !st.SetExcluded("http://a/1", true) || st.ExcludedCount() != 1 {
		t.Fatal("a migrated database can exclude a stream")
	}
	st.DB.Exec(`DELETE FROM streams`)
	st.DB.Exec(`INSERT INTO streams(ck,url) VALUES('A@SD','http://a/1')`)
	items, total := st.Excluded("", 0, 25)
	if total != 1 || items[0].Reason != "Timed out" || items[0].Auto {
		t.Fatalf("the exclusion survives a sync: %+v", items)
	}
}

// The migration to days of failing gives streams that fail now a start for their run, leaves working ones alone,
// keeps every other column and drops the stored failure-count setting.
func TestMigrateAddsFailSince(t *testing.T) {
	dir := t.TempDir()
	real := migrations
	withMigrations(t, real[:1]) // the database as it was before this migration
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(`INSERT INTO streams(ck,url,status,checked_at,fail_count,fail_reason) VALUES('A@SD','http://a/1',2,1000,9,'Timed out'),('B@SD','http://b/1',1,2000,0,'')`)
	st.DB.Exec(`INSERT INTO health_excluded(url,at,auto) VALUES('http://a/1',500,1)`)
	st.DB.Exec(`INSERT INTO meta(key,value) VALUES('setting.health_auto_exclude_after','48'),('setting.health_scope','"all"')`)
	st.Close()

	withMigrations(t, real)
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var since, fails int64
	var reason string
	st.DB.QueryRow(`SELECT fail_since, fail_count, fail_reason FROM streams WHERE url='http://a/1'`).Scan(&since, &fails, &reason)
	if since != 1000 || fails != 9 || reason != "Timed out" {
		t.Fatalf("failing stream: since %d fails %d reason %q", since, fails, reason)
	}
	st.DB.QueryRow(`SELECT fail_since FROM streams WHERE url='http://b/1'`).Scan(&since)
	if since != 0 {
		t.Fatalf("a working stream has no run of failures: %d", since)
	}
	if st.ExcludedCount() != 1 {
		t.Fatal("exclusions are kept")
	}
	if st.Meta("setting.health_auto_exclude_after") != "" || st.Meta("setting.health_scope") != `"all"` {
		t.Fatal("only the old count setting is dropped")
	}
}

// The profile-channel interval moves from minutes to hours: a stored value is rounded to whole hours (at least 1),
// and an install that never changed it keeps no override, so the new default applies.
func TestMigrateSelectedIntervalToHours(t *testing.T) {
	real := migrations
	for minutes, want := range map[string]string{"90": "2", "5": "1", "1440": "24", "": ""} {
		dir := t.TempDir()
		withMigrations(t, real[:2]) // the database as it was before this migration
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if minutes != "" {
			st.DB.Exec(`INSERT INTO meta(key,value) VALUES('setting.health_selected_interval_minutes',?)`, minutes)
		}
		st.DB.Exec(`INSERT INTO meta(key,value) VALUES('setting.health_retry_minutes','15')`)
		st.Close()

		withMigrations(t, real)
		st, err = Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Meta("setting.health_selected_interval_hours"); got != want {
			t.Fatalf("%q minutes became %q hours, want %q", minutes, got, want)
		}
		if st.Meta("setting.health_selected_interval_minutes") != "" || st.Meta("setting.health_retry_minutes") != "15" {
			t.Fatal("the old key is removed, other settings stay")
		}
		st.Close()
	}
}

// Rounds get an excluded total; stored rounds keep their totals and show 0 excluded.
func TestMigrateAddsRoundExcluded(t *testing.T) {
	dir := t.TempDir()
	real := migrations
	withMigrations(t, real[:3]) // the database as it was before this migration
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(`INSERT INTO health_rounds(started,finished,kind,label,probed,ok,bad,t_ok,t_bad,t_unchecked) VALUES(1,2,'auto','x',5,3,2,30,20,10)`)
	st.Close()

	withMigrations(t, real)
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r, ok := st.GetRound(1)
	if !ok || r.Probed != 5 || r.TotalOK != 30 || r.TotalBad != 20 || r.TotalNew != 10 || r.TotalExcluded != 0 {
		t.Fatalf("round after migrating: %+v", r)
	}
}

func TestFailedMigrationChangesNothing(t *testing.T) {
	dir := t.TempDir()
	withMigrations(t, nil)
	st, _ := Open(dir)
	st.DB.Exec(`INSERT INTO meta(key,value) VALUES('k','v')`)
	st.Close()

	withMigrations(t, []migration{
		addColumn("profiles", "note"),
		{name: "breaks halfway", up: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`ALTER TABLE profiles ADD COLUMN second TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
			return fmt.Errorf("boom")
		}},
	})
	_, err := Open(dir)
	if err == nil || !strings.Contains(err.Error(), "breaks halfway") || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "bak-v") {
		t.Fatalf("the error says which step failed, why, and where the backup is: %v", err)
	}
	// the first step is kept (it was complete); the failed one left no trace
	withMigrations(t, nil)
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(dir, "iptv.db"))
	defer db.Close()
	var v int
	db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != baselineVersion+1 {
		t.Fatalf("version after the failure: %d", v)
	}
	if _, err := db.Exec(`SELECT second FROM profiles`); err == nil {
		t.Fatal("a failed step must be rolled back")
	}
}

func TestMigrationLeavingBrokenReferencesIsRefused(t *testing.T) {
	dir := t.TempDir()
	withMigrations(t, nil)
	st, _ := Open(dir)
	st.DB.Exec(`INSERT INTO users(username,password,role,created_at) VALUES('root','x','superuser',1)`)
	st.DB.Exec(`INSERT INTO profiles(owner_id,name,token,created_at) VALUES(1,'P','t',1)`)
	st.Close()
	withMigrations(t, []migration{{name: "deletes the owner", up: func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM users`) // foreign keys are off during a migration, so this would leave an orphan
		return err
	}}})
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "refer to rows that do not exist") {
		t.Fatalf("expected the broken references to be caught: %v", err)
	}
}

func TestOpenRefusesNewerAndTooOldDatabases(t *testing.T) {
	withMigrations(t, nil)
	dir := t.TempDir()
	st, _ := Open(dir)
	st.DB.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, baselineVersion+3))
	st.Close()
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("a database from a newer app must be refused: %v", err)
	}

	dir = t.TempDir()
	st, _ = Open(dir)
	st.DB.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, baselineVersion-1))
	st.Close()
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "delete iptv.db") {
		t.Fatalf("a development database older than the baseline gets a clear refusal: %v", err)
	}
}
