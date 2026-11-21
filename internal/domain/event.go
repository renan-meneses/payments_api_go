package domain

import (
	"time"

	"github.com/google/uuid"
)

// Domain event types published to Kafka. Consumers (notifications, audit,
// ledger projections) subscribe by type; they never read our database.
const (
	EventPaymentCreated   = "payment.created"
	EventPaymentSucceeded = "payment.succeeded"
	EventPaymentFailed    = "payment.failed"
	EventPaymentExpired   = "payment.expired"
	EventPaymentRefunded  = "payment.refunded"
	EventPixConfirmed     = "pix.confirmed"
)

// Event is the transport-agnostic envelope. Infrastructure adapters own the
// wire format details (Kafka topic, Avro/JSON, headers, partition key).
type Event struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	AggregateID string            `json:"aggregate_id"`
	OccurredAt  time.Time         `json:"occurred_at"`
	Version     int               `json:"version"`
	Payload     any               `json:"payload"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// NewEvent builds an event with a fresh id. Metadata usually carries request_id
// and tenant so consumers can correlate traces.
func NewEvent(eventType string, aggregateID string, payload any, occurredAt time.Time, metadata map[string]string) Event {
	return Event{
		ID:          uuid.NewString(),
		Type:        eventType,
		AggregateID: aggregateID,
		OccurredAt:  occurredAt.UTC(),
		Version:     1,
		Payload:     payload,
		Metadata:    metadata,
	}
}

// PaymentEventPayload is the public contract of payment.* events.
type PaymentEventPayload struct {
	PaymentID           uuid.UUID       `json:"payment_id"`
	ReceivingLocationID uuid.UUID       `json:"receiving_location_id"`
	Method              PaymentMethod   `json:"method"`
	Provider            PaymentProvider `json:"provider"`
	Status              PaymentStatus   `json:"status"`
	AmountCents         int64           `json:"amount_cents"`
	Currency            Currency        `json:"currency"`
	ProviderPaymentID   string          `json:"provider_payment_id,omitempty"`
	FailureReason       string          `json:"failure_reason,omitempty"`
}

// NewPaymentEventPayload projects a payment aggregate into its event payload.
func NewPaymentEventPayload(p *Payment) PaymentEventPayload {
	return PaymentEventPayload{
		PaymentID:           p.ID,
		ReceivingLocationID: p.ReceivingLocationID,
		Method:              p.Method,
		Provider:            p.Provider,
		Status:              p.Status,
		AmountCents:         p.Amount.AmountCents,
		Currency:            p.Amount.Currency,
		ProviderPaymentID:   p.ProviderPaymentID,
		FailureReason:       p.FailureReason,
	}
}
