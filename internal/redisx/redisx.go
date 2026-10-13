// Package redisx creates the Redis client with production-safe settings.
package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Open parses url, applies conservative timeouts and verifies connectivity.
//
// Timeouts are short on purpose: Redis answers in well under a millisecond
// when healthy, so anything slower means trouble, and callers (the rate
// limiter) would rather fail open quickly than stall every HTTP request.
func Open(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		// Do not wrap err: it may echo the URL, which can contain a password.
		return nil, fmt.Errorf("parse redis url: invalid format")
	}
	opts.DialTimeout = 2 * time.Second
	opts.ReadTimeout = 300 * time.Millisecond
	opts.WriteTimeout = 300 * time.Millisecond
	opts.PoolSize = 10
	opts.MaxRetries = 1

	client := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}
