# sub2api 与 Work 0.155 抓包对齐

基于本目录的 5 模型 × WS/HTTP 核心矩阵修改网关实现。遥测零配置即按抓包中的默认参数向官方端点上报。

2026-09-18 复核后，继续修复 turn/窗口生命周期、任务隔离、Cookie、代理失败处理、工具历史及新版 TLS 差异；这些后续修改已于 09:23（Asia/Shanghai）上线。修复和验证范围见 [修复记录](REPAIRS_2026-09-18.md)，修复前证据见 [非遥测部分对齐复核](ALIGNMENT_AUDIT.md)。本文不代表完整行为对齐。

## 基础请求

- OAuth Codex Rust 版本更新为 `0.155.0-alpha.2.6`，桌面 App 为 `26.911.61220`；WebView UA 更新为 Chrome 153，Sentry release 使用同一 App 版本常量。
- `x-codex-beta-features` 更新为 `realtime_conversation,remote_compaction_v2`。
- 普通任务保留有效的 `workspace_kind=project/projectless`，即使 `workspaces` 没有目录明细；未知目录明细省略，不生成本机路径或 Git 状态。非空目录映射仍确定为项目任务。
- 手动 compaction 增加 `root_turn_id`，同步到 body 的 `client_metadata`；自动 compaction 保留 `turn_trigger` 和有效的 `workspace_kind`。预热和标题保持各自字段规则。
- Sol、Astra、Terra、Luna 的 Lite 判定及 GPT-5.5 的非 Lite 行为已有实现，增加五模型回归；`access_programs` 保留客户端显式值，不将抓包账号的资格设置为所有请求默认值。
- API Key 透传仍保留客户端原有请求头；抓包中的 `workspace-write` 是测试权限设置，不能强行覆盖其他请求的实际权限。

## 遥测

无需配置即可启用；`gateway.codex_telemetry` 仅用于覆盖默认值，示例见 `deploy/config.example.yaml`。省略整个配置节、留空模式或端点均会采用以下远端默认值。

```yaml
gateway:
  codex_telemetry:
    mode: remote
    local_path: data/codex-telemetry.jsonl
    endpoint: https://ab.chatgpt.com/otlp/v1/metrics
    statsig_api_key: ""
```

- `remote`：默认值，约每 60 秒向 `https://ab.chatgpt.com/otlp/v1/metrics` 上报。`statsig_api_key` 留空时使用官方客户端程序中内置的 `client-` 公共 SDK 键，已与本轮抓包及分发的二进制交叉核对；无需填写 OAuth 或账号密钥。切换自建采集器时不自动附带此官方公共键，显式提供的键仍可覆盖默认值。
- `local`：显式选择后仅写入 OTLP JSONL；10 MiB 滚动，保留一个 `.1` 备份。无事件时不写文件；不产生遥测网络请求。
- `off`：关闭遥测采集。

自建采集器支持 HTTPS 和本机回环 HTTP。可用 `GATEWAY_CODEX_TELEMETRY_MODE`、`GATEWAY_CODEX_TELEMETRY_LOCAL_PATH`、`GATEWAY_CODEX_TELEMETRY_ENDPOINT`、`GATEWAY_CODEX_TELEMETRY_STATSIG_API_KEY` 覆盖对应项。

初版实现以下六项可由代理直接测量的指标；9 月 18 日已扩充到 14 项，新增 HTTP、服务端 timing、模型清单、压缩和 WS→HTTP 转接指标，详见 [遥测补齐说明](TELEMETRY_2026-09-18.md)：

| 指标 | 测量位置 |
|---|---|
| `codex.websocket.request` | 实际写入的 `response.create`，包括预热；按每条消息的最终模型计数 |
| `codex.websocket.request.duration_ms` | WS 写入耗时 |
| `codex.websocket.event` | 实际收到且识别的 JSON 事件，或读取失败 |
| `codex.websocket.event.duration_ms` | WS 读取等待耗时 |
| `codex.sse_event` | HTTP 流中的完整、已识别 SSE 事件，或读取失败 |
| `codex.sse_event.duration_ms` | 读取该 SSE 事件的累计等待耗时 |

沿用抓包的 `resourceMetrics/scopeMetrics/metrics` 结构、`scope=codex`、delta aggregation、单调计数器和毫秒直方图桶。相同指标的不同标签放在同一 metric 的多个 data point 中。`success` 描述传输操作是否成功，不将其解释为模型任务成功。

遥测与推理请求采用同一账号的 Desktop 兼容环境。9 月 18 日 11:18 起，环境按账号持久保存并从 16 套模板中分配，系统构建包含 `10.0.26100` / `10.0.26200`；安装和设备标识各自独立。版本、系统与 SDK 资源字段从相应环境快照读取，见 [账号客户端环境说明](CLIENT_PROFILES_2026-09-18.md)。指标计数与耗时仍由代理的实际请求产生，不回放抓包中的数值、时间戳或用户标识，也不向官方 OTLP 增加抓包中不存在的设备身份字段。

远端批次按上游账号、代理和 TLS profile 隔离，沿用请求选定的出口；不附带 OAuth Authorization、Cookie 或账号 ID。模型名与事件类型采用白名单，未知模型记为 `other`。提示词、附件、工具输出、路径、用户及会话标识不进入 payload。

