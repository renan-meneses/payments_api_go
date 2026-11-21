package usecase

import (
	"context"
	"time"

	"github.com/renan/payments-api/internal/domain"
)

// ---------------------------------------------------------------------------
// Payment gateways (driven ports)
//
// The application declares what it needs; infrastructure adapters (Stripe,
// Mercado Pago, fake gateways) implement it. Swapping providers is a wiring
// change in cmd/api, never a business rule change.
// ---------------------------------------------------------------------------

// CardDetails is a single-use token created client-side (Stripe.js). Raw card
// data never reaches this API, keeping it out of PCI scope.
type CardDetails struct {
	Token      string
	HolderName string
}

type CardChargeRequest struct {
	PaymentID             string
	IdempotencyKey        string
	Amount                domain.Money
	Customer              domain.Customer
	Card                  CardDetails
	Description           string
	ReceivingLocationCode string
	Metadata              map[string]string
}

type CardChargeResult struct {
	ProviderPaymentID string
	ProviderStatus    string
	Approved          bool
	Pending           bool // e.g. 3DS challenge / async capture
	FailureReason     string
	CardBrand         string
	CardLast4         string
}

type CardGateway interface {
	Name() domain.PaymentProvider
	Charge(ctx context.Context, req CardChargeRequest) (CardChargeResult, error)
}

type PixChargeRequest struct {
	PaymentID             string
	IdempotencyKey        string
	Amount                domain.Money
	Customer              domain.Customer
	Description           string
	ReceivingLocationCode string
	ExpiresAt             time.Time
	Metadata              map[string]string
}

type PixChargeResult struct {
	ProviderPaymentID string
	ProviderStatus    string
	CopyPaste         string
	QRCodeImageURL    string
	ExpiresAt         time.Time
}

type PixGateway interface {
	Name() domain.PaymentProvider
	CreateCharge(ctx context.Context, req PixChargeRequest) (PixChargeResult, error)
}

type CardGatewayRegistry interface {
	Card(provider domain.PaymentProvider) (CardGateway, error)
}

type PixGatewayRegistry interface {
	Pix(provider domain.PaymentProvider) (PixGateway, error)
}

// WebhookParser translates a provider-specific HTTP payload into the normalized
// domain notification. It also verifies the provider signature, which is an
// infrastructure concern.
type WebhookParser interface {
	Provider() domain.PaymentProvider
	Parse(ctx context.Context, headers map[string]string, body []byte) (domain.PaymentNotification, error)
}

// ---------------------------------------------------------------------------
// Events, cache, idempotency
// ---------------------------------------------------------------------------

type EventPublisher interface {
	Publish(ctx context.Context, events ...domain.Event) error
}

// DashboardCache caches read models. Get returns (summary, found, error).
type DashboardCache interface {
	Get(ctx context.Context, filter domain.DashboardFilter) (*domain.DashboardSummary, bool, error)
	Put(ctx context.Context, filter domain.DashboardFilter, summary *domain.DashboardSummary) error
	Invalidate(ctx context.Context) error
}

type IdempotencyOutcome int

const (
	// IdempotencyAcquired means the caller owns the key and must execute.
	IdempotencyAcquired IdempotencyOutcome = iota
	// IdempotencyReplay means a completed response is stored for this key.
	IdempotencyReplay
	// IdempotencyInFlight means another request currently holds the key.
	IdempotencyInFlight
)

type IdempotencyRecord struct {
	Outcome     IdempotencyOutcome
	RequestHash string
	StatusCode  int
	Body        []byte
	CompletedAt time.Time
}

type IdempotencyCompletion struct {
	Key         string
	RequestHash string
	Method      string
	Path        string
	StatusCode  int
	Body        []byte
	ResourceID  string
}

// IdempotencyGuard is a optimistic lock + response cache for unsafe requests.
// The Redis adapter is the hot path; a decorator persists an audit trail to
// Postgres.
type IdempotencyGuard interface {
	Begin(ctx context.Context, key, requestHash string) (IdempotencyRecord, error)
	Complete(ctx context.Context, completion IdempotencyCompletion) error
	Abort(ctx context.Context, key string) error
}
