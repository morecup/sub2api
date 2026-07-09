# Account Monitor

这是一个独立的 Go 账号监控程序，不接入 sub2api 数据库，也不修改当前项目代码。它只依赖本目录的 `config.json` 和 `state.json`。

## 功能

- 前端登录使用自己的 `auth_token`，外部接口调用可使用单独的 `api_key`。
- 通过配置里的 `sub2api.base_url` 和 `sub2api.admin_api_key` 调用当前项目接口。
- Go 二进制内嵌前端界面，打开服务地址即可管理。
- 有独立的“监控账号”概念，只列出和检查加入监控的账号。
- 后台定时轮询检查监控账号是否正常。
- 账号异常或恢复时发送邮件。
- 对外提供接口：获取监控账号列表、获取账号状态、添加/移除/编辑监控账号、修改全局配置。
- 创建新账号时，请求字段尽量和 sub2api 创建账号接口一致；`proxy_id`、`concurrency`、`priority`、`group_ids` 会被 `account_defaults` 统一覆盖，`auto_pause_on_expired` 永远强制为 `false`。

## 启动

```bash
cd account-monitor
cp config.example.json config.json
go run . -config config.json
```

然后打开：

```text
http://127.0.0.1:8099/
```

也可以只运行一次检查：

```bash
go run . -config config.json -check-once
```

构建单个二进制：

```bash
go build -o account-monitor.exe .
```

前端静态资源通过 Go `embed` 打入二进制，不需要额外部署 `web/` 目录。

## 配置重点

- `auth_token`: 本监控服务前端登录用的固定 Token。
- `api_key`: 本监控服务给外部系统调用接口用的固定 API Key。
- `sub2api.admin_api_key`: 当前 sub2api 项目的管理员 API Key，用于读取/创建/测试账号。
- `monitor.check_mode`:
  - `status`: 只读取账号状态，判断 `status=active` 且可调度。
  - `test`: 调用 sub2api 的账号测试接口，检查更严格。
- `account_defaults`: 外部创建账号时强制套用的全局默认值，覆盖 `proxy_id`、`concurrency`、`priority`、`group_ids`，默认优先级 `8`、并发 `30`、分组 `5,7,6`。
- 创建账号时 `auto_pause_on_expired` 永远强制传 `false` 给 sub2api，即使外部请求传 `true` 也会被忽略。
- `email`: SMTP 邮件通知配置。

## API 鉴权

完整外部对接说明请看 [APIKEY_INTEGRATION.md](APIKEY_INTEGRATION.md)，其中覆盖了监控账号从创建、加入监控、查询状态、立即检查、启停、更新到移除的完整生命周期。

前端登录使用 `auth_token`。外部系统建议使用 `api_key`：

```bash
Authorization: Bearer <auth_token>
x-monitor-token: <auth_token>
x-monitor-api-key: <api_key>
Authorization: Bearer <api_key>
```

## 接口

获取监控账号列表：

```bash
curl -H "x-monitor-api-key: <api_key>" \
  http://127.0.0.1:8099/api/monitor-accounts
```

获取账号当前状态：

```bash
curl -H "x-monitor-api-key: <api_key>" \
  http://127.0.0.1:8099/api/monitor-accounts/1/status
```

立即刷新账号状态：

```bash
curl -H "x-monitor-api-key: <api_key>" \
  "http://127.0.0.1:8099/api/monitor-accounts/1/status?refresh=true"
```

把已有账号加入监控：

```bash
curl -X POST http://127.0.0.1:8099/api/monitor-accounts \
  -H "x-monitor-api-key: <api_key>" \
  -H "Content-Type: application/json" \
  -d '{"account_name": "existing-openai-account"}'
```

如果 sub2api 里存在同名账号，接口会提示改用 `account_id` 精确指定。

创建上游账号并加入监控：

```bash
curl -X POST http://127.0.0.1:8099/api/monitor-accounts \
  -H "x-monitor-api-key: <api_key>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "new-openai-key",
    "notes": "created by account-monitor",
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
    "model_id": "gpt-4.1-mini"
  }'
```

创建请求兼容 sub2api 的 `name`、`notes`、`platform`、`type`、`credentials`、`extra`、`rate_multiplier`、`load_factor`、`expires_at`、`auto_pause_on_expired`、`confirm_mixed_channel_risk`。调用方传入的 `proxy_id`、`concurrency`、`priority`、`group_ids` 会被忽略，程序会使用 `account_defaults` 覆盖；`auto_pause_on_expired` 无论传什么都会强制为 `false`。

编辑监控账号名称、测试模型或启停监控：

```bash
curl -X PATCH http://127.0.0.1:8099/api/monitor-accounts/1 \
  -H "x-monitor-api-key: <api_key>" \
  -H "Content-Type: application/json" \
  -d '{"name":"备用账号","model_id":"gpt-4.1-mini","enabled":true}'
```

移除监控账号：

```bash
curl -X DELETE http://127.0.0.1:8099/api/monitor-accounts/1 \
  -H "x-monitor-api-key: <api_key>"
```

查看可前端修改的配置：

```bash
curl -H "x-monitor-api-key: <api_key>" \
  http://127.0.0.1:8099/api/config
```

更新全局默认值：

```bash
curl -X PUT http://127.0.0.1:8099/api/config \
  -H "x-monitor-api-key: <api_key>" \
  -H "Content-Type: application/json" \
  -d '{
    "account_defaults": {
      "use_proxy": true,
      "proxy_id": 2,
      "priority": 30,
      "concurrency": 5,
      "group_ids": [1]
    }
  }'
```