最多保留 2048 条聚合序列，溢出丢弃并计数。上报在后台执行，超时或失败不会中断推理；失败批次不无限重试、不切换为直连。服务退出时停止采集并作有界刷新。

## 已知边界

- 不合成抓包中的全部 81 个指标。插件缓存、Windows 沙箱、Codex home 大小和 SQLite 历史状态无法由代理推断；也不回放 CES、Statsig 实验曝光、Sentry 或界面点击日志。
- 二进制 WS 帧原样转发，当前遥测观察器不为其生成解码事件；不把收到字节、EOF 或取消算作 `response.completed`。
- Lite HTTP 响应缺失 `Content-Type` 时，以请求的 SSE Accept 识别流；解析使用有界缓冲，不改变原始字节或等待完整响应。
- 发布前自动化测试只使用本地文件和模拟上游，未发送官方遥测。线上默认远端导出已启用；健康检查及没有导出错误，不能单独证明官方服务已收到或采用某个批次。
- 保留此前工作区未提交的修改。9 月 18 日另用隔离官方二进制与本机端点验证了新版本的 TLS/HTTP2 结构，见修复记录；应用层 MITM 抓包本身仍不构成 TLS 证据。

## 验证

新增测试覆盖 OTLP 聚合与直方图、分片与大 SSE 行、流式取消、缺失 Content-Type、真实本机 WS 收发与模型切换、账号/出口隔离、导出失败、本地滚动文件、内存上限、并发计数及配置环境变量。

初次实现新增 13 个测试函数及相关 Codex、WS、透传、压缩和会话回归通过；配置、repository、TLS fingerprint 包全量通过。零配置修订另增 4 个测试，覆盖默认上报、固定协议字段、空值回退、公共键默认值、自建端点及显式关闭；配置与遥测专项通过。

服务包全量回归有 6 个既有失败。通过 Go build overlay 使用修改前工作区文件复跑，这 6 个失败全部复现，没有覆盖或回滚现有代码：

- `TestGetModelPricing_OpenAICompactAliasesFallback`
- `TestComputeFinalAnthropicBeta_APIKeyHaiku_StillUsesAPIKeyBetas`
- `TestComputeFinalCountTokensAnthropicBeta_OAuthTransparent_NoClientBetaInjectsDefault`
- `TestBuildUpstreamRequest_APIKeyHaiku_RemainsUnmimicked`
- `TestNormalizeOpenAIResponsesLiteTools_StripsImageDetailsOnlyFromSupportedContent`
- `TestRateLimitService_HandleUpstreamError_OAuth401NoRefreshTokenIgnoredByDefault`

Windows 当前工具链禁用 CGO，`go test -race` 无法执行；普通并发计数测试已通过。未提交 Git。

## 线上发布

2026-09-17 23:24（Asia/Shanghai）已更新 LA 的 `sub2api-pool-bald-app`。前端重新构建并嵌入 Linux amd64 程序，发布标识 `e9f0b1f67-dirty-codex0155-20260917T152003Z`，版本号仍为 `0.1.165`。

- 运行进程 SHA256 与构建产物一致：`d1b94bd595dd2696c4335d1f137554d7d93c067c1a69a2c516248f0001ba3365`。
- 健康接口、首页及抽查的三个前端资源返回 HTTP 200。
- 发布后约 93 秒复查：PID 与重启次数保持稳定，新增日志未见 panic、fatal 或遥测配置、导出、序列上限告警；其他 PM2 服务的 PID、状态与重启次数均未变化。
- 线上未配置 `gateway.codex_telemetry`，因此使用默认官方 OTLP 端点及账号实际选定的代理出口。
- 旧程序保留于 `/home/bald/sub2api-pool-bald/app/.deploy-tmp/sub2api.backup.20260917T152422Z.pre-be32b65735de`；发布脚本包含健康失败自动回滚。
- 发布记录保留于线上 `.deploy-tmp/deployment-e9f0b1f67-dirty-codex0155-20260917T152003Z.json`。只重启了网关应用。

随后于 23:44 发布 GPT-6 Astra 测试入口与计价补丁，发布标识为 `e9f0b1f67-dirty-gpt6pricing-20260917T154103Z`，详见 [GPT-6 补齐与验证记录](GPT6_PRICING.md)。

2026-09-18 09:23 又发布本轮 Work 修复，发布标识为 `e9f0b1f67-dirty-work-alignment-20260918T012023Z`。健康、外网页面、运行程序指纹和发布后 167 秒进程稳定性检查通过；GPT-6 计价、限流与重试策略保留。发布前已有的 `no available accounts` 告警仍存在，观察期无遥测批次投递记录，未作真实上游请求成功或遥测送达的结论。回执与备份见 [修复记录](REPAIRS_2026-09-18.md#线上发布与复查)。

09:56 发布遥测补齐版本，标识为 `e9f0b1f67-dirty-telemetry14-20260918T015325Z`。14 项指标的覆盖、验证和发布回执见 [遥测补齐说明](TELEMETRY_2026-09-18.md)。默认端点、代理出口、限流、重试和 GPT-6 计价保持不变。

11:18 发布账号客户端环境版本，当前标识为 `e9f0b1f67-dirty-clientprofiles16-20260918T031636Z`。1,899 条 OAuth 账号记录完成迁移，独立安装/设备 ID 无重复，旧安装 ID 全部保留。详见 [环境分配与发布验证](CLIENT_PROFILES_2026-09-18.md#线上发布)。
