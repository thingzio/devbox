package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// QueueFilter narrows the queue; empty fields match everything.
type QueueFilter struct {
	Owner   string
	Name    string
	Account string
	Bucket  string
}

// QueueRow is one classified open PR in an enabled repository.
type QueueRow struct {
	RepoID  int64
	Owner   string
	Name    string
	Account string
	Number  int
	Title   string
	URL     string
	Author  string
	Result  ResultRow
	Record  []byte
}

// Queue lists classified open PRs of enabled repositories.
func (db *DB) Queue(ctx context.Context, f QueueFilter) ([]QueueRow, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT r.id, r.owner, r.name, a.login, p.number, p.title, p.url, p.author,
  res.bucket, res.reason, res.action, res.waiting_since, res.also, res.notes, COALESCE(p.record, '')
FROM results res
JOIN prs p ON p.repo_id = res.repo_id AND p.number = res.number
JOIN repos r ON r.id = p.repo_id
JOIN accounts a ON a.id = r.account_id
WHERE r.enabled = 1 AND p.state = 'open'
  AND (? = '' OR r.owner = ?) AND (? = '' OR r.name = ?) AND (? = '' OR a.login = ?) AND (? = '' OR res.bucket = ?)
ORDER BY r.owner, r.name, p.number, a.login`,
		f.Owner, f.Owner, f.Name, f.Name, f.Account, f.Account, f.Bucket, f.Bucket)
	if err != nil {
		return nil, fmt.Errorf("store: queue: %w", err)
	}
	defer rows.Close()
	var out []QueueRow
	for rows.Next() {
		var q QueueRow
		var since sql.NullInt64
		var also, notes, record string
		if err := rows.Scan(&q.RepoID, &q.Owner, &q.Name, &q.Account, &q.Number, &q.Title, &q.URL, &q.Author,
			&q.Result.Bucket, &q.Result.Reason, &q.Result.Action, &since, &also, &notes, &record); err != nil {
			return nil, err
		}
		q.Result.WaitingSince = timePtr(since)
		q.Result.Also, q.Result.Notes = decodeList(also), decodeList(notes)
		q.Record = []byte(record)
		out = append(out, q)
	}
	return out, rows.Err()
}

// PRDetail is everything the explain view shows about one PR entry.
type PRDetail struct {
	Repo                Repo
	Number              int
	Title, URL, Author  string
	State               string
	Revision            int64
	Fingerprint         string
	HeadOID             string
	HeadChangedAt       *time.Time
	RecordAt            *time.Time
	LastEnrichAttemptAt *time.Time
	RetryAt             *time.Time
	ClosedAt            *time.Time
	FirstSeenAt         time.Time
	Record              []byte
	EnrichFailures      int
	Error               string
	Result              *ResultRow
}

// PRDetails returns the PR entry under every account monitoring owner/name.
func (db *DB) PRDetails(ctx context.Context, owner, name string, number int) ([]PRDetail, error) {
	repos, err := db.ReposByName(ctx, owner, name)
	if err != nil {
		return nil, err
	}
	var out []PRDetail
	for _, repo := range repos {
		d := PRDetail{Repo: repo, Number: number}
		var headChanged, recordAt, attemptAt, retryAt, closedAt sql.NullInt64
		var firstSeen int64
		var record sql.NullString
		err := db.r.QueryRowContext(ctx, `SELECT title, url, author, state, revision, COALESCE(fingerprint, ''), COALESCE(head_oid, ''),
  head_changed_at, record_at, last_enrich_attempt_at, retry_at, closed_at, first_seen_at, record, enrich_failures, COALESCE(error, '')
FROM prs WHERE repo_id = ? AND number = ?`, repo.ID, number).Scan(&d.Title, &d.URL, &d.Author, &d.State, &d.Revision,
			&d.Fingerprint, &d.HeadOID, &headChanged, &recordAt, &attemptAt, &retryAt, &closedAt, &firstSeen, &record, &d.EnrichFailures, &d.Error)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("store: pr detail: %w", err)
		}
		d.HeadChangedAt, d.RecordAt, d.LastEnrichAttemptAt = timePtr(headChanged), timePtr(recordAt), timePtr(attemptAt)
		d.RetryAt, d.ClosedAt, d.FirstSeenAt = timePtr(retryAt), timePtr(closedAt), fromMS(firstSeen)
		if record.Valid {
			d.Record = []byte(record.String)
		}
		if d.Result, err = readResult(ctx, db.r, repo.ID, number); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// RepoStatus is the header-bar health of one repository (§9).
type RepoStatus struct {
	Repo          Repo
	Sync          RepoSync
	AccountStatus string
	AccountDetail string
	Open          int
	Pending       int
	Errored       int
	Retrying      int
	NextRetryAt   *time.Time
	LastAttemptAt *time.Time
}

// RepoStatuses reports poll and enrichment health for every repository.
func (db *DB) RepoStatuses(ctx context.Context) ([]RepoStatus, error) {
	repos, err := db.Repos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RepoStatus, 0, len(repos))
	for _, repo := range repos {
		st := RepoStatus{Repo: repo, Sync: RepoSync{RepoID: repo.ID}}
		var pollAt, successAt, nextRetry, lastAttempt sql.NullInt64
		err := db.r.QueryRowContext(ctx, `SELECT s.last_poll_at, s.last_success_at, COALESCE(s.last_error, ''), COALESCE(s.consecutive_failures, 0),
  a.status, COALESCE(a.status_detail, ''),
  (SELECT COUNT(*) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open'),
  (SELECT COUNT(*) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open' AND p.retry_at IS NOT NULL AND p.enrich_failures = 0),
  (SELECT COUNT(*) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open' AND p.error IS NOT NULL),
  (SELECT COUNT(*) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open' AND p.retry_at IS NOT NULL AND p.enrich_failures > 0),
  (SELECT MIN(p.retry_at) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open'),
  (SELECT MAX(p.last_enrich_attempt_at) FROM prs p WHERE p.repo_id = r.id AND p.state = 'open')
FROM repos r JOIN accounts a ON a.id = r.account_id LEFT JOIN repo_sync s ON s.repo_id = r.id
WHERE r.id = ?`, repo.ID).Scan(&pollAt, &successAt, &st.Sync.LastError, &st.Sync.ConsecutiveFailures,
			&st.AccountStatus, &st.AccountDetail, &st.Open, &st.Pending, &st.Errored, &st.Retrying, &nextRetry, &lastAttempt)
		if err != nil {
			return nil, fmt.Errorf("store: repo status %d: %w", repo.ID, err)
		}
		st.Sync.LastPollAt, st.Sync.LastSuccessAt = timePtr(pollAt), timePtr(successAt)
		st.NextRetryAt, st.LastAttemptAt = timePtr(nextRetry), timePtr(lastAttempt)
		out = append(out, st)
	}
	return out, nil
}

// BucketCount is the number of open PRs of one enabled repository in one bucket.
type BucketCount struct {
	Repo   string
	Bucket string
	Count  int
}

// BucketCounts feeds the devbox_prs gauge; disabled repos are excluded (§7).
func (db *DB) BucketCounts(ctx context.Context) ([]BucketCount, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT r.owner || '/' || r.name, res.bucket, COUNT(*)
FROM results res JOIN prs p ON p.repo_id = res.repo_id AND p.number = res.number JOIN repos r ON r.id = p.repo_id
WHERE r.enabled = 1 AND p.state = 'open' GROUP BY 1, 2 ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("store: bucket counts: %w", err)
	}
	defer rows.Close()
	var out []BucketCount
	for rows.Next() {
		var c BucketCount
		if err := rows.Scan(&c.Repo, &c.Bucket, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
