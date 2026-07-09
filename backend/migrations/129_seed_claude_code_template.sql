-- Migration: 129_seed_claude_code_template
-- 内置「Claude Code TTY」请求模板，对齐交互 TTY 形态的官方 CLI 客户端请求：
--   1) User-Agent / X-App / anthropic-beta / anthropic-version 等头
--   2) system 数组首项与官方 system prompt 字面一致（Dice >= 0.5）
--   3) metadata.user_id 使用 TTY JSON 字符串形态，模板内使用非空占位 account_uuid。
--
-- ON CONFLICT DO NOTHING：已部署环境（手动建过模板）跑此 migration 不会重复 / 覆盖。
-- 用户可自行编辑后续覆盖此 seed；CC 升大版时再起一条 migration 提供新模板，不动用户的旧模板。

INSERT INTO channel_monitor_request_templates (
    name, provider, description, extra_headers, body_override_mode, body_override
)
VALUES (
    'Claude Code TTY',
    'anthropic',
    '对齐 Claude Code 2.1.201 洛杉矶 Linux 抓包口径：UA + anthropic-beta + system attribution + metadata.user_id 按官方 CLI 形态生成。',
    '{
        "User-Agent": "claude-cli/2.1.201 (external, cli)",
        "x-app": "cli",
        "anthropic-version": "2023-06-01",
        "anthropic-beta": "claude-code-20250219,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20,effort-2025-11-24",
        "anthropic-dangerous-direct-browser-access": "true",
        "X-Stainless-Arch": "x64",
        "X-Stainless-Lang": "js",
        "X-Stainless-OS": "Linux",
        "X-Stainless-Package-Version": "0.94.0",
        "X-Stainless-Retry-Count": "0",
        "X-Stainless-Runtime": "node",
        "X-Stainless-Runtime-Version": "v26.3.0",
        "X-Stainless-Timeout": "600"
    }'::jsonb,
    'merge',
    '{
        "system": [
            {
                "type": "text",
                "text": "x-anthropic-billing-header: cc_version=2.1.201.500; cc_entrypoint=cli; cch=00000;"
            },
            {
                "type": "text",
                "text": "You are Claude Code, Anthropic''s official CLI for Claude."
            }
        ],
        "metadata": {
            "user_id": "{\"device_id\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"account_uuid\":\"00000000-0000-0000-0000-000000000000\",\"session_id\":\"00000000-0000-0000-0000-000000000000\"}"
        }
    }'::jsonb
)
ON CONFLICT (provider, name) DO NOTHING;
