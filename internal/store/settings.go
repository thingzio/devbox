package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SettingPolling holds the polling mode chosen in the UI ("manual" or a duration).
const SettingPolling = "poll_interval"

// Setting returns a saved UI setting and whether one is saved.
func (db *DB) Setting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := db.r.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: read setting %s: %w", key, err)
	}
	return v, true, nil
}

// SetSetting saves a UI setting.
func (db *DB) SetSetting(ctx context.Context, key, value string, now time.Time) error {
	_, err := db.w.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, ms(now))
	if err != nil {
		return fmt.Errorf("store: save setting %s: %w", key, err)
	}
	return nil
}

// DeleteSetting removes a saved UI setting, restoring its default; deleting an unset key is not an error.
func (db *DB) DeleteSetting(ctx context.Context, key string) error {
	if _, err := db.w.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: delete setting %s: %w", key, err)
	}
	return nil
}
