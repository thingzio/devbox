package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/store"
)

// Token validation errors (§8).
var (
	ErrFineGrained  = errors.New("fine-grained tokens cannot read notifications; use a classic token with the repo scope")
	ErrMissingScope = errors.New("the token lacks the repo scope")
)

// Identity is a token's owner and granted scopes.
type Identity struct {
	Login  string
	Scopes []string
}

// Identify reads the token owner's login and the scopes GitHub reports.
func Identify(ctx context.Context, c *github.Client) (Identity, error) {
	var u struct {
		Login string `json:"login"`
	}
	h, err := c.Get(ctx, "user", &u)
	if err != nil {
		return Identity{}, err
	}
	if u.Login == "" {
		return Identity{}, errors.New("auth: GitHub returned no login")
	}
	var scopes []string
	for _, s := range strings.Split(h.Get("X-OAuth-Scopes"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			scopes = append(scopes, s)
		}
	}
	return Identity{Login: u.Login, Scopes: scopes}, nil
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	Store     *store.DB
	Secrets   Secrets
	Device    *DeviceClient
	NewClient func(token string) (*github.Client, error)
	Log       *slog.Logger
	Now       func() time.Time
}

// FlowState is a device flow's progress.
type FlowState string

// Device flow states.
const (
	FlowPending FlowState = "pending"
	FlowDone    FlowState = "done"
	FlowFailed  FlowState = "failed"
)

// FlowView is what the UI shows about a device flow; device_code never leaves the server.
type FlowView struct {
	ID              string
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	State           FlowState
	Login           string
	Error           string
}

type flow struct {
	view   FlowView
	cancel context.CancelFunc
}

// Manager adds, verifies, and removes accounts.
type Manager struct {
	o    ManagerOptions
	ctx  context.Context
	mu   sync.Mutex
	flow *flow
	wg   sync.WaitGroup
}

// NewManager returns a manager whose device flows live until ctx ends or Close.
func NewManager(ctx context.Context, o ManagerOptions) *Manager {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Manager{o: o, ctx: ctx}
}

// DeviceConfigured reports whether a client ID is available for the device flow.
func (m *Manager) DeviceConfigured() bool { return m.o.Device != nil && m.o.Device.ClientID != "" }

// StartDevice begins a device flow, cancelling any previous one (§8).
func (m *Manager) StartDevice(ctx context.Context) (FlowView, error) {
	if m.o.Device == nil {
		return FlowView{}, errors.New("auth: device flow is not configured")
	}
	dc, err := m.o.Device.Start(ctx)
	if err != nil {
		return FlowView{}, err
	}
	expires := time.Duration(dc.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 15 * time.Minute
	}
	fctx, cancel := context.WithTimeout(m.ctx, expires+time.Minute)
	f := &flow{view: FlowView{
		ID: rand.Text(), UserCode: dc.UserCode, VerificationURI: dc.VerificationURI,
		ExpiresAt: m.o.Now().Add(expires), State: FlowPending,
	}, cancel: cancel}
	m.mu.Lock()
	if m.flow != nil {
		m.flow.cancel()
	}
	m.flow = f
	m.mu.Unlock()
	m.o.Log.Info("device flow started", "expires_at", f.view.ExpiresAt)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		token, err := m.o.Device.Wait(fctx, dc)
		var acct store.Account
		if err == nil {
			acct, err = m.complete(fctx, token, "oauth")
		}
		m.finish(f, acct.Login, err)
	}()
	return f.view, nil
}

func (m *Manager) finish(f *flow, login string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		f.view.State, f.view.Error = FlowFailed, err.Error()
		m.o.Log.Info("device flow ended", "err", err)
		return
	}
	f.view.State, f.view.Login = FlowDone, login
}

// Flow returns the current flow's view if id matches it.
func (m *Manager) Flow(id string) (FlowView, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow == nil || m.flow.view.ID != id {
		return FlowView{}, false
	}
	return m.flow.view, true
}

// AddPAT verifies and stores a classic personal access token (§8).
func (m *Manager) AddPAT(ctx context.Context, token string) (store.Account, error) {
	token = strings.TrimSpace(token)
	switch {
	case token == "":
		return store.Account{}, errors.New("auth: token is empty")
	case strings.HasPrefix(token, "github_pat_"):
		return store.Account{}, ErrFineGrained
	}
	return m.complete(ctx, token, "pat")
}

func (m *Manager) complete(ctx context.Context, token, kind string) (store.Account, error) {
	c, err := m.o.NewClient(token)
	if err != nil {
		return store.Account{}, err
	}
	id, err := Identify(ctx, c)
	if err != nil {
		return store.Account{}, fmt.Errorf("auth: verify token: %w", err)
	}
	if !slices.Contains(id.Scopes, "repo") {
		return store.Account{}, ErrMissingScope
	}
	if err := m.o.Secrets.Set(id.Login, token); err != nil {
		return store.Account{}, err
	}
	acct, err := m.o.Store.UpsertAccount(ctx, id.Login, kind, id.Scopes, m.o.Now())
	if err != nil {
		return store.Account{}, err
	}
	m.o.Log.Info("account verified", "account", id.Login, "kind", kind, "scopes", strings.Join(id.Scopes, ","))
	return acct, nil
}

// Remove deletes the account's token and the account with its repositories.
func (m *Manager) Remove(ctx context.Context, login string) error {
	if err := m.o.Secrets.Delete(login); err != nil && !errors.Is(err, ErrNoSecret) {
		return err
	}
	if err := m.o.Store.DeleteAccount(ctx, login); err != nil {
		return err
	}
	m.o.Log.Info("account removed", "account", login)
	return nil
}

// Close cancels any running device flow and waits for it to stop.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.flow != nil {
		m.flow.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
