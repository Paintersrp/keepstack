package digest

import (
	"fmt"

	"github.com/kelseyhightower/envconfig"
)

// Config captures runtime configuration for the digest generator.
type Config struct {
	Limit     int    `envconfig:"DIGEST_LIMIT" default:"10"`
	Sender    string `envconfig:"DIGEST_SENDER" required:"true"`
	Recipient string `envconfig:"DIGEST_RECIPIENT" required:"true"`
	SMTPHost  string `envconfig:"SMTP_HOST" required:"true"`
	SMTPPort  int    `envconfig:"SMTP_PORT" default:"587"`
	SMTPUser  string `envconfig:"SMTP_USERNAME" required:"true"`
	SMTPPass  string `envconfig:"SMTP_PASSWORD" required:"true"`
}

// LoadConfig reads digest configuration from the environment.
func LoadConfig() (Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return Config{}, fmt.Errorf("load digest config: %w", err)
	}
	if cfg.Limit <= 0 {
		return Config{}, fmt.Errorf("digest limit must be positive")
	}
	return cfg, nil
}
