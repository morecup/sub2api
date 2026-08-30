# 0.151.0-alpha.7.1 脱敏抓包记录（2026-08-30）

来源：Codex App `26.825.41651`，内置 codex-rs `0.151.0-alpha.7.1`，
Owl/Chrome `151.0.7922.174`，Windows `10.0.26100` x86_64。

本目录只保留可提交的脱敏结论。原始 mitmproxy 流量位于本机 Git 忽略目录
`.codex-official-desktop/captures/raw/`，含 Authorization、Cookie、账号 ID、
本地路径和对话内容，不得提交或外发。

## 覆盖场景

- WebSocket prewarm、首轮 turn、连续第二轮 turn
- 本地工具读取、图片附件
- WebSocket 手动 `/compact`
- WS 426 后的 HTTP/2 + zstd + SSE 回退
- Lite 模型（Terra、Luna）与非 Lite 模型（GPT-5.5）
- HTTP 手动 compaction

## 0.145 → 0.151 差异

| 项目 | 0.151 实抓结论 |
|---|---|
| App / codex-rs | `26.825.41651` / `0.151.0-alpha.7.1` |
| Rust UA | `Codex Desktop/0.151.0-alpha.7.1 (...) dumb (Codex Desktop; 26.825.41651)` |
| WebView UA | 新增 `CodexBrowser` 前缀，Chrome 151 |
| Sentry release | `codex@26.825.41651` |
| Routing hint | HTTP POST 与 WS 握手新增 `x-codex-routing-hint: model=<实际模型>` |
| Attestation | 普通 turn 也恢复完整 `v=1, s=0, t=v1...`；旧 `s=1` 画像已过时 |
| HTTP Lite | 仅 Lite 模型发送 `x-openai-internal-codex-responses-lite: true` |
| WS Lite | 握手不发送 Lite 头；`response.create.client_metadata` 写入 `ws_request_header_x_openai_internal_codex_responses_lite: "true"` |
| Cookie | HTTP POST 可携带登录会话 cookie；WS 握手不携带 cookie |
| 传输 | HTTP/2、zstd 请求体、SSE；WS 路径仍为 `GET /backend-api/codex/responses` |
| Compaction | 仍为 `POST /backend-api/codex/responses`，input 含 `compaction_trigger` |

## 0.151 turn metadata

普通用户 turn 的字段顺序与核心值：

```json
{
  "installation_id": "<uuid>",
  "session_id": "<uuid-v7>",
  "thread_id": "<same-as-session>",
  "agent_name": "/root",
  "turn_id": "<uuid-v7>",
  "window_id": "<session>:0",
  "window_number": 0,
  "context_window_id": "<uuid-v7>",
  "request_kind": "turn",
  "root_turn_id": "<root-turn-uuid-v7>",
  "thread_source": "user",
  "turn_trigger": "composer",
  "sandbox": "none",
  "sandbox_mode": "danger-full-access",
  "auto_review_enabled": false,
  "node_repl_auto_review_required": false,
  "node_repl_disabled": false,
  "workspaces": {"<redacted-path>": {"associated_remote_urls": {}, "latest_git_commit_hash": "<hash>", "has_changes": true}},
  "turn_started_at_unix_ms": 0,
  "workspace_kind": "project"
}
```

标题生成 turn 使用：

- `thread_source: "thread_title"`
- `turn_trigger: "thread_title"`
- `sandbox: "windows_elevated"`
- `sandbox_mode: "read-only"`
- 即使有 `workspaces` 也不带 `workspace_kind`

WS prewarm 不带 `root_turn_id`、`turn_trigger`、`workspaces`、
`turn_started_at_unix_ms`、`workspace_kind`。手动 compaction 不带
`root_turn_id`、`turn_trigger`、`workspaces`，其 `compaction` 对象仍为：

```json
{"trigger":"manual","reason":"user_requested","implementation":"responses_compaction_v2","phase":"standalone_turn","strategy":"memento"}
```

## 已落地

- 版本、Rust UA、WebView UA 与 Sentry release 更新到 0.151 / 26.825
- HTTP/WS routing hint 按最终上游模型生成
- 普通 HTTP turn 恢复完整 attestation
- 新增 0.151 metadata 字段及用户 / thread-title / prewarm / compaction 画像
- WS Lite 标记迁移到 payload metadata，HTTP Lite 头继续按模型条件发送
- 仅从账号保存凭据注入可选 HTTP cookie；不透传调用方 cookie，WS 不发送 cookie

## 脱敏规则

本文不得出现真实 Authorization、Cookie、账号/用户 ID、attestation token、
请求/响应 ID、邮箱、本地绝对路径、git remote、对话正文或附件内容。动态字段只保留
类型、格式或占位符。
