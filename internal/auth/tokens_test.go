package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeRefresher struct {
	mu    sync.Mutex
	calls []string
	next  Credential
	err   error
	block chan struct{}
}

func (f *fakeRefresher) Refresh(_ context.Context, refreshToken string) (Credential, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, refreshToken)
	return f.next, f.err
}

func TestCredentialEncoding(t *testing.T) {
	plain := Credential{AccessToken: "ghp_x"}
	if plain.encode() != "ghp_x" || decodeCredential("ghp_x") != plain {
		t.Fatalf("plain token round trip = %q", plain.encode())
	}
	app := Credential{AccessToken: "ghu_a", RefreshToken: "ghr_a", ExpiresAt: authNow, RefreshExpiresAt: authNow.Add(time.Hour)}
	if got := decodeCredential(app.encode()); got != app {
		t.Fatalf("grant round trip = %+v", got)
	}
}

func TestTokens(t *testing.T) {
	expiring := Credential{AccessToken: "ghu_old", RefreshToken: "ghr_old", ExpiresAt: authNow.Add(4 * time.Minute), RefreshExpiresAt: authNow.Add(time.Hour)}
	fresh := Credential{AccessToken: "ghu_new", RefreshToken: "ghr_new", ExpiresAt: authNow.Add(8 * time.Hour), RefreshExpiresAt: authNow.Add(time.Hour)}
	cases := []struct {
		name      string
		stored    Credential
		refresher *fakeRefresher
		want      string
		wantErr   error
		refreshes int
		persisted Credential
	}{
		{name: "classic token", stored: Credential{AccessToken: "ghp_x"}, refresher: &fakeRefresher{}, want: "ghp_x"},
		{name: "unexpired grant", stored: fresh, refresher: &fakeRefresher{}, want: "ghu_new"},
		{name: "expiring grant refreshes and persists the rotated token", stored: expiring, refresher: &fakeRefresher{next: fresh},
			want: "ghu_new", refreshes: 1, persisted: fresh},
		{name: "rejected refresh", stored: expiring, refresher: &fakeRefresher{err: ErrRefreshRejected}, wantErr: ErrRefreshRejected, refreshes: 1},
		{name: "expired refresh token is not tried", stored: Credential{AccessToken: "a", RefreshToken: "r", ExpiresAt: authNow, RefreshExpiresAt: authNow},
			refresher: &fakeRefresher{}, wantErr: ErrRefreshRejected},
		{name: "missing", refresher: &fakeRefresher{}, wantErr: ErrNoSecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secrets := NewMemSecrets()
			if tc.stored.AccessToken != "" {
				_ = secrets.Set("me", tc.stored.encode())
			}
			tokens := NewTokens(secrets, tc.refresher, func() time.Time { return authNow })
			got, err := tokens.Token(context.Background(), "me")
			if got != tc.want || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("Token = %q, %v; want %q, %v", got, err, tc.want, tc.wantErr)
			}
			if len(tc.refresher.calls) != tc.refreshes {
				t.Fatalf("refreshes = %v", tc.refresher.calls)
			}
			if tc.persisted.AccessToken != "" {
				raw, _ := secrets.Get("me")
				if decodeCredential(raw) != tc.persisted {
					t.Fatalf("persisted = %q", raw)
				}
			}
		})
	}
}

func TestTokensRefreshOnceForConcurrentCallers(t *testing.T) {
	secrets := NewMemSecrets()
	_ = secrets.Set("me", Credential{AccessToken: "old", RefreshToken: "ghr_old", ExpiresAt: authNow}.encode())
	r := &fakeRefresher{next: Credential{AccessToken: "new", RefreshToken: "ghr_new", ExpiresAt: authNow.Add(8 * time.Hour)}, block: make(chan struct{})}
	tokens := NewTokens(secrets, r, func() time.Time { return authNow })
	var wg sync.WaitGroup
	got := make([]string, 4)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], _ = tokens.Token(context.Background(), "me")
		}()
	}
	close(r.block)
	wg.Wait()
	for _, g := range got {
		if g != "new" {
			t.Fatalf("tokens = %v", got)
		}
	}
	if len(r.calls) != 1 {
		t.Fatalf("refreshed %d times; a rotated refresh token is single-use", len(r.calls))
	}
}

func TestTokensWithoutRefresher(t *testing.T) {
	secrets := NewMemSecrets()
	_ = secrets.Set("me", Credential{AccessToken: "a", RefreshToken: "r", ExpiresAt: authNow}.encode())
	if _, err := NewTokens(secrets, nil, func() time.Time { return authNow }).Token(context.Background(), "me"); !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("err = %v; an expired grant cannot be used without a refresher", err)
	}
}
