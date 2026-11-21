-- 000002_seed_locations.up.sql
BEGIN;

INSERT INTO receiving_locations (code, name, kind, branch_id, city, state, metadata)
VALUES
    ('STORE-A',      'Loja A - Centro',        'store',        'BR-001', 'Sao Paulo',      'SP', '{"terminal":"POS-A1"}'::jsonb),
    ('STORE-B',      'Loja B - Shopping',      'store',        'BR-002', 'Campinas',       'SP', '{"terminal":"POS-B1"}'::jsonb),
    ('ECOMMERCE',    'E-commerce',             'ecommerce',    NULL,     'Sao Paulo',      'SP', '{"channel":"web"}'::jsonb),
    ('POS-TERM-01',  'Terminal Movel 01',      'pos_terminal', 'BR-001', 'Sao Paulo',      'SP', '{"model":"MP-920"}'::jsonb)
ON CONFLICT (code) DO NOTHING;

COMMIT;
