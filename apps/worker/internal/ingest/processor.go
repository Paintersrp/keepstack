package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/example/keepstack/apps/worker/internal/observability"
)

// Processor ties together fetch, parse, and persist steps.
type Processor struct {
	fetcher *Fetcher
	store   *Store
	metrics *observability.Metrics
}

// NewProcessor constructs a Processor.
func NewProcessor(fetcher *Fetcher, store *Store, metrics *observability.Metrics) *Processor {
	return &Processor{fetcher: fetcher, store: store, metrics: metrics}
}

// Process executes the ingestion pipeline for a link identifier.
func (p *Processor) Process(ctx context.Context, linkID uuid.UUID) error {
	link, err := p.store.LookupLink(ctx, linkID)
	if err != nil {
		return fmt.Errorf("lookup link: %w", err)
	}

	if !link.CreatedAt.IsZero() {
		lag := time.Since(link.CreatedAt)
		if lag < 0 {
			lag = 0
		}
		p.metrics.QueueLagSeconds.Observe(lag.Seconds())
	}

	fetchStart := time.Now()
	result, err := p.fetcher.Fetch(ctx, link.URL)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	p.metrics.FetchLatency.Observe(time.Since(fetchStart).Seconds())

	parseStart := time.Now()
	article, diagnostics, err := Parse(result.FinalURL, result.Body)
	parseDuration := time.Since(parseStart)
	p.metrics.ParseLatency.Observe(parseDuration.Seconds())
	if err != nil {
		p.metrics.ParseFailures.Inc()
		return fmt.Errorf("parse: %w", err)
	}

	if diagnostics.LangDetectDuration > 0 {
		p.metrics.LangDetectLatency.Observe(diagnostics.LangDetectDuration.Seconds())
	}
	if diagnostics.LangDetected {
		p.metrics.LangDetect.WithLabelValues(article.Language).Inc()
	} else if diagnostics.LangDetectDuration > 0 {
		p.metrics.LangDetectErrors.Inc()
	}

	persistStart := time.Now()
	if err := p.store.PersistResult(ctx, link, article, result.Body); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	p.metrics.PersistLatency.Observe(time.Since(persistStart).Seconds())

	return nil
}
