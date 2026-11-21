-- 000001_init.up.sql
-- Core schema for the Payments API.
-- Money is stored in minor units (centavos) as BIGINT to avoid float drift.

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ---------------------------------------------------------------------------
-- Enumerations
-- ---------------------------------------------------------------------------
CREATE TYPE location_kind AS ENUM ('store', 'branch', 'ecommerce', 'pos_terminal');

CREATE TYPE payment_method AS ENUM ('credit_card', 'pix');

CREATE TYPE payment_provider AS ENUM ('stripe', 'mercadopago', 'static');

CREATE TYPE payment_status AS ENUM (
    'pending',
    'processing',
    'paid',
    'failed',
    'refunded',
    'partially_refunded',
    'expired',
    'canceled'
);

CREATE TYPE webhook_status AS ENUM ('received', 'processed', 'ignored', 'failed');

-- ---------------------------------------------------------------------------
-- updated_at trigger
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ---------------------------------------------------------------------------
-- receiving_locations: stores, branches, e-commerce and POS terminals.
-- Payments are always attributed to exactly one receiving location so the
-- dashboard can aggregate volume per point of sale.
-- ---------------------------------------------------------------------------
CREATE TABLE receiving_locations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code        text        NOT NULL UNIQUE,
    name        text        NOT NULL,
    kind        location_kind NOT NULL,
    branch_id   text,
    city        text,
    state       char(2),
    active      boolean     NOT NULL DEFAULT true,
    metadata    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX receiving_locations_kind_idx ON receiving_locations (kind) WHERE active;

CREATE TRIGGER receiving_locations_set_updated_at
    BEFORE UPDATE ON receiving_locations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- payments
-- ---------------------------------------------------------------------------
CREATE TABLE payments (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key         text        NOT NULL,
    receiving_location_id   uuid        NOT NULL REFERENCES receiving_locations (id) ON DELETE RESTRICT,

    amount_cents            bigint      NOT NULL CHECK (amount_cents > 0),
    currency                char(3)     NOT NULL DEFAULT 'BRL',
    refunded_amount_cents   bigint      NOT NULL DEFAULT 0
                            CHECK (refunded_amount_cents >= 0 AND refunded_amount_cents <= amount_cents),

    method                  payment_method   NOT NULL,
    provider                payment_provider NOT NULL,
    status                  payment_status   NOT NULL DEFAULT 'pending',

    provider_payment_id     text,
    provider_status         text,

    customer_name           text NOT NULL,
    -- NOTE: for production consider encrypting at rest or storing a hash/token
    -- only (LGPD data minimisation).
    customer_email          text,
    customer_document       text,

    card_brand              text,
    card_last4              char(4),

    pix_qr_code             text,
    pix_copy_paste          text,
    pix_qr_code_image       text,
    pix_expires_at          timestamptz,

    failure_reason          text,
    metadata                jsonb       NOT NULL DEFAULT '{}'::jsonb,

    version                 integer     NOT NULL DEFAULT 1,
    paid_at                 timestamptz,
    expires_at              timestamptz,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT payments_idempotency_key_unique UNIQUE (idempotency_key)
);

-- A gateway payment must map to at most one local payment.
CREATE UNIQUE INDEX payments_provider_payment_id_uidx
    ON payments (provider, provider_payment_id)
    WHERE provider_payment_id IS NOT NULL;

-- Dashboard: "volume per location in a period".
CREATE INDEX payments_location_created_idx
    ON payments (receiving_location_id, created_at DESC);

-- Global dashboard period scans / time series.
CREATE INDEX payments_created_at_idx ON payments (created_at DESC);

-- Status breakdown within a period.
CREATE INDEX payments_status_created_idx ON payments (status, created_at DESC);

-- Expiration sweeper: pending PIX past their deadline.
CREATE INDEX payments_pending_pix_expiration_idx
    ON payments (pix_expires_at)
    WHERE status = 'pending' AND method = 'pix';

-- Arbitrary metadata lookups (merchant order id, terminal firmware, ...).
CREATE INDEX payments_metadata_gin_idx ON payments USING gin (metadata jsonb_path_ops);

CREATE TRIGGER payments_set_updated_at
    BEFORE UPDATE ON payments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- webhook_events: inbox pattern. The unique constraint is the dedupe key,
-- guaranteeing at-most-once *processing* per provider event.
-- ---------------------------------------------------------------------------
CREATE TABLE webhook_events (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider           payment_provider NOT NULL,
    provider_event_id  text             NOT NULL,
    event_type         text             NOT NULL,
    payment_id         uuid REFERENCES payments (id) ON DELETE SET NULL,
    payload            jsonb            NOT NULL,
    headers            jsonb            NOT NULL DEFAULT '{}'::jsonb,
    status             webhook_status   NOT NULL DEFAULT 'received',
    attempts           integer          NOT NULL DEFAULT 1,
    error              text,
    received_at        timestamptz      NOT NULL DEFAULT now(),
    processed_at       timestamptz,

    CONSTRAINT webhook_events_provider_event_unique UNIQUE (provider, provider_event_id)
);

CREATE INDEX webhook_events_retry_idx
    ON webhook_events (status, received_at)
    WHERE status IN ('received', 'failed');

CREATE INDEX webhook_events_payment_idx ON webhook_events (payment_id);

-- ---------------------------------------------------------------------------
-- idempotency_logs: durable audit trail of HTTP idempotency keys. Redis is
-- the hot path; this table is the audit/forensics copy and outlives Redis.
-- ---------------------------------------------------------------------------
CREATE TABLE idempotency_logs (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key  text        NOT NULL,
    request_hash     text        NOT NULL,
    request_method   text        NOT NULL,
    request_path     text        NOT NULL,
    response_status  integer,
    response_body    jsonb,
    resource_id      uuid,
    state            text        NOT NULL CHECK (state IN ('in_progress', 'completed', 'aborted')),
    expires_at       timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    completed_at     timestamptz,

    CONSTRAINT idempotency_logs_key_unique UNIQUE (idempotency_key)
);

CREATE INDEX idempotency_logs_expires_idx ON idempotency_logs (expires_at);

COMMIT;
