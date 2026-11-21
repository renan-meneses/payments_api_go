package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/renan/payments-api/internal/domain"
)

// ReceivingLocationRepository implements the POS catalogue port.
type ReceivingLocationRepository struct {
	pool *pgxpool.Pool
}

func NewReceivingLocationRepository(pool *pgxpool.Pool) *ReceivingLocationRepository {
	return &ReceivingLocationRepository{pool: pool}
}

const locationColumns = `
	id::text,
	code,
	name,
	kind::text,
	COALESCE(branch_id, ''),
	COALESCE(city, ''),
	COALESCE(state, ''),
	active,
	metadata,
	created_at,
	updated_at`

func (r *ReceivingLocationRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ReceivingLocation, error) {
	query := `SELECT ` + locationColumns + ` FROM receiving_locations WHERE id = $1::uuid`

	location, err := scanLocation(querier(ctx, r.pool).QueryRow(ctx, query, id.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("receiving location %s: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("finding receiving location %s: %w", id, err)
	}
	return location, nil
}

func (r *ReceivingLocationRepository) ListActive(ctx context.Context) ([]domain.ReceivingLocation, error) {
	query := `SELECT ` + locationColumns + ` FROM receiving_locations WHERE active ORDER BY name`

	rows, err := querier(ctx, r.pool).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing receiving locations: %w", err)
	}
	defer rows.Close()

	locations := make([]domain.ReceivingLocation, 0, 16)
	for rows.Next() {
		location, err := scanLocation(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning receiving location: %w", err)
		}
		locations = append(locations, *location)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating receiving locations: %w", err)
	}
	return locations, nil
}

// scanLocation accepts both pgx.Row and pgx.Rows (both implement Scan).
func scanLocation(row interface{ Scan(dest ...any) error }) (*domain.ReceivingLocation, error) {
	var (
		location domain.ReceivingLocation
		id, kind string
		metadata []byte
		state    string
	)

	if err := row.Scan(
		&id,
		&location.Code,
		&location.Name,
		&kind,
		&location.BranchID,
		&location.City,
		&state,
		&location.Active,
		&metadata,
		&location.CreatedAt,
		&location.UpdatedAt,
	); err != nil {
		return nil, err
	}

	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("parsing receiving location id: %w", err)
	}
	location.ID = parsed
	location.Kind = domain.LocationKind(kind)
	location.State = strings.TrimSpace(state)

	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &location.Metadata); err != nil {
			return nil, fmt.Errorf("decoding receiving location metadata: %w", err)
		}
	}
	return &location, nil
}
