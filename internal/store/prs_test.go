package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/rules"
)

func observeNew(t *testing.T, db *DB, r Repo, now time.Time, numbers ...int) {
	t.Helper()
	var ups []Observation
	for _, n := range numbers {
		ups = append(ups, Observation{Number: n, Fingerprint: "fp1", HeadOID: "head1"})
	}
	if _, err := db.ApplyPoll(context.Background(), PollApply{RepoID: r.ID, RepoRevision: r.Revision, Owner: r.Owner, Name: r.Name, Now: now, Upserts: ups}); err != nil {
		t.Fatalf("ApplyPoll: %v", err)
	}
}

func prState(t *testing.T, db *DB, repoID int64, number int) PRState {
	t.Helper()
	states, err := db.PRStates(context.Background(), repoID)
	if err != nil {
		t.Fatalf("PRStates: %v", err)
	}
	for _, s := range states {
		if s.Number == number {
			return s
		}
	}
	t.Fatalf("pr %d not found", number)
	return PRState{}
}

func commitBucket(t *testing.T, db *DB, r Repo, number int, bucket string, now time.Time) *Transition {
	t.Helper()
	s := prState(t, db, r.ID, number)
	tr, err := db.CommitEnrichment(context.Background(), EnrichCommit{
		RepoID: r.ID, RepoRevision: r.Revision, Number: number, PRRevision: s.Revision, Now: now,
		Record: []byte(`{"version":1,"collection_rules_hash":"h"}`), AdvanceRecordAt: true,
		Title: "Fix it", Author: "alice", Result: ResultRow{Bucket: bucket, Reason: "because", Action: "act"},
	})
	if err != nil {
		t.Fatalf("CommitEnrichment: %v", err)
	}
	return tr
}

func TestApplyPollInsertsAndTracksHeadChanges(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	s := prState(t, db, r.ID, 7)
	if s.State != "open" || s.Revision != 1 || s.RetryAt == nil || !s.RetryAt.Equal(at(0)) || s.HasRecord {
		t.Fatalf("new pr state = %+v", s)
	}
	details, _ := db.PRDetails(ctx, "o", "r", 7)
	if details[0].URL != "https://github.com/o/r/pull/7" || details[0].HeadChangedAt != nil {
		t.Fatalf("new pr detail = %+v", details[0])
	}
	_, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Owner: "o", Name: "r", Now: at(2),
		Upserts: []Observation{{Number: 7, Fingerprint: "fp2", HeadOID: "head2", Exists: true, ExpectedRevision: 1, Invalidate: true}}})
	if err != nil {
		t.Fatalf("ApplyPoll: %v", err)
	}
	s = prState(t, db, r.ID, 7)
	details, _ = db.PRDetails(ctx, "o", "r", 7)
	if s.Revision != 2 || s.Fingerprint != "fp2" || s.HeadOID != "head2" || details[0].HeadChangedAt == nil || !details[0].HeadChangedAt.Equal(at(2)) {
		t.Fatalf("after head change: state %+v detail %+v", s, details[0])
	}
	_, err = db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Owner: "o", Name: "r", Now: at(4),
		Upserts: []Observation{{Number: 7, Fingerprint: "fp3", HeadOID: "head2", Exists: true, ExpectedRevision: 2}}})
	if err != nil {
		t.Fatalf("ApplyPoll: %v", err)
	}
	s = prState(t, db, r.ID, 7)
	if s.Revision != 2 || !s.RetryAt.Equal(at(2)) || s.Fingerprint != "fp3" {
		t.Fatalf("observation without invalidation changed revision or retry: %+v", s)
	}
}

func TestApplyPollGuards(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	if _, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision + 1, Now: at(1)}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale repo revision err = %v", err)
	}
	applied, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Now: at(1),
		Upserts: []Observation{{Number: 7, Fingerprint: "x", HeadOID: "y", Exists: true, ExpectedRevision: 99, Invalidate: true}}})
	if err != nil || len(applied.Skipped) != 1 {
		t.Fatalf("pr revision mismatch applied = %+v, %v", applied, err)
	}
	off, _ := db.SetRepoEnabled(ctx, r.ID, false)
	if _, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: off.Revision, Now: at(1)}); !errors.Is(err, ErrStale) {
		t.Fatalf("disabled repo err = %v", err)
	}
}

