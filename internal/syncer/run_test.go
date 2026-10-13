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

func startRun(t *testing.T, en *env) func() {
	t.Helper()
	en.eng.d.Now = time.Now
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- en.eng.Run(ctx) }()
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}
}

func (en *env) fingerprintCalls() int {
	en.src.mu.Lock()
	defer en.src.mu.Unlock()
	return en.src.fpCalls
}

func TestManualPollingWaitsForRefresh(t *testing.T) {
	en := newEnv(t, Config{Manual: true, DispatchEvery: 10 * time.Millisecond, ReclassifyEvery: time.Hour, ResyncEvery: time.Hour})
	stop := startRun(t, en)
	defer stop()
	time.Sleep(150 * time.Millisecond)
	if n := en.fingerprintCalls(); n != 0 {
		t.Fatalf("manual mode polled %d times without a refresh", n)
	}
	en.eng.Refresh()
	waitFor(t, func() bool { return en.fingerprintCalls() == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := en.fingerprintCalls(); n != 1 {
		t.Fatalf("one refresh polled %d times", n)
	}
}

func TestManualPollingStillPollsNewRepoOnce(t *testing.T) {
	en := newEnv(t, Config{Manual: true, DispatchEvery: 10 * time.Millisecond, ReclassifyEvery: time.Hour, ResyncEvery: time.Hour})
	stop := startRun(t, en)
	defer stop()
	waitFor(t, func() bool { en.eng.mu.Lock(); defer en.eng.mu.Unlock(); return len(en.eng.pokes) == 1 })
	acct, _ := en.db.Account(context.Background(), "me")
	if _, err := en.db.AddRepo(context.Background(), acct.ID, "o", "r2", rules.Default(), t0); err != nil {
		t.Fatal(err)
	}
	en.eng.ReposChanged()
	waitFor(t, func() bool { return en.fingerprintCalls() == 1 })
}

func TestSetPollingAppliesImmediately(t *testing.T) {
	en := newEnv(t, Config{PollInterval: time.Hour, DispatchEvery: 10 * time.Millisecond, ReclassifyEvery: time.Hour, ResyncEvery: time.Hour})
	stop := startRun(t, en)
	defer stop()
	waitFor(t, func() bool { return en.fingerprintCalls() == 1 })
	en.eng.SetPolling(Polling{Interval: time.Minute})
	waitFor(t, func() bool { return en.fingerprintCalls() == 2 })
	if got := en.eng.Polling(); got != (Polling{Interval: time.Minute}) {
		t.Fatalf("Polling() = %+v", got)
	}
	en.eng.SetPolling(Polling{Manual: true})
	time.Sleep(150 * time.Millisecond)
	if n := en.fingerprintCalls(); n != 2 {
		t.Fatalf("switching to manual polled (%d calls)", n)
	}
	en.eng.Refresh()
	waitFor(t, func() bool { return en.fingerprintCalls() == 3 })
}

func TestPollDelayFollowsCurrentInterval(t *testing.T) {
	en := newEnv(t, Config{PollInterval: 2 * time.Minute})
	en.eng.SetPolling(Polling{Interval: 5 * time.Minute})
	if d := en.eng.pollDelay(0); d != 5*time.Minute {
		t.Fatalf("steady delay = %v, want 5m", d)
	}
	if d := en.eng.pollDelay(1); d != 10*time.Minute {
		t.Fatalf("backoff after one failure = %v, want 10m", d)
	}
}
