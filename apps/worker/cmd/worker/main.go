package main

import (
    "context"
    "log"
    "net/http"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/prometheus/client_golang/prometheus/promhttp"

    "github.com/example/keepstack/apps/worker/internal/config"
    "github.com/example/keepstack/apps/worker/internal/ingest"
    "github.com/example/keepstack/apps/worker/internal/observability"
    "github.com/example/keepstack/apps/worker/internal/queue"
)

func main() {
    logger := log.New(os.Stdout, "keepstack-worker ", log.LstdFlags|log.LUTC)

    cfg, err := config.Load()
    if err != nil {
        logger.Fatalf("load config: %v", err)
    }

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
    if err != nil {
        logger.Fatalf("connect database: %v", err)
    }
    defer pool.Close()

    metrics := observability.NewMetrics()
    srv := startMetricsServer(cfg.MetricsAddress(), logger)
    defer func() {
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        _ = srv.Shutdown(shutdownCtx)
    }()

    subscriber, err := queue.NewSubscriber(cfg.NATSURL)
    if err != nil {
        logger.Fatalf("connect nats: %v", err)
    }
    defer subscriber.Close()

    fetcher := ingest.NewFetcher(cfg.FetchTimeout)
    store := ingest.NewStore(pool)
    processor := ingest.NewProcessor(fetcher, store, metrics)

    errCh := make(chan error, 1)
    go func() {
        errCh <- subscriber.Listen(ctx, func(jobCtx context.Context, linkID uuid.UUID) error {
            if err := processor.Process(jobCtx, linkID); err != nil {
                metrics.JobsFailed.Inc()
                return err
            }
            metrics.JobsProcessed.Inc()
            return nil
        })
    }()

    select {
    case <-ctx.Done():
        logger.Println("shutdown signal received")
    case err := <-errCh:
        if err != nil {
            logger.Fatalf("subscriber error: %v", err)
        }
    }

    logger.Println("worker stopped")
}

func startMetricsServer(addr string, logger *log.Logger) *http.Server {
    mux := http.NewServeMux()
    mux.Handle("/metrics", promhttp.Handler())

    srv := &http.Server{
        Addr:    addr,
        Handler: mux,
    }

    go func() {
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            logger.Printf("metrics server error: %v", err)
        }
    }()

    return srv
}
