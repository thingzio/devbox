package syncer

import (
	"slices"
	"testing"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/collect"
	"github.com/thingzio/devbox/internal/rules"
	"github.com/thingzio/devbox/internal/store"
)

func TestPlan(t *testing.T) {
	repo := store.Repo{ID: 1, Revision: 3, Owner: "o", Name: "r", Rules: rules.Default()}
	hash := repo.Rules.CollectionHash()
	now := t0.Add(time.Hour)
	fresh := func(n int, fp string) store.PRState {
		at := now.Add(-time.Minute)
		return store.PRState{Number: n, State: "open", Revision: 5, Fingerprint: fp, HasRecord: true,
			RecordVersion: classify.RecordVersion, RulesHash: hash, RecordAt: &at}
	}
	upserted := func(p store.PollApply) map[int]store.Observation {
		m := map[int]store.Observation{}
		for _, o := range p.Upserts {
			m[o.Number] = o
		}
		return m
	}

	t.Run("new and changed invalidate; unchanged fresh records are skipped", func(t *testing.T) {
		p := Plan(repo, []store.PRState{fresh(1, "a"), fresh(2, "b")}, []collect.Observed{obs(1, "a"), obs(2, "b2"), obs(3, "c")}, now, classify.RecordVersion)
		u := upserted(p)
		if _, ok := u[1]; ok {
			t.Fatal("unchanged fresh PR was written")
		}
		if o := u[2]; !o.Invalidate || !o.Exists || o.ExpectedRevision != 5 {
			t.Fatalf("changed = %+v", o)
		}
		if o := u[3]; !o.Invalidate || o.Exists {
			t.Fatalf("new = %+v", o)
		}
		if p.RepoRevision != 3 || len(p.Closes) != 0 {
			t.Fatalf("plan = %+v", p)
		}
	})

	t.Run("pending unchanged PRs keep revision and deadline", func(t *testing.T) {
		s := fresh(1, "a")
		s.RetryAt = &now
		s.HasRecord = false
		if p := Plan(repo, []store.PRState{s}, []collect.Observed{obs(1, "a")}, now, classify.RecordVersion); len(p.Upserts) != 0 {
			t.Fatalf("pending PR rewritten: %+v", p.Upserts)
		}
		if p := Plan(repo, []store.PRState{s}, []collect.Observed{obs(1, "a2")}, now, classify.RecordVersion); !upserted(p)[1].Invalidate {
			t.Fatal("pending PR with changed fingerprint not invalidated")
		}
	})

	t.Run("stale record version or collection hash invalidates", func(t *testing.T) {
		oldVersion := fresh(1, "a")
		oldVersion.RecordVersion = 0
		otherHash := fresh(2, "b")
		otherHash.RulesHash = "different"
		p := Plan(repo, []store.PRState{oldVersion, otherHash}, []collect.Observed{obs(1, "a"), obs(2, "b")}, now, classify.RecordVersion)
		if u := upserted(p); !u[1].Invalidate || !u[2].Invalidate {
			t.Fatalf("upserts = %+v", p.Upserts)
		}
	})

	t.Run("closed PR that reappears is reopened", func(t *testing.T) {
		s := fresh(1, "a")
		s.State = "closed"
		s.Revision = 9
		if o := upserted(Plan(repo, []store.PRState{s}, []collect.Observed{obs(1, "a")}, now, classify.RecordVersion))[1]; !o.Invalidate || o.ExpectedRevision != 9 {
			t.Fatalf("reopen = %+v", o)
		}
	})

	t.Run("open PRs missing from the observation close", func(t *testing.T) {
		closed := fresh(4, "d")
		closed.State = "closed"
		p := Plan(repo, []store.PRState{fresh(1, "a"), fresh(2, "b"), closed}, []collect.Observed{obs(1, "a")}, now, classify.RecordVersion)
		if !slices.Equal(p.Closes, []int{2}) {
			t.Fatalf("closes = %v", p.Closes)
		}
	})

	t.Run("safety reconcile is capped at 10 percent, oldest first", func(t *testing.T) {
		var states []store.PRState
		var observed []collect.Observed
		for i := 1; i <= 25; i++ {
			s := fresh(i, "x")
			old := now.Add(-31*time.Minute - time.Duration(i)*time.Minute)
			s.RecordAt = &old
			states = append(states, s)
			observed = append(observed, obs(i, "x"))
		}
		p := Plan(repo, states, observed, now, classify.RecordVersion)
		var got []int
		for _, o := range p.Upserts {
			if o.Invalidate {
				got = append(got, o.Number)
			}
		}
		if !slices.Equal(got, []int{25, 24}) {
			t.Fatalf("safety reconcile = %v, want the two oldest", got)
		}
	})

	t.Run("fatal records reconcile at most every 30 minutes", func(t *testing.T) {
		s := fresh(1, "a")
		old := now.Add(-2 * time.Hour)
		recent := now.Add(-5 * time.Minute)
		s.RecordAt, s.LastEnrichAttemptAt, s.Error = &old, &recent, "boom"
		if p := Plan(repo, []store.PRState{s}, []collect.Observed{obs(1, "a")}, now, classify.RecordVersion); len(p.Upserts) != 0 {
			t.Fatalf("fatal record reconciled early: %+v", p.Upserts)
		}
	})
}
