// Command api runs the CloudDeploy HTTP API server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/vipul2902/clouddeploy/internal/config"
	"github.com/vipul2902/clouddeploy/internal/database"
	"github.com/vipul2902/clouddeploy/internal/httpapi"
	"github.com/vipul2902/clouddeploy/internal/kube"
	"github.com/vipul2902/clouddeploy/internal/metrics"
	"github.com/vipul2902/clouddeploy/internal/redisx"
	"github.com/vipul2902/clouddeploy/internal/tracing"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// tracing.LogHandler adds trace_id/span_id to logs written inside a span.
	logger := slog.New(tracing.LogHandler{Handler: slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})}).
		With("service", "clouddeploy-api", "env", cfg.Env, "version", version)
	slog.SetDefault(logger)

	// ctx is cancelled on SIGINT (Ctrl+C) or SIGTERM (sent by Docker/Kubernetes).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Open(ctx, cfg.DatabaseURL, cfg.PoolConfig())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	// Deferred calls run LIFO: the pool closes after the server has drained,
	// so in-flight requests can still use the database during shutdown.
	defer pool.Close()
	logger.Info("database connected", "max_conns", cfg.DBMaxConns)
	if err := database.RequireLeastPrivilege(ctx, pool, logger, cfg.IsProduction()); err != nil {
		return err
	}

	rdb, err := redisx.Open(ctx, cfg.RedisURL)
	if err != nil {
		// Fail fast at startup (misconfiguration). At runtime a Redis outage
		// only degrades the service: rate limiting fails open.
		return fmt.Errorf("open redis: %w", err)
	}
	defer rdb.Close()

	// Monitoring is optional: without cluster access the API still serves
	// everything else, and monitoring endpoints answer 503.
	var kubeClient kubernetes.Interface
	if cs, err := kube.NewClientset(cfg.KubeContext); err != nil {
		logger.Warn("monitoring disabled: no cluster access", "err", err)
	} else {
		kubeClient = cs
		logger.Info("monitoring enabled", "kube_context", cfg.KubeContext)
	}

	shutdownTracing, tracingOn, err := tracing.Setup(ctx, "clouddeploy-api", version)
	if err != nil {
		return err
	}
	defer flush(shutdownTracing, logger)
	logger.Info("tracing", "enabled", tracingOn)

	registry := metrics.NewRegistry()
	handler, err := httpapi.NewApp(pool, httpapi.AppConfig{
		Metrics:         registry,
		Kube:            kubeClient,
		Redis:           rdb,
		TrustedProxies:  cfg.TrustedProxies,
		Logger:          logger,
		Version:         version,
		JWTSecret:       []byte(cfg.JWTSecret),
		AccessTokenTTL:  cfg.AccessTokenTTL,
		RefreshTokenTTL: cfg.RefreshTokenTTL,
	})
	if err != nil {
		return err
	}

	// Metrics on a separate internal port: never exposed through the public
	// Service or ingress (they reveal routes, error rates, queue depth).
	if cfg.MetricsPort != 0 {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler(registry))
		if cfg.Pprof {
			// Profiling for load tests (PPROF_ENABLED; refused in production).
			mux.HandleFunc("/debug/pprof/", pprof.Index)
			mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
			mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
			mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
			logger.Warn("pprof enabled on the metrics port")
		}
		ms := &http.Server{Addr: fmt.Sprintf(":%d", cfg.MetricsPort), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := ms.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("metrics server", "err", err)
			}
		}()
		defer ms.Close()
		logger.Info("metrics server starting", "addr", ms.Addr)
	}

	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// Graceful shutdown: stop accepting new connections, then wait for
	// in-flight requests up to ShutdownTimeout.
	logger.Info("shutdown signal received, draining connections", "timeout", cfg.ShutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("server stopped cleanly")
	return nil
}

// flush exports buffered spans before exit (bounded, so shutdown cannot hang).
func flush(shutdown func(context.Context) error, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		logger.Warn("flush traces", "err", err)
	}
}
