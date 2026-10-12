package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Account statuses.
const (
	StatusOK        = "ok"
	StatusNeedsAuth = "needs_auth"
)

// Account is a GitHub identity whose token lives in the OS keychain.
type Account struct {
	ID           int64
	Login        string
	Kind         string
	Scopes       []string
	Status       string
	StatusDetail string
	CreatedAt    time.Time
	VerifiedAt   *time.Time
}

const accountCols = `id, login, kind, scopes, status, COALESCE(status_detail, ''), created_at, verified_at`

func scanAccount(sc scanner) (Account, error) {
	var a Account
	var scopes string
	var created int64
	var verified sql.NullInt64
	if err := sc.Scan(&a.ID, &a.Login, &a.Kind, &scopes, &a.Status, &a.StatusDetail, &created, &verified); err != nil {
		return Account{}, err
	}
	for _, s := range strings.Split(scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			a.Scopes = append(a.Scopes, s)
		}
	}
	a.CreatedAt = fromMS(created)
	a.VerifiedAt = timePtr(verified)
	return a, nil
}

// UpsertAccount records a verified account; re-verification restores status ok.
func (db *DB) UpsertAccount(ctx context.Context, login, kind string, scopes []string, now time.Time) (Account, error) {
	row := db.w.QueryRowContext(ctx, `INSERT INTO accounts (login, kind, scopes, status, created_at, verified_at)
VALUES (?, ?, ?, 'ok', ?, ?)
ON CONFLICT (login) DO UPDATE SET kind = excluded.kind, scopes = excluded.scopes, status = 'ok',
  status_detail = NULL, verified_at = excluded.verified_at
RETURNING `+accountCols, login, kind, strings.Join(scopes, ","), ms(now), ms(now))
	a, err := scanAccount(row)
	if err != nil {
		return Account{}, fmt.Errorf("store: upsert account %s: %w", login, err)
	}
	return a, nil
}

// SetAccountStatus marks an account ok or needs_auth with a detail message.
func (db *DB) SetAccountStatus(ctx context.Context, login, status, detail string) error {
	res, err := db.w.ExecContext(ctx, `UPDATE accounts SET status = ?, status_detail = NULLIF(?, '') WHERE login = ?`, status, detail, login)
	if err != nil {
		return fmt.Errorf("store: set account status: %w", err)
	}
	if affected(res) == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAccount removes an account and, by cascade, its repositories and their data.
func (db *DB) DeleteAccount(ctx context.Context, login string) error {
	res, err := db.w.ExecContext(ctx, `DELETE FROM accounts WHERE login = ?`, login)
	if err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	if affected(res) == 0 {
		return ErrNotFound
	}
	return nil
}

// Account returns one account by login.
func (db *DB) Account(ctx context.Context, login string) (Account, error) {
	a, err := scanAccount(db.r.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE login = ?`, login))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("store: account %s: %w", login, err)
	}
	return a, nil
}

// Accounts lists accounts ordered by login.
func (db *DB) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT `+accountCols+` FROM accounts ORDER BY login`)
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
