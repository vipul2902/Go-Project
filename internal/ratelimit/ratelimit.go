// Package ratelimit implements distributed rate limiting on Redis using
// GCRA (the Generic Cell Rate Algorithm).
//
// Why GCRA rather than a fixed-window counter: a fixed window ("max 10 per
// minute, counter resets at :00") allows 20 requests in two seconds around
// the boundary. GCRA tracks one timestamp per key — the "theoretical arrival
// time" (TAT) of the next request — which spaces requests smoothly while
// still permitting a configured burst. State is a single key with a TTL, and
// the check-and-update runs atomically inside a Lua script, so concurrent API
// replicas can never jointly exceed the limit.
package ratelimit

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Rule is a named limit: at most Limit requests per Period, with bursts of
// up to Limit allowed when the key has been idle.
type Rule struct {
	Name   string
	Limit  int
	Period time.Duration
}

// Result of one rate-limit check.
type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration // > 0 only when denied
	ResetAfter time.Duration // until the full burst is available again
}

//go:embed gcra.lua
var gcraSource string

var gcra = redis.NewScript(gcraSource)

// Limiter checks rules against Redis.
type Limiter struct {
	rdb    *redis.Client
	logger *slog.Logger
}

// New returns a Limiter. A nil client disables limiting (every request is
// allowed), which tests use to keep unrelated suites deterministic.
func New(rdb *redis.Client, logger *slog.Logger) *Limiter {
	return &Limiter{rdb: rdb, logger: logger}
}

// Allow consumes one request for key under rule.
func (l *Limiter) Allow(ctx context.Context, rule Rule, key string) (Result, error) {
	if l == nil || l.rdb == nil {
		return Result{Allowed: true, Limit: rule.Limit, Remaining: rule.Limit}, nil
	}
	interval := rule.Period.Milliseconds() / int64(rule.Limit)
	res, err := gcra.Run(ctx, l.rdb, []string{"rl:" + rule.Name + ":" + key}, interval, rule.Limit).Int64Slice()
	if err != nil {
		return Result{}, fmt.Errorf("rate limit %s: %w", rule.Name, err)
	}
	return Result{
		Allowed:    res[0] == 1,
		Limit:      rule.Limit,
		Remaining:  int(res[1]),
		RetryAfter: time.Duration(res[2]) * time.Millisecond,
		ResetAfter: time.Duration(res[3]) * time.Millisecond,
	}, nil
}

// Reset clears key's state under rule (e.g. after a successful login, so
// the per-account limit counts consecutive failures, not all logins).
func (l *Limiter) Reset(ctx context.Context, rule Rule, key string) error {
	if l == nil || l.rdb == nil {
		return nil
	}
	return l.rdb.Del(ctx, "rl:"+rule.Name+":"+key).Err()
}

// Check runs Allow with the fail-open policy: if Redis is unavailable the
// request is allowed and the failure is logged. Rate limiting protects the
// service; an outage of the protection must not become an outage of the
// service. It writes RateLimit headers, and a 429 response when denied, and
// reports whether the caller should continue.
func (l *Limiter) Check(w http.ResponseWriter, r *http.Request, rule Rule, key string, deny func(http.ResponseWriter, *http.Request)) bool {
	res, err := l.Allow(r.Context(), rule, key)
	if err != nil {
		l.logger.WarnContext(r.Context(), "rate limiter unavailable; failing open", "rule", rule.Name, "err", err)
		return true
	}
	h := w.Header()
	h.Set("RateLimit-Limit", strconv.Itoa(res.Limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(res.Remaining))
	h.Set("RateLimit-Reset", strconv.Itoa(ceilSeconds(res.ResetAfter)))
	if !res.Allowed {
		h.Set("Retry-After", strconv.Itoa(ceilSeconds(res.RetryAfter)))
		deny(w, r)
		return false
	}
	return true
}

// Middleware applies rule to every request, keyed by key(r).
func (l *Limiter) Middleware(rule Rule, key func(*http.Request) string, deny func(http.ResponseWriter, *http.Request)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if l.Check(w, r, rule, key(r), deny) {
				next.ServeHTTP(w, r)
			}
		})
	}
}

func ceilSeconds(d time.Duration) int {
	return int((d + time.Second - 1) / time.Second)
}
