package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Cache entries staged by collection and written with an accepted outcome.
type (
	DiffCache struct {
		BaseOID, ReviewedOID, HeadOID string
		Changed                       bool
	}
	CommentCache struct {
		CommentID int64
		UpdatedAt time.Time
		IsCommand bool
	}
	PolicyCache struct {
		BaseRef, Policy string
		FetchedAt       time.Time
	}
	Caches struct {
		Diffs    []DiffCache
		Comments []CommentCache
		Policies []PolicyCache
	}
)

const cacheRetention = 30 * 24 * time.Hour

func writeCaches(ctx context.Context, tx *sql.Tx, repoID int64, c Caches, now time.Time) error {
	for _, d := range c.Diffs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cache_diff (repo_id, base_oid, reviewed_oid, head_oid, changed, created_at)
VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (repo_id, base_oid, reviewed_oid, head_oid) DO UPDATE SET changed = excluded.changed`,
			repoID, d.BaseOID, d.ReviewedOID, d.HeadOID, d.Changed, ms(now)); err != nil {
			return fmt.Errorf("store: cache diff: %w", err)
		}
	}
	for _, cm := range c.Comments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cache_comment (repo_id, comment_id, updated_at, is_command, created_at)
VALUES (?, ?, ?, ?, ?) ON CONFLICT (repo_id, comment_id, updated_at) DO NOTHING`,
			repoID, cm.CommentID, ms(cm.UpdatedAt), cm.IsCommand, ms(now)); err != nil {
			return fmt.Errorf("store: cache comment: %w", err)
		}
	}
	for _, p := range c.Policies {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cache_policy (repo_id, base_ref, policy, fetched_at) VALUES (?, ?, ?, ?)
ON CONFLICT (repo_id, base_ref) DO UPDATE SET policy = excluded.policy, fetched_at = excluded.fetched_at`,
			repoID, p.BaseRef, p.Policy, ms(p.FetchedAt)); err != nil {
			return fmt.Errorf("store: cache policy: %w", err)
		}
	}
	return nil
}

// CachedDiff returns a definite "effective diff changed" answer if cached.
func (db *DB) CachedDiff(ctx context.Context, repoID int64, base, reviewed, head string) (changed, ok bool, err error) {
	err = db.r.QueryRowContext(ctx, `SELECT changed FROM cache_diff WHERE repo_id = ? AND base_oid = ? AND reviewed_oid = ? AND head_oid = ?`,
		repoID, base, reviewed, head).Scan(&changed)
	return cacheResult(changed, err)
}

// CachedComment returns whether a comment version is a slash command, if cached.
func (db *DB) CachedComment(ctx context.Context, repoID, commentID int64, updatedAt time.Time) (isCommand, ok bool, err error) {
	err = db.r.QueryRowContext(ctx, `SELECT is_command FROM cache_comment WHERE repo_id = ? AND comment_id = ? AND updated_at = ?`,
		repoID, commentID, ms(updatedAt)).Scan(&isCommand)
	return cacheResult(isCommand, err)
}

// CachedPolicy returns a base branch's signature policy fetched within ttl.
func (db *DB) CachedPolicy(ctx context.Context, repoID int64, baseRef string, now time.Time, ttl time.Duration) (string, bool, error) {
	var policy string
	err := db.r.QueryRowContext(ctx, `SELECT policy FROM cache_policy WHERE repo_id = ? AND base_ref = ? AND fetched_at >= ?`,
		repoID, baseRef, ms(now.Add(-ttl))).Scan(&policy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: cached policy: %w", err)
	}
	return policy, true, nil
}

func cacheResult(v bool, err error) (bool, bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("store: cache read: %w", err)
	}
	return v, true, nil
}

// Purge deletes diff and comment cache rows older than 30 days.
func (db *DB) Purge(ctx context.Context, now time.Time) (int64, error) {
	cutoff := ms(now.Add(-cacheRetention))
	var total int64
	for _, q := range []string{`DELETE FROM cache_diff WHERE created_at < ?`, `DELETE FROM cache_comment WHERE created_at < ?`} {
		res, err := db.w.ExecContext(ctx, q, cutoff)
		if err != nil {
			return total, fmt.Errorf("store: purge: %w", err)
		}
		total += affected(res)
	}
	return total, nil
}
