package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// The activity tables are added next to profiles, users and channels already in the database; they follow their
// profile when it is deleted, and PruneActivity enforces the history limits.
func TestMigrateAddsActivity(t *testing.T) {
	dir := t.TempDir()
	real := migrations
	withMigrations(t, real[:4]) // the database as it was before this migration
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(`INSERT INTO users(id,username,password,role,created_at) VALUES(1,'ann','x','admin',1)`)
	st.DB.Exec(`INSERT INTO profiles(id,owner_id,name,token,created_at) VALUES(1,1,'Living room','tok',1)`)
	st.Close()

	withMigrations(t, real)
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&n)
	if n != 1 {
		t.Fatalf("profiles after migrating: %d", n)
	}
	now := time.Now().Unix()
	st.TouchClient(1, "10.0.0.0/24", "VLC", "playlist", now)
	st.TouchClient(1, "10.0.0.0/24", "VLC", "playlist", now)
	st.TouchClient(1, "10.0.0.0/24", "VLC", "guide", now)
	id := st.AddSession(Session{ProfileID: 1, CK: "A@SD", Mode: "proxy", IP: "10.0.0.0/24", UA: "VLC", Started: now - 60, Last: now - 60})
	if id == 0 {
		t.Fatal("a session of an existing profile is stored")
	}
	st.UpdateSession(id, now, 5000, 12)
	if st.AddSession(Session{ProfileID: 99, CK: "A@SD", Mode: "proxy", Started: now, Last: now}) != 0 {
		t.Fatal("a session of a missing profile is not stored")
	}
	cs, total, err := st.ActivityClients(ActivityFilter{Owner: 1}, 10, 0)
	if err != nil || total != 1 || cs[0].Playlists != 2 || cs[0].Guides != 1 {
		t.Fatalf("clients: %+v %v", cs, err)
	}
	if _, total, _ := st.ActivityClients(ActivityFilter{Owner: 2}, 10, 0); total != 0 {
		t.Fatal("somebody else's devices are not listed")
	}
	tot, _ := st.ActivityTotals(ActivityFilter{})
	if tot.Sessions != 1 || tot.Proxy != 1 || tot.Bytes != 5000 || tot.Seconds != 60 || tot.Clients != 1 {
		t.Fatalf("totals: %+v", tot)
	}
	ps, _ := st.ActivityProfiles(ActivityFilter{Owner: 1})
	if len(ps) != 1 || ps[0].Playlists != 2 || ps[0].Sessions != 1 || ps[0].Bytes != 5000 {
		t.Fatalf("profiles: %+v", ps)
	}
	chs, total, _ := st.ActivityBreakdown(ActivityFilter{}, "channel", "sessions", 10, 0)
	if total != 1 || chs[0].Key != "A@SD" || chs[0].Viewers != 1 {
		t.Fatalf("channels: %+v", chs)
	}

	st.PruneActivity(30, 0, 5000) // keep no sessions
	if _, total, _ := st.ActivitySessions(ActivityFilter{}, 10, 0); total != 0 {
		t.Fatal("0 sessions to keep deletes them")
	}
	if _, total, _ := st.ActivityClients(ActivityFilter{}, 10, 0); total != 1 {
		t.Fatal("devices stay")
	}
	st.DB.Exec(`UPDATE activity_clients SET last_seen=1`)
	st.PruneActivity(30, 100, 5000)
	if _, total, _ := st.ActivityClients(ActivityFilter{}, 10, 0); total != 0 {
		t.Fatal("devices older than the history are deleted")
	}

	st.AddSession(Session{ProfileID: 1, CK: "B", Mode: "redirect", Started: now, Last: now})
	st.TouchClient(1, "", "", "play", now)
	if _, err := st.DB.Exec(`DELETE FROM profiles WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	st.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM activity_sessions)+(SELECT COUNT(*) FROM activity_clients)`).Scan(&n)
	if n != 0 {
		t.Fatal("activity follows a deleted profile")
	}
}

