# Codex Desktop App API Endpoints

Codex 桌面端管理接口与请求画像。当前基准为 2026-08-30 实抓的
Codex App `26.825.41651`、codex-rs `0.151.0-alpha.7.1`、Owl/Chrome `151.0.7922.174`。

Base URL：`https://chatgpt.com/backend-api`

## 0.151 WebView 请求画像

管理接口由 Owl WebView 发出，与 `/codex/responses` 使用的 Rust 客户端画像不同：

| Header | 当前值或语义 |
|---|---|
| `Authorization` | `Bearer <redacted>` |
| `ChatGPT-Account-Id` | `<redacted>` |
| `User-Agent` | `CodexBrowser Mozilla/5.0 ... Chrome/151.0.0.0 ...` |
| `OAI-Language` | UI 语言，可变 |
| `Accept-Language` | 系统 WebView 语言，可变 |
| `originator` | 通常为 `Codex Desktop`；少数通用 ChatGPT 接口为 `Codex Browser` |
| `Sentry-Trace` | release 固定画像中的零 trace |
| `Baggage` | `sentry-release=codex%4026.825.41651` 等固定 release 信息 |
| `Accept-Encoding` | `gzip, deflate, br, zstd` |
| `Sec-Fetch-Site` / `Mode` / `Dest` | `none` / `no-cors` / `empty` |
| `Priority` | `u=4, i` |
| `Cookie` | 登录 WebView 会话 cookie；服务端只能使用账号保存的 cookie，不应透传调用方 cookie |

POST 请求另带 `Content-Type: application/json`。

从 26.715 开始，实抓已不再出现旧版
`X-OpenAI-Attach-Auth` / `X-OpenAI-Attach-Integrity-State`；26.825 仍未恢复，
因此当前实现不发送这两个头。

## 接口 1：查询邀请资格

```http
GET /backend-api/referrals/invite/eligibility?referral_key=codex_referral_persistent_invite
```

- 请求体：无
- Query key：`["persistent-referral-invite-eligibility"]`
- 路径没有 `/wham/` 前缀

## 接口 2：发送邀请邮件

```http
POST /backend-api/wham/referrals/invite
Content-Type: application/json

{"referral_key":"codex_referral_persistent_invite","emails":["<redacted-email>"]}
```

成功后 UI 会刷新邀请资格 query。

## 接口 3：获取重置额度

```http
GET /backend-api/wham/rate-limit-reset-credits
```

空券账号的典型响应：

```json
{"credits":[],"available_count":0,"total_earned_count":0}
```

桌面 UI 通过 WebView 直接调用；codex-rs 同时也实现了 CLI/TUI 的
`rate_limit_resets` 客户端链路。

## 接口 4：消耗重置额度

```http
POST /backend-api/wham/rate-limit-reset-credits/consume
Content-Type: application/json

{"credit_id":"<credit-id>","redeem_request_id":"<uuid-v4>"}
```

`redeem_request_id` 必填且非空；`credit_id` 在 codex-rs 协议中可选。
无可用额度时服务端可返回：

```json
{"code":"no_credit","credit":null,"windows_reset":0}
```

## 与 `/codex/responses` 的区别

`/backend-api/codex/responses` 由内置 codex-rs 发出，使用
`Codex Desktop/0.151.0-alpha.7.1 ...` UA、zstd 请求体、HTTP/2 SSE 或 WebSocket，
并携带 attestation、routing hint、session/thread/window 与 turn metadata。
完整的 0.151 脱敏差异记录见
[`codex-desktop-capture/2026-08-30_0.151.0-alpha.7.1/README.md`](codex-desktop-capture/2026-08-30_0.151.0-alpha.7.1/README.md)。

## 实现位置

| 文件 | 作用 |
|---|---|
| `backend/internal/service/codex_desktop_webview_profile.go` | WebView UA、Sentry、Fetch 与压缩画像 |
| `backend/internal/service/codex_desktop_api_service.go` | 邀请与重置额度接口 |
| `backend/internal/service/openai_codex_mimic.go` | `/codex/responses` HTTP/WS 请求画像 |
