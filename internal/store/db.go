// Package store persists devbox state in SQLite (§6).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

//go:embed migrations/*.sql
var migrations embed.FS

// Errors returned by store operations.
var (
	ErrNotFound = errors.New("store: not found")
	ErrExists   = errors.New("store: already exists")
	ErrStale    = errors.New("store: stale revision")
)

// DB is the devbox database: one writer connection and a small reader pool.
type DB struct {
	w   *sql.DB
	r   *sql.DB
	log *slog.Logger
}

// Open creates the database directory, opens SQLite in WAL mode, and applies migrations.
func Open(ctx context.Context, file string, log *slog.Logger) (*DB, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, fmt.Errorf("store: resolve %q: %w", file, err)
	}
	if strings.ContainsAny(abs, "?#") {
		return nil, fmt.Errorf("store: database path %q must not contain '?' or '#'", abs)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("store: create directory: %w", err)
	}
	w, err := sql.Open("sqlite", dsn(abs, "immediate"))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	r, err := sql.Open("sqlite", dsn(abs, ""))
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("store: open reader: %w", err)
	}
	r.SetMaxOpenConns(4)
	db := &DB{w: w, r: r, log: log}
	if err := db.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func dsn(file, txlock string) string {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	if txlock != "" {
		q.Set("_txlock", txlock)
	}
	return "file:" + file + "?" + q.Encode()
}

// Close closes both connection pools.
func (db *DB) Close() error { return errors.Join(db.r.Close(), db.w.Close()) }

func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("store: list migrations: %w", err)
	}
	sort.Strings(names)
	var done []int
	err = db.inTx(ctx, func(tx *sql.Tx) error {
		for _, name := range names {
			v, err := strconv.Atoi(strings.SplitN(path.Base(name), "_", 2)[0])
			if err != nil {
				return fmt.Errorf("store: migration %s has no numeric prefix", name)
			}
			if applied[v] {
				continue
			}
			body, err := migrations.ReadFile(name)
			if err != nil {
				return fmt.Errorf("store: read migration %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("store: apply migration %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, ms(time.Now())); err != nil {
				return fmt.Errorf("store: record migration %s: %w", name, err)
			}
			done = append(done, v)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, v := range done {
		db.log.Info("migration applied", "version", v)
	}
	return nil
}

func (db *DB) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := db.w.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func (db *DB) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func nullTime(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func timePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

func affected(res sql.Result) int64 {
	n, _ := res.RowsAffected() // SQLite always reports affected rows
	return n
}
