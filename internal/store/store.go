// Package store wraps the embedded SQLite database.
package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite"
)

const (
	StatusUnknown = 0
	StatusOK      = 1
	StatusDead    = 2
)

// Store wraps the database. Notify, when set, is told what changed
// ("streams", "catalog" or "profiles") so the UI can refresh live.
type Store struct {
	DB     *sql.DB
	Notify func(kind string)
}

func (s *Store) notify(kind string) {
	if s.Notify != nil {
		s.Notify(kind)
	}
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dir, "iptv.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := migrate(db, dir); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

// Notify2 lets other packages raise a change notification.
func (s *Store) Notify2(kind string) { s.notify(kind) }

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) Meta(key string) string {
	var v string
	_ = s.DB.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	return v
}

func (s *Store) SetMeta(key, value string) {
	_, _ = s.DB.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
}

func (s *Store) DeleteMeta(key string) {
	_, _ = s.DB.Exec(`DELETE FROM meta WHERE key=?`, key)
}

func (s *Store) MetaInt(key string) int64 {
	n, _ := strconv.ParseInt(s.Meta(key), 10, 64)
	return n
}

func (s *Store) SetMetaInt(key string, n int64) { s.SetMeta(key, strconv.FormatInt(n, 10)) }

// Ready reports whether the first successful API sync has completed.
func (s *Store) Ready() bool { return s.MetaInt("synced_at") > 0 }

// RebuildEntries recomputes the denormalised browse table from streams.
func (s *Store) RebuildEntries() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM entries`); err != nil {
		return err
	}
	// iptv-org streams (src=0) joined with channel/feed metadata.
	if _, err := tx.Exec(`
INSERT OR IGNORE INTO entries(ck,channel,feed,name,country,categories,languages,nsfw,closed,logo,src)
SELECT s.ck, s.channel, s.feed,
  c.name || CASE WHEN f.id IS NOT NULL AND f.is_main=0 AND f.name<>'' THEN ' ('||f.name||')' ELSE '' END,
  c.country, c.categories, COALESCE(f.languages,'[]'), c.is_nsfw, CASE WHEN c.closed<>'' THEN 1 ELSE 0 END,
  (SELECT l.url FROM logos l WHERE l.channel=s.channel ORDER BY (l.feed=s.feed) DESC LIMIT 1), 0
FROM (SELECT DISTINCT ck, channel, feed FROM streams WHERE src=0) s
JOIN channels c ON c.id=s.channel
LEFT JOIN feeds f ON f.channel=s.channel AND f.id=s.feed`); err != nil {
		return err
	}
	// custom streams.
	if _, err := tx.Exec(`
INSERT OR IGNORE INTO entries(ck,channel,feed,name,country,categories,languages,nsfw,closed,logo,src)
SELECT ck, NULL, NULL, MIN(title), '',
  CASE WHEN MIN(group_title)<>'' THEN '["'||REPLACE(MIN(group_title),'"','')||'"]' ELSE '[]' END,
  '[]', 0, 0, MIN(logo), MIN(src)
FROM streams WHERE src>0 GROUP BY ck`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notify("catalog")
	return nil
}
