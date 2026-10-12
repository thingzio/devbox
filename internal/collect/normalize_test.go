package collect

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func fixtureDetail(t *testing.T) *Detail {
	t.Helper()
	raw, err := os.ReadFile("testdata/detail.json")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data struct {
			Repository struct {
				PullRequest *Detail `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env.Data.Repository.PullRequest
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNormalizeFixture(t *testing.T) {
	rec, err := Normalize(fixtureDetail(t), "me", nil)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"author", rec.Author == "alice" && !rec.IsBot && rec.Fork && rec.AuthorAssociation == "FIRST_TIME_CONTRIBUTOR"},
		{"size", rec.Additions == 120 && rec.Deletions == 4},
		{"milestone", rec.Milestone != nil && rec.Milestone.Title == "v1.0" && rec.Milestone.DueOn.Equal(ts("2026-10-15T00:00:00Z"))},
		{"review requested", rec.ReviewRequestedFromMe},
		{"my reviews sorted, pending dropped", len(rec.MyReviews) == 2 && rec.MyReviews[0].State == "COMMENTED" && rec.MyReviews[1].State == "APPROVED"},
		{"approved at", rec.ApprovedAt != nil && rec.ApprovedAt.Equal(ts("2026-09-30T08:00:00Z"))},
		{"others decisive", len(rec.OthersDecisive) == 1 && rec.OthersDecisive[0].Author == "bob" && rec.OthersDecisive[0].State == "CHANGES_REQUESTED"},
		{"threads waiting", len(rec.MyThreadsWaiting) == 1 && strings.HasSuffix(rec.MyThreadsWaiting[0].URL, "discussion_r1") &&
			rec.MyThreadsWaiting[0].LastAt.Equal(ts("2026-10-01T09:00:00Z"))},
		{"unresolved", rec.UnresolvedThreads == 1},
		{"author updated", rec.AuthorUpdatedAt != nil && rec.AuthorUpdatedAt.Equal(ts("2026-10-01T09:00:00Z"))},
		{"unverified", len(rec.UnverifiedCommits) == 1 && rec.UnverifiedCommits[0].Reason == "unsigned" && strings.HasPrefix(rec.UnverifiedCommits[0].SHA, "c2")},
		{"signatures complete", rec.SignaturesComplete},
		{"head", rec.HeadCommittedAt.Equal(ts("2026-10-01T08:00:00Z")) && rec.HeadChangedAt == nil && rec.HeadOID == "h111111111111111111111111111111111111111"},
		{"merge", rec.MergeState == "BLOCKED" && rec.ReviewDecision == "APPROVED" && !rec.AutoMerge && rec.BaseRef == "main"},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s: record = %+v", c.name, rec)
		}
	}
}

func TestNormalizeHeadChangeCountsAsAuthorActivity(t *testing.T) {
	changed := ts("2026-10-02T00:00:00Z")
	rec, err := Normalize(fixtureDetail(t), "me", &changed)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.AuthorUpdatedAt.Equal(changed) || !rec.HeadChangedAt.Equal(changed) {
		t.Fatalf("head change not applied: %+v %+v", rec.AuthorUpdatedAt, rec.HeadChangedAt)
	}
}

func TestNormalizeEdgeCases(t *testing.T) {
	d := fixtureDetail(t)
	d.Author = nil
	d.Commits.TotalCount = 150
	rec, err := Normalize(d, "me", nil)
	if err != nil || rec.Author != "ghost" || rec.SignaturesComplete {
		t.Fatalf("ghost/incomplete = %+v, %v", rec, err)
	}
	d = fixtureDetail(t)
	d.Author = &Actor{Login: "dependabot[bot]", Typename: "Bot"}
	if rec, _ := Normalize(d, "me", nil); !rec.IsBot {
		t.Fatal("bot not detected")
	}
	d = fixtureDetail(t)
	d.Author = &Actor{Login: "renovate[bot]", Typename: "User"}
	if rec, _ := Normalize(d, "me", nil); !rec.IsBot {
		t.Fatal("[bot] suffix not detected")
	}
	d = fixtureDetail(t)
	d.ReviewThreads.TotalCount = 101
	rec, err = Normalize(d, "me", nil)
	if err == nil || !strings.Contains(err.Error(), "more than 100 review threads") || rec.Title != "Add GPU recipe" {
		t.Fatalf("thread overflow = %v, title %q", err, rec.Title)
	}
}
