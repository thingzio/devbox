package syncer

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/store"
)

// Reclassify re-runs classification on stored open records without GitHub
// reads; repoID 0 means every enabled repository. Passes are serialized (§7).
func (e *Engine) Reclassify(ctx context.Context, repoID int64) error {
	e.reclassMu.Lock()
	defer e.reclassMu.Unlock()
	items, err := e.d.Store.Reclassifiable(ctx, repoID)
	if err != nil {
		return err
	}
	return e.reclassifyItems(ctx, items)
}

func (e *Engine) reclassifyItems(ctx context.Context, items []store.Classifiable) error {
	now := e.now()
	changed := 0
	for _, it := range items {
		var rec classify.Record
		if err := json.Unmarshal(it.Record, &rec); err != nil {
			e.d.Log.Warn("skip undecodable record", "repo_id", it.RepoID, "pr", it.Number, "err", err)
			continue
		}
		if rec.Version != classify.RecordVersion || rec.CollectionRulesHash != it.Rules.CollectionHash() {
			continue // waiting for enrichment to replace the refreshing result
		}
		res := classify.Classify(rec, it.Rules, it.Account, now)
		tr, ok, err := e.d.Store.CommitResult(ctx, it.RepoID, it.RepoRevision, it.Number, it.PRRevision, resultRow(res), now)
		if errors.Is(err, store.ErrStale) {
			continue
		}
		if err != nil {
			return err
		}
		if ok {
			changed++
			e.published(e.d.Log.With("repo_id", it.RepoID), it.RepoID, it.Number, tr)
		}
	}
	e.d.Log.Debug("reclassify pass", "records", len(items), "changed", changed)
	return nil
}

// RulesChanged reacts to a rules edit: a collection change cancels obsolete
// work and wakes the dispatcher; a stale_days-only edit reclassifies now (§6).
func (e *Engine) RulesChanged(ctx context.Context, repoID int64, collectionChanged bool) error {
	if collectionChanged {
		e.cancelRepo(repoID)
		signal(e.wake)
		return nil
	}
	return e.Reclassify(ctx, repoID)
}