// The sample tables are added to a database that already holds activity; the old data stays, new databases get them too,
// samples follow their profile and PruneActivity drops the old ones.
func TestMigrateAddsStatsSamples(t *testing.T) {
	dir := t.TempDir()
	real := migrations
	withMigrations(t, real[:len(real)-2]) // the database as it was before this migration
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	st.DB.Exec(`INSERT INTO users(id,username,password,role,created_at) VALUES(1,'ann','x','admin',1)`)
	st.DB.Exec(`INSERT INTO profiles(id,owner_id,name,token,created_at) VALUES(1,1,'Living room','tok',1)`)
	st.AddSession(Session{ProfileID: 1, CK: "A@SD", Mode: "proxy", Started: now - 60, Last: now, Bytes: 7})
	if _, err := st.DB.Exec(`SELECT 1 FROM stats_samples`); err == nil {
		t.Fatal("the tables do not exist before the migration")
	}
	st.Close()

	withMigrations(t, real)
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v := version(t, st); v != baselineVersion+len(real) {
		t.Fatalf("version %d", v)
	}
	if _, total, _ := st.ActivitySessions(ActivityFilter{}, 10, 0); total != 1 {
		t.Fatal("activity survives the migration")
	}
	hour := now / 3600 * 3600
	st.AddSamples(hour, ServerSample{CacheHits: 3, Retries: 1}, map[int64]ProfileSample{1: {Playlists: 2}, 99: {Guides: 5}})
	st.AddSamples(hour, ServerSample{CacheHits: 2}, map[int64]ProfileSample{1: {Playlists: 1, Guides: 4}})
	var hits, retries, pl, gu, rows int64
	st.DB.QueryRow(`SELECT cache_hits, retries FROM stats_samples WHERE hour=?`, hour).Scan(&hits, &retries)
	st.DB.QueryRow(`SELECT playlists, guides FROM stats_profile_samples WHERE hour=? AND profile_id=1`, hour).Scan(&pl, &gu)
	st.DB.QueryRow(`SELECT COUNT(*) FROM stats_profile_samples`).Scan(&rows)
	if hits != 5 || retries != 1 || pl != 3 || gu != 4 || rows != 1 {
		t.Fatalf("samples add up per hour and skip missing profiles: %d %d %d %d rows=%d", hits, retries, pl, gu, rows)
	}
	st.AddSamples(hour-40*86400, ServerSample{CacheHits: 1}, map[int64]ProfileSample{1: {Playlists: 1}})
	st.PruneActivity(30, 100, 100)
	st.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM stats_samples)*10+(SELECT COUNT(*) FROM stats_profile_samples)`).Scan(&rows)
	if rows != 11 {
		t.Fatalf("samples older than the history are pruned: %d", rows)
	}
	if _, err := st.DB.Exec(`DELETE FROM profiles WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	st.DB.QueryRow(`SELECT COUNT(*) FROM stats_profile_samples`).Scan(&rows)
	if rows != 0 {
		t.Fatal("samples follow a deleted profile")
	}

	// a brand-new database has them too
	fresh, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := fresh.DB.Exec(`SELECT hour, cache_hits FROM stats_samples`); err != nil {
		t.Fatal(err)
	}
}

// A stored 0 that used to switch a cache feature off becomes the explicit switch; other stored values stay.
func TestMigrateCacheSwitches(t *testing.T) {
	dir := t.TempDir()
	real := migrations
	withMigrations(t, real[:len(real)-1])
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"segment_cache_ttl_seconds": "0", "segment_prefetch_count": "4", "hls_head_start_seconds": "0",
		"segment_cache_mb": "512", "health_scope": `"all"`,
	} {
		st.DB.Exec(`INSERT INTO meta(key,value) VALUES(?,?)`, "setting."+k, v)
	}
	st.Close()

	withMigrations(t, real)
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for k, want := range map[string]string{
		"segment_share": `"off"`, "segment_prefetch": `"off"`, "hls_head_start": `"off"`, // ttl 0 and head start 0 became switches
		"segment_cache_ttl_seconds": "", "hls_head_start_seconds": "", // the zero rows are gone: the default returns with the switch
		"segment_cache": "", "segment_cache_mb": "512", "segment_prefetch_count": "4", "health_scope": `"all"`, // untouched
	} {
		if got := st.Meta("setting." + k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// A brand-new database runs the migration too and has nothing to convert.
func TestMigrateCacheSwitchesOnNewDatabase(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Meta("setting.segment_cache") != "" || st.Meta("setting.hls_head_start") != "" {
		t.Fatal("defaults are not stored")
	}
}
