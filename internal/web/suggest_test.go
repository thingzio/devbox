package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thingzio/devbox/internal/suggest"
)

func suggestingServer(t *testing.T, result []suggest.Suggestion, err error) (*testServer, *[]string) {
	t.Helper()
	var asked []string
	ts := newTestServerWith(t, func(o *Options) {
		o.SuggestRepos = func(_ context.Context, account string) ([]suggest.Suggestion, error) {
			asked = append(asked, account)
			return result, err
		}
	})
	return ts, &asked
}

func TestSuggestRepositories(t *testing.T) {
	ts, asked := suggestingServer(t, []suggest.Suggestion{
		{Owner: "acme", Name: "api", Requested: 2, Reviewed: 1},
		{Owner: "acme", Name: "web", Reviewed: 4},
	}, nil)
	_, body := ts.get("/settings")
	if strings.Contains(body, `name="suggest"`) {
		t.Fatal("suggest offered before any account exists")
	}
	ts.account("me")
	_, body = ts.get("/settings")
	assertContains(t, "settings", body, `<form method="get" action="/settings#repos"`, `name="suggest"`, "Suggest repositories")
	if len(*asked) != 0 {
		t.Fatal("searched GitHub without being asked")
	}

	code, body := ts.get("/settings?suggest=me")
	if code != http.StatusOK || len(*asked) != 1 || (*asked)[0] != "me" {
		t.Fatalf("suggest = %d, asked %v", code, *asked)
	}
	assertContains(t, "suggestions", body, "Suggested for @me",
		`<a href="https://github.com/acme/api" target="_blank" rel="noopener noreferrer">acme/api</a>`,
		"2 open pull requests request your review · you reviewed 1 in the last 30 days",
		`<input type="hidden" name="repo" value="acme/web">`, `<input type="hidden" name="suggest" value="me">`)

	code, _, h := ts.post("/repos", url.Values{"account": {"me"}, "repo": {"acme/api"}, "suggest": {"me"}})
	if code != http.StatusSeeOther || h.Get("Location") != "/settings?suggest=me#repos" {
		t.Fatalf("add from suggestions = %d → %q", code, h.Get("Location"))
	}
	if repos, _ := ts.db.Repos(ts.ctx); len(repos) != 1 || repos[0].FullName() != "acme/api" {
		t.Fatalf("repos = %+v", repos)
	}
}

func TestSuggestRepositoriesMessages(t *testing.T) {
	cases := []struct {
		name, account string
		result        []suggest.Suggestion
		err           error
		want          string
	}{
		{"none", "me", nil, nil, "No suggestions for @me"},
		{"failure", "me", nil, errors.New("rate limited"), "could not suggest repositories for @me: rate limited"},
		{"unknown account", "ghost", nil, nil, "no account @ghost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := suggestingServer(t, tc.result, tc.err)
			ts.account("me")
			code, body := ts.get("/settings?suggest=" + tc.account)
			if code != http.StatusOK || !strings.Contains(body, tc.want) {
				t.Fatalf("suggest = %d, want %q in\n%s", code, tc.want, body)
			}
		})
	}
}
