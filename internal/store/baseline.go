package store

// baselineVersion is the database version that baselineSchema creates. It is the first version a user can have kept: databases
// from older development builds are refused (see migrate).
const baselineVersion = 4

// baselineSchema is the database as it was at baselineVersion. It is FROZEN: never edit it. Every later change is a migration
// (migrations.go), which also runs on a brand-new database right after this schema, so one path is tested for everybody.
const baselineSchema = `
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS channels(id TEXT PRIMARY KEY, name TEXT, country TEXT, network TEXT, categories TEXT, is_nsfw INTEGER, closed TEXT, website TEXT);
CREATE TABLE IF NOT EXISTS feeds(channel TEXT, id TEXT, name TEXT, is_main INTEGER, languages TEXT, format TEXT, PRIMARY KEY(channel,id));
CREATE TABLE IF NOT EXISTS logos(channel TEXT, feed TEXT, url TEXT);
CREATE INDEX IF NOT EXISTS logos_ch ON logos(channel);
CREATE TABLE IF NOT EXISTS guides(channel TEXT, feed TEXT, site TEXT, site_id TEXT, site_name TEXT, lang TEXT);
CREATE INDEX IF NOT EXISTS guides_ch ON guides(channel);
CREATE TABLE IF NOT EXISTS categories(id TEXT PRIMARY KEY, name TEXT);
CREATE TABLE IF NOT EXISTS countries(code TEXT PRIMARY KEY, name TEXT, flag TEXT);
CREATE TABLE IF NOT EXISTS languages(code TEXT PRIMARY KEY, name TEXT);
CREATE TABLE IF NOT EXISTS custom_sources(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, url TEXT, created_at INTEGER, last_ok INTEGER DEFAULT 0, last_error TEXT DEFAULT '', refresh_hours INTEGER NOT NULL DEFAULT 12, last_try INTEGER NOT NULL DEFAULT 0, m3u TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS streams(
  id INTEGER PRIMARY KEY AUTOINCREMENT, src INTEGER NOT NULL DEFAULT 0, ck TEXT NOT NULL,
  channel TEXT, feed TEXT, title TEXT, url TEXT NOT NULL, quality TEXT, user_agent TEXT, referrer TEXT,
  logo TEXT, group_title TEXT, tvg_id TEXT NOT NULL DEFAULT '', status INTEGER NOT NULL DEFAULT 0, checked_at INTEGER NOT NULL DEFAULT 0,
  fail_count INTEGER NOT NULL DEFAULT 0, fail_reason TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS streams_ck ON streams(ck);
CREATE INDEX IF NOT EXISTS streams_src ON streams(src);
CREATE INDEX IF NOT EXISTS streams_url ON streams(url);
CREATE TABLE IF NOT EXISTS entries(ck TEXT PRIMARY KEY, channel TEXT, feed TEXT, name TEXT, country TEXT, categories TEXT, languages TEXT, nsfw INTEGER, closed INTEGER, logo TEXT, src INTEGER);
CREATE INDEX IF NOT EXISTS entries_country ON entries(country);
CREATE TABLE IF NOT EXISTS health_rounds(id INTEGER PRIMARY KEY AUTOINCREMENT, started INTEGER NOT NULL, finished INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL, label TEXT NOT NULL, probed INTEGER NOT NULL DEFAULT 0, ok INTEGER NOT NULL DEFAULT 0, bad INTEGER NOT NULL DEFAULT 0, stopped INTEGER NOT NULL DEFAULT 0,
  t_ok INTEGER NOT NULL DEFAULT 0, t_bad INTEGER NOT NULL DEFAULT 0, t_unchecked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS health_checks(id INTEGER PRIMARY KEY AUTOINCREMENT, ck TEXT NOT NULL, url TEXT NOT NULL, at INTEGER NOT NULL, ok INTEGER NOT NULL,
  reason TEXT NOT NULL DEFAULT '', ms INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS health_checks_ck ON health_checks(ck, at);
CREATE TABLE IF NOT EXISTS skipped(id INTEGER PRIMARY KEY AUTOINCREMENT, source TEXT, ref TEXT, url TEXT NOT NULL DEFAULT '', reason TEXT);
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE COLLATE NOCASE, password TEXT NOT NULL, role TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS profiles(id INTEGER PRIMARY KEY AUTOINCREMENT, owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, name TEXT NOT NULL, token TEXT NOT NULL UNIQUE, mode TEXT NOT NULL DEFAULT 'auto', compat TEXT NOT NULL DEFAULT '', rev INTEGER NOT NULL DEFAULT 1, created_at INTEGER);
CREATE TABLE IF NOT EXISTS profile_channels(profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE, ck TEXT NOT NULL, position INTEGER NOT NULL, name TEXT NOT NULL DEFAULT '', stream_url TEXT NOT NULL DEFAULT '', mode TEXT NOT NULL DEFAULT '', epg_id TEXT NOT NULL DEFAULT '', PRIMARY KEY(profile_id, ck));
`
