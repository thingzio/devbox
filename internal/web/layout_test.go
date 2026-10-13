package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/syncer"
)

func assertContains(t *testing.T, page, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("%s missing %q", page, want)
		}
	}
}

func TestLayoutBrandFooterAndFavicon(t *testing.T) {
	ts := newTestServer(t)
	for _, path := range []string{"/", "/settings"} {
		_, body := ts.get(path)
		assertContains(t, path, body, `Dev<span class="accent">Box</span>`, `class="nav-logo"`, `href="/static/favicon.svg"`,
			"Version dev", `href="https://github.com/thingzio/devbox"`, `href="/metrics"`, `href="/healthz"`, "© 2026 thingz.io")
	}
	if code, body := ts.get("/static/favicon.svg"); code != http.StatusOK || !strings.Contains(body, "<svg") {
		t.Fatalf("favicon = %d", code)
	}
}

func TestHeaderShowsSyncOnEveryPage(t *testing.T) {
	ts := newTestServer(t)
	ts.repo(ts.account("me"), "o", "r")
	_, body := ts.get("/settings")
	assertContains(t, "settings", body, `action="/refresh"`, `name="next" value="/settings"`, "Last checked never", "checking every 2m", "sync-idle")
	ts.engine.setPaused(map[string]time.Time{"me": t0.Add(30 * time.Minute)})
	_, body = ts.get("/settings")
	assertContains(t, "settings while paused", body, "sync-paused")
	ts.engine.setPaused(nil)
	ts.engine.SetPolling(syncer.Polling{Manual: true})
	_, body = ts.get("/?bucket=waiting_on_me")
	assertContains(t, "manual queue", body, "sync-manual", "manual: press Refresh", `name="next" value="/?bucket=waiting_on_me"`)
}

func TestRefreshReturnsToLocalPage(t *testing.T) {
	ts := newTestServer(t)
	cases := map[string]string{
		"/settings":                "/settings",
		"/r/o/r?bucket=bot":        "/r/o/r?bucket=bot",
		"":                         "/",
		"//evil.example/":          "/",
		"/\\evil.example":          "/",
		"https://evil.example/":    "/",
		"settings":                 "/",
		"/%2F%2Fevil.example/path": "/%2F%2Fevil.example/path",
		"/\t/evil.example":         "/",
		"/":                        "/",
	}
	for next, want := range cases {
		code, _, hdr := ts.post("/refresh", url.Values{"next": {next}})
		if code != http.StatusSeeOther || hdr.Get("Location") != want {
			t.Errorf("refresh next=%q = %d %q, want %q", next, code, hdr.Get("Location"), want)
		}
	}
}

func TestQueueTilesFilterByBucket(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 1, "Initial PR", "https://github.com/o/r/pull/1", "needs_initial_review", `{}`)
	ts.seed(r, 2, "Mine PR", "https://github.com/o/r/pull/2", "waiting_on_me", `{}`)
	_, body := ts.get("/")
	assertContains(t, "queue", body,
		`<a class="stat tone-accent" href="/" aria-current="true"><span class="stat-n">2</span><span class="stat-l">All actions</span></a>`,
		`<a class="stat tone-critical" href="/?bucket=waiting_on_me"><span class="stat-n">1</span><span class="stat-l">Waiting on me</span></a>`,
		`<a class="stat tone-low" href="/?bucket=needs_initial_review"><span class="stat-n">1</span><span class="stat-l">Needs initial review</span></a>`,
		`<a class="stat tone-high zero" href="/?bucket=waiting_test_approval"><span class="stat-n">0</span>`)
	_, body = ts.get("/?bucket=waiting_on_me&account=me")
	assertContains(t, "filtered queue", body,
		`<a class="stat tone-accent" href="/?account=me"><span class="stat-n">2</span>`,
		`<a class="stat tone-critical" href="/?account=me" aria-current="true"><span class="stat-n">1</span>`,
		`href="/?account=me&amp;bucket=needs_initial_review"`, "Mine PR")
	if strings.Contains(body, "Initial PR") {
		t.Error("bucket filter shows other buckets")
	}
}

