package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

func (en *env) dispatch() int {
	en.t.Helper()
	n, err := en.eng.DispatchDue(en.ctx)
	if err != nil {
		en.t.Fatalf("DispatchDue: %v", err)
	}
	en.eng.Wait()
	return n
}

func (en *env) observeAndEnrich(observed ...collect.Observed) {
	en.t.Helper()
	en.src.set(func(f *fakeSource) { f.observed = observed })
	en.mustPoll()
	en.dispatch()
}

func TestDispatchCommitsCollectedRecords(t *testing.T) {
	en := newEnv(t, Config{})
	events, unsubscribe := en.hub.Subscribe()
	defer unsubscribe()
	en.observeAndEnrich(obs(1, "a"))
	d := en.detail(1)
	if d.Result == nil || d.Result.Bucket != string(classify.NeedsInitialReview) || d.RetryAt != nil || d.Title != "PR 1" || d.RecordAt == nil {
		t.Fatalf("after enrichment = %+v result %+v", d, d.Result)
	}
	if in := en.src.inputs[0]; in.QueuedFingerprint != "a" || in.Viewer != "me" || in.Owner != "o" || in.RepoID != en.repo.ID {
		t.Fatalf("collect input = %+v", in)
	}
	if len(events) == 0 {
		t.Fatal("no change event published")
	}
	trs := en.transitions(1)
	if len(trs) != 1 || trs[0].From != nil || trs[0].To != string(classify.NeedsInitialReview) {
		t.Fatalf("transitions = %+v", trs)
	}
}

func TestOnlyChangedPRsAreEnriched(t *testing.T) {
	en := newEnv(t, Config{})
	en.observeAndEnrich(obs(1, "a"), obs(2, "b"))
	en.clock.Advance(2 * time.Minute)
	en.observeAndEnrich(obs(1, "a"), obs(2, "b"))
	if en.src.calls(1) != 1 || en.src.calls(2) != 1 {
		t.Fatalf("unchanged PRs re-enriched: %v", en.src.collected)
	}
	en.clock.Advance(2 * time.Minute)
	en.observeAndEnrich(obs(1, "a"), obs(2, "b2"))
	if en.src.calls(1) != 1 || en.src.calls(2) != 2 {
		t.Fatalf("calls after one change = %v", en.src.collected)
	}
}

func TestMismatchCommitsObservationAndRedispatchesOnce(t *testing.T) {
	en := newEnv(t, Config{})
	first := true
	en.src.set(func(f *fakeSource) {
		f.observed = []collect.Observed{obs(1, "a")}
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			if first {
				first = false
				return collect.Outcome{Kind: collect.Mismatch, Fingerprint: "a2", HeadOID: "head-new"}
			}
			return collectedOutcome(in)
		}
	})
	en.mustPoll()
	en.dispatch()
	s := en.state(1)
	if s.Fingerprint != "a2" || s.RetryAt == nil || s.HasRecord || en.detail(1).HeadChangedAt == nil {
		t.Fatalf("detail observation not committed: %+v", s)
	}
	en.dispatch()
	if en.src.calls(1) != 2 || en.src.inputs[1].QueuedFingerprint != "a2" || en.src.inputs[1].HeadChangedAt == nil {
		t.Fatalf("re-dispatch inputs = %+v", en.src.inputs)
	}
	if started := en.dispatch(); started != 0 || en.src.calls(1) != 2 {
		t.Fatalf("detail reads repeated: started %d calls %d", started, en.src.calls(1))
	}
}

