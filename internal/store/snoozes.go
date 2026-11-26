package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SnoozeRow is a snooze as stored: the bucket and head the PR had when it was
// snoozed, and when it ends (nil: only when the PR changes).
type SnoozeRow struct {
	Until   *time.Time
	Bucket  string
	HeadOID string
}

// Snoozed reports whether the row's snooze still hides it at now: it ends when
// its time passes, the PR moves to another bucket, or new commits arrive.
func (q QueueRow) Snoozed(now time.Time) bool {
	s := q.Snooze
	return s != nil && s.Bucket == q.Result.Bucket && s.HeadOID == q.HeadOID && (s.Until == nil || now.Before(*s.Until))
}

// Snooze hides a classified PR until until (nil: until it changes), replacing
// any earlier snooze. A PR without a result yields ErrNotFound.
func (db *DB) Snooze(ctx context.Context, repoID int64, number int, until *time.Time, now time.Time) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		var bucket, head string
		err := tx.QueryRowContext(ctx, `SELECT res.bucket, COALESCE(p.head_oid, '') FROM results res
JOIN prs p ON p.repo_id = res.repo_id AND p.number = res.number
WHERE res.repo_id = ? AND res.number = ? AND p.state = 'open'`, repoID, number).Scan(&bucket, &head)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("store: read pr to snooze: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO snoozes (repo_id, number, until_at, bucket, head_oid, created_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (repo_id, number) DO UPDATE SET until_at = excluded.until_at, bucket = excluded.bucket, head_oid = excluded.head_oid,
  created_at = excluded.created_at`, repoID, number, nullTime(until), bucket, head, ms(now)); err != nil {
			return fmt.Errorf("store: snooze: %w", err)
		}
		return nil
	})
}

// Unsnooze ends a PR's snooze; a PR without one is not an error.
func (db *DB) Unsnooze(ctx context.Context, repoID int64, number int) error {
	if _, err := db.w.ExecContext(ctx, `DELETE FROM snoozes WHERE repo_id = ? AND number = ?`, repoID, number); err != nil {
		return fmt.Errorf("store: unsnooze: %w", err)
	}
	return nil
}
