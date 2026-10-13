package classify

import (
	"cmp"
	"strings"
	"time"
)

// Rankable carries the fields the queue sorts by.
type Rankable struct {
	Bucket       Bucket
	MilestoneDue *time.Time
	FirstTime    bool
	WaitingSince *time.Time
	Number       int
	Repo         string
}

// RankKey builds the sort key for one classified PR.
func RankKey(rec Record, bucket Bucket, waitingSince *time.Time, repo string) Rankable {
	k := Rankable{Bucket: bucket, FirstTime: IsFirstTime(rec.AuthorAssociation), WaitingSince: waitingSince, Number: rec.Number, Repo: repo}
	if rec.Milestone != nil {
		k.MilestoneDue = rec.Milestone.DueOn
	}
	return k
}

// Compare orders by bucket precedence, soonest milestone due date, first-time
// contributors, oldest waiting, PR number, then repository for stability.
func Compare(a, b Rankable) int {
	if c := cmp.Compare(a.Bucket.Order(), b.Bucket.Order()); c != 0 {
		return c
	}
	if c := compareNilLast(a.MilestoneDue, b.MilestoneDue); c != 0 {
		return c
	}
	if a.FirstTime != b.FirstTime {
		if a.FirstTime {
			return -1
		}
		return 1
	}
	if c := compareNilLast(a.WaitingSince, b.WaitingSince); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Number, b.Number); c != 0 {
		return c
	}
	return cmp.Compare(a.Repo, b.Repo)
}

func compareNilLast(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return a.Compare(*b)
}

// IsFirstTime reports whether the author association marks a first-time contributor.
func IsFirstTime(association string) bool {
	return association == "FIRST_TIME_CONTRIBUTOR" || association == "FIRST_TIMER"
}

// SizeBadge buckets changed lines: S < 50, M < 250, L < 1000, XL otherwise.
func SizeBadge(additions, deletions int) string {
	switch n := additions + deletions; {
	case n < 50:
		return "S"
	case n < 250:
		return "M"
	case n < 1000:
		return "L"
	default:
		return "XL"
	}
}

// Handoff is the per-repo line shown under a bucket, or "".
func Handoff(b Bucket, urls []string) string {
	switch b {
	case NeedsRereview, NeedsInitialReview, Bot:
		return "/reviewing-pull-requests " + strings.Join(urls, " ")
	case WaitingTestApproval:
		return "post each command only after reading that PR's diff"
	}
	return ""
}
