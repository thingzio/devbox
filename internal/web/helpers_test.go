package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/auth"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type rulesCall struct {
	repoID            int64
	collectionChanged bool
}

type fakeEngine struct {
	mu           sync.Mutex
	reposChanged int
	rulesCalls   []rulesCall
	paused       map[string]time.Time
}

func (f *fakeEngine) ReposChanged() { f.mu.Lock(); f.reposChanged++; f.mu.Unlock() }
func (f *fakeEngine) RulesChanged(_ context.Context, repoID int64, changed bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rulesCalls = append(f.rulesCalls, rulesCall{repoID, changed})
	return nil
}
func (f *fakeEngine) RateStatus() map[string]time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused
}
func (f *fakeEngine) reposChangedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reposChanged
}
func (f *fakeEngine) rules() []rulesCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rulesCall(nil), f.rulesCalls...)
}
func (f *fakeEngine) setPaused(p map[string]time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = p
}

type testServer struct {
	mu        sync.Mutex
	t         *testing.T
	ctx       context.Context
	db        *store.DB
	hub       *hub.Hub
	engine    *fakeEngine
	secrets   *auth.MemSecrets
	now       time.Time
	http      *httptest.Server
	client    *http.Client
	importDir string
	apiScopes string
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ts := &testServer{t: t, ctx: context.Background(), hub: hub.New(), engine: &fakeEngine{}, secrets: auth.NewMemSecrets(),
		now: t0, importDir: t.TempDir(), apiScopes: "repo, read:org"}
	db, err := store.Open(ts.ctx, filepath.Join(t.TempDir(), "d.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ts.db = db
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			_, _ = io.WriteString(w, `{"device_code":"dc","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`)
		case "/login/oauth/access_token":
			_, _ = io.WriteString(w, `{"access_token":"gho_dev"}`)
		default:
			ts.mu.Lock()
			scopes := ts.apiScopes
			ts.mu.Unlock()
			w.Header().Set("X-OAuth-Scopes", scopes)
			_, _ = io.WriteString(w, `{"login":"me"}`)
		}
	}))
	t.Cleanup(api.Close)
	mgr := auth.NewManager(ts.ctx, auth.ManagerOptions{
		Store: db, Secrets: ts.secrets,
		Device: &auth.DeviceClient{BaseURL: api.URL, ClientID: "cid", Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }},
		NewClient: func(token string) (*github.Client, error) {
			return github.New(github.Options{APIURL: api.URL, Token: token})
		},
	})
	t.Cleanup(mgr.Close)
	ts.http = httptest.NewUnstartedServer(nil)
	s, err := New(Options{
		Store: db, Hub: ts.hub, Auth: mgr, Engine: ts.engine, Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "devbox_up 1\n") }),
		Addr: ts.http.Listener.Addr().String(), PollInterval: 2 * time.Minute, ImportDir: ts.importDir,
		Now: ts.clock, Location: time.UTC,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts.http.Config.Handler = s.Handler()
	ts.http.Start()
	t.Cleanup(ts.http.Close)
	ts.client = ts.http.Client()
	ts.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return ts
}

func (ts *testServer) clock() time.Time {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.now
}

func (ts *testServer) setNow(now time.Time) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.now = now
}

func (ts *testServer) setScopes(scopes string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.apiScopes = scopes
}

func (ts *testServer) get(path string) (int, string) {
	ts.t.Helper()
	resp, err := ts.client.Get(ts.http.URL + path)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (ts *testServer) post(path string, form url.Values) (int, string, http.Header) {
	ts.t.Helper()
	req, _ := http.NewRequestWithContext(ts.ctx, http.MethodPost, ts.http.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.http.URL)
	resp, err := ts.client.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func (ts *testServer) account(login string) store.Account {
	ts.t.Helper()
	a, err := ts.db.UpsertAccount(ts.ctx, login, "oauth", []string{"repo"}, t0)
	if err != nil {
		ts.t.Fatal(err)
	}
	return a
}

func (ts *testServer) repo(a store.Account, owner, name string) store.Repo {
	ts.t.Helper()
	r, err := ts.db.AddRepo(ts.ctx, a.ID, owner, name, rules.Default(), t0)
	if err != nil {
		ts.t.Fatal(err)
	}
	return r
}

// seed stores one classified PR with the given title, URL, bucket, and record JSON.
func (ts *testServer) seed(r store.Repo, number int, title, prURL, bucket, record string) {
	ts.t.Helper()
	if _, err := ts.db.ApplyPoll(ts.ctx, store.PollApply{RepoID: r.ID, RepoRevision: r.Revision, Owner: r.Owner, Name: r.Name, Now: t0,
		Upserts: []store.Observation{{Number: number, Fingerprint: "fp", HeadOID: "h"}}}); err != nil {
		ts.t.Fatal(err)
	}
	states, _ := ts.db.PRStates(ts.ctx, r.ID)
	var rev int64
	for _, s := range states {
		if s.Number == number {
			rev = s.Revision
		}
	}
	since := t0.Add(-49 * time.Hour)
	if _, err := ts.db.CommitEnrichment(ts.ctx, store.EnrichCommit{RepoID: r.ID, RepoRevision: r.Revision, Number: number, PRRevision: rev,
		Now: t0, Record: []byte(record), AdvanceRecordAt: true, Title: title, URL: prURL, Author: "alice",
		Result: store.ResultRow{Bucket: bucket, Reason: "not reviewed by you", Action: "review", WaitingSince: &since,
			Notes: []string{"https://github.com/o/r/pull/1#discussion_r1", "approved by @bob"}}}); err != nil {
		ts.t.Fatal(err)
	}
}
