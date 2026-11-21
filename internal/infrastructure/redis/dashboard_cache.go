package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/renan/payments-api/internal/domain"
)

// DashboardCache implements usecase.DashboardCache with epoch-based
// invalidation.
//
// Keys embed a monotonic epoch (dashboard:epoch). Writing a payment INCRs the
// epoch, which logically invalidates every cached summary at once without
// SCAN/KEYS — O(1) invalidation that is safe with multiple API instances.
// Old keys expire naturally via TTL.
type DashboardCache struct {
	client   *goredis.Client
	ttl      time.Duration
	logger   *slog.Logger
	prefix   string
	epochKey string
}

func NewDashboardCache(client *goredis.Client, ttl time.Duration, logger *slog.Logger) *DashboardCache {
	return &DashboardCache{
		client:   client,
		ttl:      ttl,
		logger:   logger,
		prefix:   "dashboard:summary",
		epochKey: "dashboard:epoch",
	}
}

func (c *DashboardCache) Get(ctx context.Context, filter domain.DashboardFilter) (*domain.DashboardSummary, bool, error) {
	key, err := c.buildKey(ctx, filter)
	if err != nil {
		return nil, false, err
	}

	raw, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading dashboard cache: %w", err)
	}

	var summary domain.DashboardSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		// A corrupt entry must not break the endpoint: drop it and fall through.
		c.logger.WarnContext(ctx, "discarding corrupt dashboard cache entry", slog.String("error", err.Error()))
		_ = c.client.Del(ctx, key).Err()
		return nil, false, nil
	}
	return &summary, true, nil
}

func (c *DashboardCache) Put(ctx context.Context, filter domain.DashboardFilter, summary *domain.DashboardSummary) error {
	key, err := c.buildKey(ctx, filter)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("encoding dashboard cache entry: %w", err)
	}

	ttl := c.ttl
	// Jitter (±10%) prevents synchronized expiration of hot keys across pods.
	if c.ttl > 10*time.Second {
		jitter := time.Duration(rand.Int64N(int64(c.ttl / 10)))
		ttl += jitter
	}

	if err := c.client.Set(ctx, key, raw, ttl).Err(); err != nil {
		return fmt.Errorf("writing dashboard cache: %w", err)
	}
	return nil
}

// Invalidate advances the cache epoch. O(1), no key scanning.
func (c *DashboardCache) Invalidate(ctx context.Context) error {
	if err := c.client.Incr(ctx, c.epochKey).Err(); err != nil {
		return fmt.Errorf("advancing dashboard cache epoch: %w", err)
	}
	return nil
}

func (c *DashboardCache) buildKey(ctx context.Context, filter domain.DashboardFilter) (string, error) {
	epoch, err := c.client.Get(ctx, c.epochKey).Result()
	if errors.Is(err, goredis.Nil) {
		epoch = "0"
	} else if err != nil {
		return "", fmt.Errorf("reading dashboard cache epoch: %w", err)
	}

	location := "all"
	if filter.LocationID != nil {
		location = filter.LocationID.String()
	}
	return fmt.Sprintf("%s:v%s:%d:%d:%s:%s",
		c.prefix, epoch, filter.From.Unix(), filter.To.Unix(), location, filter.Currency), nil
}
