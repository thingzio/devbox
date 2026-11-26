package auth

import (
	"errors"
	"fmt"
	"testing"

	"github.com/zalando/go-keyring"
)

func exerciseSecrets(t *testing.T, s Secrets) {
	t.Helper()
	if _, err := s.Get("me"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("missing secret err = %v", err)
	}
	if err := s.Set("me", "tok"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, err := s.Get("me"); err != nil || got != "tok" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := s.Delete("me"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete("me"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("second Delete err = %v", err)
	}
}

func TestMemSecrets(t *testing.T) { exerciseSecrets(t, NewMemSecrets()) }

func TestKeyringSecrets(t *testing.T) {
	keyring.MockInit()
	exerciseSecrets(t, Keyring{})
}

func TestKeyringUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	defer keyring.MockInit()
	for name, op := range map[string]func() error{
		"get":    func() error { _, err := (Keyring{}).Get("me"); return err },
		"set":    func() error { return (Keyring{}).Set("me", "tok") },
		"delete": func() error { return (Keyring{}).Delete("me") },
	} {
		if err := op(); !errors.Is(err, ErrKeychainUnavailable) || errors.Is(err, ErrNoSecret) {
			t.Errorf("%s: unavailable keychain err = %v", name, err)
		}
	}
}

type countingSecrets struct {
	Secrets
	gets int
	err  error
}

func (c *countingSecrets) Get(login string) (string, error) {
	c.gets++
	if c.err != nil {
		return "", c.err
	}
	return c.Secrets.Get(login)
}

func TestCachedSecrets(t *testing.T) {
	exerciseSecrets(t, NewCachedSecrets(NewMemSecrets()))

	inner := &countingSecrets{Secrets: NewMemSecrets()}
	c := NewCachedSecrets(inner)
	if _, err := c.Get("me"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("missing err = %v", err)
	}
	if err := inner.Set("me", "tok"); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Get("me"); err != nil || got != "tok" {
		t.Fatalf("Get after a miss = %q, %v; a miss must not be cached", got, err)
	}
	gets := inner.gets
	inner.err = fmt.Errorf("%w: locked", ErrKeychainUnavailable)
	if got, err := c.Get("me"); err != nil || got != "tok" || inner.gets != gets {
		t.Fatalf("cached Get = %q, %v after %d reads; want the cached token without a keychain read", got, err, inner.gets-gets)
	}
	inner.err = nil
	if err := c.Set("me", "new"); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get("me"); got != "new" {
		t.Fatalf("Get after Set = %q", got)
	}
	if err := c.Delete("me"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get("me"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("Get after Delete err = %v", err)
	}
}

type failingSet struct{ Secrets }

func (failingSet) Set(string, string) error { return ErrKeychainUnavailable }

func TestCachedSecretsKeepsOldTokenWhenSetFails(t *testing.T) {
	inner := NewMemSecrets()
	_ = inner.Set("me", "old")
	c := NewCachedSecrets(failingSet{inner})
	if _, err := c.Get("me"); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("me", "new"); err == nil {
		t.Fatal("Set succeeded")
	}
	if got, _ := c.Get("me"); got != "old" {
		t.Fatalf("Get = %q; a failed Set must not change the cache", got)
	}
}

func TestClientID(t *testing.T) {
	old := DefaultClientID
	DefaultClientID = "built-in"
	defer func() { DefaultClientID = old }()
	t.Setenv(ClientIDEnv, "")
	if ClientID() != "built-in" {
		t.Fatalf("ClientID = %q", ClientID())
	}
	t.Setenv(ClientIDEnv, "override")
	if ClientID() != "override" {
		t.Fatalf("ClientID = %q", ClientID())
	}
}

func TestClientIDDefaultsToTheThingzioGitHubApp(t *testing.T) {
	t.Setenv(ClientIDEnv, "")
	if got := ClientID(); got != "Iv23liY3vMIWTHYgjIHn" {
		t.Fatalf("ClientID = %q, want the thingzio GitHub App", got)
	}
}
