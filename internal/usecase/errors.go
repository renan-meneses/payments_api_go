package usecase

import "errors"

// Application-level failures. Delivery adapters map these to transport codes;
// use cases never know about HTTP.
var (
	ErrProviderUnavailable     = errors.New("payment provider unavailable")
	ErrProviderDeclined        = errors.New("payment declined by provider")
	ErrProviderNotConfigured   = errors.New("payment provider not configured")
	ErrUnsupportedMethod       = errors.New("unsupported payment method")
	ErrInvalidWebhookSignature = errors.New("invalid webhook signature")
	ErrUnsupportedWebhook      = errors.New("unsupported webhook notification")
	ErrIdempotencyInProgress   = errors.New("a request with this idempotency key is still in progress")
	ErrIdempotencyKeyReuse     = errors.New("idempotency key was reused with a different payload")
)
