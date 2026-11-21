package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/renan/payments-api/internal/domain"
)

// PaymentRepository implements domain.PaymentRepository with hand-written,
// fully parameterized SQL. Every query is explicit about columns (no SELECT *)
// so adding a column cannot silently change scan behavior.
type PaymentRepository struct {
	pool *pgxpool.Pool
}

func NewPaymentRepository(pool *pgxpool.Pool) *PaymentRepository {
	return &PaymentRepository{pool: pool}
}

const paymentColumns = `
	p.id::text,
	p.idempotency_key,
	p.receiving_location_id::text,
	p.amount_cents,
	p.currency,
	p.refunded_amount_cents,
	p.method::text,
	p.provider::text,
	p.status::text,
	COALESCE(p.provider_payment_id, ''),
	COALESCE(p.provider_status, ''),
	p.customer_name,
	COALESCE(p.customer_email, ''),
	COALESCE(p.customer_document, ''),
	COALESCE(p.card_brand, ''),
	COALESCE(p.card_last4, ''),
	COALESCE(p.pix_qr_code, ''),
	COALESCE(p.pix_copy_paste, ''),
	COALESCE(p.pix_qr_code_image, ''),
	p.pix_expires_at,
	COALESCE(p.failure_reason, ''),
	p.metadata,
	p.version,
	p.paid_at,
	p.expires_at,
	p.created_at,
	p.updated_at`

func (r *PaymentRepository) Create(ctx context.Context, payment *domain.Payment) error {
	metadata, err := metadataJSON(payment.Metadata)
	if err != nil {
		return fmt.Errorf("encoding payment metadata: %w", err)
	}

	var pixExpiresAt *time.Time
	var pixQRCode, pixCopyPaste, pixQRCodeImage string
	if payment.Pix != nil {
		pixQRCode = payment.Pix.CopyPaste // BR Code payload
		pixCopyPaste = payment.Pix.CopyPaste
		pixQRCodeImage = payment.Pix.QRCodeImageURL
		if !payment.Pix.ExpiresAt.IsZero() {
			t := payment.Pix.ExpiresAt
			pixExpiresAt = &t
		}
	}

	var cardBrand, cardLast4 string
	if payment.Card != nil {
		cardBrand = payment.Card.Brand
		cardLast4 = payment.Card.Last4
	}

	const query = `
		INSERT INTO payments (
			id, idempotency_key, receiving_location_id,
			amount_cents, currency, method, provider, status,
			provider_payment_id, provider_status,
			customer_name, customer_email, customer_document,
			card_brand, card_last4,
			pix_qr_code, pix_copy_paste, pix_qr_code_image, pix_expires_at,
			failure_reason, metadata, paid_at, expires_at, created_at, updated_at
		) VALUES (
			$1::uuid, $2, $3::uuid,
			$4, $5, $6::payment_method, $7::payment_provider, $8::payment_status,
			$9, $10,
			$11, $12, $13,
			$14, $15,
			$16, $17, $18, $19,
			$20, $21::jsonb, $22, $23, $24, $25
		)`

	_, err = querier(ctx, r.pool).Exec(ctx, query,
		payment.ID.String(),
		payment.IdempotencyKey,
		payment.ReceivingLocationID.String(),
		payment.Amount.AmountCents,
		string(payment.Amount.Currency),
		string(payment.Method),
		string(payment.Provider),
		string(payment.Status),
		nullText(payment.ProviderPaymentID),
		nullText(payment.ProviderStatus),
		payment.Customer.Name,
		nullText(payment.Customer.Email),
		nullText(payment.Customer.Document),
		nullText(cardBrand),
		nullText(cardLast4),
		nullText(pixQRCode),
		nullText(pixCopyPaste),
		nullText(pixQRCodeImage),
		nullTime(pixExpiresAt),
		nullText(payment.FailureReason),
		metadata,
		nullTime(payment.PaidAt),
		nullTime(payment.ExpiresAt),
		payment.CreatedAt,
		payment.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w: payment with this idempotency key already exists", domain.ErrAlreadyExists)
		}
		return fmt.Errorf("inserting payment: %w", err)
	}
	return nil
}

// Update applies the aggregate state with optimistic locking on version.
func (r *PaymentRepository) Update(ctx context.Context, payment *domain.Payment) error {
	metadata, err := metadataJSON(payment.Metadata)
	if err != nil {
		return fmt.Errorf("encoding payment metadata: %w", err)
	}

	var cardBrand, cardLast4 string
	if payment.Card != nil {
		cardBrand = payment.Card.Brand
		cardLast4 = payment.Card.Last4
	}

	var pixQRCode, pixCopyPaste, pixQRCodeImage string
	var pixExpiresAt *time.Time
	if payment.Pix != nil {
		pixQRCode = payment.Pix.CopyPaste
		pixCopyPaste = payment.Pix.CopyPaste
		pixQRCodeImage = payment.Pix.QRCodeImageURL
		if !payment.Pix.ExpiresAt.IsZero() {
			t := payment.Pix.ExpiresAt
			pixExpiresAt = &t
		}
	}

	const query = `
		UPDATE payments SET
			status = $2::payment_status,
			provider_payment_id = $3,
			provider_status = $4,
			card_brand = $5,
			card_last4 = $6,
			pix_qr_code = $7,
			pix_copy_paste = $8,
			pix_qr_code_image = $9,
			pix_expires_at = $10,
			failure_reason = $11,
			metadata = $12::jsonb,
			refunded_amount_cents = $13,
			paid_at = $14,
			expires_at = $15,
			updated_at = $16,
			version = version + 1
		WHERE id = $1::uuid AND version = $17`

	tag, err := querier(ctx, r.pool).Exec(ctx, query,
		payment.ID.String(),
		string(payment.Status),
		nullText(payment.ProviderPaymentID),
		nullText(payment.ProviderStatus),
		nullText(cardBrand),
		nullText(cardLast4),
		nullText(pixQRCode),
		nullText(pixCopyPaste),
		nullText(pixQRCodeImage),
		nullTime(pixExpiresAt),
		nullText(payment.FailureReason),
		metadata,
		payment.RefundedAmount.AmountCents,
		nullTime(payment.PaidAt),
		nullTime(payment.ExpiresAt),
		payment.UpdatedAt,
		payment.Version,
	)
	if err != nil {
		return fmt.Errorf("updating payment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: payment %s was modified concurrently", domain.ErrConflict, payment.ID)
	}
	payment.Version++
	return nil
}

