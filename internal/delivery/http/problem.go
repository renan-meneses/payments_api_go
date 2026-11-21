package httpdelivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/renan/payments-api/internal/delivery/http/middleware"
	"github.com/renan/payments-api/internal/domain"
	"github.com/renan/payments-api/internal/usecase"
	"github.com/renan/payments-api/pkg/httpx"
)

// writeError maps application/domain errors to RFC 9457 problem responses.
// 5xx details are intentionally opaque; the real error goes to the logs only.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error, payment *usecase.PaymentView) {
	status, code, title := classifyError(err)

	detail := err.Error()
	var validation *domain.ValidationError
	if errors.As(err, &validation) {
		detail = validation.Error()
	}
	if payment != nil && payment.Payment != nil {
		detail = fmt.Sprintf("%s (payment_id=%s, status=%s)", detail, payment.Payment.ID, payment.Payment.Status)
	}

	if status >= http.StatusInternalServerError {
		logger.ErrorContext(r.Context(), "request failed",
			slog.String("request_id", middleware.RequestIDFromContext(r.Context())),
			slog.String("error", err.Error()),
		)
		detail = "an unexpected error occurred"
	}

	httpx.WriteProblem(w, status, code, title, detail)
}

func classifyError(err error) (status int, code, title string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout", "Gateway Timeout"

	case errors.Is(err, usecase.ErrInvalidWebhookSignature):
		return http.StatusUnauthorized, "invalid_webhook_signature", "Invalid Webhook Signature"

	case errors.Is(err, domain.ErrValidation), errors.Is(err, usecase.ErrUnsupportedMethod):
		return http.StatusBadRequest, "validation_error", "Validation Failed"

	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found", "Resource Not Found"

	case errors.Is(err, domain.ErrAlreadyExists),
		errors.Is(err, domain.ErrConflict),
		errors.Is(err, domain.ErrInvalidTransition):
		return http.StatusConflict, "conflict", "Conflict"

	case errors.Is(err, usecase.ErrIdempotencyInProgress):
		return http.StatusConflict, "idempotency_in_progress", "Request In Progress"

	case errors.Is(err, usecase.ErrIdempotencyKeyReuse):
		return http.StatusUnprocessableEntity, "idempotency_key_reuse", "Idempotency Key Reused"

	case errors.Is(err, usecase.ErrProviderDeclined):
		return http.StatusPaymentRequired, "payment_declined", "Payment Declined"

	case errors.Is(err, usecase.ErrProviderUnavailable):
		return http.StatusBadGateway, "provider_unavailable", "Payment Provider Unavailable"

	case errors.Is(err, usecase.ErrProviderNotConfigured):
		return http.StatusServiceUnavailable, "provider_not_configured", "Payment Provider Not Configured"

	default:
		return http.StatusInternalServerError, "internal_error", "Internal Server Error"
	}
}
