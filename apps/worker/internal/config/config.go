package config

import (
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config holds runtime settings for the worker service.
type Config struct {
	DatabaseURL  string        `envconfig:"DATABASE_URL" required:"true"`
	NATSURL      string        `envconfig:"NATS_URL" required:"true"`
	MetricsPort  int           `envconfig:"PORT" default:"9090"`
	HealthPort   int           `envconfig:"HEALTH_PORT" default:"8081"`
	FetchTimeout time.Duration `envconfig:"FETCH_TIMEOUT" default:"15s"`
}

// Load retrieves configuration from environment variables.
func Load() (Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

// MetricsAddress returns the listen address for the metrics HTTP server.
func (c Config) MetricsAddress() string {
	return fmt.Sprintf(":%d", c.MetricsPort)
}

// HealthAddress returns the listen address for the health HTTP server.
func (c Config) HealthAddress() string {
	return fmt.Sprintf(":%d", c.HealthPort)
}
