package syncer

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/store"
)

// PollRepo runs one fingerprint poll (§7) and returns the repository's
// consecutive failure count for backoff.
func (e *Engine) PollRepo(ctx context.Context, repoID int64) (int, error) {
	repo, err := e.d.Store.Repo(ctx, repoID)
	if err != nil {
		return 0, err
	}
	if !repo.Enabled {
		return 0, nil
	}
	acct, err := e.d.Store.Account(ctx, repo.Account)
	if err != nil {
		return 0, err
	}
	if acct.Status != store.StatusOK || e.isPaused(repo.Account) {
		return 0, nil
	}
	log := e.d.Log.With("repo", repo.FullName(), "poll_id", rand.Text()[:8])
	start := e.now()
	src, err := e.d.Sources(repo.Account)
	if err != nil {
		err = fmt.Errorf("%w: %w", ErrNoToken, err)
		e.accountBlocked(ctx, repo.Account, err)
		return e.recordPoll(ctx, log, repo, start, err)
	}
	observed, err := src.Fingerprints(ctx, repo.Owner, repo.Name)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if err != nil {
		e.accountBlocked(ctx, repo.Account, err)
		return e.recordPoll(ctx, log, repo, start, err)
	}
	states, err := e.d.Store.PRStates(ctx, repo.ID)
	if err != nil {
		return 0, err
	}
	plan := Plan(repo, states, observed, start, classify.RecordVersion)
	for _, o := range plan.Upserts {
		log.Debug("pr observed", "pr", o.Number, "new", !o.Exists, "invalidate", o.Invalidate, "head", o.HeadOID)
	}
	applied, err := e.d.Store.ApplyPoll(ctx, plan)
	if errors.Is(err, store.ErrStale) {
		log.Debug("poll discarded: repository changed during poll")
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, tr := range applied.Closed {
		e.cancelJob(prKey{repo.ID, tr.Number})
		e.published(log, repo.ID, tr.Number, &tr)
	}
	elapsed := e.now().Sub(start)
	e.d.Metrics.PollDuration.WithLabelValues(repo.FullName()).Observe(elapsed.Seconds())
	log.Debug("poll complete", "prs", len(observed), "inserted", len(applied.Inserted), "updated", len(applied.Updated),
		"skipped", len(applied.Skipped), "closed", len(applied.Closed), "duration", elapsed)
	if len(plan.Upserts) > 0 {
		signal(e.wake)
	}
	return e.recordPoll(ctx, log, repo, start, nil)
}

func (e *Engine) recordPoll(ctx context.Context, log *slog.Logger, repo store.Repo, at time.Time, pollErr error) (int, error) {
	s, err := e.d.Store.RecordPoll(ctx, repo.ID, at, pollErr)
	if err != nil {
		return 0, errors.Join(pollErr, err)
	}
	if pollErr == nil {
		return 0, nil
	}
	e.d.Metrics.PollFailures.WithLabelValues(repo.FullName()).Inc()
	log.Warn("poll failed", "err", pollErr, "consecutive_failures", s.ConsecutiveFailures)
	return s.ConsecutiveFailures, pollErr
}

// pollDelay is the jittered interval, or min(interval × 2^failures, 15m) after failures (§7).
func (e *Engine) pollDelay(failures int) time.Duration {
	if failures <= 0 {
		return time.Duration(float64(e.cfg.PollInterval) * (0.9 + 0.2*e.d.Rand()))
	}
	d := e.cfg.PollInterval
	for i := 0; i < failures && d < maxPollBackoff; i++ {
		d *= 2
	}
	return min(d, maxPollBackoff)
}
