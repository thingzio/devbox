// Package classify turns normalized pull request evidence into the action the
// maintainer owes (§5). It performs no I/O and never reads the clock.
package classify

import "time"

// RecordVersion changes whenever Record's shape or normalization changes;
// stored records with another version are re-collected (§4.3).
const RecordVersion = 1

// Review is one of the viewer's submitted reviews; slices are chronological.
type Review struct {
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
	Commit      string    `json:"commit,omitempty"`
}

// OtherReview is another reviewer's standing decision.
type OtherReview struct {
	Author string `json:"author"`
	State  string `json:"state"`
}

// Thread is an unresolved thread the viewer started whose last comment is the author's.
type Thread struct {
	URL    string    `json:"url"`
	LastAt time.Time `json:"last_at"`
}

// Check is one status check context on the head commit.
type Check struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Required bool   `json:"required"`
	RunID    int64  `json:"run_id,omitempty"`
}

// Unverified is a commit whose signature is not valid.
type Unverified struct {
	SHA    string `json:"sha"`
	Reason string `json:"reason"`
}

// EvidenceError records an optional read that failed and fell back (§4.3).
type EvidenceError struct {
	Field     string `json:"field"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// CoreRetry marks retained evidence while a retryable core read keeps failing (§7).
type CoreRetry struct {
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

// Milestone is the PR's milestone.
type Milestone struct {
	Title string     `json:"title"`
	DueOn *time.Time `json:"due_on,omitempty"`
}

// Record is the normalized evidence for one pull request (§4.3).
type Record struct {
	Version             int             `json:"version"`
	CollectionRulesHash string          `json:"collection_rules_hash"`
	Error               string          `json:"error,omitempty"`
	CoreRetry           *CoreRetry      `json:"core_retry,omitempty"`
	EvidenceErrors      []EvidenceError `json:"evidence_errors,omitempty"`

	Number            int        `json:"number"`
	Title             string     `json:"title"`
	URL               string     `json:"url"`
	Author            string     `json:"author"`
	IsBot             bool       `json:"is_bot"`
	Draft             bool       `json:"draft"`
	Fork              bool       `json:"fork"`
	AuthorAssociation string     `json:"author_association,omitempty"`
	Additions         int        `json:"additions"`
	Deletions         int        `json:"deletions"`
	Milestone         *Milestone `json:"milestone,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`

	BaseRef         string     `json:"base_ref"`
	BaseOID         string     `json:"base_oid"`
	HeadOID         string     `json:"head_oid"`
	HeadCommittedAt time.Time  `json:"head_committed_at"`
	HeadChangedAt   *time.Time `json:"head_changed_at,omitempty"`

	MergeState     string     `json:"merge_state"`
	ReviewDecision string     `json:"review_decision,omitempty"`
	AutoMerge      bool       `json:"auto_merge"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`

	ReviewRequestedFromMe bool          `json:"review_requested_from_me"`
	MyReviews             []Review      `json:"my_reviews,omitempty"`
	OthersDecisive        []OtherReview `json:"others_decisive,omitempty"`
	MyThreadsWaiting      []Thread      `json:"my_threads_waiting,omitempty"`
	UnresolvedThreads     int           `json:"unresolved_threads"`
	AuthorUpdatedAt       *time.Time    `json:"author_updated_at,omitempty"`
	MyLastCommentAt       *time.Time    `json:"my_last_comment_at,omitempty"`

	UnverifiedCommits  []Unverified `json:"unverified_commits,omitempty"`
	SignaturesComplete bool         `json:"signatures_complete"`
	SignaturePolicy    string       `json:"signature_policy,omitempty"`

	AwaitingRuns   int     `json:"awaiting_runs"`
	MarkerPresent  *bool   `json:"marker_present,omitempty"`
	CIPathsTouched *bool   `json:"ci_paths_touched,omitempty"`
	DiffChanged    *bool   `json:"diff_changed,omitempty"`
	Checks         []Check `json:"checks,omitempty"`
}

// HeadActivityAt is the observed head change, else the head commit date
// clamped to PR creation; commit dates are author-controlled (§4.3).
func (r Record) HeadActivityAt() time.Time {
	if r.HeadChangedAt != nil {
		return *r.HeadChangedAt
	}
	if r.HeadCommittedAt.Before(r.CreatedAt) {
		return r.CreatedAt
	}
	return r.HeadCommittedAt
}

var decisiveStates = map[string]bool{"APPROVED": true, "CHANGES_REQUESTED": true, "DISMISSED": true}

func latestReview(reviews []Review, states map[string]bool) *Review {
	for i := len(reviews) - 1; i >= 0; i-- {
		if states == nil || states[reviews[i].State] {
			r := reviews[i]
			return &r
		}
	}
	return nil
}

// ReviewBaseline is the review new commits are judged against: the viewer's
// standing decision, else the viewer's latest review. A later COMMENTED review
// does not renew an approval or a changes request.
func ReviewBaseline(reviews []Review) *Review {
	if d := latestReview(reviews, decisiveStates); d != nil && d.State != "DISMISSED" {
		return d
	}
	return latestReview(reviews, nil)
}
