// Package metrics defines devbox's Prometheus metrics.
package metrics

import (
	"context"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/thingzio/devbox/internal/store"
)

// Metrics are the instruments updated by the engine and GitHub client.
type Metrics struct {
	PollDuration   *prometheus.HistogramVec
	PollFailures   *prometheus.CounterVec
	GitHubRequests *prometheus.CounterVec
	RateRemaining  *prometheus.GaugeVec
	EnrichQueue    *prometheus.GaugeVec
	Transitions    *prometheus.CounterVec
	GraphQLCost    *prometheus.CounterVec
	Enrichments    *prometheus.CounterVec
	EnrichDuration prometheus.Histogram
	TokenErrors    *prometheus.CounterVec
}

// New creates and registers the metrics.
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		PollDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "devbox_poll_duration_seconds", Help: "Fingerprint poll duration.", Buckets: prometheus.DefBuckets,
		}, []string{"repo"}),
		PollFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devbox_poll_failures_total", Help: "Failed fingerprint polls.",
		}, []string{"repo"}),
		GitHubRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devbox_github_requests_total", Help: "GitHub API requests by kind and HTTP status (0 = transport error).",
		}, []string{"kind", "status"}),
		RateRemaining: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "devbox_github_rate_remaining", Help: "Latest remaining GitHub rate budget.",
		}, []string{"account", "resource"}),
		EnrichQueue: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "devbox_enrich_queue_depth", Help: "Due enrichment jobs waiting for a worker or a rate-limit pause.",
		}, []string{"account"}),
		Transitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devbox_transitions_total", Help: "Bucket transitions by destination bucket.",
		}, []string{"to_bucket"}),
		GraphQLCost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devbox_github_graphql_cost_total", Help: "GraphQL rate-limit points spent, by operation.",
		}, []string{"operation"}),
		Enrichments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devbox_enrichments_total",
			Help: "Enrichment attempts by outcome: collected, mismatch, failed, deferred, stale, cancelled.",
		}, []string{"outcome"}),
		EnrichDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "devbox_enrichment_duration_seconds", Help: "Time to collect one pull request's evidence.",
			Buckets: []float64{0.25, 0.5, 1, 2, 5, 10, 30, 60},
		}),
		TokenErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devbox_token_errors_total", Help: "Failed account token reads: missing or unavailable.",
		}, []string{"kind"}),
	}
	reg.MustRegister(m.PollDuration, m.PollFailures, m.GitHubRequests, m.RateRemaining, m.EnrichQueue, m.Transitions,
		m.GraphQLCost, m.Enrichments, m.EnrichDuration, m.TokenErrors)
	return m
}

// ObserveGraphQLCost adds a query's cost; it matches github.Options.OnGraphQLCost.
func (m *Metrics) ObserveGraphQLCost(operation string, cost int) {
	m.GraphQLCost.WithLabelValues(operation).Add(float64(cost))
}

// RegisterStateGauges exposes values read at scrape time: accounts paused and
// live UI event streams.
func RegisterStateGauges(reg prometheus.Registerer, paused, subscribers func() int) {
	reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "devbox_paused_accounts", Help: "Accounts whose GitHub reads are paused."},
			func() float64 { return float64(paused()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "devbox_sse_subscribers", Help: "Open UI event streams."},
			func() float64 { return float64(subscribers()) }),
	)
}

// ObserveRequest counts one GitHub request; it matches github.Options.OnRequest.
func (m *Metrics) ObserveRequest(kind string, status int) {
	m.GitHubRequests.WithLabelValues(kind, strconv.Itoa(status)).Inc()
}

// BucketCounter reads open PR counts per repository and bucket; *store.DB implements it.
type BucketCounter interface {
	BucketCounts(ctx context.Context) ([]store.BucketCount, error)
}

// RegisterPRGauge exposes devbox_prs{repo,bucket}, computed on each scrape.
func RegisterPRGauge(reg prometheus.Registerer, src BucketCounter) {
	reg.MustRegister(&prCollector{
		src:  src,
		desc: prometheus.NewDesc("devbox_prs", "Open PRs per enabled repository and bucket.", []string{"repo", "bucket"}, nil),
	})
}

type prCollector struct {
	src  BucketCounter
	desc *prometheus.Desc
}

func (c *prCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *prCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	counts, err := c.src.BucketCounts(ctx)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.desc, err)
		return
	}
	for _, bc := range counts {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(bc.Count), bc.Repo, bc.Bucket)
	}
}
