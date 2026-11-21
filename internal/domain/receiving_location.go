package domain

import (
	"time"

	"github.com/google/uuid"
)

// LocationKind classifies where a payment was received. This is the dimension
// used by the dashboard to group volume per store/branch/terminal.
type LocationKind string

const (
	LocationKindStore       LocationKind = "store"
	LocationKindBranch      LocationKind = "branch"
	LocationKindEcommerce   LocationKind = "ecommerce"
	LocationKindPOSTerminal LocationKind = "pos_terminal"
)

func (k LocationKind) IsValid() bool {
	switch k {
	case LocationKindStore, LocationKindBranch, LocationKindEcommerce, LocationKindPOSTerminal:
		return true
	default:
		return false
	}
}

// ReceivingLocation is a point of sale: a physical store, a branch, an
// e-commerce channel or a specific terminal.
type ReceivingLocation struct {
	ID        uuid.UUID      `json:"id"`
	Code      string         `json:"code"`
	Name      string         `json:"name"`
	Kind      LocationKind   `json:"kind"`
	BranchID  string         `json:"branch_id,omitempty"`
	City      string         `json:"city,omitempty"`
	State     string         `json:"state,omitempty"`
	Active    bool           `json:"active"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}
