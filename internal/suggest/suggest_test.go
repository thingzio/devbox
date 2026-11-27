package suggest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/collect"
)

var now = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

type fakeReader struct {
	searches map[string][]string
	repos    map[string]collect.RepoInfo
	queries  []string
	looked   []string
	err      error
}

func (f *fakeReader) SearchPullRequests(_ context.Context, q string) ([]string, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	for prefix, hits := range f.searches {
		if strings.Contains(q, prefix) {
			return hits, nil
		}
	}
	return nil, nil
}

func (f *fakeReader) Repository(_ context.Context, owner, name string) (collect.RepoInfo, error) {
	f.looked = append(f.looked, owner+"/"+name)
	info, ok := f.repos[strings.ToLower(owner+"/"+name)]
	if !ok {
		return collect.RepoInfo{}, errors.New("not found")
	}
	return info, nil
}

func push(owner, name string) collect.RepoInfo {
	return collect.RepoInfo{Owner: owner, Name: name, Push: true}
}

func TestSuggestRanksMaintainedRepositories(t *testing.T) {
	r := &fakeReader{
		searches: map[string][]string{
			"review-requested:@me": {"acme/api", "acme/api", "acme/web", "other/lib"},
			"reviewed-by:@me":      {"acme/web", "acme/web", "acme/web", "acme/web", "acme/docs", "watched/repo", "old/archive", "Acme/API"},
		},
		repos: map[string]collect.RepoInfo{
			"acme/api":     push("acme", "api"),
			"acme/web":     push("acme", "web"),
			"acme/docs":    push("acme", "docs"),
			"other/lib":    {Owner: "other", Name: "lib"},
			"old/archive":  {Owner: "old", Name: "archive", Push: true, Archived: true},
			"watched/repo": push("watched", "repo"),
		},
	}
	got, err := Suggest(context.Background(), r, []string{"Watched/Repo"}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []Suggestion{
		{Owner: "acme", Name: "api", Requested: 2, Reviewed: 1},
		{Owner: "acme", Name: "web", Requested: 1, Reviewed: 4},
		{Owner: "acme", Name: "docs", Reviewed: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("suggestion %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if !strings.Contains(r.queries[0], "is:pr") || !strings.Contains(r.queries[1], "updated:>=2026-10-28") {
		t.Errorf("queries = %q", r.queries)
	}
	for _, repo := range r.looked {
		if strings.EqualFold(repo, "watched/repo") {
			t.Error("checked a repository that is already watched")
		}
	}
}

func TestSuggestLimits(t *testing.T) {
	var hits []string
	repos := map[string]collect.RepoInfo{}
	for _, n := range "abcdefghijkl" {
		name := "r" + string(n)
		hits = append(hits, "o/"+name)
		repos["o/"+name] = push("o", name)
	}
	r := &fakeReader{searches: map[string][]string{"review-requested:@me": hits}, repos: repos}
	got, err := Suggest(context.Background(), r, nil, now)
	if err != nil || len(got) != Max {
		t.Fatalf("got %d suggestions, %v; want %d", len(got), err, Max)
	}
	if len(r.looked) > Max {
		t.Fatalf("checked %d repositories to fill %d slots that all qualified", len(r.looked), Max)
	}
}

func TestSuggestSearchFailure(t *testing.T) {
	if _, err := Suggest(context.Background(), &fakeReader{err: errors.New("rate limited")}, nil, now); err == nil {
		t.Fatal("search failure swallowed")
	}
}

func TestReason(t *testing.T) {
	cases := map[Suggestion]string{
		{Requested: 1}:              "1 open pull request requests your review",
		{Requested: 3, Reviewed: 1}: "3 open pull requests request your review · you reviewed 1 in the last 30 days",
		{Reviewed: 4}:               "you reviewed 4 pull requests in the last 30 days",
	}
	for s, want := range cases {
		if got := s.Reason(); got != want {
			t.Errorf("Reason(%+v) = %q, want %q", s, got, want)
		}
	}
}
