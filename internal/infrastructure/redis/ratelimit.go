package redis

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// RateLimiter is a distributed fixed-window limiter. A fixed window is chosen
// over a token bucket because it is a single Redis round trip and O(1) memory,
// which is the right trade-off for coarse API protection.
type RateLimiter struct {
	client *goredis.Client
	limit  int
	window time.Duration
	logger *slog.Logger
}

func NewRateLimiter(client *goredis.Client, limit int, window time.Duration, logger *slog.Logger) *RateLimiter {
	return &RateLimiter{client: client, limit: limit, window: window, logger: logger}
}

var incrWindowScript = goredis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return current
`)

// Allow increments the current window and reports whether the caller may
// proceed. It matches the delivery layer's RateLimiter port.
func (l *RateLimiter) Allow(ctx context.Context, key string) (allowed bool, remaining int, resetAfter time.Duration, err error) {
	windowSeconds := int64(l.window / time.Second)
	if windowSeconds < 1 {
		windowSeconds = 1
	}
	bucket := time.Now().UTC().Unix() / windowSeconds
	redisKey := fmt.Sprintf("ratelimit:%s:%d", key, bucket)

	count, err := incrWindowScript.Run(ctx, l.client, []string{redisKey}, l.window.Milliseconds()).Int64()
	if err != nil {
		return false, 0, 0, fmt.Errorf("incrementing rate limit window: %w", err)
	}

	remaining = l.limit - int(count)
	if remaining < 0 {
		remaining = 0
	}
	return int(count) <= l.limit, remaining, time.Until(time.Unix((bucket+1)*windowSeconds, 0)), nil
}
