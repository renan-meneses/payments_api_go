-- 000002_seed_locations.down.sql
BEGIN;

DELETE FROM receiving_locations
WHERE code IN ('STORE-A', 'STORE-B', 'ECOMMERCE', 'POS-TERM-01');

COMMIT;
