package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PaymentStatus is the lifecycle state of a payment.
//
//	pending ──► processing ──► paid ──► partially_refunded ──► refunded
//	   │             │           │
//	   ├────────────►├──────────►failed
//	   ├────────────►expired
//	   └────────────►canceled
type PaymentStatus string

const (
	PaymentStatusPending           PaymentStatus = "pending"
	PaymentStatusProcessing        PaymentStatus = "processing"
	PaymentStatusPaid              PaymentStatus = "paid"
	PaymentStatusFailed            PaymentStatus = "failed"
	PaymentStatusRefunded          PaymentStatus = "refunded"
	PaymentStatusPartiallyRefunded PaymentStatus = "partially_refunded"
	PaymentStatusExpired           PaymentStatus = "expired"
	PaymentStatusCanceled          PaymentStatus = "canceled"
)

func (s PaymentStatus) IsValid() bool {
	switch s {
	case PaymentStatusPending, PaymentStatusProcessing, PaymentStatusPaid, PaymentStatusFailed,
		PaymentStatusRefunded, PaymentStatusPartiallyRefunded, PaymentStatusExpired, PaymentStatusCanceled:
		return true
	default:
		return false
	}
}

// IsSettled reports whether the payment counts towards captured volume.
func (s PaymentStatus) IsSettled() bool {
	return s == PaymentStatusPaid || s == PaymentStatusPartiallyRefunded || s == PaymentStatusRefunded
}

// PaymentMethod identifies the rail used to move the money.
type PaymentMethod string

const (
	PaymentMethodCreditCard PaymentMethod = "credit_card"
	PaymentMethodPix        PaymentMethod = "pix"
)

func (m PaymentMethod) IsValid() bool {
	return m == PaymentMethodCreditCard || m == PaymentMethodPix
}

// PaymentProvider identifies which gateway processed the payment. The core
// domain only knows the identifier; provider-specific behaviour lives in the
// infrastructure adapters.
type PaymentProvider string

const (
	ProviderStripe      PaymentProvider = "stripe"
	ProviderMercadoPago PaymentProvider = "mercadopago"
	ProviderStatic      PaymentProvider = "static" // local BR Code generator (dev/tests)
)

func (p PaymentProvider) IsValid() bool {
	switch p {
	case ProviderStripe, ProviderMercadoPago, ProviderStatic:
		return true
	default:
		return false
	}
}

// Customer is the payer. The card token is intentionally *not* stored here.
type Customer struct {
	Name     string `json:"name"`
	Email    string `json:"email,omitempty"`
	Document string `json:"document,omitempty"` // CPF/CNPJ, normalized digits
}

// CardInfo holds only non-sensitive card metadata (PCI scope reduction).
type CardInfo struct {
	Brand      string `json:"brand,omitempty"`
	Last4      string `json:"last4,omitempty"`
	HolderName string `json:"holder_name,omitempty"`
}

// PixInfo holds the BR Code (copy-and-paste payload) and its rendering.
type PixInfo struct {
	CopyPaste      string    `json:"copy_paste,omitempty"`
	QRCodeImageURL string    `json:"qr_code_image_url,omitempty"` // data URI or hosted image
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
}

