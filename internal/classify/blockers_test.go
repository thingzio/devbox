package classify

import (
	"reflect"
	"testing"
	"time"
)

func TestMergeBlockers(t *testing.T) {
	failing := Check{Name: "tests", State: "FAILURE", Required: true, RunID: 42}
	cases := []struct {
		name string
		rec  Record
		want []cause
	}{
		{"clean has none", Record{MergeState: "CLEAN", SignaturesComplete: true, SignaturePolicy: "absent"}, nil},
		{"unstable is eligible", Record{MergeState: "UNSTABLE", SignaturesComplete: true, SignaturePolicy: "absent"}, nil},
		{"dirty", Record{MergeState: "DIRTY", SignaturePolicy: "absent"},
			[]cause{{"conflicts with base", "ask the author to rebase"}}},
		{"behind", Record{MergeState: "BEHIND", SignaturePolicy: "absent"},
			[]cause{{"behind base", "update the branch"}}},
		{"behind with auto-merge", Record{MergeState: "BEHIND", AutoMerge: true, SignaturePolicy: "absent"},
			[]cause{{"behind base", "run serial-auto-merge"}}},
		{"unknown", Record{MergeState: "UNKNOWN", SignaturePolicy: "absent"},
			[]cause{{"mergeability unknown", "check the merge box on GitHub"}}},
		{"other state", Record{MergeState: "DRAFT", SignaturePolicy: "absent"},
			[]cause{{"merge state draft", "check the merge box on GitHub"}}},
		{"blocked by failing required check", Record{MergeState: "BLOCKED", SignaturesComplete: true, SignaturePolicy: "absent",
			Checks: []Check{failing, {Name: "lint", State: "SUCCESS", Required: true}, {Name: "docs", State: "FAILURE"}}},
			[]cause{{"required check `tests` is failure", "`gh run rerun 42 --failed` after a flake check"}}},
		{"blocked by pending required check", Record{MergeState: "BLOCKED", SignaturesComplete: true, SignaturePolicy: "absent",
			Checks: []Check{{Name: "e2e", State: "IN_PROGRESS", Required: true, RunID: 7}}},
			[]cause{{"required check `e2e` is in_progress", "wait for the check, or investigate why it has not passed"}}},
		{"blocked without cause", Record{MergeState: "BLOCKED", SignaturesComplete: true, SignaturePolicy: "absent", UnresolvedThreads: 2},
			[]cause{{"blocked by branch policy; cause not identified (2 unresolved threads)", "check the merge box on GitHub"}}},
		{"signatures incomplete", Record{MergeState: "CLEAN", SignaturePolicy: "required"},
			[]cause{{"signature state unknown", "check commit signatures on GitHub"}}},
		{"unverified with required policy", Record{MergeState: "CLEAN", SignaturesComplete: true, SignaturePolicy: "required",
			UnverifiedCommits: []Unverified{{SHA: "abcdef0123", Reason: "unsigned"}, {SHA: "1234567890", Reason: "bad_email"}}},
			[]cause{{"unverified commits: abcdef0 (unsigned), 1234567 (bad_email)", "ask the author to sign the listed commits"}}},
		{"unverified with unreadable policy", Record{MergeState: "CLEAN", SignaturesComplete: true, SignaturePolicy: "unavailable",
			UnverifiedCommits: []Unverified{{SHA: "abcdef0123", Reason: "unsigned"}}},
			[]cause{{"1 unverified commits; signature policy unreadable: abcdef0 (unsigned)", "confirm the signature policy, then ask the author to sign if required"}}},
		{"unverified with absent policy", Record{MergeState: "CLEAN", SignaturePolicy: "absent",
			UnverifiedCommits: []Unverified{{SHA: "abcdef0123", Reason: "unsigned"}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeBlockers(tc.rec); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergeBlockers = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSummarizeDeduplicatesActions(t *testing.T) {
	reason, action := summarize([]cause{
		{"behind base", "update the branch"},
		{"signature state unknown", "check commit signatures on GitHub"},
		{"mergeability unknown", "update the branch"},
	})
	if reason != "approved; behind base; signature state unknown; mergeability unknown" {
		t.Fatalf("reason = %q", reason)
	}
	if action != "update the branch; check commit signatures on GitHub" {
		t.Fatalf("action = %q", action)
	}
}

func TestReviewBaseline(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	approved := Review{State: "APPROVED", SubmittedAt: at, Commit: "a"}
	commented := Review{State: "COMMENTED", SubmittedAt: at.Add(time.Hour), Commit: "b"}
	dismissed := Review{State: "DISMISSED", SubmittedAt: at.Add(2 * time.Hour), Commit: "c"}
	if got := ReviewBaseline([]Review{approved, commented}); got.Commit != "a" {
		t.Fatalf("later comment renewed the approval: %+v", got)
	}
	if got := ReviewBaseline([]Review{approved, commented, dismissed}); got.Commit != "c" {
		t.Fatalf("dismissed decision should fall back to latest review: %+v", got)
	}
	if ReviewBaseline(nil) != nil {
		t.Fatal("no reviews should have no baseline")
	}
}

func TestHeadActivityAt(t *testing.T) {
	created := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	backdated := Record{CreatedAt: created, HeadCommittedAt: created.Add(-48 * time.Hour)}
	if got := backdated.HeadActivityAt(); !got.Equal(created) {
		t.Fatalf("backdated commit not clamped: %v", got)
	}
	observed := created.Add(5 * time.Hour)
	withChange := Record{CreatedAt: created, HeadCommittedAt: created.Add(time.Hour), HeadChangedAt: &observed}
	if got := withChange.HeadActivityAt(); !got.Equal(observed) {
		t.Fatalf("observed head change ignored: %v", got)
	}
}

func TestBucketOrderAndTitles(t *testing.T) {
	if WaitingTestApproval.Order() >= Bot.Order() || Bot.Order() >= ReadyToMerge.Order() {
		t.Fatal("action buckets must precede non-action buckets in precedence order")
	}
	if !Bot.IsAction() || ReadyToMerge.IsAction() || Unclassified.Settled() || !Closed.Settled() {
		t.Fatal("bucket predicates wrong")
	}
	if NeedsRereview.Title() != "Needs re-review" || Bucket("x").Title() != "x" {
		t.Fatal("titles wrong")
	}
}
