package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadWorker(t *testing.T) {
	cfg, err := loadWorker(env(map[string]string{"DATABASE_URL": "postgres://x", "WORKER_BUILDER": "docker", "WORKER_DEPLOYER": "kubernetes", "KUBE_CONTEXT": "colima"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency != 2 || cfg.Port != 8081 || cfg.LeaseTTL != 30*time.Second {
		t.Errorf("defaults: %+v", cfg)
	}

	_, err = loadWorker(env(map[string]string{"WORKER_CONCURRENCY": "0", "WORKER_LEASE_TTL": "1s"}))
	for _, want := range []string{"DATABASE_URL", "WORKER_CONCURRENCY", "WORKER_LEASE_TTL", "WORKER_BUILDER", "WORKER_DEPLOYER"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing error for %s: %v", want, err)
		}
	}
	if _, err := loadWorker(env(map[string]string{"DATABASE_URL": "x", "WORKER_BUILDER": "kaniko", "WORKER_DEPLOYER": "dryrun"})); err == nil {
		t.Error("unsupported builder accepted")
	}
}

func TestLoadWorkerACRNeedsRegistryID(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "x", "WORKER_BUILDER": "acr", "WORKER_DEPLOYER": "dryrun"}
	if _, err := loadWorker(env(base)); err == nil || !strings.Contains(err.Error(), "ACR_REGISTRY_ID") {
		t.Fatalf("err = %v, want ACR_REGISTRY_ID required", err)
	}
	base["ACR_REGISTRY_ID"] = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/r"
	if _, err := loadWorker(env(base)); err != nil {
		t.Fatal(err)
	}
}
