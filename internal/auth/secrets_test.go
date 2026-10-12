package auth

import (
	"errors"
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
	if _, err := (Keyring{}).Get("me"); err == nil || errors.Is(err, ErrNoSecret) {
		t.Fatalf("unavailable keychain err = %v", err)
	}
	keyring.MockInit()
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
