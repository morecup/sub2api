# Account Monitor API Key 对接说明

本文档面向外部系统接入 `account-monitor`，覆盖监控账号从创建、加入监控、查询、检查、启停、更新到移除的完整生命周期。

## 基础信息

- 线上地址: `http://192.129.159.182:8099`
- 健康检查: `GET /healthz`
- API 前缀: `/api`
- 鉴权方式: 推荐使用 `x-monitor-api-key`

```http
x-monitor-api-key: <MONITOR_API_KEY>
```

也兼容 Bearer:

```http
Authorization: Bearer <MONITOR_API_KEY>
```

`<MONITOR_API_KEY>` 从 `account-monitor` 配置页或 `config.json` 的 `api_key` 获取。不要把 API Key 写入前端网页、日志或公开仓库。

## 核心概念

`sub2api 上游账号`: sub2api 自己的账号实体，ID 由 sub2api 分配。

`监控账号`: `account-monitor` 自己维护的监控列表项，不使用数据库，保存在 `state.json`。监控账号的 `id` 等于 sub2api 上游账号 ID。

`加入监控`: 只把一个 sub2api 账号放进 `account-monitor` 的监控列表，不会修改 sub2api 账号本体。

`创建账号并监控`: 先调用 sub2api 创建上游账号，再把返回的账号加入监控。

`移除监控`: 只从 `account-monitor` 监控列表移除，不会删除 sub2api 上游账号。

## 全局覆盖字段

外部创建上游账号时，以下字段即使传入也会被忽略，统一使用 `account-monitor` 配置页里的全局默认值:

```text
proxy_id
concurrency
priority
group_ids
auto_pause_on_expired
```

当前默认语义:

- `priority`: 默认优先级
- `concurrency`: 默认并发
- `group_ids`: 默认分组
- `proxy_id`: 由“是否使用代理”和“代理 ID”共同决定
- `auto_pause_on_expired`: 永远强制为 `false`，不允许外部开启

## 通用响应

成功响应通常直接返回 JSON 对象:

```json
{
  "items": [],
  "total": 0
}
```

错误响应:

```json
{
  "error": "invalid monitor token"
}
```

常见 HTTP 状态:

- `200`: 成功
- `400`: 请求参数错误或 sub2api 创建失败
- `401`: API Key 错误或缺失
- `404`: 监控账号不存在

## 生命周期总览

1. 配置并保存 `MONITOR_API_KEY`
2. 创建 sub2api 上游账号并加入监控，或把已有 sub2api 账号加入监控
3. 保存返回的 `account.id`
4. 查询监控账号列表和当前状态
5. 按需立即检查账号状态
6. 按需启停监控、修改监控名称或检查参数
7. 账号异常时接收邮件通知
8. 不再需要时移除监控

## 1. 健康检查

健康检查不需要 API Key。

```bash
curl http://192.129.159.182:8099/healthz
```

响应:

```json
{"ok": true}
```

## 2. 获取监控账号列表

```bash
curl -H "x-monitor-api-key: <MONITOR_API_KEY>" \
  http://192.129.159.182:8099/api/monitor-accounts
```

响应字段:

```json
{
  "items": [
    {
      "id": 466,
      "name": "account@example.com",
      "platform": "openai",
      "type": "oauth",
      "model_id": "",
      "enabled": true,
      "consecutive_failures": 0,
      "consecutive_successes": 12,
      "last_status": {
        "state": "healthy",
        "account_id": 466,
        "account_name": "account@example.com",
        "checked_at": "2026-06-02T10:49:20+08:00",
        "latency_ms": 25,
        "upstream_status": "active",
        "schedulable": true,
        "detail": "status check passed"
      },
      "created_at": "2026-06-02T10:00:00+08:00",
      "updated_at": "2026-06-02T10:49:20+08:00"
    }
  ],
  "total": 1
}
```

`last_status.state` 取值:

- `unknown`: 尚未检查或状态未知
- `healthy`: 正常
- `unhealthy`: 异常

## 3. 把已有 sub2api 账号加入监控

外部系统如果只知道账号名称，推荐先按名称加入监控。成功后保存响应里的 `account.id`，后续生命周期操作都使用这个 ID。

```bash
curl -X POST http://192.129.159.182:8099/api/monitor-accounts \
  -H "x-monitor-api-key: <MONITOR_API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "account_name": "account@example.com",
    "enabled": true
  }'
```

如果已知 sub2api 账号 ID:

