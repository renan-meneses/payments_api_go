package httpdelivery

import (
	"context"
	"net/http"
	"time"

	"github.com/renan/payments-api/pkg/httpx"
)

// Check is a dependency probe (database, cache, ...).
type Check func(ctx context.Context) error

type HealthHandler struct {
	checks map[string]Check
}

func NewHealthHandler(checks map[string]Check) *HealthHandler {
	return &HealthHandler{checks: checks}
}

// Live is the liveness probe: it only proves the process is running.
func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Ready is the readiness probe: every dependency must answer within 2s.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	results := make(map[string]string, len(h.checks))
	ready := true
	for name, check := range h.checks {
		if err := check(ctx); err != nil {
			results[name] = "unavailable"
			ready = false
			continue
		}
		results[name] = "ok"
	}

	status := http.StatusOK
	overall := "ready"
	if !ready {
		status = http.StatusServiceUnavailable
		overall = "not_ready"
	}
	httpx.WriteJSON(w, status, map[string]any{"status": overall, "checks": results})
}
