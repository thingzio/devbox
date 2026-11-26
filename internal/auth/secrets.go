// Package auth handles GitHub account authentication: the OAuth device flow,
// classic personal access tokens, and token storage in the OS keychain.
package auth

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/zalando/go-keyring"
)

// DefaultClientID is the thingzio OAuth App's client ID. Device-flow client
// IDs are public by design (no client secret is involved), so it ships in
// source; -ldflags "-X github.com/thingzio/devbox/internal/auth.DefaultClientID=..."
// replaces it in a custom build.
var DefaultClientID = "Ov23liw9A5EWYD03CV53"

// ClientIDEnv overrides DefaultClientID.
const ClientIDEnv = "DEVBOX_GITHUB_CLIENT_ID"

// Scopes requested by the device flow; repo also covers notifications.
const Scopes = "repo read:org"

// ClientID returns the OAuth client ID in effect, or "" when none is configured.
func ClientID() string {
	if v := os.Getenv(ClientIDEnv); v != "" {
		return v
	}
	return DefaultClientID
}

// Secret store errors.
var (
	// ErrNoSecret means no token is stored for the account.
	ErrNoSecret = errors.New("auth: no stored token")
	// ErrKeychainUnavailable means the OS keychain could not be read or written,
	// for example while it is locked; a later attempt may succeed.
	ErrKeychainUnavailable = errors.New("auth: keychain unavailable")
)

// Secrets stores account tokens outside the database.
type Secrets interface {
	Get(login string) (string, error)
	Set(login, token string) error
	Delete(login string) error
}

const keyringService = "devbox"

func keyringUser(login string) string { return "account:" + login }

// Keyring stores tokens in the OS keychain; there is no plaintext fallback.
type Keyring struct{}

// Get returns the account's token.
func (Keyring) Get(login string) (string, error) {
	tok, err := keyring.Get(keyringService, keyringUser(login))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNoSecret
	}
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrKeychainUnavailable, err)
	}
	return tok, nil
}

// Set stores the account's token.
func (Keyring) Set(login, token string) error {
	if err := keyring.Set(keyringService, keyringUser(login), token); err != nil {
		return fmt.Errorf("%w: %w", ErrKeychainUnavailable, err)
	}
	return nil
}

// Delete removes the account's token.
func (Keyring) Delete(login string) error {
	err := keyring.Delete(keyringService, keyringUser(login))
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNoSecret
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrKeychainUnavailable, err)
	}
	return nil
}

// CachedSecrets remembers tokens read from or written to another store, so
// polls and enrichment do not read the keychain each time. Only successful
// reads are cached; a missing token is looked up again.
type CachedSecrets struct {
	next Secrets
	mu   sync.Mutex
	m    map[string]string
}

// NewCachedSecrets caches next.
func NewCachedSecrets(next Secrets) *CachedSecrets {
	return &CachedSecrets{next: next, m: map[string]string{}}
}

// Get returns the cached token, reading next on a miss.
func (c *CachedSecrets) Get(login string) (string, error) {
	c.mu.Lock()
	tok, ok := c.m[login]
	c.mu.Unlock()
	if ok {
		return tok, nil
	}
	tok, err := c.next.Get(login)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.m[login] = tok
	c.mu.Unlock()
	return tok, nil
}

// Set stores the token in next, then caches it.
func (c *CachedSecrets) Set(login, token string) error {
	if err := c.next.Set(login, token); err != nil {
		return err
	}
	c.mu.Lock()
	c.m[login] = token
	c.mu.Unlock()
	return nil
}

// Delete forgets the token and removes it from next.
func (c *CachedSecrets) Delete(login string) error {
	c.mu.Lock()
	delete(c.m, login)
	c.mu.Unlock()
	return c.next.Delete(login)
}

// MemSecrets keeps tokens in memory, for tests.
type MemSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemSecrets returns an empty in-memory store.
func NewMemSecrets() *MemSecrets { return &MemSecrets{m: map[string]string{}} }

// Get returns the account's token.
func (s *MemSecrets) Get(login string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.m[login]
	if !ok {
		return "", ErrNoSecret
	}
	return tok, nil
}

// Set stores the account's token.
func (s *MemSecrets) Set(login, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[login] = token
	return nil
}

// Delete removes the account's token.
func (s *MemSecrets) Delete(login string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[login]; !ok {
		return ErrNoSecret
	}
	delete(s.m, login)
	return nil
}
