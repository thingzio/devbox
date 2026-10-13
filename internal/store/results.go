package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ResultRow is a stored classification result.
type ResultRow struct {
	Bucket       string
	Reason       string
	Action       string
	WaitingSince *time.Time
	Also         []string
	Notes        []string
}

// Transition is one bucket change in the append-only log.
type Transition struct {
	ID     int64
	RepoID int64
	Number int
	At     time.Time
	From   *string
	To     string
	Reason string
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func encodeList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(items) // string slices always marshal
	return string(b)
}

func decodeList(raw string) []string {
	var out []string
	_ = json.Unmarshal([]byte(raw), &out) // written by encodeList; malformed rows read as empty
	return out
}

func readResult(ctx context.Context, q rowQuerier, repoID int64, number int) (*ResultRow, error) {
	var r ResultRow
	var since sql.NullInt64
	var also, notes string
	err := q.QueryRowContext(ctx, `SELECT bucket, reason, action, waiting_since, also, notes FROM results WHERE repo_id = ? AND number = ?`,
		repoID, number).Scan(&r.Bucket, &r.Reason, &r.Action, &since, &also, &notes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read result: %w", err)
	}
	r.WaitingSince = timePtr(since)
	r.Also, r.Notes = decodeList(also), decodeList(notes)
	return &r, nil
}

func sameResult(a, b ResultRow) bool {
	sameSince := (a.WaitingSince == nil) == (b.WaitingSince == nil) &&
		(a.WaitingSince == nil || ms(*a.WaitingSince) == ms(*b.WaitingSince))
	return a.Bucket == b.Bucket && a.Reason == b.Reason && a.Action == b.Action && sameSince &&
		slices.Equal(decodeList(encodeList(a.Also)), decodeList(encodeList(b.Also))) &&
		slices.Equal(decodeList(encodeList(a.Notes)), decodeList(encodeList(b.Notes)))
}

// writeResult upserts the result and appends a transition when the bucket
// differs from the stored result, or from the last logged bucket when no
// result row exists (first classification or reopen).
func writeResult(ctx context.Context, tx *sql.Tx, repoID int64, number int, res ResultRow, now time.Time) (*Transition, error) {
	var prev sql.NullString
	cur, err := readResult(ctx, tx, repoID, number)
	if err != nil {
		return nil, err
	}
	if cur != nil {
		prev = sql.NullString{String: cur.Bucket, Valid: true}
	} else {
		err := tx.QueryRowContext(ctx, `SELECT to_bucket FROM transitions WHERE repo_id = ? AND number = ? ORDER BY id DESC LIMIT 1`,
			repoID, number).Scan(&prev)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("store: read last transition: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO results (repo_id, number, bucket, reason, action, waiting_since, also, notes, classified_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (repo_id, number) DO UPDATE SET bucket = excluded.bucket, reason = excluded.reason, action = excluded.action,
  waiting_since = excluded.waiting_since, also = excluded.also, notes = excluded.notes, classified_at = excluded.classified_at`,
		repoID, number, res.Bucket, res.Reason, res.Action, nullTime(res.WaitingSince), encodeList(res.Also), encodeList(res.Notes), ms(now))
	if err != nil {
		return nil, fmt.Errorf("store: write result: %w", err)
	}
	if prev.Valid && prev.String == res.Bucket {
		return nil, nil
	}
	tr := &Transition{RepoID: repoID, Number: number, At: now, To: res.Bucket, Reason: res.Reason}
	if prev.Valid {
		from := prev.String
		tr.From = &from
	}
	if err := insertTransition(ctx, tx, tr); err != nil {
		return nil, err
	}
	return tr, nil
}

func insertTransition(ctx context.Context, tx *sql.Tx, tr *Transition) error {
	var from sql.NullString
	if tr.From != nil {
		from = sql.NullString{String: *tr.From, Valid: true}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO transitions (repo_id, number, at, from_bucket, to_bucket, reason) VALUES (?, ?, ?, ?, ?, ?)`,
		tr.RepoID, tr.Number, ms(tr.At), from, tr.To, tr.Reason)
	if err != nil {
		return fmt.Errorf("store: insert transition: %w", err)
	}
	tr.ID, err = res.LastInsertId()
	return err
}

// Transitions returns one PR's transition history, oldest first.
func (db *DB) Transitions(ctx context.Context, repoID int64, number int) ([]Transition, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT id, at, from_bucket, to_bucket, reason FROM transitions WHERE repo_id = ? AND number = ? ORDER BY id`,
		repoID, number)
	if err != nil {
		return nil, fmt.Errorf("store: transitions: %w", err)
	}
	defer rows.Close()
	var out []Transition
	for rows.Next() {
		tr := Transition{RepoID: repoID, Number: number}
		var atMS int64
		var from sql.NullString
		if err := rows.Scan(&tr.ID, &atMS, &from, &tr.To, &tr.Reason); err != nil {
			return nil, err
		}
		tr.At = fromMS(atMS)
		if from.Valid {
			f := from.String
			tr.From = &f
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}
