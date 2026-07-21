# Codex 桌面应用抓包归档

按版本归档 Codex 独立桌面应用（ChatGPT 账号登录态）的真实请求画像，作为
sub2api 伪装层（`backend/internal/service/openai_codex_mimic.go`）的对齐基准。

## 目录

| 目录 | 应用版本 | codex.exe | 抓包日期 | 说明 |
|---|---|---|---|---|
| `2026-06-21_0.142.0-alpha.6/` | 26.616.6631.0 | 0.142.0-alpha.6 | 2026-06-21 | 首轮抓包（WS 426 拦截 → HTTP SSE 回退） |
| `2026-07-21_0.145.0-alpha.27/` | 26.715.8383.0 | 0.145.0-alpha.27 | 2026-07-21/22 | 第二轮抓包，含 0.144→0.145 diff 与风控记录 |

## 抓包方法（两轮相同）

1. mitmproxy 拦截 `/backend-api/codex/responses` 的 WebSocket 升级请求，返回 426，
   迫使客户端回退到 HTTP POST SSE（工具：`tmp/codex_capture/`）。
2. 关键点：codex.exe 的 WS 客户端**不走系统代理**（直连），但遵守
   `HTTP_PROXY`/`HTTPS_PROXY` 环境变量。有 TUN 类代理时直连会绕过抓包，
   需关 TUN 并设置上述环境变量指向 mitmproxy。
3. 本机无外网直连时，mitmdump 用 `--mode upstream:http://127.0.0.1:33210`
   链到 Clash mixed 端口。

## 跨版本要点

- 0.145 新增 `x-openai-internal-codex-responses-lite: true`（responses-lite 模型）。
- 0.145 turn POST 的 attestation 简化为 `{"v":1,"s":1}`（不带 CBOR token）；
  prewarm / compaction 仍带完整 token。
- 0.145 移除 `x-responsesapi-include-timing-metrics`（0.144 曾新增）。
- 0.145 prewarm 的 turn-metadata 增加 `thread_source` / `sandbox` / `workspaces`。
- 0.145 turn 区分用户 turn（`thread_source:"user"`，sandbox=none）与系统 turn
  （`thread_source:"system"`，sandbox=windows_elevated，无 workspaces）。
- 额度响应头（`x-codex-*`）字段集两轮一致。

## 注意

- 归档文件均为**未脱敏**原始记录（含 JWT、cookie）——账号为临时测试 free 号，
  按需求原样留存；注意不要外发整个目录。
- 两个抓包日期的账号均为临时测试号，其中 2026-07-21 的第一个账号
  （KarlRemien...）在登录约 2 分钟后被服务端吊销 token（详见 0.145 目录 README）。
