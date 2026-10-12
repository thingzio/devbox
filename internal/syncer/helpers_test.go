package syncer

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	gosync "sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/metrics"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type testClock struct {
	mu  gosync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fakeSource struct {
	mu        gosync.Mutex
	observed  []collect.Observed
	fpErr     error
	fpHook    func()
	outcome   func(ctx context.Context, in collect.Input) collect.Outcome
	fpCalls   int
	collected map[int]int
	inputs    []collect.Input
}

func (f *fakeSource) Fingerprints(context.Context, string, string) ([]collect.Observed, error) {
	f.mu.Lock()
	f.fpCalls++
	hook, obs, err := f.fpHook, slices.Clone(f.observed), f.fpErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return obs, err
}

func (f *fakeSource) Collect(ctx context.Context, in collect.Input) collect.Outcome {
	f.mu.Lock()
	f.collected[in.Number]++
	f.inputs = append(f.inputs, in)
	fn := f.outcome
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return collectedOutcome(in)
}

func (f *fakeSource) set(update func(f *fakeSource)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	update(f)
}

func goodRecord(in collect.Input) classify.Record {
	return classify.Record{
		Version: classify.RecordVersion, CollectionRulesHash: in.Rules.CollectionHash(), Number: in.Number,
		Title: fmt.Sprintf("PR %d", in.Number), URL: store.PullURL("o", "r", in.Number), Author: "alice",
		CreatedAt: t0, UpdatedAt: t0, HeadOID: "h", HeadCommittedAt: t0, HeadChangedAt: in.HeadChangedAt,
		MergeState: "CLEAN", SignaturesComplete: true, SignaturePolicy: "absent",
	}
}

func collectedOutcome(in collect.Input) collect.Outcome {
	return collect.Outcome{Kind: collect.Collected, Record: goodRecord(in)}
}

func obs(number int, fp string) collect.Observed {
	return collect.Observed{Number: number, Fingerprint: fp, HeadOID: "head-" + fp}
}

type env struct {
	t      *testing.T
	ctx    context.Context
	db     *store.DB
	hub    *hub.Hub
	src    *fakeSource
	clock  *testClock
	eng    *Engine
	repo   store.Repo
	srcErr error
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "devbox.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	acct, err := db.UpsertAccount(ctx, "me", "oauth", []string{"repo"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := db.AddRepo(ctx, acct.ID, "o", "r", rules.Default(), t0)
	if err != nil {
		t.Fatal(err)
	}
	en := &env{t: t, ctx: ctx, db: db, hub: hub.New(), src: &fakeSource{collected: map[int]int{}}, clock: &testClock{now: t0}, repo: repo}
	en.eng = New(cfg, Deps{
		Store: db, Hub: en.hub, Metrics: metrics.New(prometheus.NewRegistry()),
		Sources: func(string) (Source, error) {
			if en.srcErr != nil {
				return nil, en.srcErr
			}
			return en.src, nil
		},
		Now:  en.clock.Now,
		Rand: func() float64 { return 0.5 },
	})
	return en
}

func (en *env) poll() (int, error) { return en.eng.PollRepo(en.ctx, en.repo.ID) }

func (en *env) mustPoll() {
	en.t.Helper()
	if _, err := en.poll(); err != nil {
		en.t.Fatalf("PollRepo: %v", err)
	}
}

func (en *env) state(number int) store.PRState {
	en.t.Helper()
	states, err := en.db.PRStates(en.ctx, en.repo.ID)
	if err != nil {
		en.t.Fatal(err)
	}
	for _, s := range states {
		if s.Number == number {
			return s
		}
	}
	en.t.Fatalf("pr %d not stored", number)
	return store.PRState{}
}

func (en *env) detail(number int) store.PRDetail {
	en.t.Helper()
	d, err := en.db.PRDetails(en.ctx, "o", "r", number)
	if err != nil || len(d) == 0 {
		en.t.Fatalf("PRDetails(%d) = %v, %v", number, d, err)
	}
	return d[0]
}
