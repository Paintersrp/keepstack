package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics captures Prometheus collectors for the worker.
type Metrics struct {
	JobsProcessed     prometheus.Counter
	JobsFailed        prometheus.Counter
	JobsInProgress    prometheus.Gauge
	FetchLatency      prometheus.Histogram
	ParseLatency      prometheus.Histogram
	PersistLatency    prometheus.Histogram
	ParseFailures     prometheus.Counter
	LangDetectLatency prometheus.Histogram
	LangDetect        *prometheus.CounterVec
	LangDetectErrors  prometheus.Counter
}

// NewMetrics registers worker metrics.
func NewMetrics() *Metrics {
	const namespace = "keepstack_worker"
	return &Metrics{
		JobsProcessed: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "jobs_processed_total",
			Help:      "Number of link ingestion jobs successfully processed.",
		}),
		JobsFailed: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "jobs_failed_total",
			Help:      "Number of link ingestion jobs that failed.",
		}),
		JobsInProgress: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "jobs_in_progress",
			Help:      "Number of link ingestion jobs currently being processed.",
		}),
		FetchLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "fetch_duration_seconds",
			Help:      "Time spent fetching URLs.",
			Buckets:   prometheus.DefBuckets,
		}),
		ParseLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "parse_seconds",
			Help:      "Time spent parsing fetched HTML.",
			Buckets:   prometheus.DefBuckets,
		}),
		PersistLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "persist_duration_seconds",
			Help:      "Time spent storing parsed content.",
			Buckets:   prometheus.DefBuckets,
		}),
		ParseFailures: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "parse_failed_total",
			Help:      "Number of parse attempts that resulted in errors.",
		}),
		LangDetectLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "lang_detect_duration_seconds",
			Help:      "Time spent detecting article language.",
			Buckets:   prometheus.DefBuckets,
		}),
		LangDetect: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "lang_detect",
			Help:      "Number of successful language detections grouped by ISO code.",
		}, []string{"lang"}),
		LangDetectErrors: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "lang_detect_errors_total",
			Help:      "Number of language detection attempts that failed reliability checks.",
		}),
	}
}
