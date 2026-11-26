package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrRefreshRejected means an expiring token can no longer be renewed, so the
// account must sign in again.
var ErrRefreshRejected = errors.New("auth: token refresh rejected; sign in again")

// refreshMargin renews a token this long before it expires, so a read never
// starts with a token about to lapse.
const refreshMargin = 5 * time.Minute

// Credential is what the keychain holds for one account. Classic tokens and
// OAuth App tokens carry only AccessToken; GitHub App user tokens expire and
// carry a single-use refresh token.
type Credential struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitzero"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitzero"`
}

// encode stores a non-expiring token as the bare string, as earlier versions
// did, and an expiring grant as JSON.
func (c Credential) encode() string {
	if c.RefreshToken == "" {
		return c.AccessToken
	}
	b, _ := json.Marshal(c) //nolint:gosec // encoded only for the OS keychain; a struct of strings and times always encodes
	return string(b)
}

func decodeCredential(raw string) Credential {
	if strings.HasPrefix(raw, "{") {
		var c Credential
		if json.Unmarshal([]byte(raw), &c) == nil && c.AccessToken != "" {
			return c
		}
	}
	return Credential{AccessToken: raw}
}

// Refresher renews an expiring grant; *DeviceClient implements it.
type Refresher interface {
	Refresh(ctx context.Context, refreshToken string) (Credential, error)
}

// Tokens hands out usable access tokens, renewing expiring grants. Refreshes
// are serialized per account because GitHub rotates the refresh token on use.
type Tokens struct {
	secrets   Secrets
	refresher Refresher
	now       func() time.Time
	mu        sync.Mutex
	locks     map[string]*sync.Mutex
}

// NewTokens reads credentials from secrets; refresher may be nil when no
// client ID is configured, in which case expired grants cannot be renewed.
func NewTokens(secrets Secrets, refresher Refresher, now func() time.Time) *Tokens {
	if now == nil {
		now = time.Now
	}
	return &Tokens{secrets: secrets, refresher: refresher, now: now, locks: map[string]*sync.Mutex{}}
}

func (t *Tokens) lock(login string) *sync.Mutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	l, ok := t.locks[login]
	if !ok {
		l = &sync.Mutex{}
		t.locks[login] = l
	}
	return l
}

// Token returns the account's access token, refreshing it first when it
// expires within refreshMargin.
func (t *Tokens) Token(ctx context.Context, login string) (string, error) {
	l := t.lock(login)
	l.Lock()
	defer l.Unlock()
	raw, err := t.secrets.Get(login)
	if err != nil {
		return "", err
	}
	c := decodeCredential(raw)
	now := t.now()
	if c.RefreshToken == "" || c.ExpiresAt.IsZero() || now.Add(refreshMargin).Before(c.ExpiresAt) {
		return c.AccessToken, nil
	}
	if t.refresher == nil || (!c.RefreshExpiresAt.IsZero() && !now.Before(c.RefreshExpiresAt)) {
		return "", ErrRefreshRejected
	}
	next, err := t.refresher.Refresh(ctx, c.RefreshToken)
	if err != nil {
		return "", err
	}
	if err := t.secrets.Set(login, next.encode()); err != nil {
		return "", fmt.Errorf("store refreshed token: %w", err)
	}
	return next.AccessToken, nil
}