// Payment is the aggregate root. Mutations must go through its methods so the
// state machine invariants are always enforced.
type Payment struct {
	ID                  uuid.UUID       `json:"id"`
	IdempotencyKey      string          `json:"-"`
	ReceivingLocationID uuid.UUID       `json:"receiving_location_id"`
	Amount              Money           `json:"amount"`
	RefundedAmount      Money           `json:"refunded_amount"`
	Method              PaymentMethod   `json:"method"`
	Provider            PaymentProvider `json:"provider"`
	Status              PaymentStatus   `json:"status"`
	ProviderPaymentID   string          `json:"provider_payment_id,omitempty"`
	ProviderStatus      string          `json:"provider_status,omitempty"`
	Customer            Customer        `json:"customer"`
	Card                *CardInfo       `json:"card,omitempty"`
	Pix                 *PixInfo        `json:"pix,omitempty"`
	FailureReason       string          `json:"failure_reason,omitempty"`
	Metadata            map[string]any  `json:"metadata,omitempty"`
	Version             int             `json:"version"`
	PaidAt              *time.Time      `json:"paid_at,omitempty"`
	ExpiresAt           *time.Time      `json:"expires_at,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// NewPaymentParams groups construction inputs; adding fields later does not
// break call sites.
type NewPaymentParams struct {
	IdempotencyKey      string
	ReceivingLocationID uuid.UUID
	Amount              Money
	Method              PaymentMethod
	Provider            PaymentProvider
	Customer            Customer
	Metadata            map[string]any
	ExpiresAt           *time.Time
}

// NewPayment validates invariants and builds a pending payment.
func NewPayment(p NewPaymentParams, now time.Time) (*Payment, error) {
	if p.ReceivingLocationID == uuid.Nil {
		return nil, NewValidationError("receiving_location_id", "is required")
	}
	key := strings.TrimSpace(p.IdempotencyKey)
	if len(key) < 8 || len(key) > 128 {
		return nil, NewValidationError("idempotency_key", "must contain between 8 and 128 characters")
	}
	if !p.Amount.IsPositive() {
		return nil, NewValidationError("amount", "must be greater than zero")
	}
	if !p.Method.IsValid() {
		return nil, NewValidationError("method", "must be one of: credit_card, pix")
	}
	if !p.Provider.IsValid() {
		return nil, NewValidationError("provider", "unsupported payment provider")
	}
	if strings.TrimSpace(p.Customer.Name) == "" {
		return nil, NewValidationError("customer.name", "is required")
	}

	document := NormalizeDocument(p.Customer.Document)
	if document != "" && !IsValidDocument(document) {
		return nil, NewValidationError("customer.document", "invalid CPF or CNPJ")
	}
	if p.Method == PaymentMethodPix {
		if document == "" {
			return nil, NewValidationError("customer.document", "CPF/CNPJ is required for PIX payments")
		}
		if p.ExpiresAt == nil || !p.ExpiresAt.After(now) {
			return nil, NewValidationError("expires_at", "PIX payments require a future expiration")
		}
	}

	metadata := p.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}

	return &Payment{
		ID:                  uuid.New(),
		IdempotencyKey:      key,
		ReceivingLocationID: p.ReceivingLocationID,
		Amount:              p.Amount,
		RefundedAmount:      ZeroMoney(p.Amount.Currency),
		Method:              p.Method,
		Provider:            p.Provider,
		Status:              PaymentStatusPending,
		Customer:            Customer{Name: strings.TrimSpace(p.Customer.Name), Email: strings.TrimSpace(p.Customer.Email), Document: document},
		Metadata:            metadata,
		Version:             1,
		ExpiresAt:           p.ExpiresAt,
		CreatedAt:           now,
		UpdatedAt:           now,
	}, nil
}

// paymentTransitions encodes the allowed state machine edges.
var paymentTransitions = map[PaymentStatus]map[PaymentStatus]struct{}{
	PaymentStatusPending: {
		PaymentStatusProcessing: {},
		PaymentStatusPaid:       {},
		PaymentStatusFailed:     {},
		PaymentStatusExpired:    {},
		PaymentStatusCanceled:   {},
	},
	PaymentStatusProcessing: {
		PaymentStatusPaid:    {},
		PaymentStatusFailed:  {},
		PaymentStatusExpired: {},
	},
	PaymentStatusPaid:              {PaymentStatusPartiallyRefunded: {}, PaymentStatusRefunded: {}},
	PaymentStatusPartiallyRefunded: {PaymentStatusRefunded: {}},
}

func (p *Payment) transitionTo(next PaymentStatus, now time.Time) error {
	if p.Status == next {
		return nil
	}
	allowed, ok := paymentTransitions[p.Status]
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, p.Status, next)
	}
	if _, ok := allowed[next]; !ok {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, p.Status, next)
	}
	p.Status = next
	p.UpdatedAt = now
	return nil
}

// MarkProcessing records gateway acceptance while waiting for an async result.
func (p *Payment) MarkProcessing(providerPaymentID, providerStatus string, now time.Time) error {
	if err := p.transitionTo(PaymentStatusProcessing, now); err != nil {
		return err
	}
	if providerPaymentID != "" {
		p.ProviderPaymentID = providerPaymentID
	}
	p.ProviderStatus = providerStatus
	return nil
}

// MarkPaid settles the payment. It is idempotent: replaying a webhook for an
// already paid payment is a no-op instead of an error.
func (p *Payment) MarkPaid(providerPaymentID, providerStatus string, at time.Time) error {
	if p.Status == PaymentStatusPaid {
		return nil
	}
	if err := p.transitionTo(PaymentStatusPaid, at); err != nil {
		return err
	}
	if providerPaymentID != "" {
		p.ProviderPaymentID = providerPaymentID
	}
	p.ProviderStatus = providerStatus
	p.PaidAt = &at
	p.FailureReason = ""
	return nil
}

// MarkFailed records a terminal failure. Idempotent for webhook replays.
func (p *Payment) MarkFailed(reason, providerStatus string, now time.Time) error {
	if p.Status == PaymentStatusFailed {
		return nil
	}
	if err := p.transitionTo(PaymentStatusFailed, now); err != nil {
		return err
	}
	p.FailureReason = reason
	if providerStatus != "" {
		p.ProviderStatus = providerStatus
	}
	return nil
}

// MarkExpired closes an unpaid PIX charge whose deadline has passed.
func (p *Payment) MarkExpired(now time.Time) error {
	if p.Status == PaymentStatusExpired {
		return nil
	}
	if err := p.transitionTo(PaymentStatusExpired, now); err != nil {
		return err
	}
	p.FailureReason = "expired"
	return nil
}

// AttachCard stores tokenized card metadata returned by the gateway.
func (p *Payment) AttachCard(info CardInfo) {
	p.Card = &info
}

// AttachPixCharge stores the BR Code returned by the PIX provider.
func (p *Payment) AttachPixCharge(providerPaymentID, providerStatus string, info PixInfo, now time.Time) error {
	if p.Method != PaymentMethodPix {
		return fmt.Errorf("%w: cannot attach PIX data to a %s payment", ErrValidation, p.Method)
	}
	if p.Status != PaymentStatusPending && p.Status != PaymentStatusProcessing {
		return fmt.Errorf("%w: cannot attach PIX data in status %s", ErrInvalidTransition, p.Status)
	}
	if providerPaymentID != "" {
		p.ProviderPaymentID = providerPaymentID
	}
	p.ProviderStatus = providerStatus
	if info.ExpiresAt.IsZero() {
		if p.ExpiresAt != nil {
			info.ExpiresAt = *p.ExpiresAt
		} else {
			info.ExpiresAt = now.Add(30 * time.Minute)
		}
	}
	p.Pix = &info
	p.UpdatedAt = now
	return nil
}

// ApplyRefund registers a full or partial refund respecting the captured
// amount. The provider remains the source of truth; this guards our ledger.
func (p *Payment) ApplyRefund(amount Money, now time.Time) error {
	if p.Status != PaymentStatusPaid && p.Status != PaymentStatusPartiallyRefunded {
		return fmt.Errorf("%w: cannot refund payment in status %s", ErrInvalidTransition, p.Status)
	}
	if amount.Currency != p.Amount.Currency {
		return ErrCurrencyMismatch
	}
	if !amount.IsPositive() {
		return NewValidationError("amount", "refund amount must be greater than zero")
	}
	total := p.RefundedAmount.AmountCents + amount.AmountCents
	if total > p.Amount.AmountCents {
		return NewValidationError("amount", "refund exceeds the captured amount")
	}
	p.RefundedAmount = Money{AmountCents: total, Currency: p.Amount.Currency}
	if total == p.Amount.AmountCents {
		p.Status = PaymentStatusRefunded
	} else {
		p.Status = PaymentStatusPartiallyRefunded
	}
	p.UpdatedAt = now
	return nil
}

// IsFinal reports whether no further state change is expected.
func (p *Payment) IsFinal() bool {
	switch p.Status {
	case PaymentStatusPaid, PaymentStatusFailed, PaymentStatusRefunded, PaymentStatusExpired, PaymentStatusCanceled:
		return true
	default:
		return false
	}
}
