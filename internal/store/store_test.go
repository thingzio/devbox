package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thingzio/devbox/internal/rules"
)

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "nested", "devbox.db")
	db, err := Open(ctx, file, nil)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = db.Close()
	db, err = Open(ctx, file, nil)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema_migrations rows = %d, err = %v", n, err)
	}
	var fk int
	if err := db.w.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, err = %v", fk, err)
	}
}

func TestOpenRejectsQueryCharacters(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "bad?.db"), nil); err == nil {
		t.Fatal("path with '?' accepted")
	}
}

func TestAccounts(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "zed")
	if a.Status != StatusOK || !slices.Equal(a.Scopes, []string{"repo", "read:org"}) || a.VerifiedAt == nil {
		t.Fatalf("account = %+v", a)
	}
	if err := db.SetAccountStatus(ctx, "zed", StatusNeedsAuth, "token rejected"); err != nil {
		t.Fatalf("SetAccountStatus: %v", err)
	}
	got, _ := db.Account(ctx, "zed")
	if got.Status != StatusNeedsAuth || got.StatusDetail != "token rejected" {
		t.Fatalf("after status change: %+v", got)
	}
	again, err := db.UpsertAccount(ctx, "zed", "pat", []string{"repo"}, at(5))
	if err != nil || again.ID != a.ID || again.Status != StatusOK || again.StatusDetail != "" || again.Kind != "pat" {
		t.Fatalf("re-upsert = %+v, %v", again, err)
	}
	mustAccount(t, db, "amy")
	all, _ := db.Accounts(ctx)
	if len(all) != 2 || all[0].Login != "amy" {
		t.Fatalf("Accounts = %+v", all)
	}
	if _, err := db.Account(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account err = %v", err)
	}
	if err := db.SetAccountStatus(ctx, "nobody", StatusOK, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing status err = %v", err)
	}
}

func TestAddRepo(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	r := mustRepo(t, db, a, "NVIDIA", "aicr")
	if r.Account != "me" || r.Revision != 0 || !r.Enabled || r.Rules.StaleDays != rules.DefaultStaleDays || r.FullName() != "NVIDIA/aicr" {
		t.Fatalf("repo = %+v", r)
	}
	if _, err := db.AddRepo(ctx, a.ID, "nvidia", "AICR", rules.Default(), t0); !errors.Is(err, ErrExists) {
		t.Fatalf("case-insensitive duplicate err = %v", err)
	}
	b := mustAccount(t, db, "work")
	if _, err := db.AddRepo(ctx, b.ID, "NVIDIA", "aicr", rules.Default(), t0); err != nil {
		t.Fatalf("same repo under another account: %v", err)
	}
	byName, _ := db.ReposByName(ctx, "nvidia", "aicr")
	if len(byName) != 2 {
		t.Fatalf("ReposByName = %+v", byName)
	}
	if PullURL("NVIDIA", "aicr", 12) != "https://github.com/NVIDIA/aicr/pull/12" {
		t.Fatalf("PullURL = %s", PullURL("NVIDIA", "aicr", 12))
	}
}

func TestRepoIDsNeverReused(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	first := mustRepo(t, db, a, "o", "r")
	if err := db.DeleteRepo(ctx, first.ID); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	second := mustRepo(t, db, a, "o", "r")
	if second.ID <= first.ID {
		t.Fatalf("repo id reused: %d then %d", first.ID, second.ID)
	}
	if err := db.DeleteRepo(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
}

func TestSetRepoEnabledBumpsRevision(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	off, err := db.SetRepoEnabled(ctx, r.ID, false)
	if err != nil || off.Enabled || off.Revision != r.Revision+1 {
		t.Fatalf("disable = %+v, %v", off, err)
	}
	on, _ := db.SetRepoEnabled(ctx, r.ID, true)
	if !on.Enabled || on.Revision != r.Revision+2 {
		t.Fatalf("enable = %+v", on)
	}
}

func TestDeleteAccountCascades(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	mustRepo(t, db, a, "o", "r")
	if err := db.DeleteAccount(ctx, "me"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if repos, _ := db.Repos(ctx); len(repos) != 0 {
		t.Fatalf("repos survived account deletion: %+v", repos)
	}
}

func TestUpdateRepoRulesWithoutPRs(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	staleOnly := rules.Default()
	staleOnly.StaleDays = 14
	upd, err := db.UpdateRepoRules(ctx, r.ID, staleOnly, at(1))
	if err != nil || upd.CollectionChanged || upd.Repo.Revision != 1 || upd.Repo.Rules.StaleDays != 14 {
		t.Fatalf("stale-only update = %+v, %v", upd, err)
	}
	collect := staleOnly
	collect.TestApproval = rules.TestApproval{MarkerChecks: []string{"tests"}, Command: "/ok-to-test"}
	upd, err = db.UpdateRepoRules(ctx, r.ID, collect, at(2))
	if err != nil || !upd.CollectionChanged || upd.Repo.Revision != 2 {
		t.Fatalf("collection update = %+v, %v", upd, err)
	}
	if _, err := db.UpdateRepoRules(ctx, 999, collect, at(3)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing repo err = %v", err)
	}
}
