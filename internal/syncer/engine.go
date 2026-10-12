// Package syncer polls GitHub, schedules enrichment, and keeps stored results
// current (§7). It is the spec's "sync" package, renamed to avoid shadowing
// the standard library.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	gosync "sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/github"
	"github.com/thingzio/devbox/internal/hub"
	"github.com/thingzio/devbox/internal/metrics"
	"github.com/thingzio/devbox/internal/store"
)

// ErrNoToken wraps failures to obtain an account's token (missing or keychain error).
var ErrNoToken = errors.New("no usable token for account")

// Source is one account's view of GitHub; *collect.Source implements it.
type Source interface {
	Fingerprints(ctx context.Context, owner, name string) ([]collect.Observed, error)
	Collect(ctx context.Context, in collect.Input) collect.Outcome
}

// Config tunes the engine; zero values take defaults.
type Config struct {
	PollInterval    time.Duration // default 2m
	Workers         int           // concurrent enrichments per account, default 4
	DispatchEvery   time.Duration // default 1s
	ReclassifyEvery time.Duration // default 5m
	ResyncEvery     time.Duration // poller set reconciliation, default 30s
}

// Deps are the engine's collaborators.
type Deps struct {
	Store   *store.DB
	Hub     *hub.Hub
	Metrics *metrics.Metrics
	Log     *slog.Logger
	Sources func(account string) (Source, error)
	Now     func() time.Time
	Rand    func() float64 // [0, 1), for jitter
}

const (
	maxPollBackoff     = 15 * time.Minute
	safetyAge          = 30 * time.Minute
	staleEvidenceLimit = 30 * time.Minute
	retryBase          = 30 * time.Second
	retryCap           = 2 * time.Minute
	dispatchBatch      = 256
)

type prKey struct {
	repoID int64
	number int
}

// Engine runs pollers, enrichment workers, and reclassification.
type Engine struct {
	cfg       Config
	d         Deps
	mu        gosync.Mutex
	jobs      map[prKey]context.CancelFunc
	sems      map[string]chan struct{}
	paused    map[string]time.Time
	reclassMu gosync.Mutex
	wg        gosync.WaitGroup
	wake      chan struct{}
	resync    chan struct{}
}

// New returns an engine with defaults applied.
func New(cfg Config, d Deps) *Engine {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Minute
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.DispatchEvery <= 0 {
		cfg.DispatchEvery = time.Second
	}
	if cfg.ReclassifyEvery <= 0 {
		cfg.ReclassifyEvery = 5 * time.Minute
	}
	if cfg.ResyncEvery <= 0 {
		cfg.ResyncEvery = 30 * time.Second
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Rand == nil {
		d.Rand = rand.Float64 //nolint:gosec // jitter needs no cryptographic randomness
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Metrics == nil {
		d.Metrics = metrics.New(prometheus.NewRegistry())
	}
	if d.Hub == nil {
		d.Hub = hub.New()
	}
	return &Engine{
		cfg: cfg, d: d,
		jobs: map[prKey]context.CancelFunc{}, sems: map[string]chan struct{}{}, paused: map[string]time.Time{},
		wake: make(chan struct{}, 1), resync: make(chan struct{}, 1),
	}
}

func (e *Engine) now() time.Time { return e.d.Now().UTC() }

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// ReposChanged asks the run loop to start or stop pollers now.
func (e *Engine) ReposChanged() { signal(e.resync) }

// Wait blocks until every started enrichment job has finished.
func (e *Engine) Wait() { e.wg.Wait() }

// ObserveRate records a rate reading and pauses the account below 10% remaining (§7).
func (e *Engine) ObserveRate(account string, r github.Rate) {
	if r.Resource != "" {
		e.d.Metrics.RateRemaining.WithLabelValues(account, r.Resource).Set(float64(r.Remaining))
	}
	if r.Limit > 0 && r.Remaining*10 < r.Limit {
		e.pauseUntil(account, r.Reset, fmt.Sprintf("%s rate limit low (%d of %d left)", r.Resource, r.Remaining, r.Limit))
	}
}

func (e *Engine) pauseUntil(account string, until time.Time, why string) {
	now := e.now()
	if !until.After(now) {
		until = now.Add(time.Minute)
	}
	e.mu.Lock()
	extended := until.After(e.paused[account])
	if extended {
		e.paused[account] = until
	}
	e.mu.Unlock()
	if extended {
		e.d.Log.Info("pausing account", "account", account, "until", until, "reason", why)
	}
}

func (e *Engine) isPaused(account string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	until, ok := e.paused[account]
	if !ok {
		return false
	}
	if !e.now().Before(until) {
		delete(e.paused, account)
		return false
	}
	return true
}

// RateStatus returns accounts paused for rate limits and when they resume.
func (e *Engine) RateStatus() map[string]time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	out := map[string]time.Time{}
	for account, until := range e.paused {
		if now.Before(until) {
			out[account] = until
		}
	}
	return out
}

// accountBlocked pauses the account on rate exhaustion, or marks it
// needs_auth on a rejected or unavailable token, and reports whether it did.
func (e *Engine) accountBlocked(ctx context.Context, account string, err error) bool {
	if reset, ok := github.RateLimitReset(err); ok {
		e.pauseUntil(account, reset, "rate limit exhausted")
		return true
	}
	if !errors.Is(err, github.ErrUnauthorized) && !errors.Is(err, ErrNoToken) {
		return false
	}
	detail := "token rejected by GitHub; re-authenticate"
	if errors.Is(err, ErrNoToken) {
		detail = err.Error()
	}
	if serr := e.d.Store.SetAccountStatus(ctx, account, store.StatusNeedsAuth, detail); serr != nil {
		e.d.Log.Error("mark account needs_auth", "account", account, "err", serr)
	}
	e.d.Log.Warn("account needs re-authentication", "account", account, "err", err)
	return true
}

func (e *Engine) semaphore(account string) chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	sem, ok := e.sems[account]
	if !ok {
		sem = make(chan struct{}, e.cfg.Workers)
		e.sems[account] = sem
	}
	return sem
}

func (e *Engine) reserve(ctx context.Context, k prKey) (context.Context, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, busy := e.jobs[k]; busy {
		return nil, false
	}
	jctx, cancel := context.WithCancel(ctx)
	e.jobs[k] = cancel
	return jctx, true
}

func (e *Engine) release(k prKey) {
	e.mu.Lock()
	cancel, ok := e.jobs[k]
	delete(e.jobs, k)
	e.mu.Unlock()
	if ok {
		cancel()
	}
}

func (e *Engine) cancelJob(k prKey) {
	e.mu.Lock()
	cancel := e.jobs[k]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *Engine) cancelRepo(repoID int64) {
	e.mu.Lock()
	var cancels []context.CancelFunc
	for k, cancel := range e.jobs {
		if k.repoID == repoID {
			cancels = append(cancels, cancel)
		}
	}
	e.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (e *Engine) published(log *slog.Logger, repoID int64, number int, tr *store.Transition) {
	if tr != nil {
		from := ""
		if tr.From != nil {
			from = *tr.From
		}
		log.Info("bucket transition", "pr", number, "from", from, "to", tr.To, "reason", tr.Reason)
		e.d.Metrics.Transitions.WithLabelValues(tr.To).Inc()
	}
	e.d.Hub.Publish(hub.Event{RepoID: repoID, Number: number})
}
