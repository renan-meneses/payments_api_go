package usecase

import (
	"time"

	"github.com/google/uuid"

	"github.com/renan/payments-api/internal/domain"
)

// PaymentView is the read model returned by payment use cases. It joins the
// payment with its receiving location so handlers issue a single query.
type PaymentView struct {
	Payment  *domain.Payment
	Location *domain.ReceivingLocation
}

type CustomerInput struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Document string `json:"document"`
}

type CardInput struct {
	Token      string `json:"token"`
	HolderName string `json:"holder_name"`
	Brand      string `json:"brand"`
	Last4      string `json:"last4"`
}

// ProcessPaymentInput is the command for creating a payment.
type ProcessPaymentInput struct {
	IdempotencyKey      string
	AmountCents         int64
	Currency            string
	Method              domain.PaymentMethod
	ReceivingLocationID uuid.UUID
	Customer            CustomerInput
	Card                *CardInput
	Description         string
	Provider            domain.PaymentProvider // optional override; otherwise routed by config
	Metadata            map[string]string
}

// HandleResult reports what a webhook actually did (useful for observability).
type HandleResult struct {
	Payment      *domain.Payment
	Duplicate    bool
	Ignored      bool
	IgnoreReason string
}

// DashboardInput is the dashboard query string, already parsed.
type DashboardInput struct {
	From       *time.Time
	To         *time.Time
	LocationID *uuid.UUID
}

type DashboardResult struct {
	Summary *domain.DashboardSummary
	Cached  bool
}
