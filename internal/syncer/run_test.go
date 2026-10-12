package syncer

import (
	"context"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

func TestRunPollsEnrichesAndStopsCleanly(t *testing.T) {
	en := newEnv(t, Config{PollInterval: time.Hour, DispatchEvery: 10 * time.Millisecond, ReclassifyEvery: time.Hour, ResyncEvery: time.Hour})
	en.eng.d.Now = time.Now
	en.src.set(func(f *fakeSource) { f.observed = []collect.Observed{obs(1, "a")} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- en.eng.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := en.db.Queue(context.Background(), store.QueueFilter{})
		if err == nil && len(rows) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no result within 5s: %+v, %v", rows, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestRunStartsPollerForNewRepo(t *testing.T) {
	en := newEnv(t, Config{PollInterval: time.Hour, DispatchEvery: 10 * time.Millisecond, ReclassifyEvery: time.Hour, ResyncEvery: time.Hour})
	en.eng.d.Now = time.Now
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- en.eng.Run(ctx) }()
	fpCalls := func() int { en.src.mu.Lock(); defer en.src.mu.Unlock(); return en.src.fpCalls }
	waitFor(t, func() bool { return fpCalls() == 1 })
	acct, err := en.db.Account(context.Background(), "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := en.db.AddRepo(context.Background(), acct.ID, "o", "r2", rules.Default(), t0); err != nil {
		t.Fatal(err)
	}
	en.eng.ReposChanged()
	waitFor(t, func() bool { return fpCalls() == 2 })
	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
