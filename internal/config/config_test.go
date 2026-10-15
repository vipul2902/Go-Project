package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

const testJWTSecret = "test-secret-at-least-32-bytes-long!!"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(env(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "JWT_SECRET": testJWTSecret}))
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
		"JWT_SECRET":       testJWTSecret,
		"REDIS_URL":        "redis://x",
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
		"JWT_SECRET":        "too-short",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"APP_ENV", "PORT", "LOG_LEVEL", "HTTP_READ_TIMEOUT", "DATABASE_URL", "DB_MIN_CONNS", "JWT_SECRET", "REDIS_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

// Pool sizes become int32 in pgx; an unbounded value would overflow.
func TestLoadRejectsHugePool(t *testing.T) {
	_, err := load(env(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "JWT_SECRET": testJWTSecret,
		"DB_MAX_CONNS": "5000000000"}))
	if err == nil || !strings.Contains(err.Error(), "DB_MAX_CONNS") {
		t.Fatalf("err = %v, want DB_MAX_CONNS rejected", err)
	}
}

func TestPprofRefusedInProduction(t *testing.T) {
	_, err := load(env(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "JWT_SECRET": testJWTSecret,
		"APP_ENV": "production", "PPROF_ENABLED": "true"}))
	if err == nil || !strings.Contains(err.Error(), "PPROF_ENABLED") {
		t.Fatalf("err = %v, want PPROF_ENABLED refused in production", err)
	}
}
