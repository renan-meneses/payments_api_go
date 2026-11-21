package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/renan/payments-api/internal/domain"
	"github.com/renan/payments-api/pkg/clock"
)

type DashboardConfig struct {
	CacheTTL time.Duration
	MaxRange time.Duration
}

type GetDashboardSummaryDeps struct {
	Repository domain.DashboardRepository
	Cache      DashboardCache
	Clock      clock.Clock
	Logger     *slog.Logger
	Config     DashboardConfig
}

// GetDashboardSummary serves the frontend dashboard read model with a
// cache-aside strategy:
//
//	Redis hit  -> return (fast path, ~1ms)
//	Redis miss -> singleflight query -> PostgreSQL -> cache -> return
//
// singleflight collapses concurrent misses into one database scan, preventing
// the classic cache stampede when a popular key expires.
type GetDashboardSummary struct {
	repository domain.DashboardRepository
	cache      DashboardCache
	clock      clock.Clock
	logger     *slog.Logger
	cfg        DashboardConfig
	group      singleflight.Group
}

func NewGetDashboardSummary(deps GetDashboardSummaryDeps) *GetDashboardSummary {
	return &GetDashboardSummary{
		repository: deps.Repository,
		cache:      deps.Cache,
		clock:      deps.Clock,
		logger:     deps.Logger,
		cfg:        deps.Config,
	}
}

func (uc *GetDashboardSummary) Execute(ctx context.Context, in DashboardInput) (*DashboardResult, error) {
	filter, err := uc.buildFilter(in)
	if err != nil {
		return nil, err
	}

	if summary, found, err := uc.cache.Get(ctx, filter); err != nil {
		uc.logger.WarnContext(ctx, "dashboard cache read failed", slog.String("error", err.Error()))
	} else if found && summary != nil {
		return &DashboardResult{Summary: summary, Cached: true}, nil
	}

	key := cacheKey(filter)
	ch := uc.group.DoChan(key, func() (any, error) {
		// Detached from the caller cancellation: if the first client gives up,
		// other waiters (and the cache) still benefit from the query.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return uc.load(loadCtx, filter)
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return &DashboardResult{Summary: res.Val.(*domain.DashboardSummary)}, nil
	}
}

func (uc *GetDashboardSummary) load(ctx context.Context, filter domain.DashboardFilter) (*domain.DashboardSummary, error) {
	summary, err := uc.repository.Summary(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("querying dashboard summary: %w", err)
	}
	summary.GeneratedAt = uc.clock.Now()
	computeDerivedMetrics(summary)

	if err := uc.cache.Put(ctx, filter, summary); err != nil {
		uc.logger.WarnContext(ctx, "dashboard cache write failed", slog.String("error", err.Error()))
	}
	return summary, nil
}

func (uc *GetDashboardSummary) buildFilter(in DashboardInput) (domain.DashboardFilter, error) {
	now := uc.clock.Now().UTC()

	var to time.Time
	if in.To != nil {
		to = in.To.UTC().Truncate(time.Second)
	} else {
		// Round up to the next minute: without this, the cache key would change
		// on every request and the cache would never be hit.
		to = now.Truncate(time.Minute).Add(time.Minute)
	}

	var from time.Time
	if in.From != nil {
		from = in.From.UTC().Truncate(time.Second)
	} else {
		from = to.Add(-30 * 24 * time.Hour)
	}

	if !to.After(from) {
		return domain.DashboardFilter{}, domain.NewValidationError("to", "must be after from")
	}
	if to.Sub(from) > uc.cfg.MaxRange {
		return domain.DashboardFilter{}, domain.NewValidationError("from", "range exceeds the maximum allowed window")
	}

	return domain.DashboardFilter{
		From:       from,
		To:         to,
		LocationID: in.LocationID,
		Currency:   domain.CurrencyBRL,
	}, nil
}

// computeDerivedMetrics keeps business math in the application layer, where it
// can be unit tested, instead of burying it in SQL.
func computeDerivedMetrics(s *domain.DashboardSummary) {
	apply := func(m *domain.LocationMetrics) {
		settled := m.Status.Succeeded()
		if settled > 0 && m.Volume.AmountCents > 0 {
			m.AverageTicket = domain.Money{AmountCents: m.Volume.AmountCents / settled, Currency: m.Volume.Currency}
		}
		if m.Transactions > 0 {
			m.ApprovalRate = float64(settled) / float64(m.Transactions)
		}
		if m.Volume.AmountCents > 0 {
			m.CreditCard.Share = float64(m.CreditCard.Volume.AmountCents) / float64(m.Volume.AmountCents)
			m.Pix.Share = float64(m.Pix.Volume.AmountCents) / float64(m.Volume.AmountCents)
		}
	}
	apply(&s.Totals)
	for i := range s.ByLocation {
		apply(&s.ByLocation[i])
	}
}

func cacheKey(filter domain.DashboardFilter) string {
	location := "all"
	if filter.LocationID != nil {
		location = filter.LocationID.String()
	}
	return fmt.Sprintf("%d|%d|%s", filter.From.Unix(), filter.To.Unix(), location)
}
