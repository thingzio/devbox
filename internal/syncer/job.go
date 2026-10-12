package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/store"
)

// DispatchDue starts enrichment for due work, at most one job per PR and
// Workers per account. Paused accounts are left out of the query so their
// backlog cannot crowd out other accounts' work (§7).
func (e *Engine) DispatchDue(ctx context.Context) (int, error) {
	works, err := e.d.Store.DueWork(ctx, e.now(), dispatchBatch, e.pausedAccounts())
	if err != nil {
		return 0, err
	}
	queued := map[string]int{}
	started := 0
	for _, w := range works {
		if e.isPaused(w.Account) {
			queued[w.Account]++
			continue
		}
		key := prKey{w.RepoID, w.Number}
		jctx, ok := e.reserve(ctx, key)
		if !ok {
			continue
		}
		sem := e.semaphore(w.Account)
		select {
		case sem <- struct{}{}:
		default:
			e.release(key)
			queued[w.Account]++
			continue
		}
		started++
		e.wg.Add(1)
		go func(w store.Work) {
			defer e.wg.Done()
			defer signal(e.wake)
			defer func() { <-sem }()
			defer e.release(key)
			e.RunJob(jctx, w)
		}(w)
	}
	e.mu.Lock()
	accounts := make([]string, 0, len(e.sems))
	for account := range e.sems {
		accounts = append(accounts, account)
	}
	e.mu.Unlock()
	for _, account := range accounts {
		e.d.Metrics.EnrichQueue.WithLabelValues(account).Set(float64(queued[account]))
	}
	return started, nil
}

// RunJob performs one enrichment attempt and commits its outcome under the §7 guards.
func (e *Engine) RunJob(ctx context.Context, w store.Work) {
	log := e.d.Log.With("repo", w.Owner+"/"+w.Name, "pr", w.Number)
	src, err := e.d.Sources(w.Account)
	if err != nil {
		e.accountBlocked(ctx, w.Account, fmt.Errorf("%w: %w", ErrNoToken, err))
		return
	}
	out := src.Collect(ctx, collect.Input{
		RepoID: w.RepoID, Owner: w.Owner, Name: w.Name, Viewer: w.Account, Number: w.Number, Rules: w.Rules,
		QueuedFingerprint: w.Fingerprint, HeadChangedAt: w.HeadChangedAt, Now: e.now(),
	})
	if ctx.Err() != nil {
		log.Debug("enrichment cancelled")
		return
	}
	now := e.now()
	switch out.Kind {
	case collect.Mismatch:
		err := e.d.Store.CommitObservation(ctx, store.DetailObservation{
			RepoID: w.RepoID, RepoRevision: w.RepoRevision, Number: w.Number, PRRevision: w.PRRevision,
			Now: now, Fingerprint: out.Fingerprint, HeadOID: out.HeadOID,
		})
		switch {
		case errors.Is(err, store.ErrStale):
			log.Debug("detail observation discarded: newer revision")
		case err != nil:
			log.Error("commit detail observation", "err", err)
		default:
			log.Debug("detail observation committed", "head", out.HeadOID)
			signal(e.wake)
		}
	case collect.Collected:
		e.commitRecord(ctx, log, w, out.Record, out.Caches, now, true, "", out.RetryableEvidence)
	case collect.Failed:
		if e.accountBlocked(ctx, w.Account, out.Err) {
			log.Info("enrichment deferred: account blocked", "err", out.Err)
			return
		}
		msg := out.Err.Error()
		if out.Retryable {
			if prior, ok := e.usablePrior(w, now); ok {
				since := now
				if prior.CoreRetry != nil {
					since = prior.CoreRetry.Since
				}
				prior.CoreRetry = &classify.CoreRetry{Message: msg, Since: since}
				e.commitRecord(ctx, log, w, prior, store.Caches{}, now, false, msg, true)
				return
			}
		}
		rec := out.Record
		rec.Number, rec.Version, rec.CollectionRulesHash, rec.Error = w.Number, classify.RecordVersion, w.Rules.CollectionHash(), msg
		e.commitRecord(ctx, log, w, rec, out.Caches, now, false, msg, out.Retryable)
	}
}

// usablePrior returns the stored record when it may stand in for a retryable
// core failure: same version and collection hash, no error, collected within 30m (§7).
func (e *Engine) usablePrior(w store.Work, now time.Time) (classify.Record, bool) {
	if len(w.Record) == 0 || w.RecordAt == nil || now.Sub(*w.RecordAt) > staleEvidenceLimit {
		return classify.Record{}, false
	}
	var prior classify.Record
	if err := json.Unmarshal(w.Record, &prior); err != nil {
		return classify.Record{}, false
	}
	if prior.Error != "" || prior.Version != classify.RecordVersion || prior.CollectionRulesHash != w.Rules.CollectionHash() {
		return classify.Record{}, false
	}
	return prior, true
}

func (e *Engine) commitRecord(ctx context.Context, log *slog.Logger, w store.Work, rec classify.Record, caches store.Caches,
	now time.Time, advance bool, errMsg string, retry bool) {
	res := classify.Classify(rec, w.Rules, w.Account, now)
	data, err := json.Marshal(rec)
	if err != nil {
		log.Error("encode record", "err", err)
		return
	}
	c := store.EnrichCommit{
		RepoID: w.RepoID, RepoRevision: w.RepoRevision, Number: w.Number, PRRevision: w.PRRevision, Now: now,
		Record: data, AdvanceRecordAt: advance, Title: rec.Title, URL: rec.URL, Author: rec.Author, Error: errMsg,
		Result: resultRow(res), Caches: caches,
	}
	if retry {
		c.EnrichFailures = w.EnrichFailures + 1
		next := now.Add(e.retryDelay(c.EnrichFailures))
		c.RetryAt = &next
	}
	tr, err := e.d.Store.CommitEnrichment(ctx, c)
	switch {
	case errors.Is(err, store.ErrStale):
		log.Debug("enrichment discarded: newer revision or lifecycle change")
		return
	case err != nil:
		if ctx.Err() == nil {
			log.Error("commit enrichment", "err", err)
		}
		return
	}
	log.Debug("enrichment committed", "head", rec.HeadOID, "bucket", res.Bucket, "retry_at", c.RetryAt, "error", errMsg)
	e.published(log, w.RepoID, w.Number, tr)
}

func resultRow(r classify.Result) store.ResultRow {
	return store.ResultRow{Bucket: string(r.Bucket), Reason: r.Reason, Action: r.Action, WaitingSince: r.WaitingSince, Also: r.Also, Notes: r.Notes}
}

// retryDelay is exponential from 30s, capped at 2m, with ±20% jitter (§7).
func (e *Engine) retryDelay(failures int) time.Duration {
	d := retryBase
	for i := 1; i < failures && d < retryCap; i++ {
		d *= 2
	}
	d = time.Duration(float64(d) * (0.8 + 0.4*e.d.Rand()))
	return min(d, retryCap)
}