func TestCloseAndReopen(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	if tr := commitBucket(t, db, r, 7, "needs_initial_review", at(1)); tr == nil || tr.From != nil || tr.To != "needs_initial_review" {
		t.Fatalf("first classification transition = %+v", tr)
	}
	applied, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Now: at(2), Closes: []int{7}})
	if err != nil || len(applied.Closed) != 1 || *applied.Closed[0].From != "needs_initial_review" || applied.Closed[0].To != "closed" {
		t.Fatalf("close applied = %+v, %v", applied, err)
	}
	if rows, _ := db.Queue(ctx, QueueFilter{}); len(rows) != 0 {
		t.Fatalf("closed pr still queued: %+v", rows)
	}
	s := prState(t, db, r.ID, 7)
	if s.State != "closed" || s.RetryAt != nil {
		t.Fatalf("closed state = %+v", s)
	}
	_, err = db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Now: at(3),
		Upserts: []Observation{{Number: 7, Fingerprint: "fp9", HeadOID: "head1", Exists: true, ExpectedRevision: s.Revision, Invalidate: true}}})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if tr := commitBucket(t, db, r, 7, "waiting_on_me", at(4)); tr == nil || tr.From == nil || *tr.From != "closed" {
		t.Fatalf("reopen transition = %+v", tr)
	}
}

func TestCommitEnrichmentGuards(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	commitBucket(t, db, r, 7, "needs_initial_review", at(1))
	if tr := commitBucket(t, db, r, 7, "needs_initial_review", at(2)); tr != nil {
		t.Fatalf("same bucket wrote transition %+v", tr)
	}
	s := prState(t, db, r.ID, 7)
	if s.Revision != 3 || s.RetryAt != nil || s.RecordAt == nil || !s.RecordAt.Equal(at(2)) || s.RecordVersion != 1 || s.RulesHash != "h" {
		t.Fatalf("after commits = %+v", s)
	}
	_, err := db.CommitEnrichment(ctx, EnrichCommit{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: s.Revision - 1,
		Now: at(3), Record: []byte(`{}`), Result: ResultRow{Bucket: "own"},
		Caches: Caches{Policies: []PolicyCache{{BaseRef: "main", Policy: "required", FetchedAt: at(3)}}}})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("stale pr revision err = %v", err)
	}
	if _, ok, _ := db.CachedPolicy(ctx, r.ID, "main", at(3), time.Hour); ok {
		t.Fatal("discarded outcome wrote a cache entry")
	}
	if _, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Now: at(4), Closes: []int{7}}); err != nil {
		t.Fatal(err)
	}
	closed := prState(t, db, r.ID, 7)
	_, err = db.CommitEnrichment(ctx, EnrichCommit{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: closed.Revision,
		Now: at(5), Record: []byte(`{}`), Result: ResultRow{Bucket: "own"}})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("commit to closed pr err = %v", err)
	}
}

func TestFatalCommitKeepsRecordAt(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	commitBucket(t, db, r, 7, "needs_initial_review", at(1))
	s := prState(t, db, r.ID, 7)
	retry := at(3)
	_, err := db.CommitEnrichment(ctx, EnrichCommit{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: s.Revision,
		Now: at(2), Record: []byte(`{"version":1,"collection_rules_hash":"h","error":"boom"}`), Error: "boom",
		EnrichFailures: 1, RetryAt: &retry, Result: ResultRow{Bucket: "unclassified", Reason: "boom"}})
	if err != nil {
		t.Fatal(err)
	}
	s = prState(t, db, r.ID, 7)
	if !s.RecordAt.Equal(at(1)) || s.LastEnrichAttemptAt == nil || !s.LastEnrichAttemptAt.Equal(at(2)) || s.Error != "boom" || !s.RetryAt.Equal(retry) {
		t.Fatalf("fatal commit state = %+v", s)
	}
}

