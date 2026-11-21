package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/renan/payments-api/internal/domain"
)

// DashboardRepository serves the pre-aggregated dashboard read model.
type DashboardRepository struct {
	pool *pgxpool.Pool
}

func NewDashboardRepository(pool *pgxpool.Pool) *DashboardRepository {
	return &DashboardRepository{pool: pool}
}

// dashboardSummaryQuery returns the status breakdown, the method distribution
// and the receiving-location breakdown in a SINGLE round trip.
//
// GROUPING SETS ((location...), ()) produces one row per location plus one
// roll-up row (detected by GROUPING(rl.id) = 1) with global totals — no second
// query, no application-side summation.
//
// Index usage:
//   - payments_location_created_idx serves the filtered + grouped scan when a
//     location is supplied;
//   - payments_created_at_idx serves the global period scan.
const dashboardSummaryQuery = `
WITH filtered AS (
    SELECT p.receiving_location_id,
           p.status::text               AS status,
           p.method::text               AS method,
           p.amount_cents,
           p.refunded_amount_cents
    FROM payments p
    WHERE p.created_at >= $1
      AND p.created_at <  $2
      AND ($3::uuid IS NULL OR p.receiving_location_id = $3::uuid)
)
SELECT
    COALESCE(rl.id::text, '')          AS location_id,
    COALESCE(rl.code, '')              AS location_code,
    COALESCE(rl.name, '')              AS location_name,
    COALESCE(rl.kind::text, '')        AS location_kind,
    COUNT(*)                           AS total_transactions,
    COALESCE(SUM(f.amount_cents) FILTER (
        WHERE f.status IN ('paid', 'partially_refunded', 'refunded')), 0) AS volume_cents,
    COALESCE(SUM(f.amount_cents - f.refunded_amount_cents) FILTER (
        WHERE f.status IN ('paid', 'partially_refunded', 'refunded')), 0) AS net_volume_cents,
    COALESCE(SUM(f.refunded_amount_cents) FILTER (
        WHERE f.status IN ('paid', 'partially_refunded', 'refunded')), 0) AS refunded_cents,
    COUNT(*) FILTER (WHERE f.status = 'pending')    AS pending_count,
    COUNT(*) FILTER (WHERE f.status = 'processing') AS processing_count,
    COUNT(*) FILTER (WHERE f.status = 'paid')       AS paid_count,
    COUNT(*) FILTER (WHERE f.status = 'failed')     AS failed_count,
    COUNT(*) FILTER (WHERE f.status IN ('refunded', 'partially_refunded')) AS refunded_count,
    COUNT(*) FILTER (WHERE f.status = 'expired')    AS expired_count,
    COUNT(*) FILTER (WHERE f.method = 'credit_card') AS credit_card_count,
    COALESCE(SUM(f.amount_cents) FILTER (
        WHERE f.method = 'credit_card'
          AND f.status IN ('paid', 'partially_refunded', 'refunded')), 0) AS credit_card_volume_cents,
    COUNT(*) FILTER (WHERE f.method = 'pix')        AS pix_count,
    COALESCE(SUM(f.amount_cents) FILTER (
        WHERE f.method = 'pix'
          AND f.status IN ('paid', 'partially_refunded', 'refunded')), 0) AS pix_volume_cents
FROM filtered f
JOIN receiving_locations rl ON rl.id = f.receiving_location_id
GROUP BY GROUPING SETS ((rl.id, rl.code, rl.name, rl.kind), ())
ORDER BY GROUPING(rl.id), volume_cents DESC, rl.name`

const dashboardDailyQuery = `
SELECT
    (p.created_at AT TIME ZONE 'America/Sao_Paulo')::date AS day,
    COUNT(*) AS transactions,
    COUNT(*) FILTER (WHERE p.status IN ('paid', 'partially_refunded', 'refunded')) AS settled,
    COALESCE(SUM(p.amount_cents) FILTER (
        WHERE p.status IN ('paid', 'partially_refunded', 'refunded')), 0) AS volume_cents
FROM payments p
WHERE p.created_at >= $1
  AND p.created_at <  $2
  AND ($3::uuid IS NULL OR p.receiving_location_id = $3::uuid)
GROUP BY 1
ORDER BY 1`

