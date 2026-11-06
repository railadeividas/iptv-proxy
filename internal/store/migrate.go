package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// latestVersion is the database version this build works with.
func latestVersion() int { return baselineVersion + len(migrations) }

// migrate brings the database in dir to latestVersion.
//
//   - A new database gets the baseline schema, then every migration.
//   - An older database (but not older than the baseline) is backed up next to itself, then migrated step by step; every step is
//     one transaction, so a failing step leaves the database exactly as it was.
//   - A database from a newer build is refused: running an older app on it could damage it.
func migrate(db *sql.DB, dir string) error {
	var tables, version int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&tables)
	_ = db.QueryRow(`PRAGMA user_version`).Scan(&version)
	latest := latestVersion()
	fresh := tables == 0
	switch {
	case fresh:
		if _, err := db.Exec(baselineSchema); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, baselineVersion)); err != nil {
			return err
		}
		version = baselineVersion
	case version < baselineVersion:
		return fmt.Errorf("the database in %s is from a development build older than the first supported version (database version %d, oldest supported %d): delete iptv.db, iptv.db-wal and iptv.db-shm there and restart; the catalogue is downloaded again", dir, version, baselineVersion)
	case version > latest:
		return fmt.Errorf("the database in %s was made by a newer version of this app (database version %d, this app knows up to %d): update the app, or restore the backup (iptv.db.bak-v%d) made before that upgrade", dir, version, latest, latest)
	}
	if version == latest {
		return nil
	}

	backup := ""
	if !fresh {
		var err error
		if backup, err = backupDatabase(db, dir, version); err != nil {
			return fmt.Errorf("could not back up the database before migrating it (nothing was changed): %w", err)
		}
	}

	ctx := context.Background()
	conn, err := db.Conn(ctx) // foreign_keys is a per-connection setting, so one connection does all the steps
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)

	for v := version; v < latest; v++ {
		if err := applyMigration(ctx, conn, v, migrations[v-baselineVersion]); err != nil {
			hint := ""
			if backup != "" {
				hint = fmt.Sprintf(" (the database is unchanged; a copy from before the upgrade is in %s)", backup)
			}
			return fmt.Errorf("migrating the database from version %d to %d (%s) failed: %w%s", v, v+1, migrations[v-baselineVersion].name, err, hint)
		}
	}
	return nil
}

// applyMigration runs one step, and the new version number, in one transaction.
func applyMigration(ctx context.Context, conn *sql.Conn, from int, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := m.up(tx); err != nil {
		return err
	}
	// a step that leaves rows pointing at nothing is a bug: refuse it rather than store broken data
	rows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	broken := rows.Next()
	rows.Close()
	if broken {
		return fmt.Errorf("it left rows that refer to rows that do not exist")
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, from+1)); err != nil {
		return err
	}
	return tx.Commit()
}

// backupDatabase writes a consistent copy of the database to iptv.db.bak-v<version> (replacing the previous copy of that version).
func backupDatabase(db *sql.DB, dir string, version int) (string, error) {
	path := filepath.Join(dir, fmt.Sprintf("iptv.db.bak-v%d", version))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if _, err := db.Exec(`VACUUM INTO '` + strings.ReplaceAll(path, `'`, `''`) + `'`); err != nil {
		return "", err
	}
	return path, nil
}
