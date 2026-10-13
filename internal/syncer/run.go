package syncer

import (
	"context"
	"errors"
	gosync "sync"
	"time"

	"github.com/thingzio/devbox/internal/store"
)

// Run starts a poller per enabled repository, the dispatcher, and the
// reclassify ticker. It blocks until ctx is cancelled and every poller and
// enrichment job has stopped.
func (e *Engine) Run(ctx context.Context) error {
	pollers := map[int64]context.CancelFunc{}
	var pollWG gosync.WaitGroup
	// The first pass starts pollers for existing repositories, which wait for
	// Refresh in manual mode; repositories added later are checked once at once.
	reconcile := func(initial bool) {
		repos, err := e.d.Store.Repos(ctx)
		if err != nil {
			if ctx.Err() == nil {
				e.d.Log.Error("list repositories", "err", err)
			}
			return
		}
		enabled := map[int64]bool{}
		for _, r := range repos {
			if !r.Enabled {
				continue
			}
			enabled[r.ID] = true
			if _, running := pollers[r.ID]; running {
				continue
			}
			pctx, cancel := context.WithCancel(ctx)
			pollers[r.ID] = cancel
			pollWG.Add(1)
			pollFirst := !initial || !e.Polling().Manual
			go func(id int64) {
				defer pollWG.Done()
				e.pollLoop(pctx, id, pollFirst)
			}(r.ID)
		}
		for id, cancel := range pollers {
			if !enabled[id] {
				cancel()
				delete(pollers, id)
				e.cancelRepo(id)
			}
		}
	}
	reconcile(true)
	dispatch := time.NewTicker(e.cfg.DispatchEvery)
	defer dispatch.Stop()
	reclassify := time.NewTicker(e.cfg.ReclassifyEvery)
	defer reclassify.Stop()
	resync := time.NewTicker(e.cfg.ResyncEvery)
	defer resync.Stop()
	for {
		select {
		case <-ctx.Done():
			for _, cancel := range pollers {
				cancel()
			}
			pollWG.Wait()
			e.wg.Wait()
			return nil
		case <-e.resync:
			reconcile(false)
		case <-resync.C:
			reconcile(false)
		case <-dispatch.C:
			e.dispatch(ctx)
		case <-e.wake:
			e.dispatch(ctx)
		case <-reclassify.C:
			if err := e.Reclassify(ctx, 0); err != nil && ctx.Err() == nil {
				e.d.Log.Error("reclassify", "err", err)
			}
		}
	}
}

func (e *Engine) dispatch(ctx context.Context) {
	if _, err := e.DispatchDue(ctx); err != nil && ctx.Err() == nil {
		e.d.Log.Error("dispatch enrichment", "err", err)
	}
}

// pollLoop checks one repository on its interval, or only when poked in
// manual mode; a polling change re-evaluates the wait without checking.
func (e *Engine) pollLoop(ctx context.Context, repoID int64, pollFirst bool) {
	poke, done := e.watch(repoID)
	defer done()
	poll, failures := pollFirst, 0
	for {
		if poll {
			var err error
			failures, err = e.PollRepo(ctx, repoID)
			if ctx.Err() != nil || errors.Is(err, store.ErrNotFound) {
				return
			}
		}
		changed := e.pollingChanged()
		var tick <-chan time.Time
		var timer *time.Timer
		if !e.Polling().Manual {
			timer = time.NewTimer(e.pollDelay(failures))
			tick = timer.C
		}
		select {
		case <-ctx.Done():
		case <-poke:
			poll = true
		case <-tick:
			poll = true
		case <-changed:
			poll = false
		}
		if timer != nil {
			timer.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}
