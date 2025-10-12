package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics aggregates Prometheus collectors used by the API.
type Metrics struct {
	HTTPRequestDurationSeconds *prometheus.HistogramVec
	HTTPRequestTotal           *prometheus.CounterVec
	HTTPRequestNon2xxTotal     *prometheus.CounterVec
	LinkCreateSuccess          prometheus.Counter
	LinkCreateFailure          prometheus.Counter
	LinkListSuccess            prometheus.Counter
	LinkListFailure            prometheus.Counter
	LinkUpdateSuccess          prometheus.Counter
	LinkUpdateFailure          prometheus.Counter
	ClaimCreateSuccess         prometheus.Counter
	ClaimCreateFailure         prometheus.Counter
	ReadinessFailure           prometheus.Counter
	ReadinessMigrationGap      prometheus.Counter
	TagCreateSuccess           prometheus.Counter
	TagCreateFailure           prometheus.Counter
	TagListSuccess             prometheus.Counter
	TagListFailure             prometheus.Counter
	TagReadSuccess             prometheus.Counter
	TagReadFailure             prometheus.Counter
	TagUpdateSuccess           prometheus.Counter
	TagUpdateFailure           prometheus.Counter
	TagDeleteSuccess           prometheus.Counter
	TagDeleteFailure           prometheus.Counter
	LinkTagReadSuccess         prometheus.Counter
	LinkTagReadFailure         prometheus.Counter
	LinkTagMutateSuccess       prometheus.Counter
	LinkTagMutateFailure       prometheus.Counter
	HighlightListSuccess       prometheus.Counter
	HighlightListFailure       prometheus.Counter
	HighlightCreateSuccess     prometheus.Counter
	HighlightCreateFailure     prometheus.Counter
	HighlightUpdateSuccess     prometheus.Counter
	HighlightUpdateFailure     prometheus.Counter
	HighlightDeleteSuccess     prometheus.Counter
	HighlightDeleteFailure     prometheus.Counter
	HighlightRateLimited       prometheus.Counter
	HighlightProcessingSeconds prometheus.Histogram
}

// NewMetrics registers and returns API metrics collectors.
func NewMetrics() *Metrics {
	const namespace = "keepstack_api"
	return &Metrics{
		HTTPRequestDurationSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "http_request_duration_seconds",
			Help:      "Distribution of HTTP request durations in seconds, labelled by route and status code.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"route", "code"}),
		HTTPRequestTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "http_requests_total",
			Help:      "Total number of HTTP requests handled, labelled by route and status code.",
		}, []string{"route", "code"}),
		HTTPRequestNon2xxTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "http_requests_non_2xx_total",
			Help:      "Number of HTTP requests that resulted in non-2xx responses, labelled by route and status code.",
		}, []string{"route", "code"}),
		LinkCreateSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "link_create_success_total",
			Help:      "Number of links successfully accepted for processing.",
		}),
		LinkCreateFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "link_create_failure_total",
			Help:      "Number of link creation attempts that failed.",
		}),
		LinkListSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "link_list_success_total",
			Help:      "Number of link listing requests that succeeded.",
		}),
		LinkListFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "link_list_failure_total",
			Help:      "Number of link listing requests that failed.",
		}),
		LinkUpdateSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "link_update_success_total",
			Help:      "Number of link update requests that succeeded.",
		}),
		LinkUpdateFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "link_update_failure_total",
			Help:      "Number of link update requests that failed.",
		}),
		ClaimCreateSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "claim_create_success_total",
			Help:      "Number of claim creation requests that succeeded.",
		}),
		ClaimCreateFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "claim_create_failure_total",
			Help:      "Number of claim creation requests that failed.",
		}),
		ReadinessFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "readiness_failure_total",
			Help:      "Number of readiness probe checks that failed.",
		}),
		ReadinessMigrationGap: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "readiness_migration_gap_total",
			Help:      "Number of readiness probe failures caused by missing database migrations.",
		}),
		TagCreateSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tag_create_success_total",
			Help:      "Number of tag creation requests that succeeded.",
		}),
		TagCreateFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tag_create_failure_total",
			Help:      "Number of tag creation requests that failed.",
		}),
		TagListSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tag_list_success_total",
			Help:      "Number of tag list requests that succeeded.",
		}),
		TagListFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tag_list_failure_total",
			Help:      "Number of tag list requests that failed.",
		}),
		TagReadSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tag_read_success_total",
			Help:      "Number of tag fetch requests that succeeded.",
		}),
		TagReadFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tag_read_failure_total",
			Help:      "Number of tag fetch requests that failed.",
		}),
		TagUpdateSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tag_update_success_total",
			Help:      "Number of tag update requests that succeeded.",
		}),
		TagUpdateFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tag_update_failure_total",
			Help:      "Number of tag update requests that failed.",
		}),
		TagDeleteSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tag_delete_success_total",
			Help:      "Number of tag delete requests that succeeded.",
		}),
		TagDeleteFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tag_delete_failure_total",
			Help:      "Number of tag delete requests that failed.",
		}),
		LinkTagReadSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "link_tag_read_success_total",
			Help:      "Number of link tag read requests that succeeded.",
		}),
		LinkTagReadFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "link_tag_read_failure_total",
			Help:      "Number of link tag read requests that failed.",
		}),
		LinkTagMutateSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "link_tag_mutate_success_total",
			Help:      "Number of link tag mutation requests that succeeded.",
		}),
		LinkTagMutateFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "link_tag_mutate_failure_total",
			Help:      "Number of link tag mutation requests that failed.",
		}),
		HighlightListSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "highlight_list_success_total",
			Help:      "Number of highlight list requests that succeeded.",
		}),
		HighlightListFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "highlight_list_failure_total",
			Help:      "Number of highlight list requests that failed.",
		}),
		HighlightCreateSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "highlight_create_success_total",
			Help:      "Number of highlight creation requests that succeeded.",
		}),
		HighlightCreateFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "highlight_create_failure_total",
			Help:      "Number of highlight creation requests that failed.",
		}),
		HighlightUpdateSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "highlight_update_success_total",
			Help:      "Number of highlight update requests that succeeded.",
		}),
		HighlightUpdateFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "highlight_update_failure_total",
			Help:      "Number of highlight update requests that failed.",
		}),
		HighlightDeleteSuccess: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "highlight_delete_success_total",
			Help:      "Number of highlight delete requests that succeeded.",
		}),
		HighlightDeleteFailure: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "highlight_delete_failure_total",
			Help:      "Number of highlight delete requests that failed.",
		}),
		HighlightRateLimited: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "highlight_rate_limited_total",
			Help:      "Number of highlight requests rejected due to rate limiting.",
		}),
		HighlightProcessingSeconds: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "highlight_processing_seconds",
			Help:      "Distribution of highlight processing durations.",
			Buckets:   prometheus.DefBuckets,
		}),
	}
}
