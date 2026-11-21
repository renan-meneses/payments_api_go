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

// ProcessPaymentConfig holds routing defaults and guard rails.
type ProcessPaymentConfig struct {
	CardProvider   domain.PaymentProvider
	PixProvider    domain.PaymentProvider
	PixExpiration  time.Duration
	MaxAmountCents int64
}

type ProcessPaymentDeps struct {
	Payments       domain.PaymentRepository
	Locations      domain.ReceivingLocationRepository
	CardGateways   CardGatewayRegistry
	PixGateways    PixGatewayRegistry
	Publisher      EventPublisher
	DashboardCache DashboardCache
	Clock          clock.Clock
	Logger         *slog.Logger
	Config         ProcessPaymentConfig
}

// ProcessPayment orchestrates a new charge:
//
//  1. DB-level idempotency check (Redis is the fast path, this is the guarantee)
//  2. validate + load point of sale
//  3. persist a pending payment (claims the idempotency key)
//  4. call the provider adapter (card or PIX)
//  5. persist the resulting state and publish domain events
//
// The provider call happens *after* the local commit on purpose: if the process
// crashes mid-charge we still own a pending record to reconcile.
type ProcessPayment struct {
	payments       domain.PaymentRepository
	locations      domain.ReceivingLocationRepository
	cardGateways   CardGatewayRegistry
	pixGateways    PixGatewayRegistry
	publisher      EventPublisher
	dashboardCache DashboardCache
	clock          clock.Clock
	logger         *slog.Logger
	cfg            ProcessPaymentConfig
}

func NewProcessPayment(deps ProcessPaymentDeps) *ProcessPayment {
	return &ProcessPayment{
		payments:       deps.Payments,
		locations:      deps.Locations,
		cardGateways:   deps.CardGateways,
		pixGateways:    deps.PixGateways,
		publisher:      deps.Publisher,
		dashboardCache: deps.DashboardCache,
		clock:          deps.Clock,
		logger:         deps.Logger,
		cfg:            deps.Config,
	}
}

func (uc *ProcessPayment) Execute(ctx context.Context, in ProcessPaymentInput) (*PaymentView, error) {
	if err := uc.validate(in); err != nil {
		return nil, err
	}

	now := uc.clock.Now()

	// Redis already answered this for the common case, but the database unique
	// constraint is the definitive idempotency barrier (Redis can be flushed).
	if existing, err := uc.payments.FindByIdempotencyKey(ctx, in.IdempotencyKey); err == nil {
		uc.logger.InfoContext(ctx, "replaying existing payment for idempotency key",
			slog.String("payment_id", existing.ID.String()),
			slog.String("idempotency_key", in.IdempotencyKey),
		)
		return uc.view(ctx, existing), nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("checking idempotency key: %w", err)
	}

	location, err := uc.locations.FindByID(ctx, in.ReceivingLocationID)
	if err != nil {
		return nil, fmt.Errorf("loading receiving location: %w", err)
	}
	if !location.Active {
		return nil, fmt.Errorf("%w: receiving location %q is inactive", domain.ErrValidation, location.Code)
	}

	currency := domain.Currency(strings.ToUpper(strings.TrimSpace(in.Currency)))
	if currency == "" {
		currency = domain.CurrencyBRL
	}
	amount, err := domain.NewMoney(in.AmountCents, currency)
	if err != nil {
		return nil, err
	}
	if amount.AmountCents > uc.cfg.MaxAmountCents {
		return nil, domain.NewValidationError("amount", "exceeds the maximum allowed amount")
	}

	provider, err := uc.providerFor(in)
	if err != nil {
		return nil, err
	}

	var expiresAt *time.Time
	if in.Method == domain.PaymentMethodPix {
		expiry := now.Add(uc.cfg.PixExpiration)
		expiresAt = &expiry
	}

	payment, err := domain.NewPayment(domain.NewPaymentParams{
		IdempotencyKey:      in.IdempotencyKey,
		ReceivingLocationID: in.ReceivingLocationID,
		Amount:              amount,
		Method:              in.Method,
		Provider:            provider,
		Customer: domain.Customer{
			Name:     in.Customer.Name,
			Email:    in.Customer.Email,
			Document: in.Customer.Document,
		},
		Metadata:  toAnyMap(in.Metadata),
		ExpiresAt: expiresAt,
	}, now)
	if err != nil {
		return nil, err
	}

	// Persist before charging: this claims the idempotency key at the database
	// level and leaves an auditable trail even if the provider call hangs.
	if err := uc.payments.Create(ctx, payment); err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			existing, findErr := uc.payments.FindByIdempotencyKey(ctx, in.IdempotencyKey)
			if findErr == nil {
				return uc.view(ctx, existing), nil
			}
		}
		return nil, fmt.Errorf("persisting payment: %w", err)
	}

	events := []domain.Event{paymentEvent(domain.EventPaymentCreated, payment, now)}

	var chargeErr error
	switch in.Method {
	case domain.PaymentMethodCreditCard:
		chargeErr = uc.chargeCard(ctx, payment, in, location, &events)
	case domain.PaymentMethodPix:
		chargeErr = uc.createPixCharge(ctx, payment, in, location, &events)
	default:
		chargeErr = fmt.Errorf("%w: %s", ErrUnsupportedMethod, in.Method)
	}

	// Always persist whatever the provider told us, even on decline/transport
	// error, so the local ledger reflects the attempt.
	if err := uc.payments.Update(ctx, payment); err != nil {
		return nil, fmt.Errorf("updating payment after provider call: %w", err)
	}

	uc.publish(ctx, events)
	uc.invalidateDashboard(ctx)

	view := &PaymentView{Payment: payment, Location: location}
	if chargeErr != nil {
		return view, chargeErr
	}
	return view, nil
}