func TestQueueSidebar(t *testing.T) {
	ts := newTestServer(t)
	me := ts.account("me")
	r := ts.repo(me, "o", "r")
	ts.repo(me, "o", "s")
	ts.seed(r, 1, "Mine PR", "https://github.com/o/r/pull/1", "waiting_on_me", `{}`)
	ts.seed(r, 2, "Ready PR", "https://github.com/o/r/pull/2", "ready_to_merge", `{}`)
	ts.seed(r, 3, "Author PR", "https://github.com/o/r/pull/3", "waiting_on_author", `{}`)
	_, body := ts.get("/")
	assertContains(t, "sidebar", body,
		"Ready PR", `href="/?bucket=ready_to_merge"`,
		`<a href="/?bucket=waiting_on_author">Waiting on author</a><span class="count">1</span>`,
		`<a class="mono" href="/r/o/r">o/r</a><span class="count">1</span>`,
		`<a class="mono" href="/r/o/s">o/s</a><span class="count">0</span>`)
	if strings.Contains(body, "Author PR") {
		t.Error("waiting-on-author PR listed without a filter")
	}
	_, body = ts.get("/?bucket=waiting_on_author")
	assertContains(t, "waiting on author", body, "Waiting on author (1)", "Author PR")
	if strings.Contains(body, "Mine PR") {
		t.Error("non-action bucket filter shows action PRs")
	}
	_, body = ts.get("/?account=me&repo=o/s")
	assertContains(t, "filtered sidebar", body, `<a class="mono" href="/r/o/r">o/r</a><span class="count">1</span>`, "Nothing matches these filters.")
}

func TestRepoPageTilesFilterByBucket(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 1, "Ready PR", "https://github.com/o/r/pull/1", "ready_to_merge", `{}`)
	ts.seed(r, 2, "Mine PR", "https://github.com/o/r/pull/2", "waiting_on_me", `{}`)
	_, body := ts.get("/r/o/r")
	assertContains(t, "repo page", body, `href="/r/o/r?bucket=waiting_on_me"`, `<a href="/">Queue</a>`, `hx-get="/fragments/r/o/r"`)
	_, body = ts.get("/r/o/r?bucket=ready_to_merge")
	assertContains(t, "filtered repo page", body, "Ready to merge (1)", `hx-get="/fragments/r/o/r?bucket=ready_to_merge"`)
	if strings.Contains(body, "Mine PR") {
		t.Error("repo bucket filter shows other buckets")
	}
	_, frag := ts.get("/fragments/r/o/r?bucket=waiting_on_me")
	if !strings.Contains(frag, "Mine PR") || strings.Contains(frag, "<html") {
		t.Fatalf("repo fragment:\n%s", frag)
	}
}

func TestLiveStatusFragment(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	_, page := ts.get("/settings")
	assertContains(t, "settings", page, `id="sync"`, `hx-get="/fragments/status"`, `hx-trigger="every 30s, refresh delay:1s"`, `<section id="alerts"`)
	if strings.Contains(page, "hx-swap-oob") {
		t.Error("full page marks alerts for an out-of-band swap")
	}
	if _, err := ts.db.RecordPoll(ts.ctx, r.ID, t0.Add(-3*time.Minute), errorString("dial tcp: network is unreachable")); err != nil {
		t.Fatal(err)
	}
	code, frag := ts.get("/fragments/status")
	if code != http.StatusOK || strings.Contains(frag, "<html") {
		t.Fatalf("status fragment = %d\n%s", code, frag)
	}
	assertContains(t, "status fragment", frag, `id="sync"`, "Last checked 3 min ago", "sync-warn",
		`<section id="alerts" class="alerts" aria-label="Status" hx-swap-oob="true">`, "network is unreachable")
	if _, err := ts.db.RecordPoll(ts.ctx, r.ID, t0, nil); err != nil {
		t.Fatal(err)
	}
	_, frag = ts.get("/fragments/status")
	assertContains(t, "status fragment after recovery", frag, "Last checked just now", "sync-ok",
		`<section id="alerts" class="alerts" aria-label="Status" hx-swap-oob="true"></section>`)
}

func TestUsernamesLinkToGitHub(t *testing.T) {
	cases := map[string]string{
		"alice":           "https://github.com/alice",
		"dependabot[bot]": "https://github.com/apps/dependabot",
		"":                "",
		"a b/c":           "https://github.com/a%20b%2Fc",
	}
	for login, want := range cases {
		if got := profileURL(login); got != want {
			t.Errorf("profileURL(%q) = %q, want %q", login, got, want)
		}
	}
	ts := newTestServer(t)
	me, work := ts.account("me"), ts.account("work")
	ts.repo(work, "o", "r")
	ts.seed(ts.repo(me, "o", "r"), 1, "One", "https://github.com/o/r/pull/1", "waiting_on_me", `{}`)
	_, body := ts.get("/")
	assertContains(t, "queue", body, `<a href="https://github.com/alice">@alice</a>`, `<a class="tag" href="https://github.com/me" title="Account">@me</a>`)
	_, body = ts.get("/r/o/r")
	assertContains(t, "repo page", body, `<a href="https://github.com/me">@me</a>, <a href="https://github.com/work">@work</a>`)
	_, body = ts.get("/r/o/r/1")
	assertContains(t, "explain page", body, `<a href="https://github.com/me">@me</a>`)
	_, body = ts.get("/settings")
	assertContains(t, "settings", body, `<a href="https://github.com/work"><strong>@work</strong></a>`)
}
