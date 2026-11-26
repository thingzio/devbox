package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func queueRow(t *testing.T, db *DB, number int) QueueRow {
	t.Helper()
	rows, err := db.Queue(context.Background(), QueueFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Number == number {
			return r
		}
	}
	t.Fatalf("pr %d not in queue", number)
	return QueueRow{}
}

func TestSnoozeWakesOnTimeBucketOrPush(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	commitBucket(t, db, r, 7, "needs_initial_review", at(1))
	day := at(1).Add(24 * time.Hour)

	if err := db.Snooze(ctx, r.ID, 7, &day, at(1)); err != nil {
		t.Fatal(err)
	}
	row := queueRow(t, db, 7)
	if row.Snooze == nil || !row.Snoozed(at(2)) {
		t.Fatalf("not snoozed: %+v", row.Snooze)
	}
	if row.Snoozed(day) {
		t.Fatal("still snoozed when the time passed")
	}

	if err := db.Snooze(ctx, r.ID, 7, nil, at(1)); err != nil {
		t.Fatal(err)
	}
	if row := queueRow(t, db, 7); !row.Snoozed(at(1).Add(365 * 24 * time.Hour)) {
		t.Fatal("'until it changes' expired with time")
	}
	commitBucket(t, db, r, 7, "waiting_on_me", at(3))
	if queueRow(t, db, 7).Snoozed(at(4)) {
		t.Fatal("still snoozed after the bucket changed")
	}

	if err := db.Snooze(ctx, r.ID, 7, nil, at(5)); err != nil {
		t.Fatal(err)
	}
	s := prState(t, db, r.ID, 7)
	if _, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Owner: "o", Name: "r", Now: at(6),
		Upserts: []Observation{{Number: 7, Fingerprint: "fp2", HeadOID: "head2", Exists: true, ExpectedRevision: s.Revision, Invalidate: true}}}); err != nil {
		t.Fatal(err)
	}
	if queueRow(t, db, 7).Snoozed(at(7)) {
		t.Fatal("still snoozed after a push")
	}
}

func TestUnsnoozeAndValidation(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7, 8)
	commitBucket(t, db, r, 7, "needs_initial_review", at(1))
	if err := db.Snooze(ctx, r.ID, 8, nil, at(1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snoozing an unclassified PR err = %v", err)
	}
	if err := db.Snooze(ctx, r.ID, 99, nil, at(1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snoozing a missing PR err = %v", err)
	}
	if err := db.Snooze(ctx, r.ID, 7, nil, at(1)); err != nil {
		t.Fatal(err)
	}
	if err := db.Unsnooze(ctx, r.ID, 7); err != nil {
		t.Fatal(err)
	}
	if row := queueRow(t, db, 7); row.Snooze != nil {
		t.Fatalf("snooze survived Unsnooze: %+v", row.Snooze)
	}
	if err := db.Unsnooze(ctx, r.ID, 7); err != nil {
		t.Fatalf("unsnoozing twice err = %v", err)
	}
}

func TestPurgeDropsEndedSnoozes(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7, 8, 9)
	for _, n := range []int{7, 8, 9} {
		commitBucket(t, db, r, n, "needs_initial_review", at(1))
	}
	soon, later := at(10), at(1).Add(48*time.Hour)
	for n, until := range map[int]*time.Time{7: &soon, 8: &later, 9: nil} {
		if err := db.Snooze(ctx, r.ID, n, until, at(1)); err != nil {
			t.Fatal(err)
		}
	}
	commitBucket(t, db, r, 9, "waiting_on_me", at(2))
	if _, err := db.Purge(ctx, at(20)); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[int]bool{7: false, 8: true, 9: false} {
		if got := queueRow(t, db, n).Snooze != nil; got != want {
			t.Errorf("pr %d snooze kept = %v, want %v", n, got, want)
		}
	}
}

func TestPRSummary(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	commitBucket(t, db, r, 7, "waiting_on_me", at(1))
	if repo, title, err := db.PRSummary(ctx, r.ID, 7); err != nil || repo != "o/r" || title != "Fix it" {
		t.Fatalf("PRSummary = %q %q %v", repo, title, err)
	}
	if _, _, err := db.PRSummary(ctx, r.ID, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing PR err = %v", err)
	}
	if _, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Owner: "o", Name: "r", Now: at(2), Closes: []int{7}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PRSummary(ctx, r.ID, 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed PR err = %v", err)
	}
}
