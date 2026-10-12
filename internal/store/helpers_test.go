package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/rules"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "devbox.db"), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustAccount(t *testing.T, db *DB, login string) Account {
	t.Helper()
	a, err := db.UpsertAccount(context.Background(), login, "oauth", []string{"repo", "read:org"}, t0)
	if err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	return a
}

func mustRepo(t *testing.T, db *DB, a Account, owner, name string) Repo {
	t.Helper()
	r, err := db.AddRepo(context.Background(), a.ID, owner, name, rules.Default(), t0)
	if err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	return r
}
