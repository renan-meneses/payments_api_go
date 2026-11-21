package httpdelivery

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/renan/payments-api/internal/domain"
	"github.com/renan/payments-api/internal/usecase"
)

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

type createPaymentRequest struct {
	AmountCents         int64             `json:"amount_cents"`
	Currency            string            `json:"currency"`
	Method              string            `json:"method"`
	ReceivingLocationID string            `json:"receiving_location_id"`
	Customer            customerRequest   `json:"customer"`
	Card                *cardRequest      `json:"card,omitempty"`
	Description         string            `json:"description,omitempty"`
	Provider            string            `json:"provider,omitempty"`
	Metadata            map[string]string `json:"metadata,omitempty"`
}

type customerRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email,omitempty"`
	Document string `json:"document,omitempty"`
}

type cardRequest struct {
	Token      string `json:"token"`
	HolderName string `json:"holder_name,omitempty"`
	Brand      string `json:"brand,omitempty"`
	Last4      string `json:"last4,omitempty"`
}

func (r createPaymentRequest) toInput(idempotencyKey string) (usecase.ProcessPaymentInput, error) {
	locationID, err := uuid.Parse(r.ReceivingLocationID)
	if err != nil {
		return usecase.ProcessPaymentInput{}, fmt.Errorf("receiving_location_id must be a valid UUID")
	}

	input := usecase.ProcessPaymentInput{
		IdempotencyKey:      idempotencyKey,
		AmountCents:         r.AmountCents,
		Currency:            r.Currency,
		Method:              domain.PaymentMethod(r.Method),
		ReceivingLocationID: locationID,
		Customer: usecase.CustomerInput{
			Name:     r.Customer.Name,
			Email:    r.Customer.Email,
			Document: r.Customer.Document,
		},
		Description: r.Description,
		Provider:    domain.PaymentProvider(r.Provider),
		Metadata:    r.Metadata,
	}
	if r.Card != nil {
		input.Card = &usecase.CardInput{
			Token:      r.Card.Token,
			HolderName: r.Card.HolderName,
			Brand:      r.Card.Brand,
			Last4:      r.Card.Last4,
		}
	}
	return input, nil
}

type simulatePixRequest struct {
	Provider          string `json:"provider"`
	ProviderPaymentID string `json:"provider_payment_id"`
	AmountCents       *int64 `json:"amount_cents,omitempty"`
}

// ---------------------------------------------------------------------------
// Responses
// ---------------------------------------------------------------------------

type paymentResponse struct {
	ID                  string            `json:"id"`
	Status              string            `json:"status"`
	Method              string            `json:"method"`
	Provider            string            `json:"provider"`
	AmountCents         int64             `json:"amount_cents"`
	Currency            string            `json:"currency"`
	RefundedAmountCents int64             `json:"refunded_amount_cents"`
	ReceivingLocation   *locationResponse `json:"receiving_location,omitempty"`
	Customer            customerResponse  `json:"customer"`
	Card                *cardResponse     `json:"card,omitempty"`
	Pix                 *pixResponse      `json:"pix,omitempty"`
	ProviderPaymentID   string            `json:"provider_payment_id,omitempty"`
	ProviderStatus      string            `json:"provider_status,omitempty"`
	FailureReason       string            `json:"failure_reason,omitempty"`
	Metadata            map[string]any    `json:"metadata,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	PaidAt              *time.Time        `json:"paid_at,omitempty"`
	ExpiresAt           *time.Time        `json:"expires_at,omitempty"`
}

type customerResponse struct {
	Name     string `json:"name"`
	Email    string `json:"email,omitempty"`
	Document string `json:"document,omitempty"`
}

type cardResponse struct {
	Brand string `json:"brand,omitempty"`
	Last4 string `json:"last4,omitempty"`
}

type pixResponse struct {
	CopyPaste      string    `json:"copy_paste"`
	QRCodeImageURL string    `json:"qr_code_image_url,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type locationResponse struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func toPaymentResponse(view *usecase.PaymentView) paymentResponse {
	p := view.Payment

	response := paymentResponse{
		ID:                  p.ID.String(),
		Status:              string(p.Status),
		Method:              string(p.Method),
		Provider:            string(p.Provider),
		AmountCents:         p.Amount.AmountCents,
		Currency:            string(p.Amount.Currency),
		RefundedAmountCents: p.RefundedAmount.AmountCents,
		Customer: customerResponse{
			Name:     p.Customer.Name,
			Email:    p.Customer.Email,
			Document: p.Customer.Document,
		},
		ProviderPaymentID: p.ProviderPaymentID,
		ProviderStatus:    p.ProviderStatus,
		FailureReason:     p.FailureReason,
		Metadata:          p.Metadata,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
		PaidAt:            p.PaidAt,
		ExpiresAt:         p.ExpiresAt,
	}

	if p.Card != nil {
		response.Card = &cardResponse{Brand: p.Card.Brand, Last4: p.Card.Last4}
	}
	if p.Pix != nil {
		response.Pix = &pixResponse{
			CopyPaste:      p.Pix.CopyPaste,
			QRCodeImageURL: p.Pix.QRCodeImageURL,
			ExpiresAt:      p.Pix.ExpiresAt,
		}
	}
	if view.Location != nil {
		response.ReceivingLocation = &locationResponse{
			ID:   view.Location.ID.String(),
			Code: view.Location.Code,
			Name: view.Location.Name,
			Kind: string(view.Location.Kind),
		}
	}
	return response
}
