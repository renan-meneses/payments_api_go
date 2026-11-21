// Package gateway wires payment provider adapters behind the application's
// driven ports. The registry is the only place that knows which concrete
// providers exist, so adding a gateway is a local change here + wiring in main.
package gateway

import (
	"fmt"

	"github.com/renan/payments-api/internal/domain"
	"github.com/renan/payments-api/internal/usecase"
)

// Registry implements usecase.CardGatewayRegistry and PixGatewayRegistry.
type Registry struct {
	card map[domain.PaymentProvider]usecase.CardGateway
	pix  map[domain.PaymentProvider]usecase.PixGateway
}

func NewRegistry() *Registry {
	return &Registry{
		card: make(map[domain.PaymentProvider]usecase.CardGateway),
		pix:  make(map[domain.PaymentProvider]usecase.PixGateway),
	}
}

func (r *Registry) RegisterCard(gateway usecase.CardGateway) {
	r.card[gateway.Name()] = gateway
}

func (r *Registry) RegisterPix(gateway usecase.PixGateway) {
	r.pix[gateway.Name()] = gateway
}

func (r *Registry) Card(provider domain.PaymentProvider) (usecase.CardGateway, error) {
	gateway, ok := r.card[provider]
	if !ok {
		return nil, fmt.Errorf("%w: card provider %q", usecase.ErrProviderNotConfigured, provider)
	}
	return gateway, nil
}

func (r *Registry) Pix(provider domain.PaymentProvider) (usecase.PixGateway, error) {
	gateway, ok := r.pix[provider]
	if !ok {
		return nil, fmt.Errorf("%w: pix provider %q", usecase.ErrProviderNotConfigured, provider)
	}
	return gateway, nil
}
