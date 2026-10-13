//go:build integration

package ratelimit_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/vipul2902/clouddeploy/internal/ratelimit"
	"github.com/vipul2902/clouddeploy/internal/redisx/redistest"
)

var logger = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestBurstThenDeny(t *testing.T) {
	l := ratelimit.New(redistest.New(t), logger)
	rule := ratelimit.Rule{Name: "t", Limit: 5, Period: time.Minute} // 1 per 12s, burst 5
	ctx := context.Background()

	for i := range 5 {
		res, err := l.Allow(ctx, rule, "k")
		if err != nil || !res.Allowed || res.Remaining != 4-i {
			t.Fatalf("request %d: %+v %v", i+1, res, err)
		}
	}
	res, err := l.Allow(ctx, rule, "k")
	if err != nil || res.Allowed {
		t.Fatalf("6th request allowed: %+v %v", res, err)
	}
	// Next slot opens one emission interval (12s) after the burst.
	if res.RetryAfter < 11*time.Second || res.RetryAfter > 12*time.Second {
		t.Errorf("RetryAfter = %v, want ~12s", res.RetryAfter)
	}
	// Other keys are independent.
	if res, _ := l.Allow(ctx, rule, "other"); !res.Allowed {
		t.Error("independent key was limited")
	}
}

func TestRefillsOverTime(t *testing.T) {
	l := ratelimit.New(redistest.New(t), logger)
	rule := ratelimit.Rule{Name: "t", Limit: 2, Period: 400 * time.Millisecond} // 1 per 200ms
	ctx := context.Background()
	l.Allow(ctx, rule, "k")
	l.Allow(ctx, rule, "k")
	if res, _ := l.Allow(ctx, rule, "k"); res.Allowed {
		t.Fatal("over-limit request allowed")
	}
	time.Sleep(250 * time.Millisecond)
	if res, _ := l.Allow(ctx, rule, "k"); !res.Allowed {
		t.Fatal("request not allowed after one interval")
	}
}

func TestKeyExpiresWhenIdle(t *testing.T) {
	rdb := redistest.New(t)
	l := ratelimit.New(rdb, logger)
	rule := ratelimit.Rule{Name: "t", Limit: 10, Period: time.Minute}
	l.Allow(context.Background(), rule, "k")
	ttl := rdb.PTTL(context.Background(), "rl:t:k").Val()
	// One request uses one 6s interval; the key must expire then (no leak).
	if ttl <= 0 || ttl > 6*time.Second {
		t.Errorf("PTTL = %v, want (0, 6s]", ttl)
	}
}

func TestReset(t *testing.T) {
	l := ratelimit.New(redistest.New(t), logger)
	rule := ratelimit.Rule{Name: "t", Limit: 1, Period: time.Hour}
	ctx := context.Background()
	l.Allow(ctx, rule, "k")
	if res, _ := l.Allow(ctx, rule, "k"); res.Allowed {
		t.Fatal("limit not applied")
	}
	if err := l.Reset(ctx, rule, "k"); err != nil {
		t.Fatal(err)
	}
	if res, _ := l.Allow(ctx, rule, "k"); !res.Allowed {
		t.Fatal("not allowed after reset")
	}
}

// Two "API replicas" (separate clients and pools) hammer one key at once.
// The Lua script is atomic, so exactly Limit requests may pass in total.
func TestExactLimitUnderConcurrencyAcrossReplicas(t *testing.T) {
	rdb := redistest.New(t)
	replicaB := redis.NewClient(rdb.Options())
	t.Cleanup(func() { _ = replicaB.Close() })
	limiters := []*ratelimit.Limiter{ratelimit.New(rdb, logger), ratelimit.New(replicaB, logger)}
	rule := ratelimit.Rule{Name: "t", Limit: 50, Period: time.Hour}

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := limiters[i%2].Allow(context.Background(), rule, "shared")
			if err != nil {
				t.Error(err)
				return
			}
			if res.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 50 {
		t.Fatalf("allowed %d of 200 concurrent requests, want exactly 50", got)
	}
}

// With Redis down, Check lets requests through (fail open) instead of
// turning a Redis outage into an API outage.
func TestFailOpenWhenRedisUnavailable(t *testing.T) {
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = dead.Close() })
	l := ratelimit.New(dead, logger)
	rule := ratelimit.Rule{Name: "t", Limit: 1, Period: time.Hour}

	if _, err := l.Allow(context.Background(), rule, "k"); err == nil {
		t.Fatal("expected an error from an unreachable Redis")
	}
	for range 3 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if !l.Check(rec, req, rule, "k", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) }) {
			t.Fatal("request denied while Redis is down; want fail-open")
		}
		if rec.Header().Get("RateLimit-Limit") != "" {
			t.Error("rate limit headers set without a real check")
		}
	}
}

// One Redis round trip running the GCRA script: the per-request cost of
// rate limiting (local Redis; add network latency in production).
func BenchmarkAllow(b *testing.B) {
	l := ratelimit.New(redistest.New(b), logger)
	rule := ratelimit.Rule{Name: "bench", Limit: 1 << 30, Period: time.Hour}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := l.Allow(ctx, rule, "k"); err != nil {
			b.Fatal(err)
		}
	}
}

// A rule faster than one request per millisecond used to fail open (the
// millisecond interval rounded to 0). It must now limit precisely: requests
// are sent concurrently so they outrun the refill rate, and the number
// allowed must not exceed burst + refill during the run.
func TestSubMillisecondIntervalRule(t *testing.T) {
	rdb := redistest.New(t)
	l := ratelimit.New(rdb, logger)
	// 500µs interval (sub-millisecond) but only 2,000/s with a burst of 100:
	// far below Redis throughput, so a broken (unlimited) limiter would let
	// nearly all 15,000 requests through while a correct one allows ~2,000.
	rule := ratelimit.Rule{Name: "fast", Limit: 100, Period: 50 * time.Millisecond}
	var allowed, errs atomic.Int64
	var wg sync.WaitGroup
	// Measure on Redis's clock: the limiter uses it, and the host and VM
	// clocks drift slightly (a host-clock bound was flaky under -race).
	start := rdb.Time(context.Background()).Val()
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 300 {
				res, err := l.Allow(context.Background(), rule, "k")
				if err != nil {
					errs.Add(1)
					continue
				}
				if res.Allowed {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	elapsed := rdb.Time(context.Background()).Val().Sub(start)
	if errs.Load() > 0 {
		t.Fatalf("%d errors: a fast rule must not error (that fails open)", errs.Load())
	}
	maxAllowed := int64(100) + int64(elapsed.Seconds()*2_000) + 1
	if a := allowed.Load(); a < 100 || a > maxAllowed {
		t.Fatalf("allowed %d of 15,000 in %v; want between 100 and %d", a, elapsed.Round(time.Millisecond), maxAllowed)
	}
	t.Logf("allowed %d of 15,000 in %v (bound %d)", allowed.Load(), elapsed.Round(time.Millisecond), maxAllowed)
}
