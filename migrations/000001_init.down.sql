-- 000001_init.down.sql
BEGIN;

DROP TABLE IF EXISTS idempotency_logs;
DROP TABLE IF EXISTS webhook_events;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS receiving_locations;

DROP FUNCTION IF EXISTS set_updated_at();

DROP TYPE IF EXISTS webhook_status;
DROP TYPE IF EXISTS payment_status;
DROP TYPE IF EXISTS payment_provider;
DROP TYPE IF EXISTS payment_method;
DROP TYPE IF EXISTS location_kind;

COMMIT;
