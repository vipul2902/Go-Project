// Command worker processes queued deployments. It runs as its own process
// (same image as the API) so slow builds never compete with API requests
// and the two scale independently.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vipul2902/clouddeploy/internal/config"
	"github.com/vipul2902/clouddeploy/internal/database"
	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/httpx"
	"github.com/vipul2902/clouddeploy/internal/worker"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).
		With("service", "clouddeploy-worker", "env", cfg.Env, "version", version)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Connections: one LISTEN, one per loop, one per heartbeat, reaper, spare.
	pool, err := database.Open(ctx, cfg.DatabaseURL, database.PoolConfig{
		MaxConns: int32(cfg.Concurrency*2 + 3), MinConns: 1, MaxConnLifetime: cfg.DBMaxConnLifetime, MaxConnIdleTime: 5 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	wcfg := worker.DefaultConfig()
	wcfg.Concurrency = cfg.Concurrency
	wcfg.PollInterval = cfg.PollInterval
	wcfg.LeaseTTL = cfg.LeaseTTL
	wcfg.ReapInterval = cfg.LeaseTTL / 2
	wcfg.ShutdownGrace = cfg.ShutdownTimeout
	if cfg.Executor == config.ExecutorDryRun {
		logger.Warn("DEPLOY_EXECUTOR=dryrun: deployments are simulated; nothing is built or deployed")
	}
	w := worker.New(pool, deployments.NewService(pool), worker.DryRun{}, wcfg, logger)

	// Health endpoints for Kubernetes probes. Liveness never checks the
	// database (an outage must not restart workers in a loop).
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		pctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(pctx); err != nil {
			httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	srv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.Port), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("health server", "err", err)
		}
	}()

	err = w.Run(ctx) // blocks until SIGTERM, then drains

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return err
}
