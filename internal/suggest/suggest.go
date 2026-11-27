// Package suggest proposes repositories to watch from the account's recent
// review activity on GitHub. It only reads: two searches and a permission
// check per candidate.
package suggest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/collect"
)

// Max is how many repositories are suggested at most.
const Max = 5

const (
	candidates      = 2 * Max // repositories checked for push access, best first
	requestedWeight = 3       // an open review request outweighs a past review
	reviewWindow    = 30 * 24 * time.Hour
)

// Reader is the GitHub access suggestions need; collect.GitHubReader implements it.
type Reader interface {
	SearchPullRequests(ctx context.Context, query string) ([]string, error)
	Repository(ctx context.Context, owner, name string) (collect.RepoInfo, error)
}

// Suggestion is a repository worth watching and the evidence for it.
type Suggestion struct {
	Owner, Name string
	Requested   int // open pull requests requesting the user's review, directly or through a team
	Reviewed    int // pull requests the user reviewed in the last 30 days
}

// Reason explains a suggestion in one line.
func (s Suggestion) Reason() string {
	var parts []string
	if s.Requested > 0 {
		verb := "requests"
		if s.Requested > 1 {
			verb = "request"
		}
		parts = append(parts, fmt.Sprintf("%d open %s %s your review", s.Requested, pullRequests(s.Requested), verb))
	}
	if s.Reviewed > 0 {
		if s.Requested > 0 {
			parts = append(parts, fmt.Sprintf("you reviewed %d in the last 30 days", s.Reviewed))
		} else {
			parts = append(parts, fmt.Sprintf("you reviewed %d %s in the last 30 days", s.Reviewed, pullRequests(s.Reviewed)))
		}
	}
	return strings.Join(parts, " · ")
}

func pullRequests(n int) string {
	if n == 1 {
		return "pull request"
	}
	return "pull requests"
}

func (s Suggestion) score() int { return requestedWeight*s.Requested + s.Reviewed }

// Suggest ranks repositories by the user's open review requests and recent
// reviews, skipping watched ones (owner/name, any case), and keeps up to Max
// that the user can push to and that are not archived.
func Suggest(ctx context.Context, r Reader, watched []string, now time.Time) ([]Suggestion, error) {
	requested, err := r.SearchPullRequests(ctx, "is:pr is:open archived:false review-requested:@me")
	if err != nil {
		return nil, fmt.Errorf("search review requests: %w", err)
	}
	since := now.Add(-reviewWindow).UTC().Format("2006-01-02")
	reviewed, err := r.SearchPullRequests(ctx, "is:pr reviewed-by:@me updated:>="+since)
	if err != nil {
		return nil, fmt.Errorf("search recent reviews: %w", err)
	}
	skip := map[string]bool{}
	for _, w := range watched {
		skip[strings.ToLower(w)] = true
	}
	tally := map[string]*Suggestion{}
	count := func(repos []string, field func(*Suggestion) *int) {
		for _, full := range repos {
			key := strings.ToLower(full)
			if skip[key] {
				continue
			}
			s, ok := tally[key]
			if !ok {
				owner, name, _ := strings.Cut(full, "/")
				s = &Suggestion{Owner: owner, Name: name}
				tally[key] = s
			}
			*field(s)++
		}
	}
	count(requested, func(s *Suggestion) *int { return &s.Requested })
	count(reviewed, func(s *Suggestion) *int { return &s.Reviewed })

	ranked := make([]Suggestion, 0, len(tally))
	for _, s := range tally {
		ranked = append(ranked, *s)
	}
	slices.SortFunc(ranked, func(a, b Suggestion) int {
		return cmp.Or(cmp.Compare(b.score(), a.score()), cmp.Compare(b.Requested, a.Requested),
			cmp.Compare(strings.ToLower(a.Owner+"/"+a.Name), strings.ToLower(b.Owner+"/"+b.Name)))
	})

	var out []Suggestion
	for _, s := range ranked[:min(len(ranked), candidates)] {
		if len(out) == Max {
			break
		}
		info, err := r.Repository(ctx, s.Owner, s.Name)
		if err != nil || !info.Push || info.Archived || skip[strings.ToLower(info.Owner+"/"+info.Name)] {
			continue // unreadable, not maintained by the user, archived, or watched under its new name
		}
		s.Owner, s.Name = info.Owner, info.Name
		out = append(out, s)
	}
	return out, nil
}
