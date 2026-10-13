package classify

import "slices"

// Bucket identifies the action group a PR belongs to.
type Bucket string

// Action buckets in precedence order, then non-action buckets.
const (
	WaitingTestApproval   Bucket = "waiting_test_approval"
	WaitingOnMe           Bucket = "waiting_on_me"
	NeedsRereview         Bucket = "needs_rereview"
	ApprovedBlocked       Bucket = "approved_blocked"
	NeedsInitialReview    Bucket = "needs_initial_review"
	StaleChangesRequested Bucket = "stale_changes_requested"
	Bot                   Bucket = "bot"
	ReadyToMerge          Bucket = "ready_to_merge"
	WaitingOnAuthor       Bucket = "waiting_on_author"
	Own                   Bucket = "own"
	Draft                 Bucket = "draft"
	Unclassified          Bucket = "unclassified"
	Closed                Bucket = "closed"
)

var actionOrder = []Bucket{
	WaitingTestApproval, WaitingOnMe, NeedsRereview, ApprovedBlocked,
	NeedsInitialReview, StaleChangesRequested, Bot,
}

var order = append(slices.Clone(actionOrder), ReadyToMerge, WaitingOnAuthor, Own, Draft, Unclassified, Closed)

var titles = map[Bucket]string{
	WaitingTestApproval:   "Waiting test approval",
	WaitingOnMe:           "Waiting on me",
	NeedsRereview:         "Needs re-review",
	ApprovedBlocked:       "Approved, blocked",
	NeedsInitialReview:    "Needs initial review",
	StaleChangesRequested: "Stale changes requested",
	Bot:                   "Bot PRs",
	ReadyToMerge:          "Ready to merge",
	WaitingOnAuthor:       "Waiting on author",
	Own:                   "Own",
	Draft:                 "Drafts",
	Unclassified:          "Unclassified",
	Closed:                "Closed",
}

// ActionBuckets returns the action buckets in precedence order.
func ActionBuckets() []Bucket { return slices.Clone(actionOrder) }

// Title is the human-readable bucket name.
func (b Bucket) Title() string {
	if t, ok := titles[b]; ok {
		return t
	}
	return string(b)
}

// IsAction reports whether the bucket means the maintainer owes an action.
func (b Bucket) IsAction() bool { return slices.Contains(actionOrder, b) }

// Settled reports whether transitions consumers treat the bucket as settled.
func (b Bucket) Settled() bool { return b != Unclassified }

// Order is the bucket's display and ranking position.
func (b Bucket) Order() int {
	if i := slices.Index(order, b); i >= 0 {
		return i
	}
	return len(order)
}