func (r *DashboardRepository) Summary(ctx context.Context, filter domain.DashboardFilter) (*domain.DashboardSummary, error) {
	summary := &domain.DashboardSummary{
		Period:     domain.Period{From: filter.From, To: filter.To},
		Currency:   filter.Currency,
		ByLocation: make([]domain.LocationMetrics, 0, 16),
		Daily:      make([]domain.DailyPoint, 0, 32),
	}
	summary.Totals = emptyLocationMetrics(filter.Currency)

	if err := r.loadLocations(ctx, filter, summary); err != nil {
		return nil, err
	}
	if err := r.loadDailySeries(ctx, filter, summary); err != nil {
		return nil, err
	}
	return summary, nil
}

func (r *DashboardRepository) loadLocations(ctx context.Context, filter domain.DashboardFilter, summary *domain.DashboardSummary) error {
	rows, err := querier(ctx, r.pool).Query(ctx, dashboardSummaryQuery, filter.From, filter.To, locationFilter(filter.LocationID))
	if err != nil {
		return fmt.Errorf("querying dashboard summary: %w", err)
	}
	defer rows.Close()

	currency := filter.Currency
	for rows.Next() {
		var (
			locationID, code, name, kind                              string
			transactions, volume, netVolume, refunded                 int64
			pending, processing, paid, failed, refundedCount, expired int64
			cardCount, cardVolume, pixCount, pixVolume                int64
		)

		if err := rows.Scan(
			&locationID, &code, &name, &kind,
			&transactions, &volume, &netVolume, &refunded,
			&pending, &processing, &paid, &failed, &refundedCount, &expired,
			&cardCount, &cardVolume, &pixCount, &pixVolume,
		); err != nil {
			return fmt.Errorf("scanning dashboard row: %w", err)
		}

		metrics := domain.LocationMetrics{
			LocationCode:   code,
			LocationName:   name,
			Kind:           domain.LocationKind(kind),
			Transactions:   transactions,
			Volume:         domain.Money{AmountCents: volume, Currency: currency},
			NetVolume:      domain.Money{AmountCents: netVolume, Currency: currency},
			RefundedVolume: domain.Money{AmountCents: refunded, Currency: currency},
			Status: domain.StatusBreakdown{
				Pending:    pending,
				Processing: processing,
				Paid:       paid,
				Failed:     failed,
				Refunded:   refundedCount,
				Expired:    expired,
			},
			CreditCard: domain.MethodBreakdown{
				Transactions: cardCount,
				Volume:       domain.Money{AmountCents: cardVolume, Currency: currency},
			},
			Pix: domain.MethodBreakdown{
				Transactions: pixCount,
				Volume:       domain.Money{AmountCents: pixVolume, Currency: currency},
			},
			AverageTicket: domain.ZeroMoney(currency),
		}

		if locationID == "" {
			// Roll-up row produced by GROUPING SETS ().
			summary.Totals = metrics
			continue
		}

		id, err := uuid.Parse(locationID)
		if err != nil {
			return fmt.Errorf("parsing dashboard location id: %w", err)
		}
		metrics.LocationID = id
		summary.ByLocation = append(summary.ByLocation, metrics)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating dashboard rows: %w", err)
	}
	return nil
}

func (r *DashboardRepository) loadDailySeries(ctx context.Context, filter domain.DashboardFilter, summary *domain.DashboardSummary) error {
	rows, err := querier(ctx, r.pool).Query(ctx, dashboardDailyQuery, filter.From, filter.To, locationFilter(filter.LocationID))
	if err != nil {
		return fmt.Errorf("querying dashboard daily series: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			day          time.Time
			transactions int64
			settled      int64
			volume       int64
		)

		if err := rows.Scan(&day, &transactions, &settled, &volume); err != nil {
			return fmt.Errorf("scanning dashboard daily row: %w", err)
		}
		summary.Daily = append(summary.Daily, domain.DailyPoint{
			Date:         day,
			Transactions: transactions,
			Settled:      settled,
			Volume:       domain.Money{AmountCents: volume, Currency: filter.Currency},
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating dashboard daily rows: %w", err)
	}
	return nil
}

func emptyLocationMetrics(currency domain.Currency) domain.LocationMetrics {
	zero := domain.ZeroMoney(currency)
	return domain.LocationMetrics{
		Volume:         zero,
		NetVolume:      zero,
		RefundedVolume: zero,
		AverageTicket:  zero,
		CreditCard:     domain.MethodBreakdown{Volume: zero},
		Pix:            domain.MethodBreakdown{Volume: zero},
	}
}

func locationFilter(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}
