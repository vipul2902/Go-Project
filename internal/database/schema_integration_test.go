//go:build integration

// Integration tests run against a real PostgreSQL (make test-integration).
// They use a dedicated database so development data is never touched.
package database

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}
	ctx := context.Background()
	pool, err := Open(ctx, url, PoolConfig{MaxConns: 4, MaxConnLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// Start every test from a clean, fully migrated schema.
	if err := Migrate(ctx, pool, "reset"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, "up"); err != nil {
		t.Fatal(err)
	}
	return pool
}

// fixture inserts an org, project and environment and returns the IDs.
func fixture(t *testing.T, pool *pgxpool.Pool) (projectID, envID string) {
	t.Helper()
	ctx := context.Background()
	err := pool.QueryRow(ctx, `
		WITH o AS (INSERT INTO organizations (name, slug) VALUES ('Test', 'test') RETURNING id),
		     p AS (INSERT INTO projects (organization_id, name, repository_url) SELECT id, 'app', 'https://example.com/app' FROM o RETURNING id)
		INSERT INTO environments (project_id, name, namespace) SELECT id, 'development', 'app-dev' FROM p
		RETURNING project_id, id`).Scan(&projectID, &envID)
	if err != nil {
		t.Fatal(err)
	}
	return projectID, envID
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

const (
	uniqueViolation = "23505"
	checkViolation  = "23514"
)

func TestMigrationsRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	// down then up again proves the Down section is complete and correct.
	if err := Migrate(ctx, pool, "down"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, "up"); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyOneActiveDeploymentPerEnvironment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	projectID, envID := fixture(t, pool)

	insert := `INSERT INTO deployments (project_id, environment_id, commit_sha) VALUES ($1, $2, $3)`
	if _, err := pool.Exec(ctx, insert, projectID, envID, "aaaaaaa"); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, insert, projectID, envID, "bbbbbbb")
	if pgCode(err) != uniqueViolation {
		t.Fatalf("second active deployment: err = %v, want unique violation", err)
	}

	// Once the first finishes, a new deployment is allowed.
	if _, err := pool.Exec(ctx, `UPDATE deployments SET status = 'RUNNING', started_at = now(), completed_at = now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, projectID, envID, "bbbbbbb"); err != nil {
		t.Fatalf("deployment after completion rejected: %v", err)
	}
}

func TestIdempotencyKeyIsUniquePerEnvironment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	projectID, envID := fixture(t, pool)

	insert := `INSERT INTO deployments (project_id, environment_id, commit_sha, idempotency_key, status, started_at, completed_at)
	           VALUES ($1, $2, 'aaaaaaa', 'key-1', 'FAILED', now(), now())`
	if _, err := pool.Exec(ctx, insert, projectID, envID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, projectID, envID); pgCode(err) != uniqueViolation {
		t.Fatalf("duplicate idempotency key: err = %v, want unique violation", err)
	}
}

func TestConcurrentDeploysOnlyOneWins(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	projectID, envID := fixture(t, pool)

	const n = 4
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := pool.Exec(ctx, `INSERT INTO deployments (project_id, environment_id, commit_sha) VALUES ($1, $2, 'ccccccc')`, projectID, envID)
			errs <- err
		}()
	}
	wins := 0
	for range n {
		err := <-errs
		switch {
		case err == nil:
			wins++
		case pgCode(err) != uniqueViolation:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d concurrent inserts succeeded, want exactly 1", wins)
	}
}

func TestConstraints(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	projectID, envID := fixture(t, pool)

	tests := []struct {
		name string
		sql  string
		args []any
	}{
		{"secret with plaintext value", `INSERT INTO environment_variables (environment_id, key, value, is_secret, secret_ref) VALUES ($1, 'DB_PASS', 'hunter2', true, 'ref')`, []any{envID}},
		{"plain var without value", `INSERT INTO environment_variables (environment_id, key, is_secret) VALUES ($1, 'X', false)`, []any{envID}},
		{"lowercase env var key", `INSERT INTO environment_variables (environment_id, key, value) VALUES ($1, 'bad-key', 'v')`, []any{envID}},
		{"unknown deployment status", `INSERT INTO deployments (project_id, environment_id, commit_sha, status) VALUES ($1, $2, 'aaaaaaa', 'DONE')`, []any{projectID, envID}},
		{"terminal status without completed_at", `INSERT INTO deployments (project_id, environment_id, commit_sha, status) VALUES ($1, $2, 'aaaaaaa', 'FAILED')`, []any{projectID, envID}},
		{"invalid commit sha", `INSERT INTO deployments (project_id, environment_id, commit_sha) VALUES ($1, $2, 'not-a-sha')`, []any{projectID, envID}},
		{"uppercase email", `INSERT INTO users (email, name, password_hash) VALUES ('Bob@Example.com', 'Bob', 'x')`, nil},
		{"unknown role", `WITH u AS (INSERT INTO users (email, name, password_hash) VALUES ('r@example.com', 'R', 'x') RETURNING id)
		  INSERT INTO organization_members (organization_id, user_id, role) SELECT o.id, u.id, 'owner' FROM organizations o, u`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tt.sql, tt.args...)
			if err == nil {
				t.Fatal("expected constraint violation, got nil")
			}
			if pgCode(err) != checkViolation {
				t.Fatalf("err = %v, want check violation", err)
			}
		})
	}
}

func TestSoftDeletedProjectNameReusable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	fixture(t, pool)

	insert := `INSERT INTO projects (organization_id, name, repository_url) SELECT id, 'app', 'https://example.com/app2' FROM organizations`
	if _, err := pool.Exec(ctx, insert); pgCode(err) != uniqueViolation {
		t.Fatalf("duplicate live name: err = %v, want unique violation", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE projects SET deleted_at = now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert); err != nil {
		t.Fatalf("name reuse after soft delete rejected: %v", err)
	}
}

func TestUpdatedAtTrigger(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	projectID, _ := fixture(t, pool)

	var before, after time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM projects WHERE id = $1`, projectID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Separate transaction so now() advances.
	if err := pool.QueryRow(ctx, `UPDATE projects SET default_branch = 'develop' WHERE id = $1 RETURNING updated_at`, projectID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.After(before) {
		t.Errorf("updated_at not advanced: before=%v after=%v", before, after)
	}
}