func TestCommitObservation(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	err := db.CommitObservation(ctx, DetailObservation{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: 1, Now: at(1), Fingerprint: "fpX", HeadOID: "headX"})
	if err != nil {
		t.Fatalf("CommitObservation: %v", err)
	}
	s := prState(t, db, r.ID, 7)
	d, _ := db.PRDetails(ctx, "o", "r", 7)
	if s.Revision != 2 || s.Fingerprint != "fpX" || !s.RetryAt.Equal(at(1)) || d[0].HeadChangedAt == nil || !d[0].HeadChangedAt.Equal(at(1)) {
		t.Fatalf("after detail observation: %+v %+v", s, d[0])
	}
	if err := db.CommitObservation(ctx, DetailObservation{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: 1, Now: at(2)}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale observation err = %v", err)
	}
}

func TestCommitResult(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7)
	commitBucket(t, db, r, 7, "waiting_on_author", at(1))
	s := prState(t, db, r.ID, 7)
	same := ResultRow{Bucket: "waiting_on_author", Reason: "because", Action: "act"}
	if tr, changed, err := db.CommitResult(ctx, r.ID, r.Revision, 7, s.Revision, same, at(2)); err != nil || changed || tr != nil {
		t.Fatalf("no-op reclassify = %+v %v %v", tr, changed, err)
	}
	stale := ResultRow{Bucket: "stale_changes_requested", Reason: "8 days", Action: "nudge"}
	tr, changed, err := db.CommitResult(ctx, r.ID, r.Revision, 7, s.Revision, stale, at(3))
	if err != nil || !changed || tr == nil || *tr.From != "waiting_on_author" {
		t.Fatalf("reclassify = %+v %v %v", tr, changed, err)
	}
	if prState(t, db, r.ID, 7).Revision != s.Revision {
		t.Fatal("result-only write advanced the evidence revision")
	}
	if _, _, err := db.CommitResult(ctx, r.ID, r.Revision, 7, s.Revision-1, stale, at(4)); !errors.Is(err, ErrStale) {
		t.Fatalf("stale reclassify err = %v", err)
	}
	items, _ := db.Reclassifiable(ctx, 0)
	if len(items) != 1 || items[0].Number != 7 || items[0].Account != "me" || items[0].PRRevision != s.Revision {
		t.Fatalf("Reclassifiable = %+v", items)
	}
}

func TestDueWork(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	r := mustRepo(t, db, a, "o", "r")
	observeNew(t, db, r, at(0), 7, 8)
	off := mustRepo(t, db, a, "o", "off")
	observeNew(t, db, off, at(0), 1)
	if _, err := db.SetRepoEnabled(ctx, off.ID, false); err != nil {
		t.Fatal(err)
	}
	work, err := db.DueWork(ctx, at(0), 10)
	if err != nil || len(work) != 2 || work[0].Number != 7 || work[0].Account != "me" || work[0].Fingerprint != "fp1" || work[0].PRRevision != 1 {
		t.Fatalf("DueWork = %+v, %v", work, err)
	}
	if work, _ := db.DueWork(ctx, at(0).Add(-time.Minute), 10); len(work) != 0 {
		t.Fatalf("future work returned: %+v", work)
	}
	if err := db.SetAccountStatus(ctx, "me", StatusNeedsAuth, "x"); err != nil {
		t.Fatal(err)
	}
	if work, _ := db.DueWork(ctx, at(0), 10); len(work) != 0 {
		t.Fatalf("needs_auth account work returned: %+v", work)
	}
}

func TestRecordPoll(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	s, err := db.RecordPoll(ctx, r.ID, at(0), errors.New("network down"))
	if err != nil || s.ConsecutiveFailures != 1 || s.LastError != "network down" || s.LastSuccessAt != nil {
		t.Fatalf("failure = %+v, %v", s, err)
	}
	s, _ = db.RecordPoll(ctx, r.ID, at(1), errors.New("still down"))
	if s.ConsecutiveFailures != 2 {
		t.Fatalf("failures = %d", s.ConsecutiveFailures)
	}
	s, _ = db.RecordPoll(ctx, r.ID, at(2), nil)
	if s.ConsecutiveFailures != 0 || s.LastError != "" || !s.LastSuccessAt.Equal(at(2)) {
		t.Fatalf("success = %+v", s)
	}
}

