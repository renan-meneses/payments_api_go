package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/renan/payments-api/pkg/httpx"
)

// RateLimiter is the port implemented by the Redis limiter. Declaring it here
// (consumer side) keeps the delivery layer independent of infrastructure.
type RateLimiter interface {
	Allow(ctx context.Context, key string) (allowed bool, remaining int, resetAfter time.Duration, err error)
}

// RateLimit applies the limiter per tenant (authenticated routes) or per client
// IP (public webhooks). On Redis failure it can fail open (availability) or
// closed (hard protection); configurable per deployment.
func RateLimit(limiter RateLimiter, failOpen bool, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := TenantFromContext(r.Context())
			if key == "" {
				key = "ip:" + clientIP(r)
			}

			allowed, remaining, resetAfter, err := limiter.Allow(r.Context(), key)
			if err != nil {
				logger.WarnContext(r.Context(), "rate limiter unavailable", slog.String("error", err.Error()))
				if !failOpen {
					httpx.WriteProblem(w, http.StatusServiceUnavailable, "rate_limiter_unavailable",
						"Service Unavailable", "rate limiter is unavailable, try again later")
					return
				}
			} else {
				w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
				if !allowed {
					retryAfter := int(resetAfter.Seconds()) + 1
					w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
					httpx.WriteProblem(w, http.StatusTooManyRequests, "rate_limited",
						"Too Many Requests", "request rate limit exceeded, retry later")
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, found := cut(forwarded, ","); found {
			return first
		}
		return forwarded
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func cut(s, sep string) (before, after string, found bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}
