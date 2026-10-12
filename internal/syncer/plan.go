package syncer

import (
	"slices"
	"time"

	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/store"
)

type safetyCandidate struct {
	obs  store.Observation
	last *time.Time
}

// Plan turns a complete fingerprint observation into store writes (§7 steps 3-4).
// It is pure: the store applies the plan under revision guards.
func Plan(repo store.Repo, states []store.PRState, observed []collect.Observed, now time.Time, recordVersion int) store.PollApply {
	p := store.PollApply{RepoID: repo.ID, RepoRevision: repo.Revision, Owner: repo.Owner, Name: repo.Name, Now: now}
	rulesHash := repo.Rules.CollectionHash()
	byNumber := make(map[int]store.PRState, len(states))
	for _, s := range states {
		byNumber[s.Number] = s
	}
	seen := make(map[int]bool, len(observed))
	var safety []safetyCandidate
	for _, o := range observed {
		seen[o.Number] = true
		s, exists := byNumber[o.Number]
		ob := store.Observation{Number: o.Number, Fingerprint: o.Fingerprint, HeadOID: o.HeadOID, Exists: exists, ExpectedRevision: s.Revision}
		switch {
		case !exists, s.State != "open", s.Fingerprint != o.Fingerprint:
			ob.Invalidate = true
		case s.RetryAt != nil:
			continue // already pending: keep its revision and retry deadline
		case !s.HasRecord, s.RecordVersion != recordVersion, s.RulesHash != rulesHash:
			ob.Invalidate = true
		default:
			if last := lastCollection(s); last == nil || now.Sub(*last) >= safetyAge {
				safety = append(safety, safetyCandidate{obs: ob, last: last})
			}
			continue
		}
		p.Upserts = append(p.Upserts, ob)
	}
	slices.SortFunc(safety, func(a, b safetyCandidate) int {
		switch {
		case a.last == nil && b.last == nil:
			return a.obs.Number - b.obs.Number
		case a.last == nil:
			return -1
		case b.last == nil:
			return 1
		}
		return a.last.Compare(*b.last)
	})
	limit := max(1, len(observed)/10)
	for i := 0; i < len(safety) && i < limit; i++ {
		ob := safety[i].obs
		ob.Invalidate = true
		p.Upserts = append(p.Upserts, ob)
	}
	for _, s := range states {
		if s.State == "open" && !seen[s.Number] {
			p.Closes = append(p.Closes, s.Number)
		}
	}
	slices.Sort(p.Closes)
	return p
}

// lastCollection is when the PR was last collected for safety-reconcile
// purposes; a fatal attempt bounds it without counting as success (§7).
func lastCollection(s store.PRState) *time.Time {
	last := s.RecordAt
	if s.Error != "" && s.LastEnrichAttemptAt != nil && (last == nil || s.LastEnrichAttemptAt.After(*last)) {
		last = s.LastEnrichAttemptAt
	}
	return last
}
