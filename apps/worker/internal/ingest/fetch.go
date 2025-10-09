package ingest

import (
    "context"
    "fmt"
    "io"
    "net/http"
    "time"
)

const userAgent = "keepstack-worker/0.1"

// FetchResult contains the body and final URL obtained from fetching a link.
type FetchResult struct {
    Body    []byte
    FinalURL string
}

// Fetcher retrieves HTML documents over HTTP.
type Fetcher struct {
    client *http.Client
}

// NewFetcher constructs a Fetcher with the given timeout.
func NewFetcher(timeout time.Duration) *Fetcher {
    return &Fetcher{
        client: &http.Client{Timeout: timeout},
    }
}

// Fetch downloads the target URL.
func (f *Fetcher) Fetch(ctx context.Context, target string) (FetchResult, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
    if err != nil {
        return FetchResult{}, fmt.Errorf("build request: %w", err)
    }
    req.Header.Set("User-Agent", userAgent)

    resp, err := f.client.Do(req)
    if err != nil {
        return FetchResult{}, fmt.Errorf("fetch url: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode >= 400 {
        return FetchResult{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
    }

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return FetchResult{}, fmt.Errorf("read response: %w", err)
    }

    finalURL := target
    if resp.Request != nil && resp.Request.URL != nil {
        finalURL = resp.Request.URL.String()
    }

    return FetchResult{Body: body, FinalURL: finalURL}, nil
}