func (r *PaymentRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Payment, error) {
	query := `SELECT ` + paymentColumns + ` FROM payments p WHERE p.id = $1::uuid`
	payment, err := scanPayment(querier(ctx, r.pool).QueryRow(ctx, query, id.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("payment %s: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("finding payment %s: %w", id, err)
	}
	return payment, nil
}

func (r *PaymentRepository) FindByIdempotencyKey(ctx context.Context, key string) (*domain.Payment, error) {
	query := `SELECT ` + paymentColumns + ` FROM payments p WHERE p.idempotency_key = $1`
	payment, err := scanPayment(querier(ctx, r.pool).QueryRow(ctx, query, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("payment with idempotency key %q: %w", key, domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("finding payment by idempotency key: %w", err)
	}
	return payment, nil
}

func (r *PaymentRepository) FindByProviderPaymentID(ctx context.Context, provider domain.PaymentProvider, providerPaymentID string) (*domain.Payment, error) {
	query := `SELECT ` + paymentColumns + `
		FROM payments p
		WHERE p.provider = $1::payment_provider AND p.provider_payment_id = $2`
	payment, err := scanPayment(querier(ctx, r.pool).QueryRow(ctx, query, string(provider), providerPaymentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("payment %s/%s: %w", provider, providerPaymentID, domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("finding payment by provider id: %w", err)
	}
	return payment, nil
}

func scanPayment(row pgx.Row) (*domain.Payment, error) {
	var (
		p domain.Payment

		id, locationID               string
		currency                     string
		method, provider, status     string
		customerName                 string
		customerEmail                string
		customerDocument             string
		cardBrand, cardLast4         string
		pixQRCode, pixCopyPaste      string
		pixQRCodeImage               string
		providerPaymentID            string
		providerStatus               string
		failureReason                string
		metadata                     []byte
		pixExpiresAt, paidAt, expiry pgtype.Timestamptz
	)

	err := row.Scan(
		&id,
		&p.IdempotencyKey,
		&locationID,
		&p.Amount.AmountCents,
		&currency,
		&p.RefundedAmount.AmountCents,
		&method,
		&provider,
		&status,
		&providerPaymentID,
		&providerStatus,
		&customerName,
		&customerEmail,
		&customerDocument,
		&cardBrand,
		&cardLast4,
		&pixQRCode,
		&pixCopyPaste,
		&pixQRCodeImage,
		&pixExpiresAt,
		&failureReason,
		&metadata,
		&p.Version,
		&paidAt,
		&expiry,
		&p.CreatedAt,
		&p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	p.ID, err = uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("parsing payment id: %w", err)
	}
	p.ReceivingLocationID, err = uuid.Parse(locationID)
	if err != nil {
		return nil, fmt.Errorf("parsing receiving location id: %w", err)
	}

	p.Amount.Currency = domain.Currency(currency)
	p.RefundedAmount.Currency = domain.Currency(currency)
	p.Method = domain.PaymentMethod(method)
	p.Provider = domain.PaymentProvider(provider)
	p.Status = domain.PaymentStatus(status)
	p.ProviderPaymentID = providerPaymentID
	p.ProviderStatus = providerStatus
	p.Customer = domain.Customer{Name: customerName, Email: customerEmail, Document: customerDocument}
	p.FailureReason = failureReason

	if cardBrand != "" || cardLast4 != "" {
		p.Card = &domain.CardInfo{Brand: cardBrand, Last4: strings.TrimSpace(cardLast4)}
	}
	if pixQRCode != "" || pixCopyPaste != "" || pixQRCodeImage != "" || pixExpiresAt.Valid {
		info := &domain.PixInfo{CopyPaste: pixCopyPaste, QRCodeImageURL: pixQRCodeImage}
		if info.CopyPaste == "" {
			info.CopyPaste = pixQRCode
		}
		if pixExpiresAt.Valid {
			info.ExpiresAt = pixExpiresAt.Time
		}
		p.Pix = info
	}
	if paidAt.Valid {
		t := paidAt.Time
		p.PaidAt = &t
	}
	if expiry.Valid {
		t := expiry.Time
		p.ExpiresAt = &t
	}

	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &p.Metadata); err != nil {
			return nil, fmt.Errorf("decoding payment metadata: %w", err)
		}
	}
	if p.Metadata == nil {
		p.Metadata = map[string]any{}
	}
	return &p, nil
}

func metadataJSON(metadata map[string]any) ([]byte, error) {
	if metadata == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(metadata)
}

// nullText maps an empty string to SQL NULL. Using `any` keeps pgx encoding on
// its well-trodden text path (pgtype.Text works too, but this is simpler).
func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}
