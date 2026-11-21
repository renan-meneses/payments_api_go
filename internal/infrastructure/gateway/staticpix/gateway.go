// Package staticpix is a development/test PIX gateway that generates a valid
// static BR Code locally. It exists so the whole flow (pending payment ->
// webhook -> paid) can be exercised without PSP credentials; never enable it in
// production.
package staticpix

import (
	"context"
	"fmt"
	"strings"

	"github.com/renan/payments-api/internal/domain"
	"github.com/renan/payments-api/internal/usecase"
	"github.com/renan/payments-api/pkg/brcode"
)

type Config struct {
	PixKey       string
	MerchantName string
	MerchantCity string
}

type Gateway struct {
	cfg Config
}

func New(cfg Config) *Gateway {
	return &Gateway{cfg: cfg}
}

func (g *Gateway) Name() domain.PaymentProvider { return domain.ProviderStatic }

func (g *Gateway) CreateCharge(_ context.Context, req usecase.PixChargeRequest) (usecase.PixChargeResult, error) {
	payload, err := brcode.BuildStatic(brcode.StaticInput{
		PixKey:       g.cfg.PixKey,
		MerchantName: g.cfg.MerchantName,
		MerchantCity: g.cfg.MerchantCity,
		AmountCents:  req.Amount.AmountCents,
		TxID:         strings.ReplaceAll(req.PaymentID, "-", ""),
		Description:  req.Description,
	})
	if err != nil {
		return usecase.PixChargeResult{}, fmt.Errorf("building static BR Code: %w", err)
	}

	return usecase.PixChargeResult{
		ProviderPaymentID: "static-" + req.PaymentID,
		ProviderStatus:    "pending",
		CopyPaste:         payload,
		ExpiresAt:         req.ExpiresAt,
	}, nil
}
