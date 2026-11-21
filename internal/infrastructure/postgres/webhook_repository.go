package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/renan/payments-api/internal/domain"
)

// WebhookEventRepository implements the inbox pattern on top of the
// webhook_events table.
type WebhookEventRepository struct {
	pool *pgxpool.Pool
}

func NewWebhookEventRepository(pool *pgxpool.Pool) *WebhookEventRepository {
	return &WebhookEventRepository{pool: pool}
}

// Claim inserts the event, or re-claims a previously failed/undelivered one.
//
// The conditional ON CONFLICT ... WHERE is the whole trick: a duplicate event
// that was already processed returns no row (claimed = false), while a retry of
// a failed attempt is allowed to run again.
func (r *WebhookEventRepository) Claim(ctx context.Context, event *domain.WebhookEvent) (uuid.UUID, bool, error) {
	payload := event.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}

	const query = `
		INSERT INTO webhook_events (
			id, provider, provider_event_id, event_type, payload, status, attempts, received_at
		) VALUES (
			$1::uuid, $2::payment_provider, $3, $4, $5::jsonb, 'received', 1, $6
		)
		ON CONFLICT (provider, provider_event_id) DO UPDATE
			SET attempts = webhook_events.attempts + 1,
			    received_at = EXCLUDED.received_at,
			    error = NULL
			WHERE webhook_events.status IN ('received', 'failed')
			  AND webhook_events.attempts < 10
		RETURNING id::text`

	var rawID string
	err := querier(ctx, r.pool).QueryRow(ctx, query,
		event.ID.String(),
		string(event.Provider),
		event.ProviderEventID,
		event.EventType,
		payload,
		event.ReceivedAt,
	).Scan(&rawID)

	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil // already processed / ignored
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("claiming webhook event: %w", err)
	}

	eventID, err := uuid.Parse(rawID)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("parsing webhook event id: %w", err)
	}
	return eventID, true, nil
}

func (r *WebhookEventRepository) LinkPayment(ctx context.Context, eventID, paymentID uuid.UUID) error {
	const query = `UPDATE webhook_events SET payment_id = $2::uuid WHERE id = $1::uuid`
	if _, err := querier(ctx, r.pool).Exec(ctx, query, eventID.String(), paymentID.String()); err != nil {
		return fmt.Errorf("linking webhook event: %w", err)
	}
	return nil
}

func (r *WebhookEventRepository) MarkProcessed(ctx context.Context, eventID uuid.UUID, at time.Time) error {
	const query = `
		UPDATE webhook_events
		SET status = 'processed', processed_at = $2, error = NULL
		WHERE id = $1::uuid`
	if _, err := querier(ctx, r.pool).Exec(ctx, query, eventID.String(), at); err != nil {
		return fmt.Errorf("marking webhook event as processed: %w", err)
	}
	return nil
}

func (r *WebhookEventRepository) MarkIgnored(ctx context.Context, eventID uuid.UUID, reason string, at time.Time) error {
	const query = `
		UPDATE webhook_events
		SET status = 'ignored', processed_at = $2, error = $3
		WHERE id = $1::uuid`
	if _, err := querier(ctx, r.pool).Exec(ctx, query, eventID.String(), at, nullText(reason)); err != nil {
		return fmt.Errorf("marking webhook event as ignored: %w", err)
	}
	return nil
}

func (r *WebhookEventRepository) MarkFailed(ctx context.Context, eventID uuid.UUID, reason string) error {
	const query = `UPDATE webhook_events SET status = 'failed', error = $2 WHERE id = $1::uuid`
	if _, err := querier(ctx, r.pool).Exec(ctx, query, eventID.String(), nullText(reason)); err != nil {
		return fmt.Errorf("marking webhook event as failed: %w", err)
	}
	return nil
}
