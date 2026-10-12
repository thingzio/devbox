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
	reconcile := func() {
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
			go func(id int64) {
				defer pollWG.Done()
				e.pollLoop(pctx, id)
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
	reconcile()
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
			reconcile()
		case <-resync.C:
			reconcile()
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

func (e *Engine) pollLoop(ctx context.Context, repoID int64) {
	for {
		failures, err := e.PollRepo(ctx, repoID)
		if ctx.Err() != nil || errors.Is(err, store.ErrNotFound) {
			return
		}
		timer := time.NewTimer(e.pollDelay(failures))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
