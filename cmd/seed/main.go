// Command seed loads development fixture data. It is idempotent (fixed IDs
// plus ON CONFLICT DO NOTHING) and refuses to run in production.
//
// All seeded users share SEED_PASSWORD. These are development-only
// credentials for fictional accounts on the reserved .local domain.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/database"
)

const (
	orgID      = "00000000-0000-4000-8000-000000000001"
	adminID    = "00000000-0000-4000-8000-000000000101"
	devID      = "00000000-0000-4000-8000-000000000102"
	viewerID   = "00000000-0000-4000-8000-000000000103"
	projectID  = "00000000-0000-4000-8000-000000000201"
	envDevID   = "00000000-0000-4000-8000-000000000301"
	envStageID = "00000000-0000-4000-8000-000000000302"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if env := os.Getenv("APP_ENV"); env != "" && env != "development" {
		return fmt.Errorf("refusing to seed when APP_ENV=%q (development only)", env)
	}
	url, password := os.Getenv("DATABASE_URL"), os.Getenv("SEED_PASSWORD")
	if url == "" || len(password) < 12 {
		return fmt.Errorf("DATABASE_URL and SEED_PASSWORD (>= 12 chars) are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := database.Open(ctx, url, database.PoolConfig{MaxConns: 2, MaxConnLifetime: time.Hour})
	if err != nil {
		return err
	}
	defer pool.Close()

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	// One transaction: either all fixtures load or none do.
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for _, s := range statements(hash) {
			if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
				return fmt.Errorf("%s: %w", s.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Println("seed: ok (users: admin@clouddeploy.local, developer@clouddeploy.local, viewer@clouddeploy.local)")
	return nil
}

type stmt struct {
	name string
	sql  string
	args []any
}

func statements(hash string) []stmt {
	return []stmt{
		{"organization", `INSERT INTO organizations (id, name, slug) VALUES ($1, 'Acme Dev', 'acme-dev') ON CONFLICT DO NOTHING`, []any{orgID}},
		{"users", `INSERT INTO users (id, email, name, password_hash) VALUES
			($1, 'admin@clouddeploy.local', 'Dev Admin', $4),
			($2, 'developer@clouddeploy.local', 'Dev Developer', $4),
			($3, 'viewer@clouddeploy.local', 'Dev Viewer', $4)
			ON CONFLICT DO NOTHING`, []any{adminID, devID, viewerID, hash}},
		{"members", `INSERT INTO organization_members (organization_id, user_id, role) VALUES
			($1, $2, 'admin'), ($1, $3, 'developer'), ($1, $4, 'viewer')
			ON CONFLICT DO NOTHING`, []any{orgID, adminID, devID, viewerID}},
		{"project", `INSERT INTO projects (id, organization_id, name, repository_url, default_branch)
			VALUES ($1, $2, 'sample-api', 'https://github.com/example/sample-api', 'main') ON CONFLICT DO NOTHING`,
			[]any{projectID, orgID}},
		{"environments", `INSERT INTO environments (id, project_id, name, namespace, replicas) VALUES
			($1, $3, 'development', 'sample-api-dev', 1),
			($2, $3, 'staging', 'sample-api-staging', 2)
			ON CONFLICT DO NOTHING`, []any{envDevID, envStageID, projectID}},
		{"environment_variables", `INSERT INTO environment_variables (environment_id, key, value, is_secret, secret_ref) VALUES
			($1, 'LOG_LEVEL', 'debug', false, NULL),
			($1, 'DATABASE_PASSWORD', NULL, true, 'k8s:sample-api-dev/db-credentials#password')
			ON CONFLICT DO NOTHING`, []any{envDevID}},
		{"deployments", `INSERT INTO deployments
			(id, project_id, environment_id, commit_sha, image, status, triggered_by, error_message, created_at, started_at, completed_at) VALUES
			('00000000-0000-4000-8000-000000000401', $1, $2, 'a1b2c3d', 'sample-api:a1b2c3d', 'RUNNING', $3, NULL,
			  now() - interval '3 days', now() - interval '3 days', now() - interval '3 days' + interval '2 minutes'),
			('00000000-0000-4000-8000-000000000402', $1, $2, 'b2c3d4e', 'sample-api:b2c3d4e', 'FAILED', $3, 'rollout timed out: readiness probe failing',
			  now() - interval '1 day', now() - interval '1 day', now() - interval '1 day' + interval '5 minutes'),
			('00000000-0000-4000-8000-000000000403', $1, $2, 'c3d4e5f', 'sample-api:c3d4e5f', 'RUNNING', $3, NULL,
			  now() - interval '2 hours', now() - interval '2 hours', now() - interval '2 hours' + interval '90 seconds')
			ON CONFLICT DO NOTHING`, []any{projectID, envDevID, devID}},
	}
}
