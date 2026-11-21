package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/renan/payments-api/internal/infrastructure/idempotency"
)

// IdempotencyLogRepository persists the audit copy of idempotent requests.
type IdempotencyLogRepository struct {
	pool *pgxpool.Pool
}

func NewIdempotencyLogRepository(pool *pgxpool.Pool) *IdempotencyLogRepository {
	return &IdempotencyLogRepository{pool: pool}
}

// Record upserts the audit row. The body is only stored when it is valid JSON
// and under 64 KiB, keeping the audit table small and queryable.
func (r *IdempotencyLogRepository) Record(ctx context.Context, entry idempotency.AuditEntry) error {
	var body any
	if len(entry.Body) > 0 && len(entry.Body) <= 64*1024 && json.Valid(entry.Body) {
		body = entry.Body
	}

	const query = `
		INSERT INTO idempotency_logs (
			idempotency_key, request_hash, request_method, request_path,
			response_status, response_body, state, expires_at, created_at, completed_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6::jsonb, $7, $8, now(), $9
		)
		ON CONFLICT (idempotency_key) DO UPDATE
			SET request_hash    = EXCLUDED.request_hash,
			    request_method  = EXCLUDED.request_method,
			    request_path    = EXCLUDED.request_path,
			    response_status = EXCLUDED.response_status,
			    response_body   = EXCLUDED.response_body,
			    state           = EXCLUDED.state,
			    expires_at      = EXCLUDED.expires_at,
			    completed_at    = EXCLUDED.completed_at`

	if _, err := querier(ctx, r.pool).Exec(ctx, query,
		entry.Key,
		entry.RequestHash,
		entry.Method,
		entry.Path,
		entry.StatusCode,
		body,
		entry.State,
		entry.ExpiresAt,
		nullTime(entry.CompletedAt),
	); err != nil {
		return fmt.Errorf("recording idempotency audit entry: %w", err)
	}
	return nil
}
