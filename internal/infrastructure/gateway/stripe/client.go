// Package stripe adapts Stripe PaymentIntents to the application's CardGateway
// port. The API is called over plain HTTP to keep the dependency surface small;
// the adapter owns all Stripe-specific knowledge (form encoding, statuses).
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/renan/payments-api/internal/domain"
	"github.com/renan/payments-api/internal/usecase"
)

type Config struct {
	APIKey  string
	BaseURL string
	Timeout time.Duration
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.stripe.com"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

func (c *Client) Name() domain.PaymentProvider { return domain.ProviderStripe }

// paymentIntent is the subset of Stripe's PaymentIntent we consume.
type paymentIntent struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	LastPaymentError *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"last_payment_error"`
	LatestCharge *struct {
		PaymentMethodDetails struct {
			Card struct {
				Brand string `json:"brand"`
				Last4 string `json:"last4"`
			} `json:"card"`
		} `json:"payment_method_details"`
	} `json:"latest_charge"`
}

type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("stripe: %s (status=%d code=%s)", e.Message, e.StatusCode, e.Code)
}

// Charge creates and confirms a PaymentIntent using a client-side token. The
// PaymentID is sent as Stripe's Idempotency-Key so retries cannot double charge.
func (c *Client) Charge(ctx context.Context, req usecase.CardChargeRequest) (usecase.CardChargeResult, error) {
	form := url.Values{}
	form.Set("amount", strconv.FormatInt(req.Amount.AmountCents, 10))
	form.Set("currency", strings.ToLower(string(req.Amount.Currency)))
	form.Set("confirm", "true")
	form.Set("payment_method_data[type]", "card")
	form.Set("payment_method_data[card][token]", req.Card.Token)
	form.Set("metadata[payment_id]", req.PaymentID)
	form.Set("expand[]", "latest_charge")

	if req.Card.HolderName != "" {
		form.Set("payment_method_data[billing_details][name]", req.Card.HolderName)
	}
	if req.Description != "" {
		form.Set("description", req.Description)
	}
	if req.Customer.Email != "" {
		form.Set("receipt_email", req.Customer.Email)
	}
	if req.ReceivingLocationCode != "" {
		form.Set("metadata[receiving_location]", req.ReceivingLocationCode)
	}
	for key, value := range req.Metadata {
		form.Set("metadata["+key+"]", value)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.cfg.BaseURL, "/")+"/v1/payment_intents",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return usecase.CardChargeResult{}, fmt.Errorf("building stripe request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Idempotency-Key", req.IdempotencyKey)

	var intent paymentIntent
	if err := c.do(httpReq, &intent); err != nil {
		return usecase.CardChargeResult{}, err
	}

	result := usecase.CardChargeResult{
		ProviderPaymentID: intent.ID,
		ProviderStatus:    intent.Status,
	}
	if intent.LatestCharge != nil {
		result.CardBrand = intent.LatestCharge.PaymentMethodDetails.Card.Brand
		result.CardLast4 = intent.LatestCharge.PaymentMethodDetails.Card.Last4
	}

	switch intent.Status {
	case "succeeded":
		result.Approved = true
	case "processing":
		result.Pending = true
	default:
		// requires_action (SCA), requires_payment_method, canceled...
		reason := "card_declined"
		if intent.LastPaymentError != nil {
			if intent.LastPaymentError.Code != "" {
				reason = intent.LastPaymentError.Code
			}
			if intent.LastPaymentError.Message != "" {
				// Kept for logs/ops; never exposed verbatim to the payer.
			}
		}
		result.FailureReason = reason
	}

	return result, nil
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling stripe API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading stripe response: %w", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		var apiErr struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &apiErr)
		message := apiErr.Error.Message
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return &APIError{StatusCode: resp.StatusCode, Code: apiErr.Error.Code, Message: message}
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding stripe response: %w", err)
	}
	return nil
}
