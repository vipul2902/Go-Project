package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Worker is the deployment worker's configuration. It is separate from the
// API's Config: the worker needs neither JWT secrets nor Redis.
type Worker struct {
	Env               string
	LogLevel          slog.Level
	Port              int // health endpoints for Kubernetes probes
	DatabaseURL       string
	DBMaxConnLifetime time.Duration
	Concurrency       int
	Builder           string // "docker" | "acr" | "dryrun"
	ACRRegistryID     string // Azure resource ID of the registry; required for the acr builder
	Deployer          string // "kubernetes" | "dryrun"
	KubeContext       string // kubeconfig context; required outside a cluster
	BuildWorkDir      string
	PollInterval      time.Duration
	LeaseTTL          time.Duration
	ShutdownTimeout   time.Duration
}

// Supported builder and deployer implementations.
const (
	DryRun             = "dryrun"
	BuilderDocker      = "docker"
	BuilderACR         = "acr"
	DeployerKubernetes = "kubernetes"
)

// LoadWorker reads the worker configuration from the environment.
func LoadWorker() (Worker, error) { return loadWorker(os.Getenv) }

func loadWorker(getenv func(string) string) (Worker, error) {
	var errs []error
	cfg := Worker{
		Env:               valueOr(getenv("APP_ENV"), EnvDevelopment),
		Port:              parseInt(getenv, "WORKER_PORT", 8081, &errs),
		DatabaseURL:       getenv("DATABASE_URL"),
		DBMaxConnLifetime: parseDuration(getenv, "DB_MAX_CONN_LIFETIME", 30*time.Minute, &errs),
		Concurrency:       parseInt(getenv, "WORKER_CONCURRENCY", 2, &errs),
		Builder:           getenv("WORKER_BUILDER"),
		Deployer:          getenv("WORKER_DEPLOYER"),
		ACRRegistryID:     getenv("ACR_REGISTRY_ID"),
		KubeContext:       getenv("KUBE_CONTEXT"),
		BuildWorkDir:      valueOr(getenv("BUILD_WORKDIR"), os.TempDir()),
		PollInterval:      parseDuration(getenv, "WORKER_POLL_INTERVAL", 5*time.Second, &errs),
		LeaseTTL:          parseDuration(getenv, "WORKER_LEASE_TTL", 30*time.Second, &errs),
		ShutdownTimeout:   parseDuration(getenv, "SHUTDOWN_TIMEOUT", 20*time.Second, &errs),
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(valueOr(getenv("LOG_LEVEL"), "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL: required"))
	}
	if cfg.Concurrency < 1 || cfg.Concurrency > 32 {
		errs = append(errs, fmt.Errorf("WORKER_CONCURRENCY: %d out of range 1-32", cfg.Concurrency))
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		errs = append(errs, fmt.Errorf("WORKER_PORT: %d out of range", cfg.Port))
	}
	if cfg.LeaseTTL < 3*time.Second {
		errs = append(errs, errors.New("WORKER_LEASE_TTL: must be at least 3s"))
	}
	// Explicit on purpose: no defaults, so nobody runs simulated builds or
	// deployments believing they are real.
	switch cfg.Builder {
	case BuilderDocker, DryRun:
	case BuilderACR:
		if cfg.ACRRegistryID == "" {
			errs = append(errs, errors.New("ACR_REGISTRY_ID: required when WORKER_BUILDER=acr"))
		}
	case "":
		errs = append(errs, errors.New(`WORKER_BUILDER: required ("docker", "acr" or "dryrun")`))
	default:
		errs = append(errs, fmt.Errorf("WORKER_BUILDER: unsupported value %q", cfg.Builder))
	}
	switch cfg.Deployer {
	case DeployerKubernetes, DryRun:
	case "":
		errs = append(errs, errors.New(`WORKER_DEPLOYER: required ("kubernetes" or "dryrun")`))
	default:
		errs = append(errs, fmt.Errorf("WORKER_DEPLOYER: unsupported value %q", cfg.Deployer))
	}
	return cfg, errors.Join(errs...)
}
