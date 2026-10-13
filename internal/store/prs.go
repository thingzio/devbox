package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/rules"
)

// PRState is what the poll planner needs about one stored PR.
type PRState struct {
	Number              int
	State               string
	Revision            int64
	Fingerprint         string
	HeadOID             string
	HasRecord           bool
	RecordVersion       int
	RulesHash           string
	RecordAt            *time.Time
	LastEnrichAttemptAt *time.Time
	RetryAt             *time.Time
	Error               string
}

// Observation is the planned write for one PR seen by a poll.
type Observation struct {
	Number           int
	Fingerprint      string
	HeadOID          string
	Exists           bool
	ExpectedRevision int64
	Invalidate       bool
}

// PollApply is one complete poll observation.
type PollApply struct {
	RepoID       int64
	RepoRevision int64
	Owner, Name  string
	Now          time.Time
	Upserts      []Observation
	Closes       []int
}

// PollApplied reports what ApplyPoll changed.
type PollApplied struct {
	Inserted []int
	Updated  []int
	Skipped  []int
	Closed   []Transition
}

// RepoSync is a repository's poll bookkeeping.
type RepoSync struct {
	RepoID              int64
	LastPollAt          *time.Time
	LastSuccessAt       *time.Time
	LastError           string
	ConsecutiveFailures int
}

// Work is one due enrichment job with everything its commit guards need.
type Work struct {
	RepoID         int64
	RepoRevision   int64
	Account        string
	Owner, Name    string
	Rules          rules.Rules
	Number         int
	PRRevision     int64
	Fingerprint    string
	HeadChangedAt  *time.Time
	RecordAt       *time.Time
	Record         []byte
	EnrichFailures int
}

// EnrichCommit is one accepted enrichment outcome.
type EnrichCommit struct {
	RepoID          int64
	RepoRevision    int64
	Number          int
	PRRevision      int64
	Now             time.Time
	Record          []byte
	AdvanceRecordAt bool
	Title           string
	URL             string
	Author          string
	Error           string
	EnrichFailures  int
	RetryAt         *time.Time
	Result          ResultRow
	Caches          Caches
}

// DetailObservation commits a detail read whose fingerprint differs from the queued one.
type DetailObservation struct {
	RepoID       int64
	RepoRevision int64
	Number       int
	PRRevision   int64
	Now          time.Time
	Fingerprint  string
	HeadOID      string
}

// Classifiable is a stored open record the reclassify pass can re-run.
type Classifiable struct {
	RepoID       int64
	RepoRevision int64
	Account      string
	Rules        rules.Rules
	Number       int
	PRRevision   int64
	Record       []byte
}