func (uc *ProcessPayment) validate(in ProcessPaymentInput) error {
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return domain.NewValidationError("idempotency_key", "is required")
	}
	if in.AmountCents <= 0 {
		return domain.NewValidationError("amount_cents", "must be greater than zero")
	}
	if !in.Method.IsValid() {
		return fmt.Errorf("%w: %q", ErrUnsupportedMethod, in.Method)
	}
	if in.ReceivingLocationID == uuid.Nil {
		return domain.NewValidationError("receiving_location_id", "is required")
	}
	if strings.TrimSpace(in.Customer.Name) == "" {
		return domain.NewValidationError("customer.name", "is required")
	}
	if in.Method == domain.PaymentMethodCreditCard {
		if in.Card == nil || strings.TrimSpace(in.Card.Token) == "" {
			return domain.NewValidationError("card.token", "a card token is required for credit card payments")
		}
	}
	return nil
}

func (uc *ProcessPayment) providerFor(in ProcessPaymentInput) (domain.PaymentProvider, error) {
	if in.Provider != "" {
		if !in.Provider.IsValid() {
			return "", domain.NewValidationError("provider", "unsupported payment provider")
		}
		return in.Provider, nil
	}
	switch in.Method {
	case domain.PaymentMethodCreditCard:
		return uc.cfg.CardProvider, nil
	case domain.PaymentMethodPix:
		return uc.cfg.PixProvider, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedMethod, in.Method)
	}
}

func (uc *ProcessPayment) chargeCard(
	ctx context.Context,
	payment *domain.Payment,
	in ProcessPaymentInput,
	location *domain.ReceivingLocation,
	events *[]domain.Event,
) error {
	gateway, err := uc.cardGateways.Card(payment.Provider)
	if err != nil {
		uc.failPayment(payment, "provider_not_configured", events)
		return fmt.Errorf("%w: %s", ErrProviderNotConfigured, payment.Provider)
	}

	result, err := gateway.Charge(ctx, CardChargeRequest{
		PaymentID:             payment.ID.String(),
		IdempotencyKey:        payment.ID.String(),
		Amount:                payment.Amount,
		Customer:              payment.Customer,
		Card:                  CardDetails{Token: in.Card.Token, HolderName: in.Card.HolderName},
		Description:           in.Description,
		ReceivingLocationCode: location.Code,
		Metadata:              in.Metadata,
	})
	if err != nil {
		uc.failPayment(payment, "gateway_unavailable", events)
		return fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}

	brand, last4 := result.CardBrand, result.CardLast4
	if brand == "" {
		brand = in.Card.Brand
	}
	if last4 == "" {
		last4 = in.Card.Last4
	}
	payment.AttachCard(domain.CardInfo{Brand: brand, Last4: last4, HolderName: in.Card.HolderName})

	now := uc.clock.Now()
	switch {
	case result.Approved:
		if err := payment.MarkPaid(result.ProviderPaymentID, result.ProviderStatus, now); err != nil {
			return fmt.Errorf("marking payment as paid: %w", err)
		}
		*events = append(*events, paymentEvent(domain.EventPaymentSucceeded, payment, now))
		return nil

	case result.Pending:
		if err := payment.MarkProcessing(result.ProviderPaymentID, result.ProviderStatus, now); err != nil {
			return fmt.Errorf("marking payment as processing: %w", err)
		}
		return nil

	default:
		reason := strings.TrimSpace(result.FailureReason)
		if reason == "" {
			reason = "declined"
		}
		if err := payment.MarkFailed(reason, result.ProviderStatus, now); err != nil {
			return fmt.Errorf("marking payment as failed: %w", err)
		}
		*events = append(*events, paymentEvent(domain.EventPaymentFailed, payment, now))
		return fmt.Errorf("%w: %s", ErrProviderDeclined, reason)
	}
}

