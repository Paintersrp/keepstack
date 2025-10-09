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

	fetchStart := time.Now()
	result, err := p.fetcher.Fetch(ctx, link.URL)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	p.metrics.FetchLatency.Observe(time.Since(fetchStart).Seconds())

	parseStart := time.Now()
	article, err := Parse(result.FinalURL, result.Body)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	p.metrics.ParseLatency.Observe(time.Since(parseStart).Seconds())

	persistStart := time.Now()
	if err := p.store.PersistResult(ctx, link, article, result.Body); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	p.metrics.PersistLatency.Observe(time.Since(persistStart).Seconds())

	return nil
}
