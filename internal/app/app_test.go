package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/notify"
	"github.com/thingzio/devbox/internal/store"
	"github.com/thingzio/devbox/internal/syncer"
)

const fingerprintsJSON = `{"data":{"repository":{"pullRequests":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
{"number":7,"updatedAt":"2026-10-01T09:30:00Z","baseRefOid":"b1","headRefOid":"h1","mergeStateStatus":"CLEAN","reviewDecision":null,"isDraft":false,
"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}}]}}}}`

const detailJSON = `{"data":{"repository":{"pullRequest":{"number":7,"title":"Fix flaky test","url":"https://github.com/o/r/pull/7",
"isDraft":false,"isCrossRepository":false,"createdAt":"2026-10-01T08:00:00Z","updatedAt":"2026-10-01T09:30:00Z",
"authorAssociation":"CONTRIBUTOR","additions":10,"deletions":2,"milestone":null,"author":{"login":"alice","__typename":"User"},
"baseRefName":"main","baseRefOid":"b1","headRefOid":"h1","mergeStateStatus":"CLEAN","reviewDecision":null,"autoMergeRequest":null,
"reviewRequests":{"nodes":[]},"myReviews":{"nodes":[]},"latestOpinionatedReviews":{"nodes":[]},
"reviewThreads":{"totalCount":0,"nodes":[]},"comments":{"nodes":[]},
"commits":{"totalCount":1,"nodes":[{"commit":{"oid":"h1","signature":{"state":"VALID"}}}]},
"head":{"nodes":[{"commit":{"oid":"h1","committedDate":"2026-10-01T09:00:00Z","statusCheckRollup":{"state":"SUCCESS"}}}]}}}}}`

func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Header().Set("X-OAuth-Scopes", "repo, read:org")
			fmt.Fprint(w, `{"login":"me"}`)
		case "/graphql":
			body, _ := io.ReadAll(r.Body)
			switch {
			case strings.Contains(string(body), "query Fingerprints"):
				fmt.Fprint(w, fingerprintsJSON)
			case strings.Contains(string(body), "query PullRequestDetail"):
				fmt.Fprint(w, detailJSON)
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/repos/o/r/rules/branches/main":
			fmt.Fprint(w, `[]`)
		case "/repos/o/r/branches/main/protection/required_signatures":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Branch not protected"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func baseConfig(t *testing.T) Config {
	return Config{
		Addr: "127.0.0.1:0", DBPath: filepath.Join(t.TempDir(), "devbox.db"), Polling: syncer.Polling{Interval: time.Minute},
		GitHubAPIURL: github.DefaultAPIURL, GitHubURL: "https://github.com", Secrets: auth.NewMemSecrets(),
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Config){
		"poll interval below minimum": func(c *Config) { c.Polling = syncer.Polling{Interval: 30 * time.Second} },
		"poll interval above maximum": func(c *Config) { c.Polling = syncer.Polling{Interval: 20 * time.Minute} },
		"non-loopback without opt-in": func(c *Config) { c.Addr = "0.0.0.0:8484" },
		"malformed address":           func(c *Config) { c.Addr = "8484" },
		"bad API URL":                 func(c *Config) { c.GitHubAPIURL = "ftp://x" },
		"missing database path":       func(c *Config) { c.DBPath = "" },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig(t)
			mod(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	cfg := baseConfig(t)
	cfg.Addr, cfg.AllowRemote = "0.0.0.0:8484", true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("allow-remote rejected: %v", err)
	}
	cfg = baseConfig(t)
	cfg.Addr = "localhost:8484"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("localhost rejected: %v", err)
	}
}

func TestRunFailsWhenPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := baseConfig(t)
	cfg.Addr = ln.Addr().String()
	start := time.Now()
	err = Run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("err = %v, want a listen error", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("port conflict was not reported promptly")
	}
}

func TestRunEndToEnd(t *testing.T) {
	gh := fakeGitHub(t)
	cfg := baseConfig(t)
	cfg.GitHubAPIURL, cfg.GitHubURL = gh.URL, gh.URL
	cfg.EngineConfig = syncer.Config{DispatchEvery: 20 * time.Millisecond, ResyncEvery: 50 * time.Millisecond}
	ready := make(chan string, 1)
	cfg.Ready = func(addr string) { ready <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	var base string
	select {
	case addr := <-ready:
		base = "http://" + addr
	case err := <-done:
		t.Fatalf("Run exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server not ready")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, form url.Values) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", base)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST %s = %d", path, resp.StatusCode)
		}
	}
	post("/accounts/pat", url.Values{"token": {"ghp_e2e"}})
	post("/repos", url.Values{"account": {"me"}, "repo": {"o/r"}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), "Fix flaky test") && strings.Contains(string(body), "Needs initial review (1)") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue never showed the PR:\n%s", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not shut down")
	}
}

