# Payments API

Production-minded payment processing API in Go: credit card + PIX, idempotent
charges, asynchronous confirmation, domain events on Kafka and a cached
dashboard grouped by receiving location (store / branch / e-commerce / POS
terminal).

## Architecture

```
                    ┌─────────────────────────────────────────────┐
   HTTP clients ───►│ delivery/http  (chi, middleware, DTOs)      │
   PSP webhooks ───►│   auth │ rate limit │ idempotency │ recover  │
                    └────────────────────┬────────────────────────┘
                                         │
                    ┌────────────────────▼────────────────────────┐
                    │ usecase (application)                       │
                    │  ProcessPayment │ HandlePixWebhook │        │
                    │  GetPayment     │ GetDashboardSummary        │
                    │  ports: CardGateway, PixGateway,           │
                    │         EventPublisher, IdempotencyGuard,  │
                    │         DashboardCache, repositories       │
                    └───────┬──────────────┬──────────────┬───────┘
                            │              │              │
              ┌─────────────▼───┐ ┌────────▼──────┐ ┌─────▼──────────┐
              │ infrastructure  │ │ domain        │ │ delivery       │
              │ postgres        │ │ entities +    │ │ adapters depend│
              │ redis           │◄│ ports + state │ │ on domain only │
              │ kafka           │ │ machine       │ │                │
              │ stripe / MP     │ └───────────────┘ └────────────────┘
              └─────────────────┘
```

Dependencies point inwards. `internal/domain` imports only the standard library
and `uuid`; use cases depend on domain ports; every framework, driver and
provider lives in `infrastructure`, and the composition root is `cmd/api`.

## Directory tree

```
.
├── cmd
│   ├── api/main.go                     # composition root + graceful shutdown
│   └── events-consumer/main.go         # Kafka consumer (audit/notification stub)
├── internal
│   ├── config/config.go                # env config + validation
│   ├── domain
│   │   ├── dashboard.go                # read model + DashboardRepository port
│   │   ├── document.go                 # CPF/CNPJ validation
│   │   ├── errors.go                   # sentinel + validation errors
│   │   ├── event.go                    # domain event envelope + payloads
│   │   ├── money.go                    # int64 centavos value object
│   │   ├── payment.go                  # aggregate root + state machine
│   │   ├── receiving_location.go       # store / branch / e-commerce / POS
│   │   ├── webhook.go                  # inbox entity + normalized notification
│   │   ├── *_test.go
│   ├── usecase
│   │   ├── process_payment.go          # charge orchestration
│   │   ├── handle_pix_webhook.go       # inbox claim + settlement
│   │   ├── get_payment.go
│   │   ├── get_dashboard.go            # cache-aside + singleflight
│   │   ├── ports.go                    # driven ports (gateways, events, cache)
│   │   ├── view.go
│   │   └── errors.go
│   ├── infrastructure
│   │   ├── postgres
│   │   │   ├── db.go                   # pool, Querier, TxManager (ctx-scoped tx)
│   │   │   ├── payment_repository.go   # parametrized SQL + optimistic lock
│   │   │   ├── receiving_location_repository.go
│   │   │   ├── webhook_repository.go   # inbox claim (ON CONFLICT ... WHERE)
│   │   │   ├── dashboard_repository.go # GROUPING SETS aggregation
│   │   │   └── idempotency_repository.go
│   │   ├── redis
│   │   │   ├── client.go
│   │   │   ├── idempotency_store.go    # Lua: acquire + complete response
│   │   │   ├── dashboard_cache.go      # epoch-based invalidation + jitter
│   │   │   └── ratelimit.go            # fixed window Lua
│   │   ├── kafka
│   │   │   ├── producer.go             # acks=all, key = aggregate id
│   │   │   └── consumer.go             # manual commits, at-least-once
│   │   ├── gateway
│   │   │   ├── registry.go             # provider registry (swap by wiring)
│   │   │   ├── stripe/client.go        # card → PaymentIntent
│   │   │   ├── mercadopago/client.go   # PIX → /v1/payments + HMAC verify
│   │   │   ├── mercadopago/webhook.go  # parse + fetch authoritative state
│   │   │   └── staticpix/gateway.go    # local BR Code generator (dev only)
│   │   └── idempotency/guard.go        # Redis hot path + Postgres audit
│   └── delivery/http
│       ├── router.go
│       ├── payment_handler.go
│       ├── webhook_handler.go
│       ├── dashboard_handler.go
│       ├── health_handler.go
│       ├── dto.go
│       ├── problem.go                  # error -> RFC 9457 mapping
│       └── middleware
│           ├── auth.go                 # constant-time API key check
│           ├── context.go
│           ├── idempotency.go          # 409 / 422 / byte-for-byte replay
│           ├── logging.go              # request id, access log, recover, timeout
│           └── ratelimit.go
├── migrations
│   ├── 000001_init.up.sql / .down.sql
│   └── 000002_seed_locations.up.sql / .down.sql
├── pkg
│   ├── brcode/                         # EMV PIX payload + CRC16 (tested)
│   ├── clock/                          # injectable clock
│   ├── httpx/                          # JSON, problem+json, strict decoding
│   └── logger/                         # slog setup
├── docker-compose.yml                  # postgres, redis, kafka KRaft, api, consumer
├── Dockerfile
├── Makefile
├── go.mod
└── .env.example
```

## Running locally

```bash
cp .env.example .env       # optional; compose already injects its own env
go mod tidy                 # generates go.sum (required for the Docker build)
docker compose up --build
```

