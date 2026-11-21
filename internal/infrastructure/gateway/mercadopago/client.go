// Package mercadopago adapts Mercado Pago's PIX API to the application's
// PixGateway port, and verifies/parses its webhooks.
package mercadopago

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	AccessToken     string
	BaseURL         string
	WebhookSecret   string
	NotificationURL string
	Timeout         time.Duration
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.mercadopago.com"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

func (c *Client) Name() domain.PaymentProvider { return domain.ProviderMercadoPago }

type identification struct {
	Type   string `json:"type"`
	Number string `json:"number"`
}

type payer struct {
	Email          string          `json:"email"`
	FirstName      string          `json:"first_name,omitempty"`
	LastName       string          `json:"last_name,omitempty"`
	Identification *identification `json:"identification,omitempty"`
}

type createPaymentRequest struct {
	TransactionAmount json.Number       `json:"transaction_amount"`
	Description       string            `json:"description"`
	PaymentMethodID   string            `json:"payment_method_id"`
	ExternalReference string            `json:"external_reference"`
	NotificationURL   string            `json:"notification_url,omitempty"`
	DateOfExpiration  string            `json:"date_of_expiration,omitempty"`
	Payer             payer             `json:"payer"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}

// payment is the subset of the Mercado Pago payment resource we need.
type payment struct {
	ID                 int64      `json:"id"`
	Status             string     `json:"status"`
	StatusDetail       string     `json:"status_detail"`
	ExternalReference  string     `json:"external_reference"`
	TransactionAmount  float64    `json:"transaction_amount"`
	DateApproved       *time.Time `json:"date_approved"`
	DateLastUpdated    *time.Time `json:"date_last_updated"`
	PointOfInteraction struct {
		TransactionData struct {
			QRCode       string `json:"qr_code"`
			QRCodeBase64 string `json:"qr_code_base64"`
			TicketURL    string `json:"ticket_url"`
		} `json:"transaction_data"`
	} `json:"point_of_interaction"`
}

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("mercadopago: %s (status=%d)", e.Message, e.StatusCode)
}

// CreateCharge creates an immediate PIX charge ("cobrança imediata"). The
// returned qr_code is the BR Code copy-and-paste payload.
func (c *Client) CreateCharge(ctx context.Context, req usecase.PixChargeRequest) (usecase.PixChargeResult, error) {
	firstName, lastName := splitName(req.Customer.Name)

	body := createPaymentRequest{
		TransactionAmount: json.Number(formatAmount(req.Amount.AmountCents)),
		Description:       req.Description,
		PaymentMethodID:   "pix",
		ExternalReference: req.PaymentID,
		NotificationURL:   c.cfg.NotificationURL,
		DateOfExpiration:  req.ExpiresAt.In(time.FixedZone("BRT", -3*60*60)).Format("2006-01-02T15:04:05.000-07:00"),
		Payer: payer{
			Email:     payerEmail(req.Customer),
			FirstName: firstName,
			LastName:  lastName,
		},
		Metadata: map[string]string{
			"payment_id":         req.PaymentID,
			"receiving_location": req.ReceivingLocationCode,
		},
	}
	if req.Customer.Document != "" {
		body.Payer.Identification = &identification{Type: "CPF", Number: req.Customer.Document}
		if len(req.Customer.Document) == 14 {
			body.Payer.Identification.Type = "CNPJ"
		}
	}
	if req.Description == "" {
		body.Description = "PIX payment"
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return usecase.PixChargeResult{}, fmt.Errorf("encoding mercadopago request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.cfg.BaseURL, "/")+"/v1/payments",
		bytes.NewReader(encoded),
	)
	if err != nil {
		return usecase.PixChargeResult{}, fmt.Errorf("building mercadopago request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Idempotency-Key", req.IdempotencyKey)

	var result payment
	if err := c.do(httpReq, &result); err != nil {
		return usecase.PixChargeResult{}, err
	}

	pix := usecase.PixChargeResult{
		ProviderPaymentID: strconv.FormatInt(result.ID, 10),
		ProviderStatus:    result.Status,
		CopyPaste:         result.PointOfInteraction.TransactionData.QRCode,
		ExpiresAt:         req.ExpiresAt,
	}
	if base64 := result.PointOfInteraction.TransactionData.QRCodeBase64; base64 != "" {
		pix.QRCodeImageURL = "data:image/png;base64," + base64
	}
	return pix, nil
}

// GetPayment fetches the authoritative payment state (webhook payloads only
// carry an id, never the amount/status).
func (c *Client) GetPayment(ctx context.Context, id string) (*payment, error) {
	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/v1/payments/" + url.PathEscape(id)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("building mercadopago request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)

	var result payment
	if err := c.do(httpReq, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// VerifySignature validates the x-signature header using the documented
// manifest: "id:<data.id>;request-id:<x-request-id>;ts:<ts>;".
// An empty secret disables verification (development only).
func (c *Client) VerifySignature(signatureHeader, requestID, dataID string) error {
	if c.cfg.WebhookSecret == "" {
		return nil
	}

	var ts, v1 string
	for _, part := range strings.Split(signatureHeader, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "ts":
			ts = value
		case "v1":
			v1 = value
		}
	}
	if ts == "" || v1 == "" {
		return fmt.Errorf("malformed x-signature header")
	}

	manifest := fmt.Sprintf("id:%s;request-id:%s;ts:%s;", strings.ToLower(dataID), requestID, ts)
	mac := hmac.New(sha256.New, []byte(c.cfg.WebhookSecret))
	mac.Write([]byte(manifest))
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(v1)) {
		return fmt.Errorf("signature mismatch")
	}

	unixSeconds, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid signature timestamp")
	}
	if time.Since(time.Unix(unixSeconds, 0)).Abs() > 10*time.Minute {
		return fmt.Errorf("signature timestamp outside the tolerance window")
	}
	return nil
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling mercadopago API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading mercadopago response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		var apiErr struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(body, &apiErr)
		message := apiErr.Message
		if message == "" {
			message = apiErr.Error
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return &APIError{StatusCode: resp.StatusCode, Message: message}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding mercadopago response: %w", err)
	}
	return nil
}

func mapStatus(status string) domain.NotificationStatus {
	switch status {
	case "approved":
		return domain.NotificationApproved
	case "pending", "in_process", "authorized":
		return domain.NotificationPending
	case "rejected":
		return domain.NotificationRejected
	case "refunded", "charged_back":
		return domain.NotificationRefunded
	case "cancelled", "canceled":
		return domain.NotificationCanceled
	case "expired":
		return domain.NotificationExpired
	default:
		return domain.NotificationUnknown
	}
}

func formatAmount(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func payerEmail(customer domain.Customer) string {
	if customer.Email != "" {
		return customer.Email
	}
	return fmt.Sprintf("payer+%s@example.com", customer.Document)
}

func splitName(full string) (string, string) {
	parts := strings.Fields(strings.TrimSpace(full))
	switch len(parts) {
	case 0:
		return "Payer", ""
	case 1:
		return parts[0], ""
	default:
		return parts[0], strings.Join(parts[1:], " ")
	}
}
