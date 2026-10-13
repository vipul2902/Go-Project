package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadWorker(t *testing.T) {
	cfg, err := loadWorker(env(map[string]string{"DATABASE_URL": "postgres://x", "DEPLOY_EXECUTOR": "dryrun"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency != 2 || cfg.Port != 8081 || cfg.LeaseTTL != 30*time.Second {
		t.Errorf("defaults: %+v", cfg)
	}

	_, err = loadWorker(env(map[string]string{"WORKER_CONCURRENCY": "0", "WORKER_LEASE_TTL": "1s"}))
	for _, want := range []string{"DATABASE_URL", "WORKER_CONCURRENCY", "WORKER_LEASE_TTL", "DEPLOY_EXECUTOR"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing error for %s: %v", want, err)
		}
	}
	if _, err := loadWorker(env(map[string]string{"DATABASE_URL": "x", "DEPLOY_EXECUTOR": "kubernetes"})); err == nil {
		t.Error("unsupported executor accepted")
	}
}