func TestRunUsesSavedPollingOverDefault(t *testing.T) {
	cfg := baseConfig(t)
	db, err := store.Open(context.Background(), cfg.DBPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(context.Background(), store.SettingPolling, "manual", time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	cfg.Polling = syncer.Polling{Interval: 5 * time.Minute}
	ready := make(chan string, 1)
	cfg.Ready = func(addr string) { ready <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("Run exited: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("not ready")
	}
	get := func(path string) string {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if body := get("/"); !strings.Contains(body, "manual: press Refresh") {
		t.Errorf("saved manual choice not applied:\n%s", body)
	}
	if body := get("/settings"); !strings.Contains(body, `<option value="default">Default (5m)</option>`) ||
		!strings.Contains(body, `<option value="manual" selected>`) {
		t.Errorf("settings do not show flag default and saved override")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

type brokenSecrets struct{ auth.Secrets }

func (brokenSecrets) Get(string) (string, error) {
	return "", fmt.Errorf("%w: locked", auth.ErrKeychainUnavailable)
}

func TestAccountToken(t *testing.T) {
	stored := auth.NewMemSecrets()
	_ = stored.Set("me", "tok")
	_ = stored.Set("expired", `{"access_token":"a","refresh_token":"r","expires_at":"2000-01-01T00:00:00Z"}`)
	cases := []struct {
		name      string
		secrets   auth.Secrets
		account   string
		want      string
		noToken   bool
		transient bool
	}{
		{name: "stored", secrets: stored, account: "me", want: "tok"},
		{name: "missing needs re-authentication", secrets: stored, account: "other", noToken: true},
		{name: "locked keychain is transient", secrets: brokenSecrets{}, account: "me", transient: true},
		{name: "unrenewable grant needs re-authentication", secrets: stored, account: "expired", noToken: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := accountToken(context.Background(), auth.NewTokens(tc.secrets, nil, nil), tc.account)
			if got != tc.want {
				t.Fatalf("token = %q", got)
			}
			if errors.Is(err, syncer.ErrNoToken) != tc.noToken {
				t.Fatalf("err = %v, want ErrNoToken %v", err, tc.noToken)
			}
			if errors.Is(err, auth.ErrKeychainUnavailable) != tc.transient {
				t.Fatalf("err = %v, want keychain unavailable %v", err, tc.transient)
			}
		})
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunRemoteRequiresTheStartupToken(t *testing.T) {
	cfg := baseConfig(t)
	cfg.AllowRemote = true
	var notice, logs syncBuffer
	cfg.Notice = &notice
	cfg.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ready := make(chan string, 1)
	cfg.Ready = func(addr string) { ready <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("Run exited: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("not ready")
	}
	m := regexp.MustCompile(`http://\S+/\?token=(\S+)`).FindStringSubmatch(notice.String())
	if m == nil {
		t.Fatalf("no access URL printed: %q", notice.String())
	}
	token := m[1]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status := func(path string) int {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := status("/"); got != http.StatusUnauthorized {
		t.Errorf("GET / without token = %d", got)
	}
	if got := status("/?token=" + token); got != http.StatusSeeOther {
		t.Errorf("GET / with token = %d", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(logs.String(), token) {
		t.Fatal("access token written to the log")
	}
}

func TestRunWiresDesktopNotifications(t *testing.T) {
	cfg := baseConfig(t)
	sent := make(chan notify.Message, 1)
	cfg.Desktop = func(_ context.Context, m notify.Message) error { sent <- m; return nil }
	ready := make(chan string, 1)
	cfg.Ready = func(addr string) { ready <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("Run exited: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("not ready")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/settings/notifications/test", nil)
	req.Header.Set("Origin", "http://"+addr)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("test notification = %d", resp.StatusCode)
	}
	select {
	case m := <-sent:
		if m.Title == "" {
			t.Fatalf("empty test notification: %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("test notification never reached the desktop sender")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}
