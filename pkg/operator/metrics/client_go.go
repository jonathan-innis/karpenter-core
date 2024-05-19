package metrics

import (
	"context"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	clientmetrics "k8s.io/client-go/tools/metrics"
)

// this file contains setup logic to initialize the myriad of places
// that client-go registers metrics.  We copy the names and formats
// from Kubernetes so that we match the core controllers.

var (
	// client metrics.

	requestResult = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "client_go_requests_total",
			Help: "Number of HTTP requests, partitioned by status code, method, and host.",
		},
		[]string{"code", "method", "host"},
	)
	requestLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "client_go_requests_duration_seconds",
		Help:    "Request latency in seconds. Broken down by verb and URL.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 10),
	}, []string{"verb", "url"})
)

// RegisterClientMetrics sets up the client latency metrics from client-go.
func RegisterClientMetrics(r prometheus.Registerer) {
	// register the metrics with our registry
	r.MustRegister(requestResult, requestLatency)

	// register the metrics with client-go
	clientmetrics.Register(clientmetrics.RegisterOpts{
		RequestResult:  &resultAdapter{metric: requestResult},
		RequestLatency: &latencyAdapter{metric: requestLatency},
	})
}

// this section contains adapters, implementations, and other sundry organic, artisanally
// hand-crafted syntax trees required to convince client-go that it actually wants to let
// someone use its metrics.

// Client metrics adapters (method #1 for client-go metrics),
// copied (more-or-less directly) from k8s.io/kubernetes setup code
// (which isn't anywhere in an easily-importable place).

type resultAdapter struct {
	metric *prometheus.CounterVec
}

func (r *resultAdapter) Increment(_ context.Context, code, method, host string) {
	r.metric.WithLabelValues(code, method, host).Inc()
}

// latencyAdapter implements LatencyMetric.
type latencyAdapter struct {
	metric *prometheus.HistogramVec
}

// Observe increments the request latency metric for the given verb/URL.
func (l *latencyAdapter) Observe(_ context.Context, verb string, u url.URL, latency time.Duration) {
	l.metric.WithLabelValues(verb, u.String()).Observe(latency.Seconds())
}
