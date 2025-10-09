package main

import (
    "context"
    "errors"
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

    pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
    if err != nil {
        logger.Fatalf("connect database: %v", err)
    }
    defer pool.Close()

    publisher, err := queue.New(cfg.NATSURL)
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
