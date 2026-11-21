package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DashboardFilter is the read model query. From is inclusive, To exclusive.
type DashboardFilter struct {
	From       time.Time
	To         time.Time
	LocationID *uuid.UUID
	Currency   Currency
}

// Period documents the window used by a summary.
type Period struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// StatusBreakdown counts payments per lifecycle bucket.
type StatusBreakdown struct {
	Pending    int64 `json:"pending"`
	Processing int64 `json:"processing"`
	Paid       int64 `json:"paid"`
	Failed     int64 `json:"failed"`
	Refunded   int64 `json:"refunded"`
	Expired    int64 `json:"expired"`
}

// Succeeded is the denominator used for approval rates and average ticket.
func (s StatusBreakdown) Succeeded() int64 { return s.Paid + s.Refunded }

// MethodBreakdown aggregates one payment method.
type MethodBreakdown struct {
	Transactions int64   `json:"transactions"`
	Volume       Money   `json:"volume"`
	Share        float64 `json:"share"` // 0..1 of settled volume
}

// LocationMetrics is one row of the receiving-location breakdown.
type LocationMetrics struct {
	LocationID     uuid.UUID       `json:"location_id,omitempty"`
	LocationCode   string          `json:"location_code,omitempty"`
	LocationName   string          `json:"location_name,omitempty"` // empty on the totals row
	Kind           LocationKind    `json:"kind,omitempty"`
	Transactions   int64           `json:"transactions"`
	Volume         Money           `json:"volume"`     // captured (paid + refunded)
	NetVolume      Money           `json:"net_volume"` // captured minus refunded
	RefundedVolume Money           `json:"refunded_volume"`
	Status         StatusBreakdown `json:"status"`
	CreditCard     MethodBreakdown `json:"credit_card"`
	Pix            MethodBreakdown `json:"pix"`
	AverageTicket  Money           `json:"average_ticket"`
	ApprovalRate   float64         `json:"approval_rate"` // 0..1
}

// DailyPoint is one bucket of the volume time series.
type DailyPoint struct {
	Date         time.Time `json:"date"`
	Transactions int64     `json:"transactions"`
	Settled      int64     `json:"settled"`
	Volume       Money     `json:"volume"`
}

// DashboardSummary is the cached read model served to the dashboard.
type DashboardSummary struct {
	Period      Period            `json:"period"`
	Currency    Currency          `json:"currency"`
	Totals      LocationMetrics   `json:"totals"`
	ByLocation  []LocationMetrics `json:"by_location"`
	Daily       []DailyPoint      `json:"daily"`
	GeneratedAt time.Time         `json:"generated_at"`
}

// DashboardRepository is a read-only port implemented with optimized SQL.
// It returns pre-aggregated rows; the use case never sees payments one by one.
type DashboardRepository interface {
	Summary(ctx context.Context, filter DashboardFilter) (*DashboardSummary, error)
}

// PaymentRepository is the write/read port for the payment aggregate.
// Update uses optimistic concurrency (payment.Version) and returns
// domain.ErrConflict when another writer won the race.
type PaymentRepository interface {
	Create(ctx context.Context, payment *Payment) error
	Update(ctx context.Context, payment *Payment) error
	FindByID(ctx context.Context, id uuid.UUID) (*Payment, error)
	FindByIdempotencyKey(ctx context.Context, key string) (*Payment, error)
	FindByProviderPaymentID(ctx context.Context, provider PaymentProvider, providerPaymentID string) (*Payment, error)
}

// ReceivingLocationRepository exposes the POS catalogue.
type ReceivingLocationRepository interface {
	FindByID(ctx context.Context, id uuid.UUID) (*ReceivingLocation, error)
	ListActive(ctx context.Context) ([]ReceivingLocation, error)
}

// WebhookEventRepository implements the inbox/claim semantics used to make
// webhook handling idempotent.
type WebhookEventRepository interface {
	// Claim inserts the event or re-claims a previously failed one. It returns
	// claimed=false when the event was already processed/ignored.
	Claim(ctx context.Context, event *WebhookEvent) (id uuid.UUID, claimed bool, err error)
	LinkPayment(ctx context.Context, eventID, paymentID uuid.UUID) error
	MarkProcessed(ctx context.Context, eventID uuid.UUID, at time.Time) error
	MarkIgnored(ctx context.Context, eventID uuid.UUID, reason string, at time.Time) error
	MarkFailed(ctx context.Context, eventID uuid.UUID, reason string) error
}

// TxManager runs a function inside a database transaction. Repositories
// discover the transaction through the context, so use cases stay free of SQL
// details while still composing atomic operations.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
