# wham rate-limit-reset-credits 补充抓包（2026-07-22）

来源：UWP `OpenAI.Codex_26.715.8383.0_x64__2p2nqsd0c76g0`，内置 codex.exe `0.145.0-alpha.27`。
账号：`josephcase5093@outlook.com`（free 计划，account-id `681acc35-fe86-4c98-971e-4915f69da761`）。
抓包方式：mitmproxy（upstream 链 Clash）+ 系统代理；应用重启后打开「用量」页面触发。
目的：补齐前两轮未抓到的额度重置券端点（源码：`codex-rs/backend-client/src/client/rate_limit_resets.rs`）。

## 文件

| 文件 | 内容 |
|---|---|
| `wham_rate_limit_reset_credits_get.txt` | `GET /wham/rate-limit-reset-credits` → 200 ×2（应用实抓） |
| `wham_rate_limit_reset_credits_consume_400.txt` | `POST .../consume` → 400（**手工构造**）：`redeem_request_id` 空串触发 pydantic 风格 body 校验 |
| `wham_rate_limit_reset_credits_consume_no_credit.txt` | `POST .../consume` → 200 `no_credit`（**手工构造**）：body `{"redeem_request_id":"<uuid v4>"}` |

> 所有文件均未脱敏。两个 consume 块是 curl 经代理合成（账号无券，UI 无法触发），
> 且 cookie 头因提取范围串块重复发送了一遍——不代表应用真实行为。

## 结论

1. **GET 由 Electron UI 直接发出，不是 codex.exe**：浏览器 UA（`Chrome/150.0.0.0`）、
   `sec-fetch-*`、4 个 cookie（oai-sc / __cflb / __cf_bm / _cfuvid）、`originator: Codex Desktop`、
   `oai-language`、sentry `baggage`/`sentry-trace`；认证为 `authorization: Bearer <JWT>` +
   `chatgpt-account-id`。源码中 app-server → backend-client 的 Rust 链路是 CLI/TUI 的路径，
   桌面应用的用量页面由 UI 层直接调 REST。
2. 打开用量页面时该 GET 只随首次进入发出（与 `/wham/usage` 的轮询行为不同）。
3. 空券账号响应 `{"credits": [], "available_count": 0, "total_earned_count": 0}`。
4. consume 无券时回 200 `{"code":"no_credit","credit":null,"windows_reset":0}`，
   结构与源码 `ConsumeRateLimitResetCreditResponse` 一致；无额外风控拦截。
5. consume body 校验：`redeem_request_id` 必填非空（400 `invalid_request_error`，pydantic 风格）。
6. `/wham/usage` 响应内嵌摘要为 `{"available_count":0,"applicable_available_count":0}`——
   `applicable_available_count` 未被 CLI 源码的 `RateLimitResetCreditsSummary` 建模。
7. **26.715 不再发送 `X-OpenAI-Attach-Auth` / `X-OpenAI-Attach-Integrity-State`**：
   `docs/CODEX_DESKTOP_ENDPOINTS.md`（提取自 26.616 app.asar）记载桌面端 API 客户端统一携带
   这两个头，但本轮 26.715 实抓的 GET 及全量日志中均未出现。

## 未覆盖

- 有券账号的真实 consume（`code:"reset"`、`windows_reset` 实际值、credit 明细字段）。
  券来源看字段（`profile_user_id`、`granted_at`）疑似邀请奖励，需 `total_earned_count > 0` 的账号。
