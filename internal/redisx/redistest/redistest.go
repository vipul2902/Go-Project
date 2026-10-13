// Package redistest provides a clean Redis client for integration tests.
// Tests using it must run with -p 1 (they share one Redis database).
package redistest

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/vipul2902/clouddeploy/internal/redisx"
)

// New connects to TEST_REDIS_URL and flushes that database (never db 0,
// which development uses).
func New(t testing.TB) *redis.Client {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Fatal("TEST_REDIS_URL is required for integration tests (make test-integration)")
	}
	rdb, err := redisx.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	if rdb.Options().DB == 0 {
		t.Fatal("TEST_REDIS_URL must not use database 0 (development data)")
	}
	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}
