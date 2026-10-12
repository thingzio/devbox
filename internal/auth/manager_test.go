package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/store"
)

type managerEnv struct {
	mgr     *Manager
	db      *store.DB
	secrets *MemSecrets
}

func newManagerEnv(t *testing.T, scopes string, device *DeviceClient) *managerEnv {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "Bearer revoked" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		w.Header().Set("X-OAuth-Scopes", scopes)
		fmt.Fprint(w, `{"login":"me"}`)
	}))
	t.Cleanup(api.Close)
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "d.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	secrets := NewMemSecrets()
	mgr := NewManager(context.Background(), ManagerOptions{
		Store: db, Secrets: secrets, Device: device,
		NewClient: func(token string) (*github.Client, error) {
			return github.New(github.Options{APIURL: api.URL, Token: token})
		},
	})
	t.Cleanup(mgr.Close)
	return &managerEnv{mgr: mgr, db: db, secrets: secrets}
}

func TestAddPAT(t *testing.T) {
	ctx := context.Background()
	env := newManagerEnv(t, "repo, read:org", nil)
	if _, err := env.mgr.AddPAT(ctx, "github_pat_abc"); !errors.Is(err, ErrFineGrained) {
		t.Fatalf("fine-grained err = %v", err)
	}
	if _, err := env.mgr.AddPAT(ctx, "  "); err == nil {
		t.Fatal("empty token accepted")
	}
	if _, err := env.mgr.AddPAT(ctx, "revoked"); !errors.Is(err, github.ErrUnauthorized) {
		t.Fatalf("revoked token err = %v", err)
	}
	acct, err := env.mgr.AddPAT(ctx, " ghp_good ")
	if err != nil || acct.Login != "me" || acct.Kind != "pat" || acct.Status != store.StatusOK {
		t.Fatalf("AddPAT = %+v, %v", acct, err)
	}
	if tok, _ := env.secrets.Get("me"); tok != "ghp_good" {
		t.Fatalf("stored token = %q", tok)
	}
	if err := env.db.SetAccountStatus(ctx, "me", store.StatusNeedsAuth, "rejected"); err != nil {
		t.Fatal(err)
	}
	if acct, _ := env.mgr.AddPAT(ctx, "ghp_new"); acct.Status != store.StatusOK {
		t.Fatalf("re-auth did not restore status: %+v", acct)
	}
}

func TestAddPATRequiresRepoScope(t *testing.T) {
	env := newManagerEnv(t, "read:org, notifications", nil)
	if _, err := env.mgr.AddPAT(context.Background(), "ghp_x"); !errors.Is(err, ErrMissingScope) {
		t.Fatalf("err = %v", err)
	}
	if _, err := env.secrets.Get("me"); !errors.Is(err, ErrNoSecret) {
		t.Fatal("token stored despite missing scope")
	}
}

func TestDeviceFlowAddsAccount(t *testing.T) {
	ds := newDeviceServer(t, `{"error":"authorization_pending"}`, `{"access_token":"gho_dev"}`)
	device := &DeviceClient{BaseURL: ds.server.URL, ClientID: "cid", Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}
	env := newManagerEnv(t, "repo, read:org", device)
	if !env.mgr.DeviceConfigured() {
		t.Fatal("device flow not configured")
	}
	first, err := env.mgr.StartDevice(context.Background())
	if err != nil || first.State != FlowPending || first.UserCode != "ABCD-1234" {
		t.Fatalf("StartDevice = %+v, %v", first, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		view, ok := env.mgr.Flow(first.ID)
		if !ok {
			t.Fatal("flow vanished")
		}
		if view.State == FlowDone {
			if view.Login != "me" {
				t.Fatalf("view = %+v", view)
			}
			break
		}
		if view.State == FlowFailed || time.Now().After(deadline) {
			t.Fatalf("flow = %+v", view)
		}
		time.Sleep(10 * time.Millisecond)
	}
	acct, err := env.db.Account(context.Background(), "me")
	if err != nil || acct.Kind != "oauth" {
		t.Fatalf("account = %+v, %v", acct, err)
	}
	if tok, _ := env.secrets.Get("me"); tok != "gho_dev" {
		t.Fatalf("token = %q", tok)
	}
	second, err := env.mgr.StartDevice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env.mgr.Flow(first.ID); ok {
		t.Fatal("previous flow still addressable after a new one started")
	}
	if _, ok := env.mgr.Flow(second.ID); !ok {
		t.Fatal("new flow missing")
	}
}

func TestRemove(t *testing.T) {
	ctx := context.Background()
	env := newManagerEnv(t, "repo", nil)
	if _, err := env.mgr.AddPAT(ctx, "ghp_x"); err != nil {
		t.Fatal(err)
	}
	if err := env.mgr.Remove(ctx, "me"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := env.db.Account(ctx, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("account survived: %v", err)
	}
	if _, err := env.secrets.Get("me"); !errors.Is(err, ErrNoSecret) {
		t.Fatal("token survived")
	}
	if err := env.mgr.Remove(ctx, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second Remove err = %v", err)
	}
}