// PRStates lists every stored PR of a repository.
func (db *DB) PRStates(ctx context.Context, repoID int64) ([]PRState, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT number, state, revision, COALESCE(fingerprint, ''), COALESCE(head_oid, ''),
  record IS NOT NULL, COALESCE(json_extract(record, '$.version'), 0), COALESCE(json_extract(record, '$.collection_rules_hash'), ''),
  record_at, last_enrich_attempt_at, retry_at, COALESCE(error, '')
FROM prs WHERE repo_id = ? ORDER BY number`, repoID)
	if err != nil {
		return nil, fmt.Errorf("store: pr states: %w", err)
	}
	defer rows.Close()
	var out []PRState
	for rows.Next() {
		var s PRState
		var recordAt, attemptAt, retryAt sql.NullInt64
		if err := rows.Scan(&s.Number, &s.State, &s.Revision, &s.Fingerprint, &s.HeadOID, &s.HasRecord, &s.RecordVersion,
			&s.RulesHash, &recordAt, &attemptAt, &retryAt, &s.Error); err != nil {
			return nil, err
		}
		s.RecordAt, s.LastEnrichAttemptAt, s.RetryAt = timePtr(recordAt), timePtr(attemptAt), timePtr(retryAt)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ApplyPoll persists a complete poll observation if the repo is still enabled at
// the captured revision. Per-PR updates are guarded by the expected revision.
func (db *DB) ApplyPoll(ctx context.Context, p PollApply) (PollApplied, error) {
	var out PollApplied
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if err := repoCurrent(ctx, tx, p.RepoID, p.RepoRevision); err != nil {
			return err
		}
		now := ms(p.Now)
		for _, o := range p.Upserts {
			if !o.Exists {
				res, err := tx.ExecContext(ctx, `INSERT INTO prs (repo_id, number, title, url, author, state, revision, fingerprint, head_oid, retry_at, first_seen_at)
VALUES (?, ?, '', ?, '', 'open', 1, ?, ?, ?, ?) ON CONFLICT (repo_id, number) DO NOTHING`,
					p.RepoID, o.Number, PullURL(p.Owner, p.Name, o.Number), o.Fingerprint, o.HeadOID, now, now)
				if err != nil {
					return fmt.Errorf("store: insert pr %d: %w", o.Number, err)
				}
				if affected(res) == 0 {
					out.Skipped = append(out.Skipped, o.Number)
				} else {
					out.Inserted = append(out.Inserted, o.Number)
				}
				continue
			}
			inc := 0
			if o.Invalidate {
				inc = 1
			}
			res, err := tx.ExecContext(ctx, `UPDATE prs SET
  head_changed_at = CASE WHEN head_oid IS NOT NULL AND head_oid <> ? THEN ? ELSE head_changed_at END,
  head_oid = ?, fingerprint = ?, state = 'open', closed_at = NULL,
  revision = revision + ?,
  retry_at = CASE WHEN ? = 1 THEN ? ELSE retry_at END
WHERE repo_id = ? AND number = ? AND revision = ?`,
				o.HeadOID, now, o.HeadOID, o.Fingerprint, inc, inc, now, p.RepoID, o.Number, o.ExpectedRevision)
			if err != nil {
				return fmt.Errorf("store: update pr %d: %w", o.Number, err)
			}
			if affected(res) == 0 {
				out.Skipped = append(out.Skipped, o.Number)
			} else {
				out.Updated = append(out.Updated, o.Number)
			}
		}
		for _, n := range p.Closes {
			tr, err := closePR(ctx, tx, p.RepoID, n, p.Now)
			if err != nil {
				return err
			}
			if tr != nil {
				out.Closed = append(out.Closed, *tr)
			}
		}
		return nil
	})
	return out, err
}

func closePR(ctx context.Context, tx *sql.Tx, repoID int64, number int, now time.Time) (*Transition, error) {
	res, err := tx.ExecContext(ctx, `UPDATE prs SET state = 'closed', closed_at = ?, revision = revision + 1, retry_at = NULL, enrich_failures = 0
WHERE repo_id = ? AND number = ? AND state = 'open'`, ms(now), repoID, number)
	if err != nil {
		return nil, fmt.Errorf("store: close pr %d: %w", number, err)
	}
	if affected(res) == 0 {
		return nil, nil
	}
	prev, err := readResult(ctx, tx, repoID, number)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM results WHERE repo_id = ? AND number = ?`, repoID, number); err != nil {
		return nil, fmt.Errorf("store: delete result: %w", err)
	}
	tr := &Transition{RepoID: repoID, Number: number, At: now, To: string(classify.Closed), Reason: "closed or merged"}
	if prev != nil {
		from := prev.Bucket
		tr.From = &from
	}
	if err := insertTransition(ctx, tx, tr); err != nil {
		return nil, err
	}
	return tr, nil
}

func repoCurrent(ctx context.Context, tx *sql.Tx, repoID, revision int64) error {
	var cur int64
	var enabled bool
	err := tx.QueryRowContext(ctx, `SELECT revision, enabled FROM repos WHERE id = ?`, repoID).Scan(&cur, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return fmt.Errorf("store: read repo revision: %w", err)
	}
	if cur != revision || !enabled {
		return ErrStale
	}
	return nil
}

// guardPR enforces the commit conditions inside the write transaction.
func guardPR(ctx context.Context, tx *sql.Tx, repoID, repoRevision int64, number int, prRevision int64) error {
	var rRev, pRev int64
	var enabled bool
	var state string
	err := tx.QueryRowContext(ctx, `SELECT r.revision, r.enabled, p.revision, p.state FROM prs p JOIN repos r ON r.id = p.repo_id
WHERE p.repo_id = ? AND p.number = ?`, repoID, number).Scan(&rRev, &enabled, &pRev, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return fmt.Errorf("store: read guard: %w", err)
	}
	if rRev != repoRevision || !enabled || pRev != prRevision || state != "open" {
		return ErrStale
	}
	return nil
}

