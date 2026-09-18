-- Account-local, persisted compatibility environments. No credentials or
-- account status are changed. Preserve the installation UUID previously
-- derived by codexInstallationIDForAccount; do not rotate existing clients.
-- Pool order and release snapshot match codexClientEnvironmentPool (v1).
WITH candidates AS (
    SELECT id,
           (id % 16)::int AS template_index,
           sha256(convert_to('sub2api:codex-installation:account:' || id::text, 'UTF8')) AS installation_hash,
           sha256(convert_to('sub2api:codex-device:account:' || id::text, 'UTF8')) AS device_hash
    FROM accounts
    WHERE platform = 'openai' AND type = 'oauth'
      AND NOT (COALESCE(extra, '{}'::jsonb) ? 'openai_codex_client_profile')
), identities AS (
    SELECT id, template_index,
           encode(installation_hash, 'hex') AS ih,
           substr('89ab', ((get_byte(installation_hash, 8) >> 4) & 3) + 1, 1) AS iv,
           encode(device_hash, 'hex') AS dh,
           substr('89ab', ((get_byte(device_hash, 8) >> 4) & 3) + 1, 1) AS dv
    FROM candidates
), updated AS (
UPDATE accounts AS a
SET extra = COALESCE(a.extra, '{}'::jsonb) || jsonb_build_object(
    'openai_codex_client_profile', jsonb_build_object(
        'schema_version', 1,
        'template_id', 'windows-v1-' || lpad(i.template_index::text, 2, '0'),
        'installation_id', substr(ih, 1, 8) || '-' || substr(ih, 9, 4) || '-4' || substr(ih, 14, 3) || '-' || iv || substr(ih, 18, 3) || '-' || substr(ih, 21, 12),
        'device_id', substr(dh, 1, 8) || '-' || substr(dh, 9, 4) || '-4' || substr(dh, 14, 3) || '-' || dv || substr(dh, 18, 3) || '-' || substr(dh, 21, 12),
        'codex_version', '0.155.0-alpha.2.6',
        'app_version', '26.911.61220',
        'os', 'Windows',
        'os_version', CASE WHEN i.template_index < 8 THEN '10.0.26100' ELSE '10.0.26200' END,
        'arch', 'x86_64',
        'locale', CASE WHEN i.template_index % 2 = 0 THEN 'zh-CN' ELSE 'en-US' END,
        'timezone', 'Asia/Shanghai',
        'screen_size_sum', (ARRAY[3000, 4000, 4000, 6000])[(i.template_index % 8) / 2 + 1],
        'screen_scale', (ARRAY[1.0, 1.0, 1.25, 1.5])[(i.template_index % 8) / 2 + 1],
        'browser_version', '153.0.0.0',
        'otel_sdk_version', '0.31.0'
    ))
FROM identities AS i
WHERE a.id = i.id
  AND NOT (COALESCE(a.extra, '{}'::jsonb) ? 'openai_codex_client_profile')
RETURNING a.id
)
INSERT INTO scheduler_outbox (event_type, account_id)
SELECT 'account_changed', id FROM updated;
