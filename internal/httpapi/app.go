package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"k8s.io/client-go/kubernetes"

	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/environments"
	"github.com/vipul2902/clouddeploy/internal/kube"
	"github.com/vipul2902/clouddeploy/internal/monitoring"
	"github.com/vipul2902/clouddeploy/internal/organizations"
	"github.com/vipul2902/clouddeploy/internal/projects"
	"github.com/vipul2902/clouddeploy/internal/ratelimit"
)

// AppConfig holds what NewApp needs beyond the database pool.
type AppConfig struct {
	Logger          *slog.Logger
	Version         string
	JWTSecret       []byte
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	// Redis enables rate limiting; nil disables it (most integration tests).
	Redis          *redis.Client
	TrustedProxies []netip.Prefix
	// Kube enables monitoring (health, app logs); nil answers 503.
	Kube kubernetes.Interface
}

// NewApp wires every service and handler into the root HTTP handler. It is
// the composition root shared by cmd/api and the integration test harness,
// so tests exercise exactly the production wiring.
func NewApp(pool *pgxpool.Pool, cfg AppConfig) (http.Handler, error) {
	d, err := appDeps(pool, cfg)
	if err != nil {
		return nil, err
	}
	return NewRouter(d), nil
}

// AppRoutes lists every route of the production wiring.
func AppRoutes(pool *pgxpool.Pool, cfg AppConfig) ([]string, error) {
	d, err := appDeps(pool, cfg)
	if err != nil {
		return nil, err
	}
	return Routes(d), nil
}

func appDeps(pool *pgxpool.Pool, cfg AppConfig) (Deps, error) {
	tokens, err := auth.NewTokenIssuer(cfg.JWTSecret, cfg.AccessTokenTTL)
	if err != nil {
		return Deps{}, err
	}
	authSvc, err := auth.NewService(pool, tokens, cfg.RefreshTokenTTL)
	if err != nil {
		return Deps{}, err
	}
	limiter := ratelimit.New(cfg.Redis, cfg.Logger)
	var soft map[string]ReadinessCheck
	if cfg.Redis != nil {
		soft = map[string]ReadinessCheck{"redis": func(ctx context.Context) error { return cfg.Redis.Ping(ctx).Err() }}
	}
	projectSvc := projects.NewService(pool)
	envSvc := environments.NewService(pool)
	deploySvc := deployments.NewService(pool)
	var inspector *kube.Inspector
	if cfg.Kube != nil {
		inspector = kube.NewInspector(cfg.Kube)
	}
	return Deps{
		Logger:         cfg.Logger,
		Version:        cfg.Version,
		Checks:         map[string]ReadinessCheck{"postgres": pool.Ping},
		SoftChecks:     soft,
		Limiter:        limiter,
		TrustedProxies: cfg.TrustedProxies,
		Auth:           auth.NewHandler(authSvc, limiter),
		Organizations:  organizations.NewHandler(organizations.NewService(pool)),
		Projects:       projects.NewHandler(projectSvc),
		Environments:   environments.NewHandler(envSvc, projectSvc),
		Deployments:    deployments.NewHandler(deploySvc, projectSvc, envSvc),
		Monitoring:     monitoring.NewHandler(projectSvc, envSvc, deploySvc, inspector),
	}, nil
}
