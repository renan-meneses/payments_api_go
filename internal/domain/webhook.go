package domain

import (
	"time"

	"github.com/google/uuid"
)

// WebhookStatus tracks the inbox processing state of a provider event.
type WebhookStatus string

const (
	WebhookStatusReceived  WebhookStatus = "received"
	WebhookStatusProcessed WebhookStatus = "processed"
	WebhookStatusIgnored   WebhookStatus = "ignored"
	WebhookStatusFailed    WebhookStatus = "failed"
)

// WebhookEvent is the inbound event envelope persisted *before* processing
// (inbox pattern). The (provider, provider_event_id) pair is the dedupe key.
type WebhookEvent struct {
	ID              uuid.UUID         `json:"id"`
	Provider        PaymentProvider   `json:"provider"`
	ProviderEventID string            `json:"provider_event_id"`
	EventType       string            `json:"event_type"`
	PaymentID       *uuid.UUID        `json:"payment_id,omitempty"`
	Payload         []byte            `json:"payload"`
	Headers         map[string]string `json:"headers,omitempty"`
	Status          WebhookStatus     `json:"status"`
	Attempts        int               `json:"attempts"`
	Error           string            `json:"error,omitempty"`
	ReceivedAt      time.Time         `json:"received_at"`
	ProcessedAt     *time.Time        `json:"processed_at,omitempty"`
}

// NotificationStatus is the normalized provider-agnostic payment outcome.
// Gateways translate their own vocabulary (approved, in_process, charged_back,
// ...) into this small set so the use case stays provider independent.
type NotificationStatus string

const (
	NotificationApproved NotificationStatus = "approved"
	NotificationPending  NotificationStatus = "pending"
	NotificationRejected NotificationStatus = "rejected"
	NotificationRefunded NotificationStatus = "refunded"
	NotificationExpired  NotificationStatus = "expired"
	NotificationCanceled NotificationStatus = "canceled"
	NotificationUnknown  NotificationStatus = "unknown"
)

// PaymentNotification is the normalized result of parsing a provider webhook.
type PaymentNotification struct {
	Provider          PaymentProvider
	EventID           string
	EventType         string
	ProviderPaymentID string
	Status            NotificationStatus
	AmountCents       *int64
	OccurredAt        time.Time
	RawPayload        []byte
	Metadata          map[string]string
}
