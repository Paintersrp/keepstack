package config

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/kelseyhightower/envconfig"
)

const defaultDevUserID = "00000000-0000-0000-0000-000000000001"

// Config captures runtime configuration for the API service.
type Config struct {
	DatabaseURL string    `envconfig:"DATABASE_URL" required:"true"`
	NATSURL     string    `envconfig:"NATS_URL" required:"true"`
	Port        int       `envconfig:"PORT" default:"8080"`
	DevMode     bool      `envconfig:"DEV_MODE" default:"false"`
	DevUserID   uuid.UUID `env:"-"`
	DevUserRaw  string    `envconfig:"DEV_USER_ID" default:""`
}

// Load reads configuration values from the environment.
func Load() (Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}

	raw := cfg.DevUserRaw
	if raw == "" {
		raw = defaultDevUserID
	}

	id, err := uuid.Parse(raw)
	if err != nil {
		return Config{}, fmt.Errorf("parse dev user id: %w", err)
	}
	cfg.DevUserID = id
	return cfg, nil
}

// Address returns the TCP listen address for the HTTP server.
func (c Config) Address() string {
	return fmt.Sprintf(":%d", c.Port)
}