// RecordPoll stores a poll's outcome and returns the updated bookkeeping.
func (db *DB) RecordPoll(ctx context.Context, repoID int64, now time.Time, pollErr error) (RepoSync, error) {
	var row *sql.Row
	if pollErr == nil {
		row = db.w.QueryRowContext(ctx, `UPDATE repo_sync SET last_poll_at = ?, last_success_at = ?, last_error = NULL, consecutive_failures = 0
WHERE repo_id = ? RETURNING last_poll_at, last_success_at, COALESCE(last_error, ''), consecutive_failures`, ms(now), ms(now), repoID)
	} else {
		row = db.w.QueryRowContext(ctx, `UPDATE repo_sync SET last_poll_at = ?, last_error = ?, consecutive_failures = consecutive_failures + 1
WHERE repo_id = ? RETURNING last_poll_at, last_success_at, COALESCE(last_error, ''), consecutive_failures`, ms(now), pollErr.Error(), repoID)
	}
	s := RepoSync{RepoID: repoID}
	var pollAt, successAt sql.NullInt64
	err := row.Scan(&pollAt, &successAt, &s.LastError, &s.ConsecutiveFailures)
	if errors.Is(err, sql.ErrNoRows) {
		return RepoSync{}, ErrNotFound
	}
	if err != nil {
		return RepoSync{}, fmt.Errorf("store: record poll: %w", err)
	}
	s.LastPollAt, s.LastSuccessAt = timePtr(pollAt), timePtr(successAt)
	return s, nil
}

// DueWork lists due open PRs in enabled repos of ok accounts, oldest first and
// at most perAccount per account, so one account's backlog cannot crowd out
// another's. Accounts in skip (paused for rate limits) are excluded.
func (db *DB) DueWork(ctx context.Context, now time.Time, perAccount int, skip []string) ([]Work, error) {
	args := []any{ms(now)}
	exclude := ""
	if len(skip) > 0 {
		exclude = " AND a.login NOT IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(skip)), ", ") + ")"
		for _, login := range skip {
			args = append(args, login)
		}
	}
	args = append(args, perAccount)
	query := `SELECT repo_id, repo_revision, login, owner, name, rules, number, pr_revision,
  fingerprint, head_changed_at, record_at, record, enrich_failures
FROM (
  SELECT p.repo_id, r.revision AS repo_revision, a.login, r.owner, r.name, r.rules, p.number, p.revision AS pr_revision,
    COALESCE(p.fingerprint, '') AS fingerprint, p.head_changed_at, p.record_at, p.record, p.enrich_failures, p.retry_at,
    ROW_NUMBER() OVER (PARTITION BY a.id ORDER BY p.retry_at, p.repo_id, p.number) AS rank_in_account
  FROM prs p JOIN repos r ON r.id = p.repo_id JOIN accounts a ON a.id = r.account_id
  WHERE p.state = 'open' AND r.enabled = 1 AND a.status = 'ok' AND p.retry_at IS NOT NULL AND p.retry_at <= ?` + exclude + `
)
WHERE rank_in_account <= ?
ORDER BY retry_at, repo_id, number`
	rows, err := db.r.QueryContext(ctx, query, args...) //nolint:gosec // only "?" placeholders are concatenated
	if err != nil {
		return nil, fmt.Errorf("store: due work: %w", err)
	}
	defer rows.Close()
	var out []Work
	for rows.Next() {
		var w Work
		var rawRules string
		var headChanged, recordAt sql.NullInt64
		var record sql.NullString
		if err := rows.Scan(&w.RepoID, &w.RepoRevision, &w.Account, &w.Owner, &w.Name, &rawRules, &w.Number, &w.PRRevision,
			&w.Fingerprint, &headChanged, &recordAt, &record, &w.EnrichFailures); err != nil {
			return nil, err
		}
		if w.Rules, err = rules.Parse([]byte(rawRules)); err != nil {
			return nil, fmt.Errorf("store: repo %d rules: %w", w.RepoID, err)
		}
		w.HeadChangedAt, w.RecordAt = timePtr(headChanged), timePtr(recordAt)
		if record.Valid {
			w.Record = []byte(record.String)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// CommitEnrichment writes an accepted enrichment outcome, its result, any
// transition, and staged caches atomically, or returns ErrStale.
func (db *DB) CommitEnrichment(ctx context.Context, c EnrichCommit) (*Transition, error) {
	var tr *Transition
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if err := guardPR(ctx, tx, c.RepoID, c.RepoRevision, c.Number, c.PRRevision); err != nil {
			return err
		}
		advance := 0
		if c.AdvanceRecordAt {
			advance = 1
		}
		now := ms(c.Now)
		_, err := tx.ExecContext(ctx, `UPDATE prs SET
  record = ?, record_at = CASE WHEN ? = 1 THEN ? ELSE record_at END,
  last_enrich_attempt_at = ?, error = NULLIF(?, ''), enrich_failures = ?, retry_at = ?,
  revision = revision + 1,
  title = CASE WHEN ? <> '' THEN ? ELSE title END,
  url = CASE WHEN ? <> '' THEN ? ELSE url END,
  author = CASE WHEN ? <> '' THEN ? ELSE author END
WHERE repo_id = ? AND number = ?`,
			string(c.Record), advance, now, now, c.Error, c.EnrichFailures, nullTime(c.RetryAt),
			c.Title, c.Title, c.URL, c.URL, c.Author, c.Author, c.RepoID, c.Number)
		if err != nil {
			return fmt.Errorf("store: write enrichment: %w", err)
		}
		if tr, err = writeResult(ctx, tx, c.RepoID, c.Number, c.Result, c.Now); err != nil {
			return err
		}
		return writeCaches(ctx, tx, c.RepoID, c.Caches, c.Now)
	})
	if err != nil {
		return nil, err
	}
	return tr, nil
}