func TestRetryableFatalKeepsLastGoodResultUntilBound(t *testing.T) {
	en := newEnv(t, Config{})
	en.observeAndEnrich(obs(1, "a"))
	good := en.detail(1).Result.Bucket
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			rec := classify.Record{Number: in.Number}
			return collect.Outcome{Kind: collect.Failed, Record: rec, Err: errors.New("HTTP 502"), Retryable: true}
		}
	})
	en.clock.Advance(2 * time.Minute)
	en.observeAndEnrich(obs(1, "a2"))
	d := en.detail(1)
	if d.Result.Bucket != good || !slices.ContainsFunc(d.Result.Notes, func(n string) bool { return strings.HasPrefix(n, "evidence stale since") }) {
		t.Fatalf("stale evidence not retained: %+v", d.Result)
	}
	if d.Error != "HTTP 502" || d.EnrichFailures != 1 || d.RetryAt == nil || !d.RetryAt.Equal(en.clock.Now().Add(30*time.Second)) {
		t.Fatalf("retry bookkeeping = %+v", d)
	}
	if len(en.transitions(1)) != 1 {
		t.Fatalf("keep-stale wrote a transition: %+v", en.transitions(1))
	}
	en.clock.Advance(31 * time.Minute)
	en.dispatch()
	d = en.detail(1)
	if d.Result.Bucket != string(classify.Unclassified) || d.EnrichFailures != 2 {
		t.Fatalf("after bound = %+v %+v", d, d.Result)
	}
	if trs := en.transitions(1); len(trs) != 2 || trs[1].To != string(classify.Unclassified) {
		t.Fatalf("transitions = %+v", trs)
	}
}

func TestNonRetryableFatalIsUnclassifiedWithoutFastRetry(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			return collect.Outcome{Kind: collect.Failed, Record: classify.Record{Number: in.Number, Title: "T"}, Err: errors.New("more than 100 review threads")}
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	d := en.detail(1)
	if d.Result.Bucket != string(classify.Unclassified) || d.RetryAt != nil || d.EnrichFailures != 0 || d.RecordAt != nil || d.Title != "T" {
		t.Fatalf("non-retryable fatal = %+v %+v", d, d.Result)
	}
	var rec classify.Record
	if err := json.Unmarshal(d.Record, &rec); err != nil || rec.Version != classify.RecordVersion || rec.CollectionRulesHash == "" {
		t.Fatalf("error record = %+v, %v", rec, err)
	}
}

func TestRetryableEvidenceSchedulesRetryThenClears(t *testing.T) {
	en := newEnv(t, Config{})
	retry := true
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			out := collectedOutcome(in)
			out.RetryableEvidence = retry
			return out
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	if d := en.detail(1); d.RetryAt == nil || d.EnrichFailures != 1 || d.RecordAt == nil {
		t.Fatalf("evidence retry = %+v", d)
	}
	retry = false
	en.clock.Advance(time.Minute)
	en.dispatch()
	if d := en.detail(1); d.RetryAt != nil || d.EnrichFailures != 0 {
		t.Fatalf("retry not cleared = %+v", d)
	}
}

func TestFailedWorkResumesAfterRestart(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			return collect.Outcome{Kind: collect.Failed, Err: &github.TransportError{Err: errors.New("reset")}, Retryable: true, Record: classify.Record{Number: in.Number}}
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	en.src.set(func(f *fakeSource) { f.outcome = nil })
	restarted := New(Config{}, Deps{Store: en.db, Hub: en.hub, Sources: func(string) (Source, error) { return en.src, nil }, Now: en.clock.Now, Rand: func() float64 { return 0.5 }})
	en.clock.Advance(time.Minute)
	if _, err := restarted.DispatchDue(en.ctx); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	if d := en.detail(1); d.Result.Bucket != string(classify.NeedsInitialReview) || d.RetryAt != nil {
		t.Fatalf("after restart = %+v", d.Result)
	}
}

