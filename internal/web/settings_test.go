package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/rules"
)

func TestParseRepoReference(t *testing.T) {
	ok := map[string]string{
		"NVIDIA/aicr":                               "NVIDIA/aicr",
		"  NVIDIA/aicr  ":                           "NVIDIA/aicr",
		"https://github.com/NVIDIA/aicr":            "NVIDIA/aicr",
		"https://github.com/NVIDIA/aicr/":           "NVIDIA/aicr",
		"http://www.github.com/NVIDIA/aicr.git":     "NVIDIA/aicr",
		"github.com/NVIDIA/aicr/pull/12":            "NVIDIA/aicr",
		"https://github.com/NVIDIA/aicr/pulls?q=is": "NVIDIA/aicr",
		"o/repo.with_dots-and_underscores":          "o/repo.with_dots-and_underscores",
	}
	for in, want := range ok {
		owner, name, err := parseRepoReference(in)
		if err != nil || owner+"/"+name != want {
			t.Errorf("parseRepoReference(%q) = %s/%s, %v; want %s", in, owner, name, err, want)
		}
	}
	for _, in := range []string{"", "NVIDIA", "gitlab.com/o/r", "https://gitlab.com/o/r", "-bad/r", "o/r r", "o/..", "git@github.com:o/r.git", "o/" + strings.Repeat("x", 101)} {
		if owner, name, err := parseRepoReference(in); err == nil {
			t.Errorf("parseRepoReference(%q) = %s/%s, want error", in, owner, name)
		}
	}
}

