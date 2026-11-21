package httpdelivery

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/renan/payments-api/internal/domain"
	"github.com/renan/payments-api/internal/usecase"
	"github.com/renan/payments-api/pkg/httpx"
)

type WebhookHandler struct {
	handlePix       *usecase.HandlePixWebhook
	parsers         map[domain.PaymentProvider]usecase.WebhookParser
	logger          *slog.Logger
	allowSimulation bool
}

func NewWebhookHandler(
	handlePix *usecase.HandlePixWebhook,
	parsers []usecase.WebhookParser,
	logger *slog.Logger,
	allowSimulation bool,
) *WebhookHandler {
	byProvider := make(map[domain.PaymentProvider]usecase.WebhookParser, len(parsers))
	for _, parser := range parsers {
		byProvider[parser.Provider()] = parser
	}
	return &WebhookHandler{
		handlePix:       handlePix,
		parsers:         byProvider,
		logger:          logger,
		allowSimulation: allowSimulation,
	}
}

func (h *WebhookHandler) SimulationEnabled() bool { return h.allowSimulation }

// MercadoPago handles POST /v1/webhooks/mercadopago.
func (h *WebhookHandler) MercadoPago(w http.ResponseWriter, r *http.Request) {
	h.process(w, r, domain.ProviderMercadoPago)
}

func (h *WebhookHandler) process(w http.ResponseWriter, r *http.Request, provider domain.PaymentProvider) {
	parser, ok := h.parsers[provider]
	if !ok {
		httpx.WriteProblem(w, http.StatusNotImplemented, "webhook_not_supported",
			"Webhook Not Supported", "no parser is configured for this provider")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_body", "Invalid Webhook Body", "could not read the request body")
		return
	}

	notification, err := parser.Parse(r.Context(), httpx.HeaderMap(r.Header), body)
	if err != nil {
		switch {
		case errors.Is(err, usecase.ErrInvalidWebhookSignature):
			h.logger.WarnContext(r.Context(), "webhook signature verification failed",
				slog.String("provider", string(provider)),
				slog.String("error", err.Error()),
			)
			httpx.WriteProblem(w, http.StatusUnauthorized, "invalid_webhook_signature",
				"Invalid Webhook Signature", "signature verification failed")

		case errors.Is(err, usecase.ErrUnsupportedWebhook),
			domain.IsValidation(err),
			errors.Is(err, domain.ErrNotFound):
			// Acknowledge events we can never process, otherwise the provider
			// retries them forever.
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ignored"})

		default:
			h.logger.ErrorContext(r.Context(), "failed to parse webhook",
				slog.String("provider", string(provider)),
				slog.String("error", err.Error()),
			)
			httpx.WriteProblem(w, http.StatusBadGateway, "webhook_parse_failed",
				"Webhook Processing Failed", "could not fetch or parse the provider notification")
		}
		return
	}

	result, err := h.handlePix.Execute(r.Context(), notification)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
			return
		}
		h.logger.ErrorContext(r.Context(), "failed to process webhook",
			slog.String("provider", string(provider)),
			slog.String("event_id", notification.EventID),
			slog.String("error", err.Error()),
		)
		httpx.WriteProblem(w, http.StatusInternalServerError, "webhook_processing_failed",
			"Webhook Processing Failed", "the notification could not be processed")
		return
	}

	status := "processed"
	switch {
	case result.Duplicate:
		status = "duplicate"
	case result.Ignored:
		status = "ignored"
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": status})
}

// SimulatePix is a development-only endpoint that fabricates an approval
// notification. It is only registered when ALLOW_PIX_SIMULATION=true.
func (h *WebhookHandler) SimulatePix(w http.ResponseWriter, r *http.Request) {
	if !h.allowSimulation {
		httpx.WriteProblem(w, http.StatusNotFound, "not_found", "Not Found", "")
		return
	}

	var request simulatePixRequest
	if err := httpx.DecodeJSON(w, r, &request, 1<<20); err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_body", "Invalid Request Body", err.Error())
		return
	}
	if request.ProviderPaymentID == "" {
		httpx.WriteProblem(w, http.StatusBadRequest, "validation_error", "Validation Failed",
			"provider_payment_id is required")
		return
	}

	provider := domain.PaymentProvider(request.Provider)
	if provider == "" {
		provider = domain.ProviderStatic
	}

	notification := domain.PaymentNotification{
		Provider:          provider,
		EventID:           fmt.Sprintf("sim:%s:%d", request.ProviderPaymentID, time.Now().UnixNano()),
		EventType:         "payment.simulated",
		ProviderPaymentID: request.ProviderPaymentID,
		Status:            domain.NotificationApproved,
		AmountCents:       request.AmountCents,
		OccurredAt:        time.Now().UTC(),
		RawPayload:        []byte(`{"simulated":true}`),
	}

	result, err := h.handlePix.Execute(r.Context(), notification)
	if err != nil {
		writeError(w, r, h.logger, err, nil)
		return
	}

	response := map[string]any{"status": "processed"}
	if result.Payment != nil {
		response["payment_id"] = result.Payment.ID.String()
		response["payment_status"] = string(result.Payment.Status)
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}
