package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct{ DB *sql.DB }

func Open(directory string) (*Store, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	// Keep the original filename so an existing installation opens its database.
	abs, err := filepath.Abs(filepath.Join(directory, "http-wrapper.db"))
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := url.Values{"_pragma": {"busy_timeout(30000)", "foreign_keys(1)", "journal_mode(WAL)"}, "_txlock": {"immediate"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	// WAL readers remain available while SQLite serializes write transactions.
	// Immediate write transactions avoid read-to-write upgrade races.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	s := &Store{DB: db}
	if err := s.migrate(context.Background(), migrations); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate(ctx context.Context, source fs.FS) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}
	files, err := fs.Glob(source, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, name := range files {
		var count int
		if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations WHERE name=?", name).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			continue
		}
		body, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			if name == "migrations/008_scan_paths.sql" {
				err = backfillScanPaths(ctx, tx)
			}
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(name) VALUES(?)", name)
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func backfillScanPaths(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []struct{ name, source, target string }{
		{"scan_jobs", "root_path", "root_key"},
		{"scan_index", "escaped_path", "path_key"},
	} {
		// Read a bounded page before writing so this also handles a populated index.
		var cursor int64
		for {
			rows, err := tx.QueryContext(ctx, "SELECT rowid,"+table.source+" FROM "+table.name+" WHERE rowid>? ORDER BY rowid LIMIT 500", cursor)
			if err != nil {
				return err
			}
			type row struct {
				id   int64
				path string
			}
			batch := make([]row, 0, 500)
			for rows.Next() {
				var item row
				if err = rows.Scan(&item.id, &item.path); err != nil {
					rows.Close()
					return err
				}
				batch = append(batch, item)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(batch) == 0 {
				break
			}
			for _, item := range batch {
				decoded, err := url.PathUnescape(item.path)
				if err != nil {
					return fmt.Errorf("invalid stored scan path")
				}
				if _, err = tx.ExecContext(ctx, "UPDATE "+table.name+" SET "+table.target+"=? WHERE rowid=?", decoded, item.id); err != nil {
					return err
				}
				cursor = item.id
			}
		}
	}
	return nil
}