The stack starts Postgres, Redis, Kafka (KRaft, no ZooKeeper), applies
migrations, seeds four receiving locations and starts the API on `:8080` plus
the events consumer.

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz

# grab a seeded location id
docker compose exec postgres psql -U payments -d payments \
  -c "select id, code from receiving_locations order by code;"
```

### 1. Create a PIX charge

```bash
curl -i -X POST http://localhost:8080/v1/payments \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: dev-key' \
  -H 'Idempotency-Key: 7f4c1e0a-9d1f-4b6a-9d0a-5c2f0b8e1a01' \
  -d '{
    "amount_cents": 14990,
    "currency": "BRL",
    "method": "pix",
    "receiving_location_id": "<STORE-A-UUID>",
    "customer": {"name": "Maria Silva", "email": "maria@example.com", "document": "52998224725"},
    "description": "Order #1042"
  }'
```

Response `201` contains `pix.copy_paste` (the BR Code) and `pix.expires_at`.
Replaying the exact request with the same key returns the stored response and
`Idempotency-Replayed: true`; changing the payload returns `422`.

### 2. Confirm the PIX (dev simulation)

With `PIX_PROVIDER=static` and `ALLOW_PIX_SIMULATION=true`:

```bash
curl -X POST http://localhost:8080/v1/webhooks/pix-simulation \
  -H 'Content-Type: application/json' \
  -d '{"provider":"static","provider_payment_id":"static-<PAYMENT-UUID>"}'
```

The use case claims the inbox event, settles the payment, publishes
`payment.succeeded` + `pix.confirmed` and invalidates the dashboard epoch.

### 3. Read the dashboard

```bash
curl 'http://localhost:8080/v1/dashboard/summary?from=2026-01-01&to=2026-12-31' \
  -H 'X-API-Key: dev-key'
```

`X-Cache: HIT|MISS` shows the cache path. Group by a single location with
`&location_id=<uuid>`.

### 4. Credit card (Stripe test mode)

Set `STRIPE_API_KEY=sk_test_...` and send a token created client-side:

```bash
curl -X POST http://localhost:8080/v1/payments \
  -H 'Content-Type: application/json' -H 'X-API-Key: dev-key' \
  -H 'Idempotency-Key: card-charge-00000001' \
  -d '{
    "amount_cents": 5000,
    "method": "credit_card",
    "receiving_location_id": "<ECOMMERCE-UUID>",
    "customer": {"name": "John Doe", "email": "john@example.com"},
    "card": {"token": "tok_visa", "holder_name": "John Doe"}
  }'
```

## Key design decisions

**Idempotency is enforced twice.** Redis holds a short-lived record
(`in_progress` -> `completed`) with a hash of method+path+body; the `payments`
table has a unique `idempotency_key`. If Redis is wiped, the database still
prevents double charges. 5xx responses release the key so retries re-execute;
4xx/2xx responses are replayed verbatim. The Redis adapter is decorated with a
Postgres audit trail (`idempotency_logs`).

**Webhooks use an inbox.** `webhook_events` has a unique
`(provider, provider_event_id)`. The claim is a single
`INSERT ... ON CONFLICT DO UPDATE ... WHERE status IN ('received','failed')`:
duplicates that already succeeded return no row, failed attempts can be
reclaimed. Mercado Pago payloads are never trusted for amounts — the adapter
fetches the authoritative payment from the API and verifies the HMAC signature.

**The payment state machine lives in the domain.** `MarkPaid` is idempotent
(replayed confirmations are no-ops); illegal edges (e.g. `failed -> paid`) are
rejected so a poisoned webhook cannot silently rewrite the ledger. Updates use
optimistic locking (`version`) to avoid lost updates between API and webhook
workers.

**Dashboard is one SQL query.** `GROUPING SETS ((location...), ())` returns the
per-location rows plus the global roll-up in a single round trip; status and
method breakdowns use `FILTER` aggregates. The Redis key embeds a monotonic
epoch: every payment mutation `INCR dashboard:epoch`, invalidating all cached
windows in O(1) without `SCAN`. TTLs are jittered and concurrent misses are
collapsed with `singleflight`.

**Money is `int64` centavos** everywhere (DB `BIGINT`, Go value object). No
floats in the ledger path.

**Provider swapping** is a wiring change: `gateway.Registry` maps
`domain.PaymentProvider` to adapters implementing `usecase.CardGateway` /
`usecase.PixGateway`. Business rules never import Stripe or Mercado Pago.

**Card data never touches the server.** Only single-use tokens are accepted;
the response stores brand + last4 (PCI scope reduction). Customer documents are
stored raw for PIX; production should encrypt the column or store a token
(LGPD).

## Known trade-offs / next steps

- Events are published right after the DB commit, best effort. For guaranteed
  delivery add a transactional outbox (`outbox_messages` + relay) and make
  consumers idempotent.
- Card SCA (`requires_action`) is surfaced as a declined payment; a full
  implementation needs a Stripe webhook for `payment_intent.succeeded`.
- Refunds are modelled and applied from webhooks, but there is no refund
  endpoint yet.
- The API key identifies a tenant, but tables are not tenant-scoped yet; add
  `tenant_id` and row-level security before multi-tenant production use.
- `staticpix` exists only for local development; disable it outside dev.
- Dashboard history beyond the TTL would be better served by a materialized
  view refreshed on a schedule, or a columnar store.

## Tests

```bash
go test -race ./...
```

Covers the money/document validators, the payment state machine, refund rules
and BR Code checksum generation (CRC-16/CCITT-FALSE known vector).
