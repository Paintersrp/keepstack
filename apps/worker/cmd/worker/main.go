package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
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

	var dbReady atomic.Bool
	var queueReady atomic.Bool

	pool, err := connectDatabase(ctx, logger, cfg.DatabaseURL)
	if err != nil {
		logger.Fatalf("connect database: %v", err)
	}
	defer pool.Close()
	dbReady.Store(true)

	metrics := observability.NewMetrics()
	metricsSrv := startMetricsServer(cfg.MetricsAddress(), logger)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metricsSrv.Shutdown(shutdownCtx)
	}()

	healthSrv := startHealthServer(cfg.HealthAddress(), &dbReady, &queueReady, logger)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = healthSrv.Shutdown(shutdownCtx)
	}()

	subscriber, err := connectNATS(ctx, logger, cfg.NATSURL)
	if err != nil {
		logger.Fatalf("connect nats: %v", err)
	}
	defer subscriber.Close()

	fetcher := ingest.NewFetcher(cfg.FetchTimeout)
	store := ingest.NewStore(pool)
	processor := ingest.NewProcessor(fetcher, store, metrics)

	processJob := func(jobCtx context.Context, linkID uuid.UUID) error {
		metrics.JobsInFlight.Inc()
		defer metrics.JobsInFlight.Dec()

		return processor.Process(jobCtx, linkID)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- subscriber.Listen(ctx, func(jobCtx context.Context, linkID uuid.UUID) error {
			if err := processJob(jobCtx, linkID); err != nil {
				metrics.JobsFailed.Inc()
				return err
			}
			metrics.JobsProcessed.Inc()
			return nil
		}, func() {
			queueReady.Store(true)
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

func connectDatabase(ctx context.Context, logger *log.Logger, url string) (*pgxpool.Pool, error) {
	backoff := time.Second
	var lastErr error

	for attempts := 1; ; attempts++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		pool, err := pgxpool.New(attemptCtx, url)
		cancel()
		if err == nil {
			logger.Printf("database connection established after %d attempt(s)", attempts)
			return pool, nil
		}

		lastErr = err
		if ctx.Err() != nil {
			break
		}

		logger.Printf("database connection failed (attempt %d): %v", attempts, err)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled while connecting to database: %w", ctx.Err())
		}

		if backoff < 8*time.Second {
			backoff *= 2
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("context cancelled before database connection established")
	}

	return nil, fmt.Errorf("connect to database: %w", lastErr)
}

func connectNATS(ctx context.Context, logger *log.Logger, url string) (*queue.Subscriber, error) {
	backoff := time.Second
	var lastErr error

	for attempts := 1; ; attempts++ {
		subscriber, err := queue.NewSubscriber(url)
		if err == nil {
			logger.Printf("nats connection established after %d attempt(s)", attempts)
			return subscriber, nil
		}

		lastErr = err
		if ctx.Err() != nil {
			break
		}

		logger.Printf("nats connection failed (attempt %d): %v", attempts, err)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled while connecting to nats: %w", ctx.Err())
		}

		if backoff < 8*time.Second {
			backoff *= 2
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("context cancelled before nats connection established")
	}

	return nil, fmt.Errorf("connect to nats: %w", lastErr)
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

func startHealthServer(addr string, dbReady, queueReady *atomic.Bool, logger *log.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if !dbReady.Load() || !queueReady.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Printf("health server error: %v", err)
		}
	}()

	return srv
}
