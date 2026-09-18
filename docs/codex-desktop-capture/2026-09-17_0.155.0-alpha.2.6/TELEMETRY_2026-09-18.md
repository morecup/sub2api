# Work 遥测补齐（2026-09-18）

在原有 6 个指标基础上新增 8 个，共 14 个。未配置时仍使用现有官方 OTLP 端点，按请求实际选定的账号、代理和 TLS profile 分批；限流、重试次数、间隔和转接条件未修改。当前实现只记录代理能够确认的事件。

## 新增指标及来源

| 指标 | 触发和取值 | 证据 |
|---|---|---|
| `codex.api_request` | 实际 OAuth 模型 HTTP 请求，按 `status`、`success` 计数；无 HTTP 响应时状态为 `unknown` | 官方文档；本轮抓包未观察到此名称 |
| `codex.api_request.duration_ms` | 对应 HTTP 调用耗时，包含成功、HTTP 错误和传输失败；不包含响应体读取时间 | 官方文档；模拟上游测试 |
| `codex.responses_api_overhead.duration_ms` | `timing_metrics.responses_duration_excl_engine_and_client_tool_time_ms`，四舍五入到整数毫秒 | 103 条抓包与对应 OTLP 直方图逐项核对 |
| `codex.responses_api_inference_time.duration_ms` | `timing_metrics.engine_service_total_ms`，四舍五入到整数毫秒 | 同上 |
| `codex.responses_api_engine_iapi_tbt.duration_ms` | `timing_metrics.engine_iapi_tbt_across_engine_calls_ms`，保留小数 | 同上 |
| `codex.remote_models.fetch_update.duration_ms` | 真实 OAuth 模型清单拉取到处理返回的耗时；304 也记录。该操作没有请求模型，使用 `other` | 抓包名称、类型、单位和分桶；本地替换 HTTP transport 验证 |
| `codex.task.compact` | 收到 `response.compaction.compacting` 时记录 `type=remote_v2`；同一响应的重复开始事件只记一次 | 抓包及 SSE/WS 测试 |
| `codex.transport.fallback_to_http` | 网关实际执行 WS→HTTP bridge 的首次 HTTP 尝试，`from_wire_api=responses_websocket` | 抓包字段及 bridge 集成测试；未增加转接行为 |

原有 SSE、WS request、WS event 各自的计数和耗时指标继续保留。独立耗时指标只生成 histogram，独立计数只生成 sum，不附加不存在的配对指标或 success 标签。

HTTP 计数定义参考 [OpenAI 官方遥测文档](https://learn.chatgpt.com/docs/config-file/config-advanced#observability-and-telemetry)。匿名 metrics 与可配置的日志/追踪导出是独立管线；本轮扩充已有 OTLP metrics，不将提示词或工具结果变为遥测日志。

## 同步修正的统计问题

- 写入 WS 前登记模型；接收 `response.created` 后按响应 ID 关联，延迟到达的 timing 不再沿用新请求的模型。每条连接最多保留 32 条关联，ID 不进入遥测；未知或已淘汰的响应标记为 `other`。并发待响应请求无法判定归属时优先采用上游明确返回的模型，否则标记为 `other`。
- SSE 支持分片、多行 data 和小型 timing JSON，返回给业务的字节不变。行缓存与事件 data 各自最多 4 KiB；超大事件仍可统计可识别的类型，但不据截断数据生成 timing。
- 重复的 WS 读取错误不会反复计数。流结束、取消、读取错误不会被伪装成完成事件。事件 success 表示读取成功与否，模型失败仍由事件 kind 区分。
- 指标名、维度和模型名使用白名单。缺失、null、字符串、负数、非有限或超出 duration 表示范围的耗时不生成样本；真实零值保留。
- 保持 60 秒 delta 聚合、2,048 条序列上限和原有导出失败处理。遥测请求继续避免附带模型请求的 Authorization、Cookie、账号 ID、提示词、路径和工具输出；账号只出现在本地投递诊断中。

## 覆盖边界

9 月 17 日会话共观察到 81 个指标名称。目前 14 个实现中，12 个出现在该抓包中，另 2 个是文档定义的 HTTP 指标；其余 69 个抓包指标需要客户端状态，不能仅凭模型流量可靠构造。逐项清单见 [telemetry-coverage.json](telemetry-coverage.json)。

主要缺口包括桌面进程启动、预热任务的完整生命周期、插件/MCP/Hook 的实际执行、文件和 SQLite 活动、Windows 沙箱、技能加载、完整用户 turn、工具执行和进程数。特别是：

- 不把一次 `response.completed` 直接当成整个用户 turn 完成；后面可能还要执行工具和续接。
- 不把响应 token 用量直接当成完整 turn 用量；跨请求工具轮次和客户端取消边界并不完整。
- 不把账号出站代理当作 Work 沙箱的 managed network proxy。
- 仅见 `generate=false` 无法得到客户端预热任务的创建、完成及被首轮消费的完整时间，因此未填充 `startup_prewarm` 指标。
- 缓存、插件、文件等指标不会通过空闲定时器或随机事件补数。下游若未提供真实客户端事件，这些缺口仍然存在。

## 验证

- 103 条真实 timing 的纯数值夹具回放：三个指标的 count、sum、min、max 和每个 bucket 全部与抓包一致；夹具不含响应/账号 ID、凭据或正文。
- 新增及最终相关专项：124 条测试/子测试通过，覆盖 200/401/429/传输错误、缺省及关闭配置、代理与账号隔离、真实 WS 帧、HTTP bridge、模型清单 200/304、SSE 每字节分片、多行、取消、未知模型及字段白名单。
- 服务包 unit 全量：9,579 条测试/子测试通过，3 条跳过；保留原先 5 个失败函数（含子测试共 7 条 fail），失败名单与修复前一致。最后增加的极大耗时拒绝检查由上述最终专项覆盖。
- 三个限流/工具帧重试核心文件 SHA256 与本轮修改前一致；HTTP bridge 的重试和选择条件未改。

## 发布结果

2026-09-18 09:56:34（Asia/Shanghai）已发布到 `107.175.76.239:18082` 的 `sub2api-pool-bald-app`，标识 `e9f0b1f67-dirty-telemetry14-20260918T015325Z`，版本号仍为 `0.1.165`。

- 程序及运行进程 SHA256：`fc665ab11ac61ab07c8147fcbfe96d49231fb2e1e07ccf81586b6caa3ae09389`。构建前后 3,265 个源码文件校验一致，相对上一线上版本仅本轮 6 个实现文件和 2 个测试/夹具文件发生变化。
- 发布后 90 秒复查：健康与后台页面返回 HTTP 200，外网抽查三个资源与本地构建一致；PID `3528754`、重启计数 `47` 稳定，其他 PM2 服务未随本次发布重启。GPT-6 计价文件哈希不变。
- 复查窗口未见 panic、fatal 或遥测配置/导出/序列上限告警，也未观察到批次投递记录；不能据此声称官方采集端已收到数据。线上仍有既有的 `no available accounts` 错误。本次没有额外向官方发送测试请求或模拟活动。
- 旧版备份：`/home/bald/sub2api-pool-bald/app/.deploy-tmp/sub2api.backup.20260918T015626Z.pre-78f51461b377`。
- 发布回执：`/home/bald/sub2api-pool-bald/app/.deploy-tmp/deployment-e9f0b1f67-dirty-telemetry14-20260918T015325Z.json`。本地构建、校验及复查记录位于 `.codex-official-desktop/2026-09-18/telemetry-*`。
