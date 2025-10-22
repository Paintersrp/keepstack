package digest

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/kelseyhightower/envconfig"
)

// Config captures runtime configuration for the digest generator.
type Config struct {
	Limit     int    `envconfig:"DIGEST_LIMIT" default:"10"`
	Sender    string `envconfig:"DIGEST_SENDER" required:"true"`
	Recipient string `envconfig:"DIGEST_RECIPIENT" required:"true"`
	SMTPURL   string `envconfig:"SMTP_URL" required:"true"`

	Transport Transport
}

// Transport captures SMTP delivery configuration derived from SMTP_URL.
type Transport struct {
	Scheme   string
	Host     string
	Port     int
	Username string
	Password string
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

	transport, err := parseSMTPURL(cfg.SMTPURL)
	if err != nil {
		return Config{}, err
	}
	cfg.Transport = transport

	return cfg, nil
}

// ParseTransport constructs a Transport definition from the provided SMTP_URL string.
func ParseTransport(raw string) (Transport, error) {
	return parseSMTPURL(raw)
}

func parseSMTPURL(raw string) (Transport, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Transport{}, fmt.Errorf("SMTP_URL must not be empty")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return Transport{}, fmt.Errorf("parse SMTP_URL: %w", err)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "smtp":
		host := parsed.Hostname()
		if host == "" {
			return Transport{}, fmt.Errorf("SMTP_URL missing host")
		}

		port := 587
		if portStr := parsed.Port(); portStr != "" {
			p, err := strconv.Atoi(portStr)
			if err != nil || p <= 0 {
				return Transport{}, fmt.Errorf("invalid SMTP_URL port %q", portStr)
			}
			port = p
		}

		if parsed.User == nil {
			return Transport{}, fmt.Errorf("SMTP_URL must include username and password")
		}

		username := parsed.User.Username()
		if username == "" {
			return Transport{}, fmt.Errorf("SMTP_URL username must not be empty")
		}

		password, ok := parsed.User.Password()
		if !ok || password == "" {
			return Transport{}, fmt.Errorf("SMTP_URL password must not be empty")
		}

		return Transport{
			Scheme:   "smtp",
			Host:     host,
			Port:     port,
			Username: username,
			Password: password,
		}, nil
	case "log":
		return Transport{Scheme: "log"}, nil
	default:
		return Transport{}, fmt.Errorf("unsupported SMTP_URL scheme %q", parsed.Scheme)
	}
}
