package usecase

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/renan/payments-api/internal/domain"
)

type GetPaymentDeps struct {
	Payments  domain.PaymentRepository
	Locations domain.ReceivingLocationRepository
	Logger    *slog.Logger
}

// GetPayment is a read-only use case: fetch the aggregate and its point of sale.
type GetPayment struct {
	payments  domain.PaymentRepository
	locations domain.ReceivingLocationRepository
	logger    *slog.Logger
}

func NewGetPayment(deps GetPaymentDeps) *GetPayment {
	return &GetPayment{payments: deps.Payments, locations: deps.Locations, logger: deps.Logger}
}

func (uc *GetPayment) Execute(ctx context.Context, id uuid.UUID) (*PaymentView, error) {
	payment, err := uc.payments.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("loading payment %s: %w", id, err)
	}

	var location *domain.ReceivingLocation
	if loc, err := uc.locations.FindByID(ctx, payment.ReceivingLocationID); err != nil {
		// A missing location must not hide the payment itself.
		uc.logger.WarnContext(ctx, "receiving location not found for payment",
			slog.String("payment_id", payment.ID.String()),
			slog.String("receiving_location_id", payment.ReceivingLocationID.String()),
		)
	} else {
		location = loc
	}

	return &PaymentView{Payment: payment, Location: location}, nil
}
