package web

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/hub"
)

func TestQueueRendersRanksAndEscapes(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 1, "<script>alert(1)</script>", "https://github.com/o/r/pull/1", "needs_initial_review",
		`{"number":1,"author_association":"FIRST_TIMER","additions":300,"deletions":10,"milestone":{"title":"v1","due_on":"2026-10-20T00:00:00Z"}}`)
	ts.seed(r, 2, "Plain title", "javascript:alert(1)", "waiting_on_me", `{"number":2}`)
	code, body := ts.get("/")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{
		"&lt;script&gt;alert(1)&lt;/script&gt;", "first-time", ">L<", ">v1<", "2 days", "@me",
		`href="https://github.com/o/r/pull/1#discussion_r1"`, "approved by @bob", "2 actions: 1 Waiting on me, 1 Needs initial review",
		`hx-get="/fragments/queue"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	for _, banned := range []string{"<script>alert(1)", "javascript:alert", `href="#ZgotmplZ"`} {
		if strings.Contains(body, banned) {
			t.Errorf("body contains %q", banned)
		}
	}
	if strings.Index(body, "Waiting on me (1)") > strings.Index(body, "Needs initial review (1)") {
		t.Error("buckets not in precedence order")
	}
}

func TestQueueEmptyStates(t *testing.T) {
	ts := newTestServer(t)
	if _, body := ts.get("/"); !strings.Contains(body, "Add a GitHub account in Settings") {
		t.Fatalf("no-account message missing:\n%s", body)
	}
	a := ts.account("me")
	if _, body := ts.get("/"); !strings.Contains(body, "Add a repository in Settings") {
		t.Fatal("no-repo message missing")
	}
	ts.repo(a, "o", "r")
	if _, body := ts.get("/"); !strings.Contains(body, "Nothing is waiting on you.") {
		t.Fatal("all-clear message missing")
	}
}

func TestQueueFiltersAndFragment(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 1, "First", "https://github.com/o/r/pull/1", "needs_initial_review", `{}`)
	ts.seed(r, 2, "Second", "https://github.com/o/r/pull/2", "waiting_on_me", `{}`)
	_, body := ts.get("/?bucket=waiting_on_me")
	if strings.Contains(body, "First") || !strings.Contains(body, "Second") || !strings.Contains(body, `hx-get="/fragments/queue?bucket=waiting_on_me"`) {
		t.Fatalf("bucket filter failed:\n%s", body)
	}
	code, frag := ts.get("/fragments/queue?repo=o/r")
	if code != http.StatusOK || strings.Contains(frag, "<html") || !strings.Contains(frag, "First") {
		t.Fatalf("fragment = %d\n%s", code, frag)
	}
}

func TestStatusBarShowsStaleRepo(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	if _, err := ts.db.RecordPoll(ts.ctx, r.ID, t0, errorString("dial tcp: network is unreachable")); err != nil {
		t.Fatal(err)
	}
	ts.setNow(t0.Add(10 * time.Minute))
	_, body := ts.get("/")
	if !strings.Contains(body, "no successful poll yet") || !strings.Contains(body, "network is unreachable") {
		t.Fatalf("stale status missing:\n%s", body)
	}
	if _, err := ts.db.RecordPoll(ts.ctx, r.ID, ts.clock(), nil); err != nil {
		t.Fatal(err)
	}
	if _, body := ts.get("/"); strings.Contains(body, "no successful poll yet") || strings.Contains(body, "network is unreachable") {
		t.Fatal("status not cleared after a successful poll")
	}
	ts.setNow(ts.clock().Add(4 * time.Minute))
	if _, body := ts.get("/"); !strings.Contains(body, "last successful poll 4m ago") {
		t.Fatal("staleness not reported after 3 minutes")
	}
}

func TestStatusBarShowsAuthAndRateProblems(t *testing.T) {
	ts := newTestServer(t)
	ts.account("me")
	if err := ts.db.SetAccountStatus(ts.ctx, "me", "needs_auth", "token rejected by GitHub"); err != nil {
		t.Fatal(err)
	}
	ts.engine.setPaused(map[string]time.Time{"me": t0.Add(30 * time.Minute)})
	_, body := ts.get("/")
	if !strings.Contains(body, "@me needs re-authentication: token rejected by GitHub") || !strings.Contains(body, "paused for rate limits until 12:30") {
		t.Fatalf("auth/rate banners missing:\n%s", body)
	}
}

func TestRepoPageShowsBothAccounts(t *testing.T) {
	ts := newTestServer(t)
	me, work := ts.account("me"), ts.account("work")
	ts.seed(ts.repo(me, "NVIDIA", "aicr"), 7, "Mine", "https://github.com/NVIDIA/aicr/pull/7", "needs_initial_review", `{}`)
	ts.seed(ts.repo(work, "NVIDIA", "aicr"), 7, "Theirs", "https://github.com/NVIDIA/aicr/pull/7", "waiting_on_me", `{}`)
	code, body := ts.get("/r/nvidia/AICR")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{"@me", "@work", "/reviewing-pull-requests https://github.com/NVIDIA/aicr/pull/7", `class="copy"`, "Waiting on me (1)", "Needs initial review (1)"} {
		if !strings.Contains(body, want) {
			t.Errorf("repo page missing %q", want)
		}
	}
	if code, _ := ts.get("/r/nobody/nothing"); code != http.StatusNotFound {
		t.Fatalf("unknown repo status = %d", code)
	}
}

func TestExplainPage(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 3, "Explain me", "https://github.com/o/r/pull/3", "needs_initial_review",
		`{"number":3,"author":"alice","fork":true,"merge_state":"CLEAN","signature_policy":"absent","evidence_errors":[{"field":"diff","message":"timeout","retryable":true}]}`)
	code, body := ts.get("/r/o/r/3")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{"Explain me", "needs_initial_review", "Fork", "yes", "diff: timeout (retry pending)", "→ needs_initial_review"} {
		if !strings.Contains(body, want) {
			t.Errorf("explain page missing %q", want)
		}
	}
	if code, _ := ts.get("/r/o/r/999"); code != http.StatusNotFound {
		t.Fatalf("unknown PR status = %d", code)
	}
	if code, _ := ts.get("/r/o/r/abc"); code != http.StatusNotFound {
		t.Fatalf("non-numeric PR status = %d", code)
	}
}

func TestSecurityHeadersHostAndOrigin(t *testing.T) {
	ts := newTestServer(t)
	resp, err := ts.client.Get(ts.http.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'", "X-Content-Type-Options": "nosniff",
		"Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	req, _ := http.NewRequestWithContext(ts.ctx, http.MethodGet, ts.http.URL+"/", nil)
	req.Host = "evil.example"
	if resp, err := ts.client.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebinding host status = %v, %v", resp.StatusCode, err)
	}
	cross, _ := http.NewRequestWithContext(ts.ctx, http.MethodPost, ts.http.URL+"/repos", strings.NewReader(url.Values{"repo": {"o/r"}}.Encode()))
	cross.Header.Set("Origin", "https://evil.example")
	if resp, err := ts.client.Do(cross); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST status = %v, %v", resp.StatusCode, err)
	}
	bare, _ := http.NewRequestWithContext(ts.ctx, http.MethodPost, ts.http.URL+"/repos", nil)
	if resp, err := ts.client.Do(bare); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST without Origin status = %v, %v", resp.StatusCode, err)
	}
}

func TestEventsStream(t *testing.T) {
	ts := newTestServer(t)
	ctx, cancel := context.WithCancel(ts.ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.http.URL+"/events", nil)
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", resp.Header.Get("Content-Type"))
	}
	deadline := time.Now().Add(2 * time.Second)
	for ts.hub.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	ts.hub.Publish(hub.Event{RepoID: 4, Number: 9})
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() {
		if lines.Text() == `data: {"repo":4,"number":9}` {
			return
		}
	}
	t.Fatal("event not delivered")
}

func TestStaticAssetsMetricsHealth(t *testing.T) {
	ts := newTestServer(t)
	if code, body := ts.get("/static/app.js"); code != http.StatusOK || !strings.Contains(body, "EventSource") {
		t.Fatalf("app.js = %d", code)
	}
	if code, body := ts.get("/metrics"); code != http.StatusOK || !strings.Contains(body, "devbox_up") {
		t.Fatalf("metrics = %d %q", code, body)
	}
	if code, body := ts.get("/healthz"); code != http.StatusOK || body != "ok" {
		t.Fatalf("healthz = %d %q", code, body)
	}
	data, err := os.ReadFile("static/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717" {
		t.Fatal("vendored htmx.min.js does not match the pinned 2.0.11 checksum")
	}
}

func TestAge(t *testing.T) {
	now := t0
	cases := map[time.Duration]string{0: "today", 23 * time.Hour: "today", 24 * time.Hour: "1 day", 49 * time.Hour: "2 days", -time.Hour: "today"}
	for d, want := range cases {
		since := now.Add(-d)
		if got := age(now, &since); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
	if age(now, nil) != "—" {
		t.Error("nil since")
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// TestBrowserPostWithNullOrigin reproduces what browsers send under
// Referrer-Policy: no-referrer: form and fetch POSTs carry "Origin: null"
// plus Sec-Fetch-Site, which pages cannot forge.
func TestBrowserPostWithNullOrigin(t *testing.T) {
	ts := newTestServer(t)
	ts.account("me")
	send := func(origin, fetchSite string) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(ts.ctx, http.MethodPost, ts.http.URL+"/repos",
			strings.NewReader(url.Values{"account": {"me"}, "repo": {"o/r"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		resp, err := ts.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := send("null", "same-origin"); code != http.StatusSeeOther {
		t.Fatalf("browser same-origin POST with Origin: null = %d, want 303", code)
	}
	for _, tc := range []struct{ origin, site string }{
		{"null", ""}, {"null", "cross-site"}, {"https://evil.example", "same-origin"}, {"https://evil.example", "cross-site"},
	} {
		if code := send(tc.origin, tc.site); code != http.StatusForbidden {
			t.Errorf("POST Origin=%q Sec-Fetch-Site=%q = %d, want 403", tc.origin, tc.site, code)
		}
	}
}
