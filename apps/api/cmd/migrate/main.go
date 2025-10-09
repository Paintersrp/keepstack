package main

import (
	"context"
	"database/sql"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/example/keepstack/apps/api/internal/config"
)

const defaultMigrationsDir = "db/migrations"

func main() {
	logger := log.New(os.Stdout, "keepstack-migrate ", log.LstdFlags|log.LUTC)

	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	migrationsDir := os.Getenv("MIGRATIONS_DIR")
	if migrationsDir == "" {
		migrationsDir = defaultMigrationsDir
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		logger.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if err := db.PingContext(context.Background()); err != nil {
		logger.Fatalf("ping database: %v", err)
	}

	if err := goose.SetDialect("postgres"); err != nil {
		logger.Fatalf("set goose dialect: %v", err)
	}

	if err := goose.Up(db, migrationsDir); err != nil {
		logger.Fatalf("apply migrations: %v", err)
	}

	logger.Printf("migrations applied from %s", migrationsDir)
}
