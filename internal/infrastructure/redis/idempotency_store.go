package redis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/renan/payments-api/internal/usecase"
)

// IdempotencyStore implements usecase.IdempotencyGuard with a two-phase Redis
// record:
//
//	BEGIN    SET key {state:in_progress, request_hash} PX lockTTL   (NX semantics)
//	COMPLETE SET key {state:completed, response...}     PX ttl
//
// The lock TTL is short (45s by default): if the process crashes mid-request
// the key self-heals and the client can retry instead of being blocked forever.
type IdempotencyStore struct {
	client  *goredis.Client
	lockTTL time.Duration
	ttl     time.Duration
}

func NewIdempotencyStore(client *goredis.Client, lockTTL, ttl time.Duration) *IdempotencyStore {
	return &IdempotencyStore{client: client, lockTTL: lockTTL, ttl: ttl}
}

// beginScript atomically returns the existing record or claims the key.
var beginScript = goredis.NewScript(`
local existing = redis.call('GET', KEYS[1])
if existing then
  return {0, existing}
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return {1, ARGV[1]}
`)

// completeScript avoids resurrecting an expired/aborted key.
var completeScript = goredis.NewScript(`
local existing = redis.call('GET', KEYS[1])
if not existing then
  return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return 1
`)

type storedRecord struct {
	State       string    `json:"state"`
	RequestHash string    `json:"request_hash"`
	StatusCode  int       `json:"status_code,omitempty"`
	Body        string    `json:"body,omitempty"` // base64 so JSON stays valid
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

func (s *IdempotencyStore) Begin(ctx context.Context, key, requestHash string) (usecase.IdempotencyRecord, error) {
	initial, err := json.Marshal(storedRecord{State: "in_progress", RequestHash: requestHash})
	if err != nil {
		return usecase.IdempotencyRecord{}, fmt.Errorf("encoding idempotency record: %w", err)
	}

	result, err := beginScript.Run(ctx, s.client, []string{idempotencyKey(key)},
		initial, s.lockTTL.Milliseconds()).Result()
	if err != nil {
		return usecase.IdempotencyRecord{}, fmt.Errorf("acquiring idempotency key: %w", err)
	}

	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return usecase.IdempotencyRecord{}, fmt.Errorf("unexpected redis reply while acquiring idempotency key")
	}
	acquired, _ := values[0].(int64)
	raw, _ := values[1].(string)

	var record storedRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return usecase.IdempotencyRecord{}, fmt.Errorf("decoding idempotency record: %w", err)
	}

	out := usecase.IdempotencyRecord{
		RequestHash: record.RequestHash,
		StatusCode:  record.StatusCode,
		CompletedAt: record.CompletedAt,
	}
	if record.Body != "" {
		body, err := base64.StdEncoding.DecodeString(record.Body)
		if err != nil {
			return usecase.IdempotencyRecord{}, fmt.Errorf("decoding idempotent response body: %w", err)
		}
		out.Body = body
	}

	switch {
	case acquired == 1:
		out.Outcome = usecase.IdempotencyAcquired
	case record.State == "completed":
		out.Outcome = usecase.IdempotencyReplay
	default:
		out.Outcome = usecase.IdempotencyInFlight
	}
	return out, nil
}

func (s *IdempotencyStore) Complete(ctx context.Context, completion usecase.IdempotencyCompletion) error {
	encoded, err := json.Marshal(storedRecord{
		State:       "completed",
		RequestHash: completion.RequestHash,
		StatusCode:  completion.StatusCode,
		Body:        base64.StdEncoding.EncodeToString(completion.Body),
		CompletedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("encoding idempotent response: %w", err)
	}

	if _, err := completeScript.Run(ctx, s.client, []string{idempotencyKey(completion.Key)},
		encoded, s.ttl.Milliseconds()).Result(); err != nil {
		return fmt.Errorf("storing idempotent response: %w", err)
	}
	return nil
}

func (s *IdempotencyStore) Abort(ctx context.Context, key string) error {
	if err := s.client.Del(ctx, idempotencyKey(key)).Err(); err != nil && !errors.Is(err, goredis.Nil) {
		return fmt.Errorf("releasing idempotency key: %w", err)
	}
	return nil
}

func idempotencyKey(key string) string {
	return "idem:req:" + key
}
