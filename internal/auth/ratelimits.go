package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/vipul2902/clouddeploy/internal/middleware"
	"github.com/vipul2902/clouddeploy/internal/ratelimit"
)

// Rate limits for unauthenticated endpoints, the ones exposed to the internet.
var (
	// Per client IP: slows password spraying from one source.
	LoginPerIP = ratelimit.Rule{Name: "login_ip", Limit: 20, Period: time.Minute}
	// Per target account: slows distributed brute force against one user.
	// Reset on successful login, so it effectively counts failures. The
	// trade-off: an attacker can lock a victim out for up to 15 minutes —
	// accepted, as it is bounded and visible, and far better than an
	// unlimited guessing rate.
	LoginPerAccount = ratelimit.Rule{Name: "login_account", Limit: 10, Period: 15 * time.Minute}
	RegisterPerIP   = ratelimit.Rule{Name: "register_ip", Limit: 5, Period: time.Hour}
	RefreshPerIP    = ratelimit.Rule{Name: "refresh_ip", Limit: 60, Period: time.Minute}
	// Per authenticated user, across all API endpoints.
	APIPerUser = ratelimit.Rule{Name: "api_user", Limit: 600, Period: time.Minute}
)

// ipKey returns the client IP for rate limiting ("unknown" if absent, so
// such requests share one conservative bucket rather than bypassing limits).
func ipKey(r *http.Request) string {
	if ip, ok := middleware.ClientIPFrom(r.Context()); ok {
		return ip.String()
	}
	return "unknown"
}

// accountKey hashes the normalized email so raw addresses (personal data)
// are never stored in Redis keys.
func accountKey(email string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(h[:16])
}

// UserKey keys per-user limits; used by the router after RequireAuth.
func UserKey(r *http.Request) string { return UserIDFrom(r.Context()) }