```bash
curl -X POST http://192.129.159.182:8099/api/monitor-accounts \
  -H "x-monitor-api-key: <MONITOR_API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "account_id": 466,
    "enabled": true
  }'
```

响应:

```json
{
  "created_upstream_account": false,
  "account": {
    "id": 466,
    "name": "account@example.com",
    "platform": "openai",
    "type": "oauth",
    "model_id": "",
    "enabled": true,
    "last_status": {
      "state": "unknown",
      "account_id": 466,
      "account_name": "account@example.com",
      "upstream_status": "active",
      "schedulable": true
    }
  }
}
```

注意:

- 名称必须能在 sub2api 中精确匹配到唯一账号。
- 如果同名账号超过 1 个，会返回错误，调用方应改用 `account_id`。

## 4. 创建 sub2api 账号并加入监控

创建请求尽量与 sub2api `POST /api/v1/admin/accounts` 保持一致。

推荐字段:

```json
{
  "name": "new-openai-key",
  "notes": "created by external system",
  "platform": "openai",
  "type": "apikey",
  "credentials": {
    "api_key": "sk-xxx",
    "base_url": "https://api.openai.com"
  },
  "extra": {},
  "rate_multiplier": 1,
  "load_factor": 30,
  "expires_at": null,
  "auto_pause_on_expired": false,
  "confirm_mixed_channel_risk": false,
  "enabled": true,
  "model_id": ""
}
```

调用示例:

```bash
curl -X POST http://192.129.159.182:8099/api/monitor-accounts \
  -H "x-monitor-api-key: <MONITOR_API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "new-openai-key",
    "platform": "openai",
    "type": "apikey",
    "credentials": {
      "api_key": "sk-xxx",
      "base_url": "https://api.openai.com"
    },
    "extra": {},
    "enabled": true
  }'
```

响应:

```json
{
  "created_upstream_account": true,
  "account": {
    "id": 470,
    "name": "new-openai-key",
    "platform": "openai",
    "type": "apikey",
    "enabled": true,
    "last_status": {
      "state": "unknown",
      "account_id": 470,
      "account_name": "new-openai-key",
      "upstream_status": "active"
    }
  }
}
```

支持的 sub2api 风格字段:

- `name`
- `notes`
- `platform`
- `type`
- `credentials`
- `extra`
- `rate_multiplier`
- `load_factor`
- `expires_at`: Unix 秒级时间戳；`null` 表示不设置
- `auto_pause_on_expired`: 兼容接收，但永远强制传 `false` 给 sub2api
- `confirm_mixed_channel_risk`

监控专用字段:

- `enabled`: 是否加入后立即启用监控，默认 `true`
- `model_id`: 仅当监控配置 `check_mode=test` 时用于测试接口；`status` 模式不会使用

会被覆盖的字段:

- `proxy_id`
- `concurrency`
- `priority`
- `group_ids`
- `auto_pause_on_expired`

兼容字段:

- `refresh_token`
- `api_key`
- `base_url`
- `client_id`

这些兼容字段仍可用，但新接入建议统一放到 `credentials` 里。

## 5. 查询单个账号当前状态

查询缓存状态，不主动触发检查:

```bash
curl -H "x-monitor-api-key: <MONITOR_API_KEY>" \
  http://192.129.159.182:8099/api/monitor-accounts/466/status
```

响应:

```json
{
  "state": "healthy",
  "account_id": 466,
  "account_name": "account@example.com",
  "checked_at": "2026-06-02T10:49:20+08:00",
  "latency_ms": 25,
  "upstream_status": "active",
  "schedulable": true,
  "detail": "status check passed"
}
```

## 6. 立即检查账号状态

方式一:

```bash
curl -X POST http://192.129.159.182:8099/api/monitor-accounts/466/check \
  -H "x-monitor-api-key: <MONITOR_API_KEY>"
```

方式二:

```bash
curl -H "x-monitor-api-key: <MONITOR_API_KEY>" \
  "http://192.129.159.182:8099/api/monitor-accounts/466/status?refresh=true"
```

立即检查会更新 `last_status`、`consecutive_failures`、`consecutive_successes`，并可能触发邮件通知。

## 7. 启停监控或更新监控元数据

只更新 `account-monitor` 的监控元数据，不会修改 sub2api 上游账号本体。

```bash
curl -X PATCH http://192.129.159.182:8099/api/monitor-accounts/466 \
  -H "x-monitor-api-key: <MONITOR_API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "display-name",
    "model_id": "",
    "enabled": true
  }'
```

