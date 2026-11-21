// Package middleware contains the HTTP middleware chain: request id, structured
// logging, panic recovery, authentication, rate limiting and idempotency.
package middleware

import (
	"context"

	"github.com/google/uuid"
)

type contextKey string

const (
	requestIDKey      contextKey = "request_id"
	tenantKey         contextKey = "tenant"
	idempotencyKeyKey contextKey = "idempotency_key"
)

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

func WithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey, tenant)
}

func TenantFromContext(ctx context.Context) string {
	if tenant, ok := ctx.Value(tenantKey).(string); ok {
		return tenant
	}
	return ""
}

func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idempotencyKeyKey, key)
}

func IdempotencyKeyFromContext(ctx context.Context) string {
	if key, ok := ctx.Value(idempotencyKeyKey).(string); ok {
		return key
	}
	return ""
}

func newRequestID() string {
	return uuid.NewString()
}
