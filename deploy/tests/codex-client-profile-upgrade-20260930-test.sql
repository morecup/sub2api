-- Run with psql -X -v ON_ERROR_STOP=1 -f <this file> against a test database.
-- Temporary tables shadow application tables; the transaction always rolls back.
\set ON_ERROR_STOP on
BEGIN;
CREATE TEMP TABLE accounts (id bigint PRIMARY KEY, platform text, type text, extra jsonb);
CREATE TEMP TABLE scheduler_outbox (event_type text, account_id bigint);
INSERT INTO accounts
SELECT i, 'openai', 'oauth', jsonb_build_object('unrelated', 'preserve',
    'openai_codex_client_profile', jsonb_build_object(
        'schema_version', 1, 'template_id', 'windows-v2-' || lpad(i::text, 2, '0'),
        'codex_version', '0.155.0-alpha.16', 'app_version', '26.917.51856',
        'browser_version', '153.0.0.0', 'otel_sdk_version', '0.31.0',
        'installation_id', 'installation-' || i, 'device_id', 'device-' || i,
        'locale', 'en-US', 'timezone', 'Asia/Shanghai', 'screen_scale', 1.25,
        'screen_size_sum', 4000, 'os_version', '10.0.26200'))
FROM generate_series(0, 15) AS i;
INSERT INTO accounts SELECT id + 100, platform, type, extra FROM accounts WHERE id < 8;
UPDATE accounts SET extra = jsonb_set(extra, '{openai_codex_client_profile,template_id}', '"custom"') WHERE id = 100;
UPDATE accounts SET extra = jsonb_set(extra, '{openai_codex_client_profile,codex_version}', '"99.0.0"') WHERE id = 101;
UPDATE accounts SET extra = jsonb_set(extra, '{openai_codex_client_profile,app_version}', '"99.0.0"') WHERE id = 102;
UPDATE accounts SET type = 'apikey' WHERE id = 103;
UPDATE accounts SET platform = 'anthropic' WHERE id = 104;
UPDATE accounts SET extra = jsonb_set(extra, '{openai_codex_client_profile,browser_version}', '"999.0.0"') WHERE id = 105;
UPDATE accounts SET extra = jsonb_set(extra, '{openai_codex_client_profile,otel_sdk_version}', '"99.0.0"') WHERE id = 106;
UPDATE accounts SET extra = jsonb_set(extra, '{openai_codex_client_profile,template_id}', '"windows-v2-16"') WHERE id = 107;
INSERT INTO accounts SELECT 200, platform, type,
    jsonb_set(extra, '{openai_codex_client_profile,template_id}', '"capture-20260923"')
FROM accounts WHERE id = 0;
CREATE TEMP TABLE original_accounts AS TABLE accounts;
\ir ../../backend/migrations/241_upgrade_codex_capture_20260930.sql
DO $$
DECLARE a record; expected jsonb;
BEGIN
    IF (SELECT count(*) FROM scheduler_outbox) <> 17 THEN RAISE EXCEPTION 'wrong invalidation count'; END IF;
    FOR a IN SELECT * FROM original_accounts LOOP
        expected := a.extra;
        IF a.id < 16 OR a.id = 200 THEN
            expected := jsonb_set(expected, '{openai_codex_client_profile}',
                (expected->'openai_codex_client_profile') || jsonb_build_object(
                    'codex_version', '0.159.0', 'app_version', '26.928.20755',
                    'browser_version', '154.0.0.0',
                    'template_id', CASE WHEN a.id = 200 THEN 'capture-20260930'
                        ELSE 'windows-v3-' || lpad(a.id::text, 2, '0') END));
            IF NOT EXISTS (SELECT 1 FROM scheduler_outbox WHERE account_id = a.id AND event_type = 'account_changed')
                THEN RAISE EXCEPTION 'missing invalidation %', a.id; END IF;
        END IF;
        IF (SELECT extra FROM accounts WHERE id = a.id) IS DISTINCT FROM expected
            THEN RAISE EXCEPTION 'unexpected identity, environment or custom change %', a.id; END IF;
    END LOOP;
END $$;
\ir ../../backend/migrations/241_upgrade_codex_capture_20260930.sql
DO $$ BEGIN
    IF (SELECT count(*) FROM scheduler_outbox) <> 17 THEN RAISE EXCEPTION 'upgrade is not idempotent'; END IF;
END $$;
ROLLBACK;
\echo Codex client profile migration passed (17 upgrades, 8 exclusions, repeat is a no-op).
