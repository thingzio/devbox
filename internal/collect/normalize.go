package collect

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/classify"
)

func login(a *Actor) string {
	if a == nil {
		return ""
	}
	return a.Login
}

// Normalize converts a detail node into a Record (§4.3). When mandatory
// evidence is incomplete it returns an error together with the partially
// filled record so an error result keeps its title and link.
func Normalize(d *Detail, viewer string, headChangedAt *time.Time) (classify.Record, error) {
	author, isBot := "ghost", false
	if d.Author != nil && d.Author.Login != "" {
		author = d.Author.Login
		isBot = d.Author.Typename == "Bot" || strings.HasSuffix(author, "[bot]")
	}
	rec := classify.Record{
		Number: d.Number, Title: d.Title, URL: d.URL, Author: author, IsBot: isBot,
		Draft: d.IsDraft, Fork: d.IsCrossRepository, AuthorAssociation: d.AuthorAssociation,
		Additions: d.Additions, Deletions: d.Deletions, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		BaseRef: d.BaseRefName, BaseOID: d.BaseRefOid, HeadOID: d.HeadRefOid,
		MergeState: d.MergeStateStatus, ReviewDecision: d.ReviewDecision, AutoMerge: d.AutoMergeRequest != nil,
		SignaturesComplete: d.Commits.TotalCount <= len(d.Commits.Nodes),
	}
	if headChangedAt != nil {
		t := *headChangedAt
		rec.HeadChangedAt = &t
	}
	if d.Milestone != nil {
		rec.Milestone = &classify.Milestone{Title: d.Milestone.Title, DueOn: d.Milestone.DueOn}
	}
	if hc := d.headCommit(); hc != nil {
		rec.HeadCommittedAt = hc.CommittedDate
	}

	var authorTimes []time.Time
	for _, c := range d.Comments.Nodes {
		if login(c.Author) == author {
			authorTimes = append(authorTimes, c.CreatedAt)
		}
	}
	for _, th := range d.ReviewThreads.Nodes {
		if !th.IsResolved {
			rec.UnresolvedThreads++
		}
		if len(th.Last.Nodes) == 0 || login(th.Last.Nodes[0].Author) != author {
			continue
		}
		last := th.Last.Nodes[0]
		authorTimes = append(authorTimes, last.CreatedAt)
		if !th.IsResolved && len(th.First.Nodes) > 0 && login(th.First.Nodes[0].Author) == viewer {
			rec.MyThreadsWaiting = append(rec.MyThreadsWaiting, classify.Thread{URL: th.First.Nodes[0].URL, LastAt: last.CreatedAt})
		}
	}
	if headChangedAt != nil {
		authorTimes = append(authorTimes, *headChangedAt)
	}
	if len(authorTimes) > 0 {
		latest := slices.MaxFunc(authorTimes, func(a, b time.Time) int { return a.Compare(b) })
		rec.AuthorUpdatedAt = &latest
	}

	for _, rr := range d.ReviewRequests.Nodes {
		if rr.RequestedReviewer != nil && rr.RequestedReviewer.Login == viewer {
			rec.ReviewRequestedFromMe = true
		}
	}
	for _, r := range d.MyReviews.Nodes {
		if r.State == "PENDING" || r.SubmittedAt == nil {
			continue
		}
		commit := ""
		if r.Commit != nil {
			commit = r.Commit.OID
		}
		rec.MyReviews = append(rec.MyReviews, classify.Review{State: r.State, SubmittedAt: *r.SubmittedAt, Commit: commit})
	}
	slices.SortStableFunc(rec.MyReviews, func(a, b classify.Review) int { return a.SubmittedAt.Compare(b.SubmittedAt) })
	for _, r := range d.LatestOpinionatedReviews.Nodes {
		if r.State == "APPROVED" && r.SubmittedAt != nil && (rec.ApprovedAt == nil || r.SubmittedAt.After(*rec.ApprovedAt)) {
			t := *r.SubmittedAt
			rec.ApprovedAt = &t
		}
		if who := login(r.Author); who != "" && who != viewer && (r.State == "APPROVED" || r.State == "CHANGES_REQUESTED") {
			rec.OthersDecisive = append(rec.OthersDecisive, classify.OtherReview{Author: who, State: r.State})
		}
	}
	for _, c := range d.Commits.Nodes {
		state := "UNSIGNED"
		if c.Commit.Signature != nil && c.Commit.Signature.State != "" {
			state = c.Commit.Signature.State
		}
		if state != "VALID" {
			rec.UnverifiedCommits = append(rec.UnverifiedCommits, classify.Unverified{SHA: c.Commit.OID, Reason: strings.ToLower(state)})
		}
	}
	if d.ReviewThreads.TotalCount > len(d.ReviewThreads.Nodes) {
		return rec, errors.New("more than 100 review threads; cannot evaluate replies")
	}
	return rec, nil
}
