# 0.145.0-alpha.27 抓包记录（2026-07-21/22）

来源：UWP `OpenAI.Codex_26.715.8383.0_x64__2p2nqsd0c76g0`，内置 codex.exe `0.145.0-alpha.27`。
账号：`josephcase5093@outlook.com`（free 计划，account-id `681acc35-fe86-4c98-971e-4915f69da761`）。
抓包方式：mitmproxy upstream 链 Clash；WS 升级回 426 → HTTP POST SSE 回退；`HTTP_PROXY`/`HTTPS_PROXY`
环境变量引导 codex.exe WS 客户端进代理。

## 文件

| 文件 | 内容 |
|---|---|
| `ws_upgrade_prewarm_headers.txt` | WS 升级请求（prewarm ×2，被 426 拦截） |
| `http_post_turn_system_headers.txt` | 系统 turn（标题生成）：`thread_source:"system"`、`sandbox:"windows_elevated"`、无 workspaces |
| `http_post_turn_user_headers.txt` | 用户 turn ×2：`thread_source:"user"`、`sandbox:"none"`、workspaces + `workspace_kind:"project"` |
| `http_post_compaction_headers.txt` | 压缩请求：`request_kind:"compaction"`，attestation 带完整 CBOR token |
| `get_models_headers.txt` | `GET /codex/models?client_version=0.145.0`（gpt-5.6-terra 清单） |
| `quota_response_headers.txt` | 额度相关响应头（与 0.142 字段集一致） |
| `raw/codex_flows.txt` | 全量流量（TUN 关闭后，upstream 链 Clash 时期） |
| `raw/codex_flows_round1_tun_era.txt` | 全量流量（TUN 时期，含 token_revoked 事件） |

> 所有文件（含整理后的）均未脱敏——测试 free 账号，原始值留存。

## 0.144 → 0.145 请求画像 diff

基准：代码当前伪装 `0.144.0-alpha.4 / 26.707.31123`。

### 请求头

| 项 | 0.145 实抓 | 0.144 代码基准 | 动作 |
|---|---|---|---|
| `version` | `0.145.0-alpha.27` | `0.144.0-alpha.4` | 升级 |
| UA 应用版本 | `26.715.61943` | `26.707.31123` | 升级 |
| `x-openai-internal-codex-responses-lite` | `true`（每个 POST） | 无 | **新增** |
| `x-responsesapi-include-timing-metrics` | 不发送 | `true` | **移除** |
| `x-oai-attestation`（turn POST） | `{"v":1,"s":1}`（无 token） | `{"v":1,"s":0,"t":"v1..."}`（完整 CBOR） | **按请求类型区分** |
| `x-oai-attestation`（prewarm / compaction） | `{"v":1,"s":0,"t":"v1..."}` | 同左 | 保持 |

### turn-metadata

| 场景 | 0.145 实抓 |
|---|---|
| prewarm | `thread_source:"user"`、`sandbox:"none"`、**带 workspaces**（0.142 prewarm 无这些字段） |
| 用户 turn | `thread_source:"user"`、`sandbox:"none"`、workspaces + `workspace_kind:"project"` |
| 系统 turn（标题） | `thread_source:"system"`、`sandbox:"windows_elevated"`、无 workspaces |
| compaction | 沿用所在线程的 session/thread/turn 标识 |

### 未变

- WS 握手：`openai-beta: responses_websockets=2026-02-06`、`permessage-deflate`
- `x-codex-beta-features: remote_compaction_v2`
- `content-encoding: zstd`；`session-id == thread-id == x-client-request-id`
- 额度响应头字段集（`x-codex-primary-used-percent` 等）

## 风控观察

1. **新账号清算极快**：2026-07-21 首个测试号（KarlRemien808117+8juivnt@outlook.com，
   批量注册特征）登录约 2 分钟后所有请求 401，响应 `token_revoked`
   （"Encountered invalidated oauth token"）。无预警、不可恢复。
2. **版本红线**：models 清单 `minimal_client_version: "0.144.0"`。0.144 画像目前仍被接受
   （turn 200），但已踩最低线；`gpt-5.6-terra` 的 `use_responses_lite: true` 意味着
   缺 `x-openai-internal-codex-responses-lite` 的客户端与真实行为可区分。
3. 第二个账号（josephcase5093）在抓包期间（含多轮 turn + compaction）工作正常，
   说明风控触发更偏向账号来源特征，而非单次请求画像。

## 对 sub2api 伪装层的行动项

- [x] `codexDesktopVersion` → `0.145.0-alpha.27`，UA → `26.715.61943`
- [x] turn POST 增加 `x-openai-internal-codex-responses-lite: true`
- [x] 移除 `x-responsesapi-include-timing-metrics`
- [x] attestation 按请求类型区分：turn 发 `{"v":1,"s":1}`，prewarm/compaction 发完整 token
- [x] prewarm metadata 补 `thread_source` / `sandbox` / `workspaces`（核对后现状已符合，未改动）
- [x] turn metadata 恢复 `workspace_kind:"project"`（workspaces 非空时，实抓确认 0.145 已回归该字段）
- [x] models 清单 `client_version` 改为三段式 `0.145.0`（去掉 alpha 后缀，修复既有保真缺陷）
