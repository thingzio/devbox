package classify

import (
	"slices"
	"testing"
	"time"
)

func TestCompareOrdersBySpec(t *testing.T) {
	soon := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	later := soon.Add(72 * time.Hour)
	old := soon.Add(-96 * time.Hour)
	recent := soon.Add(-24 * time.Hour)
	items := []Rankable{
		{Bucket: NeedsInitialReview, WaitingSince: &recent, Number: 9, Repo: "o/r"},
		{Bucket: NeedsInitialReview, WaitingSince: &old, Number: 8, Repo: "o/r"},
		{Bucket: NeedsInitialReview, FirstTime: true, WaitingSince: &recent, Number: 7, Repo: "o/r"},
		{Bucket: NeedsInitialReview, MilestoneDue: &later, WaitingSince: &recent, Number: 6, Repo: "o/r"},
		{Bucket: NeedsInitialReview, MilestoneDue: &soon, WaitingSince: &recent, Number: 5, Repo: "o/r"},
		{Bucket: WaitingOnMe, WaitingSince: &recent, Number: 4, Repo: "o/r"},
		{Bucket: NeedsInitialReview, Number: 3, Repo: "o/r"},
		{Bucket: NeedsInitialReview, WaitingSince: &old, Number: 8, Repo: "a/b"},
	}
	slices.SortFunc(items, Compare)
	var got []int
	for _, it := range items {
		got = append(got, it.Number)
	}
	want := []int{4, 5, 6, 7, 8, 8, 9, 3}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if items[4].Repo != "a/b" {
		t.Fatalf("ties should break by repo name, got %s first", items[4].Repo)
	}
}

func TestRankKey(t *testing.T) {
	due := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	since := due.Add(-time.Hour)
	rec := Record{Number: 3, AuthorAssociation: "FIRST_TIMER", Milestone: &Milestone{Title: "v1", DueOn: &due}}
	k := RankKey(rec, WaitingOnMe, &since, "o/r")
	if !k.FirstTime || k.MilestoneDue == nil || !k.MilestoneDue.Equal(due) || k.Number != 3 || k.Repo != "o/r" || k.Bucket != WaitingOnMe {
		t.Fatalf("RankKey = %+v", k)
	}
}

func TestIsFirstTime(t *testing.T) {
	for assoc, want := range map[string]bool{
		"FIRST_TIME_CONTRIBUTOR": true, "FIRST_TIMER": true, "CONTRIBUTOR": false, "MEMBER": false, "": false,
	} {
		if got := IsFirstTime(assoc); got != want {
			t.Errorf("IsFirstTime(%q) = %v", assoc, got)
		}
	}
}

func TestSizeBadge(t *testing.T) {
	cases := []struct {
		add, del int
		want     string
	}{{0, 0, "S"}, {30, 19, "S"}, {40, 10, "M"}, {200, 49, "M"}, {200, 50, "L"}, {999, 0, "L"}, {500, 500, "XL"}}
	for _, tc := range cases {
		if got := SizeBadge(tc.add, tc.del); got != tc.want {
			t.Errorf("SizeBadge(%d, %d) = %s, want %s", tc.add, tc.del, got, tc.want)
		}
	}
}

func TestHandoff(t *testing.T) {
	urls := []string{"https://github.com/o/r/pull/1", "https://github.com/o/r/pull/2"}
	want := "/reviewing-pull-requests https://github.com/o/r/pull/1 https://github.com/o/r/pull/2"
	for _, b := range []Bucket{NeedsRereview, NeedsInitialReview, Bot} {
		if got := Handoff(b, urls); got != want {
			t.Errorf("Handoff(%s) = %q", b, got)
		}
	}
	if got := Handoff(WaitingTestApproval, urls); got != "post each command only after reading that PR's diff" {
		t.Errorf("test approval handoff = %q", got)
	}
	if got := Handoff(WaitingOnMe, urls); got != "" {
		t.Errorf("waiting on me handoff = %q, want empty", got)
	}
}
