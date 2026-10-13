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

// Repo is a monitored repository owned by one account.
type Repo struct {
	ID        int64
	AccountID int64
	Revision  int64
	Account   string
	Owner     string
	Name      string
	Enabled   bool
	Rules     rules.Rules
	AddedAt   time.Time
}

// FullName is owner/name.
func (r Repo) FullName() string { return r.Owner + "/" + r.Name }

// PullURL is the canonical GitHub URL of a pull request.
func PullURL(owner, name string, number int) string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, name, number)
}

// RulesUpdate describes the effect of a rules edit.
type RulesUpdate struct {
	Repo              Repo
	CollectionChanged bool
	Transitions       []Transition
}

const repoSelect = `SELECT r.id, r.account_id, r.revision, a.login, r.owner, r.name, r.enabled, r.rules, r.added_at
FROM repos r JOIN accounts a ON a.id = r.account_id`

func scanRepo(sc scanner) (Repo, error) {
	var r Repo
	var raw string
	var added int64
	if err := sc.Scan(&r.ID, &r.AccountID, &r.Revision, &r.Account, &r.Owner, &r.Name, &r.Enabled, &raw, &added); err != nil {
		return Repo{}, err
	}
	rl, err := rules.Parse([]byte(raw))
	if err != nil {
		return Repo{}, fmt.Errorf("store: repo %d rules: %w", r.ID, err)
	}
	r.Rules = rl
	r.AddedAt = fromMS(added)
	return r, nil
}

// AddRepo starts monitoring owner/name for an account.
func (db *DB) AddRepo(ctx context.Context, accountID int64, owner, name string, rl rules.Rules, now time.Time) (Repo, error) {
	var id int64
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO repos (account_id, owner, name, rules, added_at) VALUES (?, ?, ?, ?, ?)`,
			accountID, owner, name, rl.JSON(), ms(now))
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return ErrExists
			}
			return fmt.Errorf("store: add repo: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO repo_sync (repo_id) VALUES (?)`, id)
		return err
	})
	if err != nil {
		return Repo{}, err
	}
	return db.Repo(ctx, id)
}

// Repo returns one repository.
func (db *DB) Repo(ctx context.Context, id int64) (Repo, error) {
	r, err := scanRepo(db.r.QueryRowContext(ctx, repoSelect+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, ErrNotFound
	}
	return r, err
}

// Repos lists all repositories ordered by name and account.
func (db *DB) Repos(ctx context.Context) ([]Repo, error) {
	return db.queryRepos(ctx, repoSelect+` ORDER BY r.owner, r.name, a.login`)
}

// ReposByName lists every account's entry for owner/name (case-insensitive).
func (db *DB) ReposByName(ctx context.Context, owner, name string) ([]Repo, error) {
	return db.queryRepos(ctx, repoSelect+` WHERE r.owner = ? AND r.name = ? ORDER BY a.login`, owner, name)
}

func (db *DB) queryRepos(ctx context.Context, query string, args ...any) ([]Repo, error) {
	rows, err := db.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list repos: %w", err)
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRepoEnabled toggles monitoring and advances the repo revision.
func (db *DB) SetRepoEnabled(ctx context.Context, id int64, enabled bool) (Repo, error) {
	res, err := db.w.ExecContext(ctx, `UPDATE repos SET enabled = ?, revision = revision + 1 WHERE id = ?`, enabled, id)
	if err != nil {
		return Repo{}, fmt.Errorf("store: set repo enabled: %w", err)
	}
	if affected(res) == 0 {
		return Repo{}, ErrNotFound
	}
	return db.Repo(ctx, id)
}

// DeleteRepo removes a repository and, by cascade, its PRs, results, transitions, and caches.
func (db *DB) DeleteRepo(ctx context.Context, id int64) error {
	res, err := db.w.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete repo: %w", err)
	}
	if affected(res) == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateRepoRules stores new rules and advances the repo revision. When the
// collection settings change, every open PR is invalidated and its result is
// replaced by an unclassified refresh placeholder in the same transaction.
func (db *DB) UpdateRepoRules(ctx context.Context, id int64, rl rules.Rules, now time.Time) (RulesUpdate, error) {
	var upd RulesUpdate
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT rules FROM repos WHERE id = ?`, id).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("store: read rules: %w", err)
		}
		old, err := rules.Parse([]byte(raw))
		if err != nil {
			return fmt.Errorf("store: stored rules: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE repos SET rules = ?, revision = revision + 1 WHERE id = ?`, rl.JSON(), id); err != nil {
			return fmt.Errorf("store: update rules: %w", err)
		}
		upd.CollectionChanged = old.CollectionHash() != rl.CollectionHash()
		if !upd.CollectionChanged {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE prs SET revision = revision + 1, retry_at = ? WHERE repo_id = ? AND state = 'open'`, ms(now), id); err != nil {
			return fmt.Errorf("store: invalidate prs: %w", err)
		}
		numbers, err := openResultNumbers(ctx, tx, id)
		if err != nil {
			return err
		}
		refresh := ResultRow{Bucket: string(classify.Unclassified), Reason: "collection rules changed; refreshing evidence"}
		for _, n := range numbers {
			tr, err := writeResult(ctx, tx, id, n, refresh, now)
			if err != nil {
				return err
			}
			if tr != nil {
				upd.Transitions = append(upd.Transitions, *tr)
			}
		}
		return nil
	})
	if err != nil {
		return RulesUpdate{}, err
	}
	upd.Repo, err = db.Repo(ctx, id)
	return upd, err
}

func openResultNumbers(ctx context.Context, tx *sql.Tx, repoID int64) ([]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.number FROM results r JOIN prs p ON p.repo_id = r.repo_id AND p.number = r.number
WHERE r.repo_id = ? AND p.state = 'open' ORDER BY r.number`, repoID)
	if err != nil {
		return nil, fmt.Errorf("store: list results: %w", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
