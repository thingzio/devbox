package collect

import "time"

// GraphQL response shapes for PullRequestDetail (§4.2).
type (
	Actor struct {
		Login    string `json:"login"`
		Typename string `json:"__typename,omitempty"`
	}
	Milestone struct {
		Title string     `json:"title"`
		DueOn *time.Time `json:"dueOn"`
	}
	AutoMerge struct {
		EnabledAt *time.Time `json:"enabledAt"`
	}
	ReviewRequest struct {
		RequestedReviewer *Actor `json:"requestedReviewer"`
	}
	OID struct {
		OID string `json:"oid"`
	}
	ReviewNode struct {
		State       string     `json:"state"`
		SubmittedAt *time.Time `json:"submittedAt"`
		Commit      *OID       `json:"commit,omitempty"`
		Author      *Actor     `json:"author,omitempty"`
	}
	ThreadComment struct {
		Author    *Actor    `json:"author"`
		URL       string    `json:"url,omitempty"`
		CreatedAt time.Time `json:"createdAt"`
	}
	ThreadNode struct {
		IsResolved bool                 `json:"isResolved"`
		First      Nodes[ThreadComment] `json:"first"`
		Last       Nodes[ThreadComment] `json:"last"`
	}
	CommentNode struct {
		Author     *Actor    `json:"author"`
		CreatedAt  time.Time `json:"createdAt"`
		UpdatedAt  time.Time `json:"updatedAt"`
		DatabaseID int64     `json:"databaseId"`
	}
	Signature struct {
		State string `json:"state"`
	}
	Rollup struct {
		State string `json:"state"`
	}
	Commit struct {
		OID               string     `json:"oid"`
		CommittedDate     time.Time  `json:"committedDate"`
		Signature         *Signature `json:"signature"`
		StatusCheckRollup *Rollup    `json:"statusCheckRollup"`
	}
	CommitNode struct {
		Commit Commit `json:"commit"`
	}
)

// Nodes is a GraphQL connection.
type Nodes[T any] struct {
	TotalCount int `json:"totalCount"`
	Nodes      []T `json:"nodes"`
}

// Detail is one pullRequest node of the PullRequestDetail query.
type Detail struct {
	Number                   int                  `json:"number"`
	Title                    string               `json:"title"`
	URL                      string               `json:"url"`
	IsDraft                  bool                 `json:"isDraft"`
	IsCrossRepository        bool                 `json:"isCrossRepository"`
	CreatedAt                time.Time            `json:"createdAt"`
	UpdatedAt                time.Time            `json:"updatedAt"`
	AuthorAssociation        string               `json:"authorAssociation"`
	Additions                int                  `json:"additions"`
	Deletions                int                  `json:"deletions"`
	Milestone                *Milestone           `json:"milestone"`
	Author                   *Actor               `json:"author"`
	BaseRefName              string               `json:"baseRefName"`
	BaseRefOid               string               `json:"baseRefOid"`
	HeadRefOid               string               `json:"headRefOid"`
	MergeStateStatus         string               `json:"mergeStateStatus"`
	ReviewDecision           string               `json:"reviewDecision"`
	AutoMergeRequest         *AutoMerge           `json:"autoMergeRequest"`
	ReviewRequests           Nodes[ReviewRequest] `json:"reviewRequests"`
	MyReviews                Nodes[ReviewNode]    `json:"myReviews"`
	LatestOpinionatedReviews Nodes[ReviewNode]    `json:"latestOpinionatedReviews"`
	ReviewThreads            Nodes[ThreadNode]    `json:"reviewThreads"`
	Comments                 Nodes[CommentNode]   `json:"comments"`
	Commits                  Nodes[CommitNode]    `json:"commits"`
	Head                     Nodes[CommitNode]    `json:"head"`
}

func (d *Detail) headCommit() *Commit {
	if n := len(d.Head.Nodes); n > 0 {
		return &d.Head.Nodes[n-1].Commit
	}
	return nil
}

// FingerprintFields makes a detail read a complete observation (§4.2).
func (d *Detail) FingerprintFields() FingerprintFields {
	rollup := ""
	if hc := d.headCommit(); hc != nil && hc.StatusCheckRollup != nil {
		rollup = hc.StatusCheckRollup.State
	}
	return FingerprintFields{
		UpdatedAt: d.UpdatedAt, BaseOID: d.BaseRefOid, HeadOID: d.HeadRefOid, MergeState: d.MergeStateStatus,
		ReviewDecision: d.ReviewDecision, Draft: d.IsDraft, Rollup: rollup,
	}
}

const detailQuery = `query PullRequestDetail($owner: String!, $name: String!, $number: Int!, $viewer: String!) {
  rateLimit { limit cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title url isDraft isCrossRepository createdAt updatedAt
      authorAssociation additions deletions
      milestone { title dueOn }
      author { login __typename }
      baseRefName baseRefOid headRefOid mergeStateStatus reviewDecision
      autoMergeRequest { enabledAt }
      reviewRequests(first: 50) { nodes { requestedReviewer { __typename ... on User { login } } } }
      myReviews: reviews(author: $viewer, last: 50) { nodes { state submittedAt commit { oid } } }
      latestOpinionatedReviews(first: 50) { nodes { state submittedAt author { login } } }
      reviewThreads(first: 100) { totalCount nodes { isResolved
        first: comments(first: 1) { nodes { author { login } url } }
        last: comments(last: 1) { nodes { author { login } createdAt } } } }
      comments(last: 100) { nodes { author { login } createdAt updatedAt databaseId } }
      commits(first: 100) { totalCount nodes { commit { oid signature { state } } } }
      head: commits(last: 1) { nodes { commit { oid committedDate statusCheckRollup { state } } } }
    }
  }
}`

const fingerprintsQuery = `query Fingerprints($owner: String!, $name: String!, $endCursor: String) {
  rateLimit { limit cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    pullRequests(states: OPEN, first: 100, after: $endCursor) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number updatedAt baseRefOid headRefOid mergeStateStatus reviewDecision isDraft
        commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      }
    }
  }
}`

const checksQuery = `query Checks($owner: String!, $name: String!, $oid: GitObjectID!, $pr: Int!, $endCursor: String) {
  rateLimit { limit cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    object(oid: $oid) { ... on Commit { statusCheckRollup { contexts(first: 100, after: $endCursor) {
      pageInfo { hasNextPage endCursor }
      nodes { __typename
        ... on CheckRun { name status conclusion isRequired(pullRequestNumber: $pr) checkSuite { workflowRun { databaseId } } }
        ... on StatusContext { context state isRequired(pullRequestNumber: $pr) } } } } } }
  }
}`