func TestAddRepoFlow(t *testing.T) {
	ts := newTestServer(t)
	ts.account("me")
	code, _, hdr := ts.post("/repos", url.Values{"account": {"me"}, "repo": {"https://github.com/NVIDIA/aicr/pull/12"}})
	if code != http.StatusSeeOther || hdr.Get("Location") != "/settings" {
		t.Fatalf("add repo = %d %v", code, hdr)
	}
	repos, _ := ts.db.Repos(ts.ctx)
	if len(repos) != 1 || repos[0].FullName() != "NVIDIA/aicr" || ts.engine.reposChangedCount() != 1 {
		t.Fatalf("repos = %+v, reposChanged = %d", repos, ts.engine.reposChangedCount())
	}
	code, body, _ := ts.post("/repos", url.Values{"account": {"me"}, "repo": {"nvidia/AICR"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "already monitored") {
		t.Fatalf("duplicate = %d\n%s", code, body)
	}
	code, body, _ = ts.post("/repos", url.Values{"account": {"me"}, "repo": {"gitlab.com/o/r"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "not a github.com repository") {
		t.Fatalf("foreign host = %d\n%s", code, body)
	}
	if code, _, _ := ts.post("/repos", url.Values{"account": {"nobody"}, "repo": {"o/r"}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown account = %d", code)
	}
}

func TestRepoActions(t *testing.T) {
	ts := newTestServer(t)
	r := ts.repo(ts.account("me"), "o", "r")
	ts.seed(r, 1, "One", "https://github.com/o/r/pull/1", "needs_initial_review", `{}`)
	id := "/repos/" + itoa(r.ID)
	if code, _, _ := ts.post(id+"/disable", nil); code != http.StatusSeeOther {
		t.Fatalf("disable = %d", code)
	}
	if got, _ := ts.db.Repo(ts.ctx, r.ID); got.Enabled {
		t.Fatal("repo still enabled")
	}
	if code, _, _ := ts.post(id+"/enable", nil); code != http.StatusSeeOther {
		t.Fatalf("enable = %d", code)
	}
	events, unsubscribe := ts.hub.Subscribe()
	defer unsubscribe()
	code, body, _ := ts.post(id+"/rules", url.Values{"rules": {`{"stale": 3}`}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "unknown field") {
		t.Fatalf("invalid rules = %d\n%s", code, body)
	}
	code, _, _ = ts.post(id+"/rules", url.Values{"rules": {`{"test_approval":{"marker_checks":["tests"],"command":"/ok-to-test"},"stale_days":3}`}})
	if code != http.StatusSeeOther {
		t.Fatalf("valid rules = %d", code)
	}
	if calls := ts.engine.rules(); len(calls) != 1 || !calls[0].collectionChanged || calls[0].repoID != r.ID {
		t.Fatalf("rules calls = %+v", calls)
	}
	select {
	case ev := <-events:
		if ev != (hub.Event{RepoID: r.ID, Number: 1}) {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("rules edit did not publish the refresh transition")
	}
	if code, _, _ := ts.post(id+"/frobnicate", nil); code != http.StatusNotFound {
		t.Fatalf("unknown action = %d", code)
	}
	if code, _, _ := ts.post(id+"/delete", nil); code != http.StatusSeeOther {
		t.Fatalf("delete = %d", code)
	}
	if repos, _ := ts.db.Repos(ts.ctx); len(repos) != 0 {
		t.Fatalf("repo survived delete: %+v", repos)
	}
	if code, _, _ := ts.post(id+"/enable", nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("action on deleted repo = %d", code)
	}
}

func TestAccountFlows(t *testing.T) {
	ts := newTestServer(t)
	code, body, _ := ts.post("/accounts/pat", url.Values{"token": {"github_pat_123"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "fine-grained tokens cannot read notifications") || strings.Contains(body, "github_pat_123") {
		t.Fatalf("fine-grained = %d\n%s", code, body)
	}
	if code, _, _ := ts.post("/accounts/pat", url.Values{"token": {"ghp_ok"}}); code != http.StatusSeeOther {
		t.Fatalf("classic token = %d", code)
	}
	_, settings := ts.get("/settings")
	if !strings.Contains(settings, "@me") || !strings.Contains(settings, "pat") || !strings.Contains(settings, "Sign in with GitHub") {
		t.Fatalf("settings page:\n%s", settings)
	}
	if code, _, _ := ts.post("/accounts/me/delete", nil); code != http.StatusSeeOther {
		t.Fatalf("remove = %d", code)
	}
	if accounts, _ := ts.db.Accounts(ts.ctx); len(accounts) != 0 {
		t.Fatalf("account survived removal: %+v", accounts)
	}
	ts.setScopes("read:org")
	if code, body, _ := ts.post("/accounts/pat", url.Values{"token": {"ghp_noscope"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "repo scope") {
		t.Fatalf("missing scope = %d\n%s", code, body)
	}
}

var flowIDPattern = regexp.MustCompile(`hx-get="/accounts/device/([A-Z2-7]+)"`)

func TestDeviceFlowFragments(t *testing.T) {
	ts := newTestServer(t)
	code, frag, _ := ts.post("/accounts/device", nil)
	if code != http.StatusOK || !strings.Contains(frag, "ABCD-1234") || !strings.Contains(frag, `href="https://github.com/login/device"`) {
		t.Fatalf("start fragment = %d\n%s", code, frag)
	}
	m := flowIDPattern.FindStringSubmatch(frag)
	if m == nil {
		t.Fatalf("no polling URL in fragment:\n%s", frag)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, status := ts.get("/accounts/device/" + m[1])
		if strings.Contains(status, "Added @me") {
			if strings.Contains(status, "hx-trigger") {
				t.Fatal("finished fragment keeps polling")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("device flow did not finish:\n%s", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, body := ts.get("/accounts/device/UNKNOWN"); !strings.Contains(body, "start again") {
		t.Fatalf("unknown flow:\n%s", body)
	}
}

func TestImportFlow(t *testing.T) {
	ts := newTestServer(t)
	ts.account("me")
	dir := filepath.Join(ts.importDir, "NVIDIA")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	overlay := `{"test_approval":{"marker_checks":["tests / Test","tests-skip"],"command":"/ok-to-test","ci_paths":[".github/"],"ci_paths_command":"/ok-to-test confirm-ci"},"stale_days":7}`
	if err := os.WriteFile(filepath.Join(dir, "aicr.json"), []byte(overlay), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, settings := ts.get("/settings")
	if !strings.Contains(settings, "NVIDIA/aicr.json") || strings.Contains(settings, "notes.txt") {
		t.Fatalf("import candidates:\n%s", settings)
	}
	if code, _, _ := ts.post("/import", url.Values{"file": {"NVIDIA/aicr.json"}, "account": {"me"}}); code != http.StatusSeeOther {
		t.Fatalf("import = %d", code)
	}
	repos, _ := ts.db.Repos(ts.ctx)
	want, _ := rules.Parse([]byte(overlay))
	if len(repos) != 1 || repos[0].FullName() != "NVIDIA/aicr" || repos[0].Rules.CollectionHash() != want.CollectionHash() {
		t.Fatalf("imported repos = %+v", repos)
	}
	for _, bad := range []string{"../etc/passwd.json", "NVIDIA/missing.json", "NVIDIA/notes.txt"} {
		if code, _, _ := ts.post("/import", url.Values{"file": {bad}, "account": {"me"}}); code != http.StatusUnprocessableEntity {
			t.Errorf("import %q = %d, want 422", bad, code)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestParseRepoReferenceRejectsReservedPaths(t *testing.T) {
	for _, in := range []string{
		"https://github.com/orgs/NVIDIA/repositories",
		"github.com/settings/tokens",
		"https://github.com/users/mchmarny/projects",
		"https://github.com/topics/kubernetes",
		"https://github.com/search/code",
		"https://github.com/notifications/beta",
		"https://github.com/marketplace/actions",
		"https://github.com/sponsors/someone",
		"https://github.com/apps/dependabot",
		"https://github.com/enterprises/acme",
	} {
		if owner, name, err := parseRepoReference(in); err == nil {
			t.Errorf("parseRepoReference(%q) = %s/%s, want error for a reserved GitHub path", in, owner, name)
		}
	}
}

func TestSettingsShowsRateRemaining(t *testing.T) {
	ts := newTestServer(t)
	ts.account("me")
	ts.engine.mu.Lock()
	ts.engine.rates = map[string][]github.Rate{"me": {
		{Resource: "core", Limit: 5000, Remaining: 4990},
		{Resource: "graphql", Limit: 5000, Remaining: 4812},
	}}
	ts.engine.mu.Unlock()
	_, body := ts.get("/settings")
	if !strings.Contains(body, "core 4990/5000, graphql 4812/5000") {
		t.Fatalf("rate remaining missing from settings:\n%s", body)
	}
}