func TestLateCompletionIsDiscarded(t *testing.T) {
	for _, lifecycle := range []string{"close", "disable", "delete"} {
		t.Run(lifecycle, func(t *testing.T) {
			en := newEnv(t, Config{})
			release := make(chan struct{})
			started := make(chan struct{})
			en.src.set(func(f *fakeSource) {
				f.observed = []collect.Observed{obs(1, "a")}
				f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
					close(started)
					<-release
					return collectedOutcome(in)
				}
			})
			en.mustPoll()
			if _, err := en.eng.DispatchDue(en.ctx); err != nil {
				t.Fatal(err)
			}
			<-started
			switch lifecycle {
			case "close":
				en.src.set(func(f *fakeSource) { f.observed = nil })
				en.mustPoll()
			case "disable":
				if _, err := en.db.SetRepoEnabled(en.ctx, en.repo.ID, false); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := en.db.DeleteRepo(en.ctx, en.repo.ID); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			en.eng.Wait()
			details, err := en.db.PRDetails(en.ctx, "o", "r", 1)
			if err != nil {
				t.Fatal(err)
			}
			if lifecycle == "delete" {
				if len(details) != 0 {
					t.Fatalf("late completion recreated a deleted repo's PR: %+v", details)
				}
				return
			}
			if len(details) != 1 || details[0].Result != nil || details[0].RecordAt != nil {
				t.Fatalf("late completion committed: %+v", details)
			}
		})
	}
}

func TestBlockedAccountLeavesWorkPending(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			return collect.Outcome{Kind: collect.Failed, Err: fmt.Errorf("%w: revoked", github.ErrUnauthorized), Record: classify.Record{Number: in.Number}}
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	acct, _ := en.db.Account(en.ctx, "me")
	if acct.Status != store.StatusNeedsAuth {
		t.Fatalf("account = %+v", acct)
	}
	if d := en.detail(1); d.Result != nil || d.RetryAt == nil {
		t.Fatalf("blocked job committed: %+v", d)
	}
	if started := en.dispatch(); started != 0 {
		t.Fatalf("dispatched %d jobs for a needs_auth account", started)
	}
}

func TestDispatchHonorsPauseAndWorkerLimit(t *testing.T) {
	en := newEnv(t, Config{Workers: 1})
	release := make(chan struct{})
	en.src.set(func(f *fakeSource) {
		f.observed = []collect.Observed{obs(1, "a"), obs(2, "b")}
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			<-release
			return collectedOutcome(in)
		}
	})
	en.mustPoll()
	n, err := en.eng.DispatchDue(en.ctx)
	if err != nil || n != 1 {
		t.Fatalf("started = %d, %v; want 1 with one worker", n, err)
	}
	if again, _ := en.eng.DispatchDue(en.ctx); again != 0 {
		t.Fatalf("reserved PR dispatched twice: %d", again)
	}
	close(release)
	en.eng.Wait()
	en.eng.ObserveRate("me", github.Rate{Resource: "core", Limit: 5000, Remaining: 1, Reset: t0.Add(time.Hour)})
	if started := en.dispatch(); started != 0 {
		t.Fatalf("dispatched %d jobs while paused", started)
	}
}

func TestRulesChanged(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			out := collectedOutcome(in)
			out.Record.MyReviews = []classify.Review{{State: "CHANGES_REQUESTED", SubmittedAt: t0, Commit: "h"}}
			out.Record.MyLastCommentAt = &t0
			return out
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	en.clock.Advance(5 * 24 * time.Hour)
	if b := en.detail(1).Result.Bucket; b != string(classify.WaitingOnAuthor) {
		t.Fatalf("bucket = %s", b)
	}
	staleSoon := rules.Default()
	staleSoon.StaleDays = 3
	upd, err := en.db.UpdateRepoRules(en.ctx, en.repo.ID, staleSoon, en.clock.Now())
	if err != nil || upd.CollectionChanged {
		t.Fatalf("stale-only update = %+v, %v", upd, err)
	}
	if err := en.eng.RulesChanged(en.ctx, en.repo.ID, upd.CollectionChanged); err != nil {
		t.Fatal(err)
	}
	if b := en.detail(1).Result.Bucket; b != string(classify.StaleChangesRequested) {
		t.Fatalf("stale_days edit not reclassified: %s", b)
	}
	collectRules := staleSoon
	collectRules.TestApproval = rules.TestApproval{MarkerChecks: []string{"tests"}, Command: "/ok-to-test"}
	upd, err = en.db.UpdateRepoRules(en.ctx, en.repo.ID, collectRules, en.clock.Now())
	if err != nil || !upd.CollectionChanged {
		t.Fatalf("collection update = %+v, %v", upd, err)
	}
	if err := en.eng.RulesChanged(en.ctx, en.repo.ID, true); err != nil {
		t.Fatal(err)
	}
	en.dispatch()
	if in := en.src.inputs[len(en.src.inputs)-1]; in.Rules.CollectionHash() != collectRules.CollectionHash() {
		t.Fatal("re-collection used the old rules")
	}
	if b := en.detail(1).Result.Bucket; b == string(classify.Unclassified) {
		t.Fatal("refresh placeholder not replaced")
	}
}

