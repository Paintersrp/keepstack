package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/example/keepstack/apps/api/internal/config"
	httpapi "github.com/example/keepstack/apps/api/internal/http"
	"github.com/example/keepstack/apps/api/internal/observability"
	"github.com/example/keepstack/apps/api/internal/queue"
)

func main() {
	logger := log.New(os.Stdout, "keepstack-api ", log.LstdFlags|log.LUTC)

	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := connectDatabase(ctx, logger, cfg.DatabaseURL)
	if err != nil {
		logger.Fatalf("connect database: %v", err)
	}
	defer pool.Close()

	publisher, err := connectNATS(ctx, logger, cfg.NATSURL)
	if err != nil {
		logger.Fatalf("connect nats: %v", err)
	}
	defer publisher.Close()

	metrics := observability.NewMetrics()

	e := echo.New()

	server := httpapi.NewServer(cfg, pool, publisher, metrics)
	server.RegisterRoutes(e)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Shutdown(shutdownCtx); err != nil {
			logger.Printf("server shutdown error: %v", err)
		}
	}()

	logger.Printf("starting server on %s", cfg.Address())
	if err := e.Start(cfg.Address()); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("server error: %v", err)
	}

	logger.Println("server stopped")
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

func connectNATS(ctx context.Context, logger *log.Logger, url string) (queue.Publisher, error) {
	backoff := time.Second
	var lastErr error

	for attempts := 1; ; attempts++ {
		publisher, err := queue.New(url)
		if err == nil {
			logger.Printf("nats connection established after %d attempt(s)", attempts)
			return publisher, nil
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
