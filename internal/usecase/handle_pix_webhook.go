package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/renan/payments-api/internal/domain"
	"github.com/renan/payments-api/pkg/clock"
)

type HandlePixWebhookDeps struct {
	Payments       domain.PaymentRepository
	Webhooks       domain.WebhookEventRepository
	Tx             domain.TxManager
	Publisher      EventPublisher
	DashboardCache DashboardCache
	Clock          clock.Clock
	Logger         *slog.Logger
}

// HandlePixWebhook processes asynchronous PIX confirmations.
//
// Idempotency is guaranteed at three levels:
//  1. the inbox table unique constraint (provider, provider_event_id)
//  2. the payment state machine (MarkPaid is a no-op when already paid)
//  3. HTTP replays by the provider return 200 without side effects
type HandlePixWebhook struct {
	payments       domain.PaymentRepository
	webhooks       domain.WebhookEventRepository
	tx             domain.TxManager
	publisher      EventPublisher
	dashboardCache DashboardCache
	clock          clock.Clock
	logger         *slog.Logger
}

func NewHandlePixWebhook(deps HandlePixWebhookDeps) *HandlePixWebhook {
	return &HandlePixWebhook{
		payments:       deps.Payments,
		webhooks:       deps.Webhooks,
		tx:             deps.Tx,
		publisher:      deps.Publisher,
		dashboardCache: deps.DashboardCache,
		clock:          deps.Clock,
		logger:         deps.Logger,
	}
}

func (uc *HandlePixWebhook) Execute(ctx context.Context, n domain.PaymentNotification) (*HandleResult, error) {
	if strings.TrimSpace(n.ProviderPaymentID) == "" {
		return nil, domain.NewValidationError("provider_payment_id", "is required")
	}
	if strings.TrimSpace(n.EventID) == "" {
		return nil, domain.NewValidationError("event_id", "is required")
	}

	now := uc.clock.Now()
	event := &domain.WebhookEvent{
		ID:              uuid.New(),
		Provider:        n.Provider,
		ProviderEventID: n.EventID,
		EventType:       n.EventType,
		Payload:         n.RawPayload,
		Status:          domain.WebhookStatusReceived,
		Attempts:        1,
		ReceivedAt:      now,
	}

	// Inbox claim: inserts on first delivery, re-claims failed attempts and
	// refuses events that were already processed.
	eventID, claimed, err := uc.webhooks.Claim(ctx, event)
	if err != nil {
		return nil, fmt.Errorf("claiming webhook event: %w", err)
	}
	if !claimed {
		uc.logger.InfoContext(ctx, "duplicate webhook event ignored",
			slog.String("provider", string(n.Provider)),
			slog.String("event_id", n.EventID),
		)
		return &HandleResult{Duplicate: true}, nil
	}

	result := &HandleResult{}
	events, err := uc.apply(ctx, eventID, n, now, result)
	if err != nil {
		// The event row stays in "failed" and can be re-claimed on retry.
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if markErr := uc.webhooks.MarkFailed(bg, eventID, truncate(err.Error(), 1000)); markErr != nil {
			uc.logger.ErrorContext(bg, "failed to mark webhook event as failed",
				slog.String("event_id", eventID.String()), slog.String("error", markErr.Error()))
		}
		return nil, err
	}

	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()

	if result.Ignored {
		if err := uc.webhooks.MarkIgnored(markCtx, eventID, result.IgnoreReason, now); err != nil {
			uc.logger.WarnContext(markCtx, "failed to mark webhook event as ignored",
				slog.String("event_id", eventID.String()), slog.String("error", err.Error()))
		}
	} else if err := uc.webhooks.MarkProcessed(markCtx, eventID, now); err != nil {
		uc.logger.WarnContext(markCtx, "failed to mark webhook event as processed",
			slog.String("event_id", eventID.String()), slog.String("error", err.Error()))
	}

	uc.publish(ctx, events)
	if len(events) > 0 {
		uc.invalidateDashboard(ctx)
	}

	if result.Payment != nil {
		uc.logger.InfoContext(ctx, "pix webhook processed",
			slog.String("payment_id", result.Payment.ID.String()),
			slog.String("status", string(result.Payment.Status)),
			slog.String("event_id", n.EventID),
		)
	}
	return result, nil
}

