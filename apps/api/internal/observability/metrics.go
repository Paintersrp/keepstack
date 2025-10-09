package observability

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics aggregates Prometheus collectors used by the API.
type Metrics struct {
    LinkCreateSuccess prometheus.Counter
    LinkCreateFailure prometheus.Counter
    LinkListSuccess   prometheus.Counter
    LinkListFailure   prometheus.Counter
}

// NewMetrics registers and returns API metrics collectors.
func NewMetrics() *Metrics {
    const namespace = "keepstack_api"
    return &Metrics{
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
    }
}
