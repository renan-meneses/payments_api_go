// Package httpdelivery is the inbound HTTP adapter: routing, middleware and
// request/response mapping. It translates transport concerns into use case
// calls and use case errors into RFC 9457 problems.
package httpdelivery

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/renan/payments-api/internal/delivery/http/middleware"
	"github.com/renan/payments-api/internal/usecase"
)

type RouterConfig struct {
	RequestTimeout        time.Duration
	RateLimitFailOpen     bool
	IdempotencyMaxCapture int
}

type RouterDependencies struct {
	Logger      *slog.Logger
	Config      RouterConfig
	Payments    *PaymentHandler
	Dashboard   *DashboardHandler
	Webhooks    *WebhookHandler
	Health      *HealthHandler
	Auth        *middleware.Authenticator
	RateLimiter middleware.RateLimiter
	Idempotency usecase.IdempotencyGuard
}

// NewRouter builds the middleware chain and route table.
//
// Middleware order matters: request id first (so every log has it), then panic
// recovery, then access logs, then the timeout.
func NewRouter(deps RouterDependencies) http.Handler {
	router := chi.NewRouter()

	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer(deps.Logger))
	router.Use(middleware.Logger(deps.Logger))
	router.Use(middleware.Timeout(deps.Config.RequestTimeout))

	router.Get("/healthz", deps.Health.Live)
	router.Get("/readyz", deps.Health.Ready)

	router.Route("/v1", func(r chi.Router) {
		// Public webhooks: authenticated by provider signature, protected by
		// the IP-based rate limiter.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(deps.RateLimiter, deps.Config.RateLimitFailOpen, deps.Logger))
			r.Post("/webhooks/mercadopago", deps.Webhooks.MercadoPago)
			if deps.Webhooks.SimulationEnabled() {
				r.Post("/webhooks/pix-simulation", deps.Webhooks.SimulatePix)
			}
		})

		// Authenticated API surface.
		r.Group(func(r chi.Router) {
			r.Use(deps.Auth.Middleware)
			r.Use(middleware.RateLimit(deps.RateLimiter, deps.Config.RateLimitFailOpen, deps.Logger))

			r.With(middleware.Idempotency(deps.Idempotency, deps.Logger, deps.Config.IdempotencyMaxCapture)).
				Post("/payments", deps.Payments.Create)

			r.Get("/payments/{paymentID}", deps.Payments.Get)
			r.Get("/dashboard/summary", deps.Dashboard.Summary)
		})
	})

	return router
}
