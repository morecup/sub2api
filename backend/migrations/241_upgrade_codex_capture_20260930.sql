-- Upgrade the September 23 default profile to the captured September 30 release.
-- Preserve identities, OS/display/locale settings, custom profiles and other data.
WITH updated AS (
    UPDATE accounts
    SET extra = jsonb_set(extra, '{openai_codex_client_profile}',
        (extra->'openai_codex_client_profile') || jsonb_build_object(
            'codex_version', '0.159.0',
            'app_version', '26.928.20755',
            'browser_version', '154.0.0.0',
            'template_id', CASE
                WHEN extra #>> '{openai_codex_client_profile,template_id}' = 'capture-20260923'
                    THEN 'capture-20260930'
                ELSE replace(extra #>> '{openai_codex_client_profile,template_id}', 'windows-v2-', 'windows-v3-')
            END))
    WHERE platform = 'openai' AND type = 'oauth'
      AND extra #>> '{openai_codex_client_profile,schema_version}' = '1'
      AND extra #>> '{openai_codex_client_profile,codex_version}' = '0.155.0-alpha.16'
      AND extra #>> '{openai_codex_client_profile,app_version}' = '26.917.51856'
      AND extra #>> '{openai_codex_client_profile,browser_version}' = '153.0.0.0'
      AND extra #>> '{openai_codex_client_profile,otel_sdk_version}' = '0.31.0'
      AND (extra #>> '{openai_codex_client_profile,template_id}' = 'capture-20260923'
           OR extra #>> '{openai_codex_client_profile,template_id}' ~ '^windows-v2-(0[0-9]|1[0-5])$')
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id)
SELECT 'account_changed', id FROM updated;
