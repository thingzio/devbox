package store

import (
	"context"
	"testing"
)

func TestQueueSameRepoTwoAccounts(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	me, work := mustAccount(t, db, "me"), mustAccount(t, db, "work")
	r1, r2 := mustRepo(t, db, me, "NVIDIA", "aicr"), mustRepo(t, db, work, "NVIDIA", "aicr")
	observeNew(t, db, r1, at(0), 7)
	observeNew(t, db, r2, at(0), 7)
	commitBucket(t, db, r1, 7, "waiting_on_me", at(1))
	commitBucket(t, db, r2, 7, "needs_initial_review", at(1))
	rows, err := db.Queue(ctx, QueueFilter{Owner: "nvidia", Name: "AICR"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("Queue = %+v, %v", rows, err)
	}
	accounts := map[string]string{}
	for _, row := range rows {
		accounts[row.Account] = row.Result.Bucket
	}
	if accounts["me"] != "waiting_on_me" || accounts["work"] != "needs_initial_review" {
		t.Fatalf("rows by account = %v", accounts)
	}
	mine, _ := db.Queue(ctx, QueueFilter{Account: "work"})
	if len(mine) != 1 || mine[0].Account != "work" || mine[0].Title != "Fix it" {
		t.Fatalf("account filter = %+v", mine)
	}
	details, _ := db.PRDetails(ctx, "NVIDIA", "aicr", 7)
	if len(details) != 2 || details[0].Result == nil {
		t.Fatalf("PRDetails = %+v", details)
	}
}

func TestQueueFiltersAndExcludesDisabled(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := mustAccount(t, db, "me")
	r := mustRepo(t, db, a, "o", "r")
	off := mustRepo(t, db, a, "o", "off")
	observeNew(t, db, r, at(0), 1, 2)
	observeNew(t, db, off, at(0), 3)
	commitBucket(t, db, r, 1, "waiting_on_me", at(1))
	commitBucket(t, db, r, 2, "draft", at(1))
	commitBucket(t, db, off, 3, "waiting_on_me", at(1))
	if _, err := db.SetRepoEnabled(ctx, off.ID, false); err != nil {
		t.Fatal(err)
	}
	all, _ := db.Queue(ctx, QueueFilter{})
	if len(all) != 2 {
		t.Fatalf("disabled repo rows leaked: %+v", all)
	}
	drafts, _ := db.Queue(ctx, QueueFilter{Bucket: "draft"})
	if len(drafts) != 1 || drafts[0].Number != 2 {
		t.Fatalf("bucket filter = %+v", drafts)
	}
	counts, _ := db.BucketCounts(ctx)
	if len(counts) != 2 {
		t.Fatalf("BucketCounts included disabled repo: %+v", counts)
	}
}

func TestRepoStatuses(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	r := mustRepo(t, db, mustAccount(t, db, "me"), "o", "r")
	observeNew(t, db, r, at(0), 1, 2)
	commitBucket(t, db, r, 1, "waiting_on_me", at(1))
	if _, err := db.RecordPoll(ctx, r.ID, at(2), nil); err != nil {
		t.Fatal(err)
	}
	statuses, err := db.RepoStatuses(ctx)
	if err != nil || len(statuses) != 1 {
		t.Fatalf("RepoStatuses = %+v, %v", statuses, err)
	}
	st := statuses[0]
	if st.Open != 2 || st.Pending != 1 || st.Errored != 0 || st.AccountStatus != StatusOK || st.Sync.LastSuccessAt == nil || st.NextRetryAt == nil {
		t.Fatalf("status = %+v", st)
	}
}
