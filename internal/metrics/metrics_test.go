package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/thingzio/devbox/internal/store"
)

type fakeCounts struct {
	counts []store.BucketCount
	err    error
}

func (f fakeCounts) BucketCounts(context.Context) ([]store.BucketCount, error) {
	return f.counts, f.err
}

func TestMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.ObserveRequest("graphql", 200)
	m.ObserveRequest("graphql", 200)
	if got := testutil.ToFloat64(m.GitHubRequests.WithLabelValues("graphql", "200")); got != 2 {
		t.Fatalf("requests = %v", got)
	}
	RegisterPRGauge(reg, fakeCounts{counts: []store.BucketCount{{Repo: "o/r", Bucket: "waiting_on_me", Count: 3}}})
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := false
	for _, f := range families {
		if f.GetName() == "devbox_prs" && f.GetMetric()[0].GetGauge().GetValue() == 3 {
			found = true
		}
	}
	if !found {
		t.Fatal("devbox_prs gauge missing")
	}
}

func TestPRGaugeReportsErrors(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterPRGauge(reg, fakeCounts{err: errors.New("db closed")})
	if _, err := reg.Gather(); err == nil {
		t.Fatal("store error not surfaced")
	}
}

func TestEngineMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.ObserveGraphQLCost("PullRequestDetail", 2)
	m.ObserveGraphQLCost("PullRequestDetail", 1)
	m.Enrichments.WithLabelValues("collected").Inc()
	m.EnrichDuration.Observe(0.2)
	m.TokenErrors.WithLabelValues("unavailable").Inc()
	if got := testutil.ToFloat64(m.GraphQLCost.WithLabelValues("PullRequestDetail")); got != 3 {
		t.Fatalf("graphql cost = %v", got)
	}
	RegisterStateGauges(reg, func() int { return 2 }, func() int { return 5 })
	want := map[string]float64{"devbox_paused_accounts": 2, "devbox_sse_subscribers": 5, "devbox_enrichments_total": 1, "devbox_token_errors_total": 1}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, f := range families {
		for _, mt := range f.GetMetric() {
			got[f.GetName()] += mt.GetGauge().GetValue() + mt.GetCounter().GetValue()
		}
	}
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s = %v, want %v", name, got[name], v)
		}
	}
	if testutil.CollectAndCount(m.EnrichDuration) != 1 {
		t.Error("enrichment duration histogram missing")
	}
}
