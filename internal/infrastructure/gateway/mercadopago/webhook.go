package mercadopago

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/renan/payments-api/internal/domain"
	"github.com/renan/payments-api/internal/usecase"
	"github.com/renan/payments-api/pkg/clock"
)

// WebhookParser verifies and normalizes Mercado Pago payment notifications.
// The webhook body only carries an id, so the authoritative state is fetched
// from the API — never trusted from the request body.
type WebhookParser struct {
	client *Client
	clock  clock.Clock
}

func NewWebhookParser(client *Client, clk clock.Clock) *WebhookParser {
	return &WebhookParser{client: client, clock: clk}
}

func (p *WebhookParser) Provider() domain.PaymentProvider { return domain.ProviderMercadoPago }

func (p *WebhookParser) Parse(ctx context.Context, headers map[string]string, body []byte) (domain.PaymentNotification, error) {
	var payload struct {
		ID     int64  `json:"id"`
		Type   string `json:"type"`
		Action string `json:"action"`
		Data   struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return domain.PaymentNotification{}, domain.NewValidationError("body", "malformed webhook payload")
	}
	if payload.Data.ID == "" {
		return domain.PaymentNotification{}, domain.NewValidationError("data.id", "is required")
	}
	if payload.Type != "" && payload.Type != "payment" {
		// merchant_order, subscription, ... are not part of this integration.
		return domain.PaymentNotification{}, fmt.Errorf("%w: %s", usecase.ErrUnsupportedWebhook, payload.Type)
	}

	if err := p.client.VerifySignature(headers["x-signature"], headers["x-request-id"], payload.Data.ID); err != nil {
		return domain.PaymentNotification{}, fmt.Errorf("%w: %v", usecase.ErrInvalidWebhookSignature, err)
	}

	payment, err := p.client.GetPayment(ctx, payload.Data.ID)
	if err != nil {
		return domain.PaymentNotification{}, fmt.Errorf("fetching mercadopago payment %s: %w", payload.Data.ID, err)
	}

	eventID := fmt.Sprintf("mp:%s", payload.Data.ID)
	if payload.ID > 0 {
		eventID = fmt.Sprintf("mp:%d", payload.ID)
	}

	eventType := payload.Type
	if eventType == "" {
		eventType = "payment"
	}
	if payload.Action != "" {
		eventType += "." + payload.Action
	}

	occurredAt := p.clock.Now()
	if payment.DateLastUpdated != nil {
		occurredAt = *payment.DateLastUpdated
	} else if payment.DateApproved != nil {
		occurredAt = *payment.DateApproved
	}

	amountCents := int64(math.Round(payment.TransactionAmount * 100))

	return domain.PaymentNotification{
		Provider:          domain.ProviderMercadoPago,
		EventID:           eventID,
		EventType:         eventType,
		ProviderPaymentID: strconv.FormatInt(payment.ID, 10),
		Status:            mapStatus(payment.Status),
		AmountCents:       &amountCents,
		OccurredAt:        occurredAt,
		RawPayload:        body,
		Metadata: map[string]string{
			"status_detail":      payment.StatusDetail,
			"external_reference": payment.ExternalReference,
			"provider_status":    strings.TrimSpace(payment.Status),
		},
	}, nil
}
