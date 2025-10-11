package main

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/example/keepstack/apps/api/internal/config"
	"github.com/example/keepstack/apps/api/internal/digest"
	"github.com/example/keepstack/apps/api/internal/schema"
)

func main() {
	logger := log.New(os.Stdout, "keepstack-cron ", log.LstdFlags|log.LUTC)

	if len(os.Args) < 2 {
		logger.Fatalf("expected subcommand (available: digest)")
	}

	switch os.Args[1] {
	case "digest":
		if err := runDigest(logger); err != nil {
			if errors.Is(err, digest.ErrNoUnreadLinks) {
				logger.Println("no unread links, skipping digest dispatch")
				return
			}
			logger.Fatalf("digest run failed: %v", err)
		}
	case "verify-schema":
		if err := runVerifySchema(logger); err != nil {
			logger.Fatalf("schema verification failed: %v", err)
		}
	default:
		logger.Fatalf("unknown subcommand %q", os.Args[1])
	}
}

func runDigest(logger *log.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	digestCfg, err := digest.LoadConfig()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	svc, err := digest.New(pool, digestCfg)
	if err != nil {
		return err
	}

	count, err := svc.Send(ctx, cfg.DevUserID)
	if err != nil {
		return err
	}

	logger.Printf("sent digest with %d unread links", count)
	return nil
}

func runVerifySchema(logger *log.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := schema.Verify(ctx, pool); err != nil {
		return err
	}

	logger.Println("database schema verified")
	return nil
}
