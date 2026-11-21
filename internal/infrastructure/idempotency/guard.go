// Package idempotency decorates the Redis idempotency store with a durable
// Postgres audit trail. The Redis key is short-lived (TTL) while the audit row
// survives for reconciliation and forensics.
package idempotency

import (
	"context"
	"log/slog"
	"time"

	"github.com/renan/payments-api/internal/usecase"
)

// AuditEntry is one completed (or aborted) idempotent request.
type AuditEntry struct {
	Key         string
	RequestHash string
	Method      string
	Path        string
	StatusCode  int
	Body        []byte
	State       string
	ExpiresAt   time.Time
	CompletedAt *time.Time
}

// AuditRepository persists audit entries.
type AuditRepository interface {
	Record(ctx context.Context, entry AuditEntry) error
}

// Guard implements usecase.IdempotencyGuard.
type Guard struct {
	store  usecase.IdempotencyGuard
	audit  AuditRepository
	ttl    time.Duration
	logger *slog.Logger
}

func NewGuard(store usecase.IdempotencyGuard, audit AuditRepository, ttl time.Duration, logger *slog.Logger) *Guard {
	return &Guard{store: store, audit: audit, ttl: ttl, logger: logger}
}

func (g *Guard) Begin(ctx context.Context, key, requestHash string) (usecase.IdempotencyRecord, error) {
	return g.store.Begin(ctx, key, requestHash)
}

func (g *Guard) Complete(ctx context.Context, completion usecase.IdempotencyCompletion) error {
	storeErr := g.store.Complete(ctx, completion)

	// The audit write must not be cancelled by the client disconnecting and
	// must never break the request: idempotency already succeeded in Redis.
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()

	now := time.Now().UTC()
	entry := AuditEntry{
		Key:         completion.Key,
		RequestHash: completion.RequestHash,
		Method:      completion.Method,
		Path:        completion.Path,
		StatusCode:  completion.StatusCode,
		Body:        completion.Body,
		State:       "completed",
		ExpiresAt:   now.Add(g.ttl),
		CompletedAt: &now,
	}
	if err := g.audit.Record(bg, entry); err != nil {
		g.logger.WarnContext(bg, "failed to record idempotency audit entry",
			slog.String("idempotency_key", completion.Key),
			slog.String("error", err.Error()),
		)
	}
	return storeErr
}

// Abort releases the key so the client can retry after a 5xx.
func (g *Guard) Abort(ctx context.Context, key string) error {
	return g.store.Abort(ctx, key)
}