可更新字段:

- `name`: 监控列表展示名
- `model_id`: `check_mode=test` 时的测试模型
- `enabled`: 账号级监控开关

说明:

- `enabled=false` 后账号仍在监控列表中，但后台不会自动检查它。
- 手动调用 `/check` 仍然可以检查该账号。
- 全局“启用后台监控”关闭时，所有账号都不会被后台自动轮询。

## 8. 移除监控

```bash
curl -X DELETE http://192.129.159.182:8099/api/monitor-accounts/466 \
  -H "x-monitor-api-key: <MONITOR_API_KEY>"
```

响应:

```json
{"deleted": true}
```

注意:

- 这只删除 `account-monitor` 的监控记录。
- 不会删除 sub2api 上游账号。
- 如果需要删除 sub2api 账号，应调用 sub2api 自己的管理接口。

## 9. 邮件通知生命周期

后台监控每 `monitor.check_interval_seconds` 秒轮询一次。当前默认最小配置为 `5` 秒。

触发异常邮件:

- 账号检查结果为 `unhealthy`
- 连续失败次数达到 `monitor.failure_threshold`
- 达到阈值时只发一次
- 如果账号一直不可用，不会每次轮询都重复发

触发恢复邮件:

- 账号之前处于异常状态
- 后续检查结果为 `healthy`
- 连续成功次数达到 `monitor.recovery_threshold`
- 并且 `monitor.notify_on_recovery=true`

不会自动轮询的情况:

- 全局 `monitor.enabled=false`
- 单个监控账号 `enabled=false`
- 账号已经从监控列表移除

检查模式:

- `status`: 只读取 sub2api 账号状态，判断 `status=active` 且可调度
- `test`: 调用 sub2api 账号测试接口，检查更严格

## 10. 建议的接入流程

创建账号场景:

1. 外部系统生成 sub2api 风格创建请求
2. 调用 `POST /api/monitor-accounts`
3. 保存响应中的 `account.id`
4. 调用 `POST /api/monitor-accounts/{id}/check` 做首次检查
5. 后续定期调用列表或状态接口同步结果
6. 账号不再需要监控时调用 `DELETE /api/monitor-accounts/{id}`

已有账号场景:

1. 调用 `POST /api/monitor-accounts`，传 `account_name` 或 `account_id`
2. 保存响应中的 `account.id`
3. 调用状态接口或立即检查接口
4. 通过 `PATCH` 启停监控
5. 通过 `DELETE` 移除监控

重试建议:

- `POST /api/monitor-accounts` 创建上游账号目前不提供幂等保证。
- 如果创建请求超时，调用方应先按账号名称查询或加入监控，确认是否已经创建成功，再决定是否重试。
- 已有账号加入监控可以重复调用，同一个 ID 会覆盖监控列表中的旧记录。

## 11. JavaScript 示例

```js
const baseURL = "http://192.129.159.182:8099"
const apiKey = process.env.MONITOR_API_KEY

async function monitorFetch(path, options = {}) {
  const res = await fetch(`${baseURL}${path}`, {
    ...options,
    headers: {
      "x-monitor-api-key": apiKey,
      "Content-Type": "application/json",
      ...(options.headers || {})
    }
  })
  const data = await res.json()
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`)
  return data
}

async function createOpenAIAPIKeyAccount() {
  return monitorFetch("/api/monitor-accounts", {
    method: "POST",
    body: JSON.stringify({
      name: "new-openai-key",
      platform: "openai",
      type: "apikey",
      credentials: {
        api_key: "sk-xxx",
        base_url: "https://api.openai.com"
      },
      extra: {},
      enabled: true
    })
  })
}
```

## 12. Python 示例

```python
import os
import requests

BASE_URL = "http://192.129.159.182:8099"
API_KEY = os.environ["MONITOR_API_KEY"]

def monitor_request(method, path, **kwargs):
    headers = kwargs.pop("headers", {})
    headers["x-monitor-api-key"] = API_KEY
    resp = requests.request(method, BASE_URL + path, headers=headers, **kwargs)
    data = resp.json() if resp.content else None
    if not resp.ok:
        raise RuntimeError(data.get("error") if isinstance(data, dict) else resp.text)
    return data

result = monitor_request(
    "POST",
    "/api/monitor-accounts",
    json={
        "account_name": "account@example.com",
        "enabled": True,
    },
)
print(result["account"]["id"])
```
