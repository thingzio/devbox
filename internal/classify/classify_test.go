package classify

import (
	"slices"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/rules"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func hours(h float64) time.Time { return t0.Add(time.Duration(h * float64(time.Hour))) }
func days(d float64) time.Time  { return hours(d * 24) }
func ptr[T any](v T) *T         { return &v }

const me = "me"

var sweepRules = rules.Rules{StaleDays: 7, TestApproval: rules.TestApproval{
	MarkerChecks:   []string{"tests / Test", "tests-skip"},
	Command:        "/ok-to-test",
	CIPaths:        []string{".github/"},
	CIPathsCommand: "/ok-to-test confirm-ci",
}}

// baseRecord is a non-draft PR by alice, never reviewed by me, mergeable.
func baseRecord() Record {
	return Record{
		Version: RecordVersion, Number: 7, Title: "Fix flaky test", URL: "https://github.com/o/r/pull/7",
		Author: "alice", CreatedAt: hours(0), UpdatedAt: hours(2),
		BaseRef: "main", BaseOID: "base0000000", HeadOID: "head111aaaa", HeadCommittedAt: hours(1),
		MergeState: "CLEAN", SignaturesComplete: true, SignaturePolicy: "absent",
	}
}

func review(state string, at time.Time, commit string) Review {
	return Review{State: state, SubmittedAt: at, Commit: commit}
}

type classifyCase struct {
	name      string
	mod       func(r *Record)
	rules     *rules.Rules
	now       time.Time
	bucket    Bucket
	reason    string
	action    string
	since     *time.Time
	noSince   bool
	also      []string
	notes     []string
	checkAlso bool
}

func TestClassify(t *testing.T) {
	cases := []classifyCase{
		{name: "record error is unclassified", mod: func(r *Record) { r.Error = "boom" },
			bucket: Unclassified, reason: "boom", action: "check the PR on GitHub", noSince: true},
		{name: "draft", mod: func(r *Record) { r.Draft = true }, bucket: Draft, reason: "draft", since: ptr(hours(0))},
		{name: "own", mod: func(r *Record) { r.Author = me }, bucket: Own, reason: "your pull request"},
		{name: "bot approved and clean", mod: func(r *Record) { r.IsBot = true; r.ReviewDecision = "APPROVED" },
			bucket: Bot, reason: "approved; ready to merge", action: "merge", since: ptr(hours(0))},
		{name: "bot approved and behind", mod: func(r *Record) { r.IsBot = true; r.ReviewDecision = "APPROVED"; r.MergeState = "BEHIND" },
			bucket: Bot, reason: "approved; behind base", action: "update the branch"},
		{name: "bot without decision", mod: func(r *Record) { r.IsBot = true },
			bucket: Bot, reason: "no review decision", action: "review in batch"},
		{name: "bot review required", mod: func(r *Record) { r.IsBot = true; r.ReviewDecision = "REVIEW_REQUIRED" },
			bucket: Bot, reason: "review required", action: "review in batch"},
		{name: "awaiting workflow runs", mod: func(r *Record) { r.AwaitingRuns = 2 },
			bucket: WaitingTestApproval, reason: "2 workflow runs awaiting approval",
			action: "approve and run workflows after reading the diff", since: ptr(hours(1)),
			also: []string{"Needs initial review"}, checkAlso: true},
		{name: "one awaiting run", mod: func(r *Record) { r.AwaitingRuns = 1 },
			bucket: WaitingTestApproval, reason: "1 workflow run awaiting approval"},
		{name: "conflicted PR never waits for test approval", mod: func(r *Record) { r.AwaitingRuns = 1; r.MergeState = "DIRTY" },
			bucket: WaitingOnAuthor, reason: "waiting on author"},
		{name: "test approval since clamps backdated commit", mod: func(r *Record) { r.AwaitingRuns = 1; r.HeadCommittedAt = hours(-5) },
			bucket: WaitingTestApproval, since: ptr(hours(0))},
		{name: "observed head change dates test approval", mod: func(r *Record) { r.AwaitingRuns = 1; r.HeadChangedAt = ptr(hours(6)) },
			bucket: WaitingTestApproval, since: ptr(hours(6))},
		{name: "fork missing marker", rules: &sweepRules,
			mod:    func(r *Record) { r.Fork = true; r.MarkerPresent = ptr(false); r.CIPathsTouched = ptr(false) },
			bucket: WaitingTestApproval, reason: "fork PR; no `tests / Test` or `tests-skip` check on head111",
			action: "comment `/ok-to-test` after reading the diff"},
		{name: "fork missing marker changes CI", rules: &sweepRules,
			mod:    func(r *Record) { r.Fork = true; r.MarkerPresent = ptr(false); r.CIPathsTouched = ptr(true) },
			bucket: WaitingTestApproval, reason: "fork PR; no `tests / Test` or `tests-skip` check on head111; changes CI files",
			action: "comment `/ok-to-test confirm-ci` after reading the diff"},
		{name: "fork missing marker with unreadable files", rules: &sweepRules,
			mod:    func(r *Record) { r.Fork = true; r.MarkerPresent = ptr(false) },
			bucket: WaitingTestApproval, reason: "fork PR; no `tests / Test` or `tests-skip` check on head111; file list unreadable",
			action: "comment `/ok-to-test confirm-ci` after reading the diff"},
		{name: "fork with marker present", rules: &sweepRules,
			mod:    func(r *Record) { r.Fork = true; r.MarkerPresent = ptr(true) },
			bucket: NeedsInitialReview},
		{name: "review requested and replies", mod: func(r *Record) {
			r.ReviewRequestedFromMe = true
			r.MyThreadsWaiting = []Thread{{URL: "https://github.com/o/r/pull/7#c1", LastAt: hours(5)}, {URL: "https://github.com/o/r/pull/7#c2", LastAt: hours(3)}}
		}, bucket: WaitingOnMe, reason: "your review is requested; author replied in your threads (2)", action: "review",
			since: ptr(hours(5)), also: []string{"Needs initial review"}, checkAlso: true,
			notes: []string{"https://github.com/o/r/pull/7#c1", "https://github.com/o/r/pull/7#c2"}},
		{name: "replies only", mod: func(r *Record) {
			r.MyReviews = []Review{review("COMMENTED", hours(1.5), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.AuthorUpdatedAt = ptr(hours(5))
			r.MyThreadsWaiting = []Thread{{URL: "https://github.com/o/r/pull/7#c1", LastAt: hours(5)}}
		}, bucket: WaitingOnMe, action: "reply in the linked threads"},
		{name: "author silent since my comment", mod: func(r *Record) {
			r.ReviewRequestedFromMe = true
			r.MyReviews = []Review{review("COMMENTED", hours(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(3))
			r.AuthorUpdatedAt = ptr(hours(2))
		}, bucket: WaitingOnAuthor, reason: "waiting on author", since: ptr(hours(3))},
		{name: "diff changed since approval", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.AuthorUpdatedAt = ptr(hours(4))
			r.HeadChangedAt = ptr(hours(4))
			r.DiffChanged = ptr(true)
		}, bucket: NeedsRereview, reason: "diff changed since your review (approved at old2222)", action: "re-review", since: ptr(hours(4))},
		{name: "diff unknown since approval", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.HeadChangedAt = ptr(hours(4))
		}, bucket: NeedsRereview, reason: "could not confirm the diff is unchanged since your review (approved at old2222)"},
		{name: "diff unchanged after branch update", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.DiffChanged = ptr(false)
			r.ReviewDecision = "APPROVED"
			r.ApprovedAt = ptr(hours(1.5))
		}, bucket: ReadyToMerge, reason: "approved; ready to merge", action: "merge", since: ptr(hours(1.5)),
			notes: []string{"branch updated, diff unchanged"}},
		{name: "re-review does not need author activity", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(6))
			r.AuthorUpdatedAt = ptr(hours(4))
			r.DiffChanged = ptr(true)
		}, bucket: NeedsRereview},
		{name: "re-review never when conflicted", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.DiffChanged = ptr(true)
			r.MergeState = "DIRTY"
		}, bucket: WaitingOnAuthor},
		{name: "re-review since clamps to review time", mod: func(r *Record) {
			r.MyReviews = []Review{review("APPROVED", hours(1.5), "old2222bbbb")}
			r.MyLastCommentAt = ptr(hours(1.5))
			r.HeadCommittedAt = hours(-10)
			r.DiffChanged = ptr(true)
		}, bucket: NeedsRereview, since: ptr(hours(1.5))},
		{name: "approved and behind", mod: func(r *Record) {
			r.ReviewDecision = "APPROVED"
			r.ApprovedAt = ptr(hours(3))
			r.MergeState = "BEHIND"
			r.MyReviews = []Review{review("APPROVED", hours(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(3))
		}, bucket: ApprovedBlocked, reason: "approved; behind base", action: "update the branch", since: ptr(hours(3))},
		{name: "approved and behind with auto-merge", mod: func(r *Record) {
			r.ReviewDecision = "APPROVED"
			r.MergeState = "BEHIND"
			r.AutoMerge = true
			r.MyReviews = []Review{review("APPROVED", hours(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(3))
		}, bucket: ApprovedBlocked, action: "run serial-auto-merge", since: ptr(hours(2))},
		{name: "approved with failing required check", mod: func(r *Record) {
			r.ReviewDecision = "APPROVED"
			r.MergeState = "BLOCKED"
			r.Checks = []Check{{Name: "tests", State: "FAILURE", Required: true, RunID: 42}}
			r.MyReviews = []Review{review("APPROVED", hours(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(3))
		}, bucket: ApprovedBlocked, reason: "approved; required check `tests` is failure",
			action: "`gh run rerun 42 --failed` after a flake check"},
		{name: "initial review", bucket: NeedsInitialReview, reason: "not reviewed by you", action: "review", since: ptr(hours(0))},
		{name: "stale changes requested", now: days(9), mod: func(r *Record) {
			r.MyReviews = []Review{review("CHANGES_REQUESTED", days(1), "head111aaaa")}
			r.MyLastCommentAt = ptr(days(1))
			r.AuthorUpdatedAt = ptr(hours(2))
			r.HeadChangedAt = ptr(hours(1))
		}, bucket: StaleChangesRequested, reason: "changes requested; no author activity in 8 days since your feedback",
			action: "nudge the author, or close", since: ptr(days(1))},
		{name: "changes requested not yet stale", now: days(5), mod: func(r *Record) {
			r.MyReviews = []Review{review("CHANGES_REQUESTED", days(1), "head111aaaa")}
			r.MyLastCommentAt = ptr(days(1))
			r.AuthorUpdatedAt = ptr(hours(2))
			r.HeadChangedAt = ptr(hours(1))
		}, bucket: WaitingOnAuthor},
		{name: "unknown push history cannot backdate staleness", now: days(9), mod: func(r *Record) {
			r.MyReviews = []Review{review("COMMENTED", days(0.1), "head111aaaa"), review("CHANGES_REQUESTED", days(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(days(3))
		}, bucket: WaitingOnAuthor},
		{name: "unknown push history dates staleness from standing request", now: days(10.5), mod: func(r *Record) {
			r.MyReviews = []Review{review("COMMENTED", days(0.1), "head111aaaa"), review("CHANGES_REQUESTED", days(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(days(3))
		}, bucket: StaleChangesRequested, reason: "changes requested; no author activity in 7 days since your feedback", since: ptr(days(3))},
		{name: "re-review suppresses stale nudge", now: days(9), mod: func(r *Record) {
			r.MyReviews = []Review{review("CHANGES_REQUESTED", days(1), "old2222bbbb")}
			r.MyLastCommentAt = ptr(days(1))
			r.AuthorUpdatedAt = ptr(hours(2))
			r.DiffChanged = ptr(true)
		}, bucket: NeedsRereview, also: nil, checkAlso: true},
		{name: "ready to merge", mod: func(r *Record) {
			r.ReviewDecision = "APPROVED"
			r.ApprovedAt = ptr(hours(3))
			r.MyReviews = []Review{review("APPROVED", hours(3), "head111aaaa")}
			r.MyLastCommentAt = ptr(hours(3))
		}, bucket: ReadyToMerge, since: ptr(hours(3))},
		{name: "notes from reviews, checks, and evidence", mod: func(r *Record) {
			r.OthersDecisive = []OtherReview{{Author: "bob", State: "APPROVED"}, {Author: "carol", State: "CHANGES_REQUESTED"}}
			r.MergeState = "UNSTABLE"
			r.EvidenceErrors = []EvidenceError{{Field: "signature_policy", Message: "HTTP 403"}, {Field: "diff", Message: "timeout", Retryable: true}}
			r.CoreRetry = &CoreRetry{Message: "HTTP 502", Since: hours(5)}
		}, bucket: NeedsInitialReview, notes: []string{
			"approved by @bob", "changes requested by @carol", "non-required checks failing",
			"signature_policy unavailable: HTTP 403", "diff unavailable: timeout (retry pending)",
			"evidence stale since 2026-10-01T17:00:00Z, retrying: HTTP 502",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := baseRecord()
			if tc.mod != nil {
				tc.mod(&rec)
			}
			rl := rules.Default()
			if tc.rules != nil {
				rl = *tc.rules
			}
			now := tc.now
			if now.IsZero() {
				now = days(1)
			}
			got := Classify(rec, rl, me, now)
			if got.Bucket != tc.bucket {
				t.Fatalf("bucket = %s (%q), want %s", got.Bucket, got.Reason, tc.bucket)
			}
			if tc.reason != "" && got.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.reason)
			}
			if tc.action != "" && got.Action != tc.action {
				t.Errorf("action = %q, want %q", got.Action, tc.action)
			}
			if tc.noSince && got.WaitingSince != nil {
				t.Errorf("waiting since = %v, want none", got.WaitingSince)
			}
			if tc.since != nil && (got.WaitingSince == nil || !got.WaitingSince.Equal(*tc.since)) {
				t.Errorf("waiting since = %v, want %v", got.WaitingSince, *tc.since)
			}
			if tc.checkAlso && !slices.Equal(got.Also, tc.also) {
				t.Errorf("also = %v, want %v", got.Also, tc.also)
			}
			for _, note := range tc.notes {
				if !slices.Contains(got.Notes, note) {
					t.Errorf("notes %v missing %q", got.Notes, note)
				}
			}
		})
	}
}
