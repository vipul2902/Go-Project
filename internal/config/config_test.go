package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(env(map[string]string{"DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != EnvDevelopment || cfg.Port != 8080 || cfg.LogLevel != slog.LevelInfo {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.DBMaxConns != 10 || cfg.DBMinConns != 1 {
		t.Errorf("unexpected pool defaults: max=%d min=%d", cfg.DBMaxConns, cfg.DBMinConns)
	}
	if cfg.ShutdownTimeout != 20*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 20s", cfg.ShutdownTimeout)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"DATABASE_URL":     "postgres://x",
		"APP_ENV":          "production",
		"PORT":             "9090",
		"LOG_LEVEL":        "debug",
		"SHUTDOWN_TIMEOUT": "5s",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.IsProduction() || cfg.Addr() != ":9090" || cfg.LogLevel != slog.LevelDebug || cfg.ShutdownTimeout != 5*time.Second {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := load(env(map[string]string{
		"APP_ENV":           "qa",
		"PORT":              "99999",
		"LOG_LEVEL":         "loud",
		"HTTP_READ_TIMEOUT": "-1s",
		"DB_MIN_CONNS":      "50",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"APP_ENV", "PORT", "LOG_LEVEL", "HTTP_READ_TIMEOUT", "DATABASE_URL", "DB_MIN_CONNS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
