// Package config loads application configuration from environment variables.
//
// Configuration is read once at startup and passed explicitly to the
// components that need it; there is no global config state.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vipul2902/clouddeploy/internal/database"
)

// Environment names supported by the application.
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Config holds all runtime configuration.
type Config struct {
	Env             string
	Port            int
	LogLevel        slog.Level
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration

	DatabaseURL       string
	DBMaxConns        int
	DBMinConns        int
	DBMaxConnLifetime time.Duration
	DBMaxConnIdleTime time.Duration
}

// Load reads configuration from the environment, applying defaults and
// validating the result. It returns every validation problem at once so a
// misconfigured deployment can be fixed in a single pass.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	var errs []error

	cfg := Config{
		Env:             valueOr(getenv("APP_ENV"), EnvDevelopment),
		Port:            parseInt(getenv, "PORT", 8080, &errs),
		ReadTimeout:     parseDuration(getenv, "HTTP_READ_TIMEOUT", 10*time.Second, &errs),
		WriteTimeout:    parseDuration(getenv, "HTTP_WRITE_TIMEOUT", 15*time.Second, &errs),
		IdleTimeout:     parseDuration(getenv, "HTTP_IDLE_TIMEOUT", 60*time.Second, &errs),
		ShutdownTimeout: parseDuration(getenv, "SHUTDOWN_TIMEOUT", 20*time.Second, &errs),

		DatabaseURL: getenv("DATABASE_URL"),
		// Small defaults: each Postgres connection costs ~5-10 MB server-side, and
		// total connections = replicas x MaxConns must stay under max_connections.
		DBMaxConns:        parseInt(getenv, "DB_MAX_CONNS", 10, &errs),
		DBMinConns:        parseInt(getenv, "DB_MIN_CONNS", 1, &errs),
		DBMaxConnLifetime: parseDuration(getenv, "DB_MAX_CONN_LIFETIME", 30*time.Minute, &errs),
		DBMaxConnIdleTime: parseDuration(getenv, "DB_MAX_CONN_IDLE_TIME", 5*time.Minute, &errs),
	}

	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL: required"))
	}
	if cfg.DBMaxConns < 1 || cfg.DBMinConns < 0 || cfg.DBMinConns > cfg.DBMaxConns {
		errs = append(errs, fmt.Errorf("DB_MAX_CONNS/DB_MIN_CONNS: need 0 <= min (%d) <= max (%d), max >= 1", cfg.DBMinConns, cfg.DBMaxConns))
	}

	switch cfg.Env {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("APP_ENV: unsupported value %q", cfg.Env))
	}

	if cfg.Port < 1 || cfg.Port > 65535 {
		errs = append(errs, fmt.Errorf("PORT: %d out of range 1-65535", cfg.Port))
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(valueOr(getenv("LOG_LEVEL"), "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	return cfg, errors.Join(errs...)
}

// Addr returns the listen address for the HTTP server.
func (c Config) Addr() string { return fmt.Sprintf(":%d", c.Port) }

// PoolConfig returns the database pool settings.
func (c Config) PoolConfig() database.PoolConfig {
	return database.PoolConfig{
		MaxConns:        int32(c.DBMaxConns),
		MinConns:        int32(c.DBMinConns),
		MaxConnLifetime: c.DBMaxConnLifetime,
		MaxConnIdleTime: c.DBMaxConnIdleTime,
	}
}

// IsProduction reports whether the app runs in production.
func (c Config) IsProduction() bool { return c.Env == EnvProduction }

func valueOr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func parseInt(getenv func(string) string, key string, def int, errs *[]error) int {
	raw := getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: invalid integer %q", key, raw))
		return def
	}
	return v
}

func parseDuration(getenv func(string) string, key string, def time.Duration, errs *[]error) time.Duration {
	raw := getenv(key)
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		*errs = append(*errs, fmt.Errorf("%s: invalid positive duration %q", key, raw))
		return def
	}
	return v
}
