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
var DefaultClientID = "Ov23liiJyzLATLhYZScs"

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

// ErrNoSecret means no token is stored for the account.
var ErrNoSecret = errors.New("auth: no stored token")

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
		return "", fmt.Errorf("auth: keychain unavailable: %w", err)
	}
	return tok, nil
}

// Set stores the account's token.
func (Keyring) Set(login, token string) error {
	if err := keyring.Set(keyringService, keyringUser(login), token); err != nil {
		return fmt.Errorf("auth: keychain unavailable: %w", err)
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
		return fmt.Errorf("auth: keychain unavailable: %w", err)
	}
	return nil
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
