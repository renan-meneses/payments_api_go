package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/renan/payments-api/pkg/httpx"
)

// Authenticator validates static API keys (one per tenant). Keys never appear
// in logs; only the tenant identifier does.
type Authenticator struct {
	keys map[string]string
}

func NewAuthenticator(keys map[string]string) *Authenticator {
	return &Authenticator{keys: keys}
}

// Middleware accepts X-API-Key or Authorization: Bearer <key>.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if key == "" {
			if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
				key = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
			}
		}

		tenant, ok := a.authenticate(key)
		if !ok {
			w.Header().Set("WWW-Authenticate", `ApiKey realm="payments-api"`)
			httpx.WriteProblem(w, http.StatusUnauthorized, "unauthorized",
				"Unauthorized", "missing or invalid API key")
			return
		}

		next.ServeHTTP(w, r.WithContext(WithTenant(r.Context(), tenant)))
	})
}

// authenticate compares against every configured key in constant time so an
// attacker cannot learn key prefixes from timing.
func (a *Authenticator) authenticate(candidate string) (string, bool) {
	if candidate == "" {
		return "", false
	}
	var (
		tenant string
		found  bool
	)
	for key, keyTenant := range a.keys {
		matches := subtle.ConstantTimeCompare([]byte(key), []byte(candidate)) == 1
		if matches {
			tenant = keyTenant
			found = true
		}
	}
	return tenant, found
}