func TestReclassifyIsTimeDependentAndGuarded(t *testing.T) {
	en := newEnv(t, Config{})
	en.src.set(func(f *fakeSource) {
		f.outcome = func(_ context.Context, in collect.Input) collect.Outcome {
			out := collectedOutcome(in)
			out.Record.MyReviews = []classify.Review{{State: "CHANGES_REQUESTED", SubmittedAt: t0, Commit: "h"}}
			out.Record.MyLastCommentAt = &t0
			return out
		}
	})
	en.observeAndEnrich(obs(1, "a"))
	items, err := en.db.Reclassifiable(en.ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	en.clock.Advance(2 * time.Minute)
	en.observeAndEnrich(obs(1, "a2"))
	en.clock.Advance(8 * 24 * time.Hour)
	if err := en.eng.reclassifyItems(en.ctx, items); err != nil {
		t.Fatalf("stale pass: %v", err)
	}
	if b := en.detail(1).Result.Bucket; b != string(classify.WaitingOnAuthor) {
		t.Fatalf("stale reclassification overwrote newer evidence: %s", b)
	}
	if err := en.eng.Reclassify(en.ctx, 0); err != nil {
		t.Fatal(err)
	}
	if b := en.detail(1).Result.Bucket; b != string(classify.StaleChangesRequested) {
		t.Fatalf("time-dependent rule not applied: %s", b)
	}
}

func TestBaseAdvanceInvalidatesWithoutHeadChange(t *testing.T) {
	en := newEnv(t, Config{})
	fields := collect.FingerprintFields{UpdatedAt: t0, BaseOID: "base1", HeadOID: "head1", MergeState: "CLEAN"}
	en.src.set(func(f *fakeSource) {
		f.observed = []collect.Observed{{Number: 1, Fingerprint: collect.Fingerprint(fields), HeadOID: "head1"}}
	})
	en.mustPoll()
	if _, err := en.eng.DispatchDue(en.ctx); err != nil {
		t.Fatal(err)
	}
	en.eng.Wait()
	before := en.state(1)
	fields.BaseOID = "base2"
	en.src.set(func(f *fakeSource) {
		f.observed = []collect.Observed{{Number: 1, Fingerprint: collect.Fingerprint(fields), HeadOID: "head1"}}
	})
	en.clock.Advance(2 * time.Minute)
	en.mustPoll()
	after := en.state(1)
	if after.Revision != before.Revision+1 || after.RetryAt == nil {
		t.Fatalf("base advance did not invalidate: before %+v after %+v", before, after)
	}
	if d := en.detail(1); d.HeadChangedAt != nil {
		t.Fatalf("base advance recorded a head change at %v", d.HeadChangedAt)
	}
}

func TestRetryDelay(t *testing.T) {
	en := newEnv(t, Config{})
	for failures, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 9: 2 * time.Minute} {
		if got := en.eng.retryDelay(failures); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", failures, got, want)
		}
	}
}
