//go:build integration

// Integration tests run against a real PostgreSQL (make test-integration).
// They use a dedicated database so development data is never touched.
package database_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vipul2902/clouddeploy/internal/database"
	"github.com/vipul2902/clouddeploy/internal/database/dbtest"
)

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
	pool := dbtest.New(t)
	ctx := context.Background()
	// down then up again proves the Down section is complete and correct.
	if err := database.Migrate(ctx, pool, "down"); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, pool, "up"); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyOneActiveDeploymentPerEnvironment(t *testing.T) {
	pool := dbtest.New(t)
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
	pool := dbtest.New(t)
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
	pool := dbtest.New(t)
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
	pool := dbtest.New(t)
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
	pool := dbtest.New(t)
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
	pool := dbtest.New(t)
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

// Migration 00002 rewrites the legacy secret_ref format before validating
// the new CHECK; existing rows must survive the upgrade.
func TestMigrationConvertsLegacySecretRefs(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool, "reset"); err != nil {
		t.Fatal(err)
	}
	if err := database.MigrateTo(ctx, pool, 1); err != nil {
		t.Fatal(err)
	}
	_, envID := fixture(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO environment_variables (environment_id, key, is_secret, secret_ref)
		VALUES ($1, 'DB_PASSWORD', true, 'k8s:app-dev/db-credentials#password')`, envID); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, pool, "up"); err != nil {
		t.Fatalf("upgrade with legacy data: %v", err)
	}
	var ref string
	if err := pool.QueryRow(ctx, `SELECT secret_ref FROM environment_variables WHERE key = 'DB_PASSWORD'`).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if ref != "db-credentials/password" {
		t.Errorf("secret_ref = %q, want db-credentials/password", ref)
	}
	// And the new constraint now rejects the legacy format.
	if _, err := pool.Exec(ctx, `UPDATE environment_variables SET secret_ref = 'k8s:x/y#z'`); pgCode(err) != checkViolation {
		t.Errorf("legacy format still accepted: %v", err)
	}
}