// apply performs the state transition inside a single database transaction.
func (uc *HandlePixWebhook) apply(
	ctx context.Context,
	eventID uuid.UUID,
	n domain.PaymentNotification,
	now time.Time,
	result *HandleResult,
) ([]domain.Event, error) {
	var events []domain.Event

	err := uc.tx.WithinTx(ctx, func(txCtx context.Context) error {
		payment, err := uc.payments.FindByProviderPaymentID(txCtx, n.Provider, n.ProviderPaymentID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				// Unknown payment (another environment, deleted data, ...).
				// Acknowledge the webhook: retrying will never succeed.
				result.Ignored = true
				result.IgnoreReason = "payment not found"
				return nil
			}
			return fmt.Errorf("loading payment by provider id: %w", err)
		}
		result.Payment = payment

		if err := uc.webhooks.LinkPayment(txCtx, eventID, payment.ID); err != nil {
			return fmt.Errorf("linking webhook event to payment: %w", err)
		}

		occurredAt := n.OccurredAt
		if occurredAt.IsZero() || occurredAt.After(now) {
			occurredAt = now
		}

		switch n.Status {
		case domain.NotificationApproved:
			if n.AmountCents != nil && *n.AmountCents != payment.Amount.AmountCents {
				result.Ignored = true
				result.IgnoreReason = "amount mismatch"
				uc.logger.ErrorContext(txCtx, "pix notification amount does not match the payment",
					slog.String("payment_id", payment.ID.String()),
					slog.Int64("expected_cents", payment.Amount.AmountCents),
					slog.Int64("received_cents", *n.AmountCents),
				)
				return nil
			}
			if payment.Status == domain.PaymentStatusPaid {
				return nil // duplicate confirmation, nothing to persist
			}
			if err := payment.MarkPaid(n.ProviderPaymentID, "approved", occurredAt); err != nil {
				return fmt.Errorf("settling payment: %w", err)
			}
			events = append(events,
				paymentEvent(domain.EventPaymentSucceeded, payment, occurredAt),
				paymentEvent(domain.EventPixConfirmed, payment, occurredAt),
			)

		case domain.NotificationRefunded:
			if payment.Status == domain.PaymentStatusRefunded {
				return nil
			}
			if payment.Status != domain.PaymentStatusPaid &&
				payment.Status != domain.PaymentStatusPartiallyRefunded {
				uc.logger.WarnContext(txCtx, "ignoring refund for a payment that was not settled",
					slog.String("payment_id", payment.ID.String()),
					slog.String("status", string(payment.Status)),
				)
				return nil
			}
			amount := payment.Amount
			if n.AmountCents != nil {
				refund, moneyErr := domain.NewMoney(*n.AmountCents, payment.Amount.Currency)
				if moneyErr != nil {
					return fmt.Errorf("invalid refund amount: %w", moneyErr)
				}
				amount = refund
			}
			if err := payment.ApplyRefund(amount, occurredAt); err != nil {
				return fmt.Errorf("applying refund: %w", err)
			}
			events = append(events, paymentEvent(domain.EventPaymentRefunded, payment, occurredAt))

		case domain.NotificationRejected, domain.NotificationCanceled:
			if payment.IsFinal() {
				return nil
			}
			if err := payment.MarkFailed(string(n.Status), string(n.Status), occurredAt); err != nil {
				return fmt.Errorf("failing payment: %w", err)
			}
			events = append(events, paymentEvent(domain.EventPaymentFailed, payment, occurredAt))

		case domain.NotificationExpired:
			if payment.IsFinal() {
				return nil
			}
			if err := payment.MarkExpired(occurredAt); err != nil {
				return fmt.Errorf("expiring payment: %w", err)
			}
			events = append(events, paymentEvent(domain.EventPaymentExpired, payment, occurredAt))

		case domain.NotificationPending, domain.NotificationUnknown:
			// Nothing to settle yet; the provider will send another event.
			return nil
		}

		if err := uc.payments.Update(txCtx, payment); err != nil {
			return fmt.Errorf("updating payment: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

func (uc *HandlePixWebhook) publish(ctx context.Context, events []domain.Event) {
	if len(events) == 0 {
		return
	}
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()

	if err := uc.publisher.Publish(pubCtx, events...); err != nil {
		uc.logger.ErrorContext(pubCtx, "failed to publish webhook domain events",
			slog.String("error", err.Error()),
			slog.Int("event_count", len(events)),
		)
	}
}

func (uc *HandlePixWebhook) invalidateDashboard(ctx context.Context) {
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	if err := uc.dashboardCache.Invalidate(cacheCtx); err != nil {
		uc.logger.WarnContext(cacheCtx, "failed to invalidate dashboard cache", slog.String("error", err.Error()))
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
