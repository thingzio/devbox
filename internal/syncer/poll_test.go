package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

func TestPollInsertsAndCloses(t *testing.T) {
	en := newEnv(t, Config{})
	events, unsubscribe := en.hub.Subscribe()
	defer unsubscribe()
	en.src.set(func(f *fakeSource) { f.observed = []collect.Observed{obs(1, "a"), obs(2, "b")} })
	en.mustPoll()
	if s := en.state(1); s.State != "open" || s.RetryAt == nil || !s.RetryAt.Equal(t0) {
		t.Fatalf("new pr state = %+v", s)
	}
	en.src.set(func(f *fakeSource) { f.observed = []collect.Observed{obs(2, "b")} })
	en.clock.Advance(2 * time.Minute)
	en.mustPoll()
	if s := en.state(1); s.State != "closed" {
		t.Fatalf("missing pr not closed: %+v", s)
	}
	select {
	case ev := <-events:
		if ev.Number != 1 {
			t.Fatalf("event = %+v", ev)
		}
	default:
		t.Fatal("close not published")
	}
}

func TestPollFailureThenRecovery(t *testing.T) {
	en := newEnv(t, Config{PollInterval: 2 * time.Minute})
	en.src.set(func(f *fakeSource) { f.fpErr = &github.TransportError{Err: errors.New("network unreachable")} })
	if failures, err := en.poll(); failures != 1 || err == nil {
		t.Fatalf("first failure = %d, %v", failures, err)
	}
	if failures, _ := en.poll(); failures != 2 {
		t.Fatalf("second failure = %d", failures)
	}
	if d := en.eng.pollDelay(2); d != 8*time.Minute {
		t.Fatalf("backoff after 2 failures = %v, want 8m", d)
	}
	if d := en.eng.pollDelay(5); d != 15*time.Minute {
		t.Fatalf("backoff cap = %v, want 15m", d)
	}
	if d := en.eng.pollDelay(0); d != 2*time.Minute {
		t.Fatalf("steady interval with mid jitter = %v", d)
	}
	statuses, _ := en.db.RepoStatuses(en.ctx)
	if !strings.Contains(statuses[0].Sync.LastError, "network unreachable") || statuses[0].Sync.LastSuccessAt != nil {
		t.Fatalf("failure not recorded: %+v", statuses[0].Sync)
	}
	en.src.set(func(f *fakeSource) { f.fpErr = nil; f.observed = []collect.Observed{obs(1, "a")} })
	en.clock.Advance(8 * time.Minute)
	if failures, err := en.poll(); failures != 0 || err != nil {
		t.Fatalf("recovery = %d, %v", failures, err)
	}
	statuses, _ = en.db.RepoStatuses(en.ctx)
	if statuses[0].Sync.LastError != "" || statuses[0].Sync.ConsecutiveFailures != 0 || !statuses[0].Sync.LastSuccessAt.Equal(en.clock.Now()) {
		t.Fatalf("recovery not recorded: %+v", statuses[0].Sync)
	}
}

func TestPollSkipsUnavailableAccountsAndRepos(t *testing.T) {
	en := newEnv(t, Config{})
	if _, err := en.db.SetRepoEnabled(en.ctx, en.repo.ID, false); err != nil {
		t.Fatal(err)
	}
	en.mustPoll()
	if _, err := en.db.SetRepoEnabled(en.ctx, en.repo.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := en.db.SetAccountStatus(en.ctx, "me", store.StatusNeedsAuth, "x"); err != nil {
		t.Fatal(err)
	}
	en.mustPoll()
	if _, err := en.db.UpsertAccount(en.ctx, "me", "oauth", []string{"repo"}, t0); err != nil {
		t.Fatal(err)
	}
	en.eng.ObserveRate("me", github.Rate{Resource: "graphql", Limit: 5000, Remaining: 100, Reset: t0.Add(time.Hour)})
	en.mustPoll()
	if en.src.fpCalls != 0 {
		t.Fatalf("polled %d times while unavailable", en.src.fpCalls)
	}
	if until, ok := en.eng.RateStatus()["me"]; !ok || !until.Equal(t0.Add(time.Hour)) {
		t.Fatalf("rate status = %v", en.eng.RateStatus())
	}
	en.clock.Advance(61 * time.Minute)
	en.mustPoll()
	if en.src.fpCalls != 1 || len(en.eng.RateStatus()) != 0 {
		t.Fatalf("pause did not lift: calls %d status %v", en.src.fpCalls, en.eng.RateStatus())
	}
}

func TestPollAuthFailures(t *testing.T) {
	cases := []struct {
		name       string
		fpErr      error
		srcErr     error
		wantStatus string
	}{
		{name: "token rejected", fpErr: fmt.Errorf("%w: Bad credentials", github.ErrUnauthorized), wantStatus: store.StatusNeedsAuth},
		{name: "keychain unavailable", srcErr: errors.New("keychain locked"), wantStatus: store.StatusNeedsAuth},
		{name: "sso is per org", fpErr: &github.SSORequiredError{URL: "https://github.com/orgs/o/sso"}, wantStatus: store.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			en := newEnv(t, Config{})
			en.srcErr = tc.srcErr
			en.src.set(func(f *fakeSource) { f.fpErr = tc.fpErr })
			if _, err := en.poll(); err == nil {
				t.Fatal("poll succeeded")
			}
			acct, _ := en.db.Account(en.ctx, "me")
			if acct.Status != tc.wantStatus {
				t.Fatalf("account status = %s (%s)", acct.Status, acct.StatusDetail)
			}
			statuses, _ := en.db.RepoStatuses(en.ctx)
			if statuses[0].Sync.LastError == "" {
				t.Fatal("poll error not recorded")
			}
		})
	}
}

func TestPollDiscardsWhenRepoChangesMidPoll(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.observed = []collect.Observed{obs(1, "a")}
		f.fpHook = func() {
			rl := rules.Default()
			rl.StaleDays = 3
			if _, err := en.db.UpdateRepoRules(context.Background(), en.repo.ID, rl, t0); err != nil {
				t.Errorf("UpdateRepoRules: %v", err)
			}
		}
	})
	if failures, err := en.poll(); failures != 0 || err != nil {
		t.Fatalf("poll = %d, %v", failures, err)
	}
	if states, _ := en.db.PRStates(en.ctx, en.repo.ID); len(states) != 0 {
		t.Fatalf("stale observation persisted: %+v", states)
	}
}

func TestObservedHeadChangeIsTimestampedByObservation(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) { f.observed = []collect.Observed{{Number: 1, Fingerprint: "a", HeadOID: "h1"}} })
	en.mustPoll()
	if en.detail(1).HeadChangedAt != nil {
		t.Fatal("first observation counted as a push")
	}
	en.clock.Advance(5 * time.Minute)
	en.src.set(func(f *fakeSource) { f.observed = []collect.Observed{{Number: 1, Fingerprint: "b", HeadOID: "h2"}} })
	en.mustPoll()
	if got := en.detail(1).HeadChangedAt; got == nil || !got.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("head changed at = %v, want observation time", got)
	}
}