func TestRulesEditInvalidatesOpenPRs(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 7, 8)
	commitBucket(t, db, r, 7, "needs_initial_review", at(1))
	if _, err := db.ApplyPoll(ctx, PollApply{RepoID: r.ID, RepoRevision: r.Revision, Now: at(2), Closes: []int{8}}); err != nil {
		t.Fatal(err)
	}
	before7, before8 := prState(t, db, r.ID, 7), prState(t, db, r.ID, 8)
	rl := rules.Default()
	rl.TestApproval = rules.TestApproval{MarkerChecks: []string{"tests"}, Command: "/ok-to-test"}
	upd, err := db.UpdateRepoRules(ctx, r.ID, rl, at(3))
	if err != nil || !upd.CollectionChanged || len(upd.Transitions) != 1 || upd.Transitions[0].To != "unclassified" {
		t.Fatalf("UpdateRepoRules = %+v, %v", upd, err)
	}
	after7, after8 := prState(t, db, r.ID, 7), prState(t, db, r.ID, 8)
	if after7.Revision != before7.Revision+1 || !after7.RetryAt.Equal(at(3)) {
		t.Fatalf("open pr not invalidated: %+v", after7)
	}
	if after8.Revision != before8.Revision {
		t.Fatalf("closed pr invalidated: %+v", after8)
	}
	rows, _ := db.Queue(ctx, QueueFilter{})
	if len(rows) != 1 || rows[0].Result.Reason != "collection rules changed; refreshing evidence" {
		t.Fatalf("queue after rules edit = %+v", rows)
	}
}

func TestCachesPurgeAndCascade(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	r := mustRepo(t, db, a, "o", "r")
	observeNew(t, db, r, at(0), 7)
	s := prState(t, db, r.ID, 7)
	_, err := db.CommitEnrichment(ctx, EnrichCommit{RepoID: r.ID, RepoRevision: r.Revision, Number: 7, PRRevision: s.Revision, Now: at(1),
		Record: []byte(`{}`), Result: ResultRow{Bucket: "own"}, Caches: Caches{
			Diffs:    []DiffCache{{BaseOID: "b", ReviewedOID: "r", HeadOID: "h", Changed: true}},
			Comments: []CommentCache{{CommentID: 99, UpdatedAt: at(0), IsCommand: true}},
			Policies: []PolicyCache{{BaseRef: "main", Policy: "required", FetchedAt: at(1)}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if changed, ok, _ := db.CachedDiff(ctx, r.ID, "b", "r", "h"); !ok || !changed {
		t.Fatal("diff cache miss")
	}
	if isCmd, ok, _ := db.CachedComment(ctx, r.ID, 99, at(0)); !ok || !isCmd {
		t.Fatal("comment cache miss")
	}
	if _, ok, _ := db.CachedComment(ctx, r.ID, 99, at(5)); ok {
		t.Fatal("edited comment hit stale cache entry")
	}
	if p, ok, _ := db.CachedPolicy(ctx, r.ID, "main", at(30), time.Hour); !ok || p != "required" {
		t.Fatal("policy cache miss inside TTL")
	}
	if _, ok, _ := db.CachedPolicy(ctx, r.ID, "main", at(62), time.Hour); ok {
		t.Fatal("policy cache hit after TTL")
	}
	if n, err := db.Purge(ctx, at(1).Add(31*24*time.Hour)); err != nil || n != 2 {
		t.Fatalf("Purge = %d, %v", n, err)
	}
	if err := db.DeleteRepo(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	readded := mustRepo(t, db, a, "o", "r")
	if _, ok, _ := db.CachedPolicy(ctx, readded.ID, "main", at(30), time.Hour); ok {
		t.Fatal("re-added repo inherited a policy cache entry")
	}
	var n int
	if err := db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM cache_policy`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cache_policy rows after delete = %d, %v", n, err)
	}
}
