# Codex Desktop App API Endpoints

从 Codex 桌面端 Electron 应用（`app.asar`，版本 26.616.6631.0）中提取的四个管理接口。

Base URL: `https://chatgpt.com/backend-api`

## 通用请求头

所有接口通过统一的 API 客户端（`request-BypB0TG1.js` 中的 `p` 实例）发出，自动携带以下头：

| Header | 值 | 说明 |
|---|---|---|
| `Authorization` | `Bearer <jwt>` | Electron 主进程注入的 OAuth JWT |
| `ChatGPT-Account-Id` | `<account_uuid>` | Electron 主进程注入的账号 ID |
| `OAI-Language` | `en` | UI 语言标识，可变 |
| `X-OpenAI-Attach-Auth` | `1` | 桌面端特有，CLI 不发送 |
| `X-OpenAI-Attach-Integrity-State` | `1` | 桌面端特有，CLI 不发送 |
| `originator` | `Codex Desktop` | 桌面端标识，区别于 CLI 的 `codex_cli_rs` |

POST 请求额外携带：

| Header | 值 |
|---|---|
| `Content-Type` | `application/json` |

## 接口 1：查询邀请资格

| 项目 | 值 |
|---|---|
| **方法** | `GET` |
| **路径** | `/referrals/invite/eligibility` |
| **完整 URL** | `https://chatgpt.com/backend-api/referrals/invite/eligibility` |
| **Query 参数** | `referral_key=codex_referral_persistent_invite` |
| **请求体** | 无 |
| **代码函数** | `ie()` in `codex-api-BFMAEsqy.js` |
| **Query Key** | `["persistent-referral-invite-eligibility"]` |
| **轮询间隔** | 5 秒（`staleTime: FIVE_SECONDS`） |

### 请求头

```
GET /backend-api/referrals/invite/eligibility?referral_key=codex_referral_persistent_invite

Authorization: Bearer <jwt>
ChatGPT-Account-Id: <account_uuid>
OAI-Language: en
X-OpenAI-Attach-Auth: 1
X-OpenAI-Attach-Integrity-State: 1
originator: Codex Desktop
```

> **注意**：此路径没有 `/wham/` 前缀，是 `/referrals/invite/eligibility`。

## 接口 2：发送邀请邮件

| 项目 | 值 |
|---|---|
| **方法** | `POST` |
| **路径** | `/wham/referrals/invite` |
| **完整 URL** | `https://chatgpt.com/backend-api/wham/referrals/invite` |
| **请求体** | `{"referral_key":"codex_referral_persistent_invite","emails":["xxx@xxx.com"]}` |
| **代码函数** | `X(e)` in `codex-api-BFMAEsqy.js` |
| **成功后** | 使 `["persistent-referral-invite-eligibility"]` query 失效刷新 |

### 请求头

```
POST /backend-api/wham/referrals/invite

Authorization: Bearer <jwt>
ChatGPT-Account-Id: <account_uuid>
Content-Type: application/json
OAI-Language: en
X-OpenAI-Attach-Auth: 1
X-OpenAI-Attach-Integrity-State: 1
originator: Codex Desktop
```

### 请求体字段

| 字段 | 类型 | 值 |
|---|---|---|
| `referral_key` | string | 固定值 `"codex_referral_persistent_invite"` |
| `emails` | string[] | 被邀请人邮箱数组 |

## 接口 3：获取重置额度

| 项目 | 值 |
|---|---|
| **方法** | `GET` |
| **路径** | `/wham/rate-limit-reset-credits` |
| **完整 URL** | `https://chatgpt.com/backend-api/wham/rate-limit-reset-credits` |
| **请求体** | 无 |
| **代码函数** | `oe()` in `codex-api-BFMAEsqy.js` |
| **Query Key** | `["rate-limit-reset-credits"]` |
| **轮询间隔** | 5 秒（`staleTime: FIVE_SECONDS`） |

### 请求头

```
GET /backend-api/wham/rate-limit-reset-credits

Authorization: Bearer <jwt>
ChatGPT-Account-Id: <account_uuid>
OAI-Language: en
X-OpenAI-Attach-Auth: 1
X-OpenAI-Attach-Integrity-State: 1
originator: Codex Desktop
```

## 接口 4：消耗重置额度

| 项目 | 值 |
|---|---|
| **方法** | `POST` |
| **路径** | `/wham/rate-limit-reset-credits/consume` |
| **完整 URL** | `https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume` |
| **请求体** | `{"credit_id":"xxx","redeem_request_id":"xxx"}` |
| **代码函数** | `ce(e)` in `codex-api-BFMAEsqy.js` |
| **成功后** | 使 `["rate-limit-status"]` 和 `["rate-limit-reset-credits"]` 两个 query 失效刷新 |

### 请求头

```
POST /backend-api/wham/rate-limit-reset-credits/consume

Authorization: Bearer <jwt>
ChatGPT-Account-Id: <account_uuid>
Content-Type: application/json
OAI-Language: en
X-OpenAI-Attach-Auth: 1
X-OpenAI-Attach-Integrity-State: 1
originator: Codex Desktop
```

### 请求体字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `credit_id` | string | 额度 ID，从接口 3 获取 |
| `redeem_request_id` | string | 兑换请求 ID |

## CLI vs 桌面端

这四个接口**只存在于桌面端**（Electron/JS），CLI（Rust）中没有。

CLI 的 `backend-client` 只有相关的：
- `GET /wham/usage`（获取速率限制）
- `POST /wham/accounts/send_add_credits_nudge_email`（发送充值提醒邮件）

没有邀请和重置额度功能。

## 源文件位置

| 文件 | 说明 |
|---|---|
| `webview/assets/codex-api-BFMAEsqy.js` | 四个接口的调用代码 |
| `webview/assets/request-BypB0TG1.js` | API 客户端和默认头构建 |
| `webview/assets/src-C7fSIbpz.js` | Header 常量定义（`X-OpenAI-Attach-Auth` 等） |
| `webview/assets/vscode-api-B8VvwF1m.js` | Electron 主进程通信层 |