func (uc *ProcessPayment) createPixCharge(
	ctx context.Context,
	payment *domain.Payment,
	in ProcessPaymentInput,
	location *domain.ReceivingLocation,
	events *[]domain.Event,
) error {
	gateway, err := uc.pixGateways.Pix(payment.Provider)
	if err != nil {
		uc.failPayment(payment, "provider_not_configured", events)
		return fmt.Errorf("%w: %s", ErrProviderNotConfigured, payment.Provider)
	}

	expiresAt := uc.clock.Now().Add(uc.cfg.PixExpiration)
	if payment.ExpiresAt != nil {
		expiresAt = *payment.ExpiresAt
	}

	result, err := gateway.CreateCharge(ctx, PixChargeRequest{
		PaymentID:             payment.ID.String(),
		IdempotencyKey:        payment.ID.String(),
		Amount:                payment.Amount,
		Customer:              payment.Customer,
		Description:           in.Description,
		ReceivingLocationCode: location.Code,
		ExpiresAt:             expiresAt,
		Metadata:              in.Metadata,
	})
	if err != nil {
		uc.failPayment(payment, "gateway_unavailable", events)
		return fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}

	if err := payment.AttachPixCharge(result.ProviderPaymentID, result.ProviderStatus, domain.PixInfo{
		CopyPaste:      result.CopyPaste,
		QRCodeImageURL: result.QRCodeImageURL,
		ExpiresAt:      result.ExpiresAt,
	}, uc.clock.Now()); err != nil {
		return fmt.Errorf("attaching PIX charge: %w", err)
	}

	// The payment stays pending until the PIX confirmation webhook arrives.
	return nil
}

// failPayment records a local failure and queues the corresponding event.
func (uc *ProcessPayment) failPayment(payment *domain.Payment, reason string, events *[]domain.Event) {
	now := uc.clock.Now()
	if err := payment.MarkFailed(reason, "", now); err != nil {
		// The payment may already be in a terminal state; log and continue.
		uc.logger.Warn("could not mark payment as failed", slog.String("payment_id", payment.ID.String()), slog.String("error", err.Error()))
		return
	}
	*events = append(*events, paymentEvent(domain.EventPaymentFailed, payment, now))
}

func (uc *ProcessPayment) view(ctx context.Context, payment *domain.Payment) *PaymentView {
	location, err := uc.locations.FindByID(ctx, payment.ReceivingLocationID)
	if err != nil {
		uc.logger.WarnContext(ctx, "could not load receiving location for payment",
			slog.String("payment_id", payment.ID.String()),
			slog.String("error", err.Error()),
		)
	}
	return &PaymentView{Payment: payment, Location: location}
}

// publish and invalidateDashboard deliberately use a detached context: after
// the database commit, a client disconnect must not drop domain events.
func (uc *ProcessPayment) publish(ctx context.Context, events []domain.Event) {
	if len(events) == 0 {
		return
	}
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()

	if err := uc.publisher.Publish(pubCtx, events...); err != nil {
		uc.logger.ErrorContext(pubCtx, "failed to publish payment events",
			slog.String("error", err.Error()),
			slog.Int("event_count", len(events)),
		)
	}
}

func (uc *ProcessPayment) invalidateDashboard(ctx context.Context) {
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	if err := uc.dashboardCache.Invalidate(cacheCtx); err != nil {
		uc.logger.WarnContext(cacheCtx, "failed to invalidate dashboard cache", slog.String("error", err.Error()))
	}
}

func paymentEvent(eventType string, p *domain.Payment, at time.Time) domain.Event {
	return domain.NewEvent(eventType, p.ID.String(), domain.NewPaymentEventPayload(p), at, map[string]string{
		"provider": string(p.Provider),
		"method":   string(p.Method),
	})
}

func toAnyMap(in map[string]string) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