// CommitObservation records a newer detail observation and invalidates the PR.
func (db *DB) CommitObservation(ctx context.Context, o DetailObservation) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		if err := guardPR(ctx, tx, o.RepoID, o.RepoRevision, o.Number, o.PRRevision); err != nil {
			return err
		}
		now := ms(o.Now)
		_, err := tx.ExecContext(ctx, `UPDATE prs SET
  head_changed_at = CASE WHEN head_oid IS NOT NULL AND head_oid <> ? THEN ? ELSE head_changed_at END,
  head_oid = ?, fingerprint = ?, revision = revision + 1, retry_at = ?
WHERE repo_id = ? AND number = ?`, o.HeadOID, now, o.HeadOID, o.Fingerprint, now, o.RepoID, o.Number)
		if err != nil {
			return fmt.Errorf("store: commit observation: %w", err)
		}
		return nil
	})
}

// Reclassifiable lists open PRs with records in enabled repos (repoID 0 = all).
func (db *DB) Reclassifiable(ctx context.Context, repoID int64) ([]Classifiable, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT p.repo_id, r.revision, a.login, r.rules, p.number, p.revision, p.record
FROM prs p JOIN repos r ON r.id = p.repo_id JOIN accounts a ON a.id = r.account_id
WHERE p.state = 'open' AND r.enabled = 1 AND p.record IS NOT NULL AND (? = 0 OR p.repo_id = ?)
ORDER BY p.repo_id, p.number`, repoID, repoID)
	if err != nil {
		return nil, fmt.Errorf("store: reclassifiable: %w", err)
	}
	defer rows.Close()
	var out []Classifiable
	for rows.Next() {
		var c Classifiable
		var rawRules, record string
		if err := rows.Scan(&c.RepoID, &c.RepoRevision, &c.Account, &rawRules, &c.Number, &c.PRRevision, &record); err != nil {
			return nil, err
		}
		if c.Rules, err = rules.Parse([]byte(rawRules)); err != nil {
			return nil, fmt.Errorf("store: repo %d rules: %w", c.RepoID, err)
		}
		c.Record = []byte(record)
		out = append(out, c)
	}
	return out, rows.Err()
}

// CommitResult stores a reclassified result without advancing the evidence revision.
// It reports whether anything changed.
func (db *DB) CommitResult(ctx context.Context, repoID, repoRev int64, number int, prRev int64, res ResultRow, now time.Time) (*Transition, bool, error) {
	var tr *Transition
	changed := false
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if err := guardPR(ctx, tx, repoID, repoRev, number, prRev); err != nil {
			return err
		}
		cur, err := readResult(ctx, tx, repoID, number)
		if err != nil {
			return err
		}
		if cur != nil && sameResult(*cur, res) {
			return nil
		}
		changed = true
		tr, err = writeResult(ctx, tx, repoID, number, res, now)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return tr, changed, nil
}
