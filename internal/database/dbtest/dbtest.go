// Package dbtest provides a migrated PostgreSQL pool for integration tests.
// Tests using it must run with -p 1 because they share one test database.
package dbtest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vipul2902/clouddeploy/internal/database"
)

// New returns a pool connected to TEST_DATABASE_URL with the schema reset
// and fully migrated, so every test starts from an empty database.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests (make test-integration)")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, url, database.PoolConfig{MaxConns: 8, MaxConnLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	for _, cmd := range []string{"reset", "up"} {
		if err := database.Migrate(ctx, pool, cmd); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}
