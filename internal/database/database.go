// Package database owns the PostgreSQL connection pool and schema migrations.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/vipul2902/clouddeploy/internal/tracing"
	"github.com/vipul2902/clouddeploy/migrations"
)

// PoolConfig bounds the connection pool.
type PoolConfig struct {
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// Open creates a connection pool and verifies connectivity. The pool is safe
// for concurrent use and should be created once per process.
func Open(ctx context.Context, url string, pc PoolConfig) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// Do not wrap err: it may echo the URL, which contains the password.
		return nil, fmt.Errorf("parse database url: invalid format")
	}
	// Spans for queries that run inside a traced operation (no-op otherwise).
	cfg.ConnConfig.Tracer = tracing.PGXTracer{}
	cfg.MaxConns = pc.MaxConns
	cfg.MinConns = pc.MinConns
	cfg.MaxConnLifetime = pc.MaxConnLifetime
	cfg.MaxConnIdleTime = pc.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate applies schema migrations. command is a goose command such as
// "up", "down", "status" or "reset".
func Migrate(ctx context.Context, pool *pgxpool.Pool, command string) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	return migrateDB(ctx, db, command)
}

// MigrateTo applies migrations up to and including version. Used by tests
// that need to insert data in an old schema shape before migrating forward.
func MigrateTo(ctx context.Context, pool *pgxpool.Pool, version int64) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}
	_, err = provider.UpTo(ctx, version)
	return err
}

func migrateDB(ctx context.Context, db *sql.DB, command string) error {
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}

	switch command {
	case "up":
		_, err = provider.Up(ctx)
	case "down":
		_, err = provider.Down(ctx)
	case "reset":
		_, err = provider.DownTo(ctx, 0)
	case "status":
		var statuses []*goose.MigrationStatus
		statuses, err = provider.Status(ctx)
		for _, s := range statuses {
			applied := "pending"
			if s.State == goose.StateApplied {
				applied = "applied " + s.AppliedAt.Format(time.RFC3339)
			}
			fmt.Printf("%05d  %-40s %s\n", s.Source.Version, s.Source.Path, applied)
		}
	default:
		return fmt.Errorf("unknown migrate command %q (want up|down|reset|status)", command)
	}
	if err != nil {
		return fmt.Errorf("migrate %s: %w", command, err)
	}
	return nil
}
