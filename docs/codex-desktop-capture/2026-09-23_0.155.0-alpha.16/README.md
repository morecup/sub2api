# GPT-6 Sol / Luna：桌面本地 Work 抓包

2026-09-23，通过独立 ChatGPT/Codex 桌面副本的 UI 完成两个新模型的本地项目测试。测试绑定合成 Git 项目，实际读取 CSV、接收图片、运行命令、写入并核验 JSON；普通 Chat 会话不作为本轮覆盖证据。

官方于 9 月 22 日宣布向 Work/Codex 推出两个模型，见 [官方更新日志](https://learn.chatgpt.com/docs/changelog)。本轮以账号实际返回的模型配置和实际请求为准，不把文档中的 API 能力直接当成桌面端配置。

## 客户端与隔离方式

| 项目 | 9 月 17 日基线 | 本次实测 |
|---|---|---|
| Windows Store 包 | 26.911.7940.0 | 26.917.6896.0 |
| App 内部版本 | 26.911.61220 | 26.917.51856 |
| bundled CLI | 0.155.0-alpha.2.6 | 0.155.0-alpha.16 |
| WebView Chromium UA | 153.0.0.0 | 153.0.0.0 |
| OTLP Rust SDK | 0.31.0 | 0.31.0 |

使用本机已安装 Store 包的副本，并未安装或启动另一个发布渠道的版本。Store 的只读更新检查无法确定是否还有更高版本，因此这里不宣称该包是全网最新版本。

副本使用独立 profile、CODEX_HOME、代理端口 38093 和调试端口 9345。按用户指示只复制当前认证文件，没有复制任务历史。原 Store 安装目录不修改；副本的 ASAR 仅做已核对的等长单实例判断补丁。新版在非 Store 副本中会遇到 package identity 初始化问题，因此额外通过 `CODEX_CLI_PATH` 显式选择**同包、未修改的 CLI**。这些启动差异已记入私有 session 元数据。

文件夹选择对话框未接受 Computer Use 输入，故先在独立配置中登记合成项目，再通过 UI 执行模型任务。图片通过 UI 编辑器的粘贴处理器附加。没有直接构造或回放模型请求来代替 UI 测试。

## 覆盖结果

| 模型 | 传输 | 文本及追问 | 图片 | 读写、执行、失败恢复 | 压缩及压缩后落盘 | 正文中断及续接 | 主任务请求数 |
|---|---|---|---|---|---|---|---:|
| gpt-6-sol | WS | 通过 | 通过 | 通过 | 通过 | 通过 | 16 |
| gpt-6-luna | WS | 通过 | 通过 | 通过 | 通过 | 通过 | 21 |
| gpt-6-luna | HTTP/SSE | 通过 | 通过 | 通过 | 通过 | 通过 | 15 |
| gpt-6-sol | HTTP/SSE | 通过 | 通过 | 通过 | 通过 | 通过 | 15 |

核心矩阵使用 `low`、标准速度、请求批准的本地项目权限。67 条主任务请求包含预热、工具续接、压缩和未成功触发中断的短回复；不是 67 条用户消息。四个结果文件均独立核验为 5900 分、3 类商品、三种正确图形，并确认压缩后的 `after_compact=true` 已落盘。四组均有真实失败工具输出与验证脚本成功回传。

另外各执行了一次高强度快速模式请求：Sol 经 HTTP、Luna 经 WS，均捕获到 `reasoning.effort=high`、`service_tier=priority` 和正常完成事件。Luna 的该任务另有一次预热。两项补充测试不要求重复生成核心矩阵文件。

本轮合计 78 条模型请求：Sol 32、Luna 38，另有 8 条 `gpt-5.6-luna` 标题请求。核心矩阵产生 4 次成功的主动中断，4 次恢复均正常完成。计数与逐请求结构见 [矩阵证据](matrix-evidence.json)；磁盘与隔离校验见 [验证结果](validation.json)。

HTTP 回退由隔离代理对 WS 握手显式返回 426 触发，两组各两次，**不是上游自然报错**。确认 HTTP 后移除注入，后续请求仍使用该任务的 HTTP 路径。最后的 Luna 快速测试重新使用正常 WS，未进行注入。

Luna 曾把测试标签 `CANCEL` 理解为取消指令，直接结束回复；Sol 也有一次短回复结束。这些完成的回复不计为中断。改用 `STREAM` 标签后，先观察正文 delta，再通过 UI 点停止。一次因回复先结束而失败的 Stop 点击也在记录中明确纠正。Luna 另遇到 Windows Store Python 启动别名失败，随后改用已有 Python 解释器恢复；子脚本退出码和外层工具退出码分开记录。

## 模型和请求差异

- 两个新模型的实抓配置均为 `use_responses_lite=true`，输入包含文本与图片，默认推理为 `medium`，该账号收到的默认上下文窗口为 **272000**。Sol 的返回列表为 low/medium/high/xhigh/max/ultra；Luna 为 low/medium/high/xhigh/max。列表存在不等于每档都经过请求验证，本轮验证的是 low 和 high。
- WS 的 Lite 标记继续位于 `response.create.client_metadata`，握手不带 Lite 头；HTTP POST 携带 `x-openai-internal-codex-responses-lite: true`。模型路由与实际选择一致。
- 本次普通请求的 `x-codex-beta-features` 为 `remote_compaction_v2`。旧项目固定值包含 `realtime_conversation`；该差异可能同时受功能配置影响，不能仅凭一次新 profile 认定此功能已被删除。
- 相对旧样本，turn metadata 新观察到 `analytics_enabled`、`model`、`reasoning_effort`；client metadata 新观察到 `guardian_credits_requested`。这些字段应按真实功能和请求状态解释，不能从字段存在推定固定值适用于所有账号。
- 版本、字段名、HTTP/WS 结构、模型配置均保存在 [协议证据](protocol-evidence.json)，不包含认证值、设备/会话标识或用户正文。

## 遥测

观察到 OTLP metrics、CES intake、Statsig/rgstr、statsc flush、CES 辅助端点和 Sentry envelope。OTLP resource 为 `codex-app-server`，`service.version=0.155.0-alpha.16`；SDK 仍为 Rust OpenTelemetry 0.31.0。

本次共观察到 **79 个指标名**。相对旧样本多观察到 8 个，其中以下四个旧版二进制已包含同名字符串，因此属于本轮扩大覆盖：

- `codex.approval.requested`
- `codex.rollout.persistence.append`
- `codex.rollout.persistence.item_bytes`
- `codex.rollout.persistence.turn_bytes`

另四个在旧版二进制中未检出，在新版中检出：

- `codex.rollout_compression.read`
- `codex.rollout_compression.read.io_duration_ms`
- `codex.websocket.continuation`
- `codex.windows_sandbox.runner_result`

二进制字符串检索辅助判断版本差异，不等于完整代码语义证明。旧样本中的部分指标本轮没有触发，也不代表已移除。网关若没有实际执行本地 rollout 或 Windows sandbox，不应凭空生成对应测量。

## 项目实现对齐（2026-09-23）

本地实现及生产更新均已完成。原有固定会话与后台表单工作区改动保留。生产验证见下文与 [发布验证](production-validation.json)。

- 默认 CLI/App 更新为 `0.155.0-alpha.16` / `26.917.51856`。新增 migration 240，只升级已知旧版默认环境；保留设备、安装 ID、系统、语言、时区和屏幕资料。自定义模板或版本不覆盖。新账号使用 v2 环境池，缓存或导入的旧快照也按同一规则处理。
- 新增 Sol/Luna 默认目录、模型映射预置、后台测试选项、推理与图片能力；保留原自动测试默认模型。桌面能力为默认 272000 / 最大 872000，上游实际同步的能力快照优先。Sol 包含 ultra，Luna 到 max；修复两个新模型的 `max` 被旧逻辑归为 `xhigh`。OAuth 可用 Lite；API Key 目录继续使用标准 Responses。
- 普通 turn、标题、WS 预热补齐 `analytics_enabled`、实际映射后的 `model`、实际请求 `reasoning_effort`；压缩 metadata 只补分析开关，省略 model/effort。HTTP header/body、WS 各帧同步，保留明确的分析关闭设置，服务配置关闭时优先关闭。传输遥测遵守该开关。
- 本代理的 Work beta 默认值改为 `remote_compaction_v2`。未提供实时对话能力，因此不声明 `realtime_conversation`；这不表示官方删除了该功能。
- 补充复核 `/wham/usage` 的 WebView 请求头，新增实抓的 `oai-app-brand: chatgpt`，并允许桌面业务透传保留该字段。该默认头只用于用量查询；reset-credit 路径未观察到，不额外添加。
- `guardian_credits_requested` 实抓为字符串 `"true"`；客户端传入时保留，代理没有自行请求 Guardian credits 的功能，故不会替未声明的客户端默认开启此字段。
- OTLP 中两个新模型使用真实模型名，继续按账号、账号代理和客户端快照分批。新增真实 WS 写入对应的 `codex.websocket.continuation` delta counter，记录 full/incremental 与 warmup/generation，以及可观察的 no_previous_request/other/incremental 原因；没有伪造 connection_closed 决策、本地 rollout、审批或沙箱事件。
- 再次核对 OTLP 原始请求：`https://ab.chatgpt.com/otlp/v1/metrics`、`content-type: application/json`、`user-agent: OTel-OTLP-Exporter-Rust/0.31.0`、`statsig-api-key`、`accept: */*`，没有请求体压缩头。现有 exporter 与这些实抓值一致；CES/Statsig/Sentry 的桌面 UI 事件不由代理伪造。

计价来自 [Sol 官方模型文档](https://developers.openai.com/api/docs/models/gpt-6-sol) 与 [Luna 官方模型文档](https://developers.openai.com/api/docs/models/gpt-6-luna)，不是从请求流量推算：

| 模型 | 输入 | 缓存读取 | 缓存写入 | 输出 |
|---|---:|---:|---:|---:|
| gpt-6-sol | $2 | $0.20 | $2.50 | $10 |
| gpt-6-luna | $0.10 | $0.01 | $0.125 | $0.50 |

均为每百万 token 的标准价格。Fast/priority 为适用价格的 2 倍，Flex 为一半；总输入超过 272000 时，整次请求输入及缓存价格 2 倍、输出 1.5 倍。资源目录、动态价格缺失回退和 Billing 硬编码回退均已补齐，保留运营者显式价格覆盖。

### 验证

- Go 1.27、`unit http2legacy` 下的 Codex / 模型 / 计价 / WS / 请求元数据相关回归通过，包含新加的迁移身份保持、分析关闭、模型切换、续接计数和计费边界测试。
- 前端模型列表、映射和选择器 23 项测试通过；`pnpm run typecheck` 通过。
- 使用独立本地 PostgreSQL 执行 [迁移测试](../../../deploy/tests/codex-client-profile-upgrade-test.sql)：17 个升级样本、8 个排除样本、全量 JSON 身份与环境保持检查以及幂等性检查均通过。测试数据库已停止。
- `deploy/tests/grok-http2-build-tags-test.sh` 通过；`grokhttp2` 默认与 `http2legacy` 路径通过；repository 的 `unit` 与 `unit http2legacy` 两组通过。
- Linux amd64、CGO=0、`embed,http2legacy` 发布构建通过；生产使用仓库 `tools/deploy_vps.py` 重新构建前端和后端并部署。
- 全量 service 测试不能宣称全绿：`TestOpenAIGatewayServiceForward_ResponsesLiteNormalizationIsLiteOnly/managed` 的错误输入测试得到空模拟上游响应并在 `openai_gateway_forward.go:1125` 崩溃。已在独立、未修改的 `c52dfa425` 检出中复现同一测试、同一行；属于既有问题，不是此次对齐引入。没有通过删除断言隐藏该失败。

### 生产更新与计费验证（2026-09-23）

- 通过正式脚本构建、上传、备份和替换，仅目标 PM2 应用发生一次 stop/start。PM2 守护日志确认一次启动；此操作保留 `restart_time` 计数，不能将计数未增加误判为没有替换进程。运行文件与上传文件 SHA-256 相同，构建包含 `embed,http2legacy`，内外网健康与后台页面均为 200。
- 1910 条已知旧默认客户端配置完成版本迁移；排除 CLI/App/template 三个升级字段后的整体摘要与更新前相同。配置文件、分组倍率、自定义价格策略及其他 PM2 进程保持不变。
- 线上动态价格在部署前已同步 Sol/Luna；此次补齐随发布的资源表、缺失回退、模型目录及协议实现。后台价格接口、部署后的资源文件与官方标准价一致。
- 首次线上冒烟发现原有账号显式映射仍缺 Sol/Luna，因此返回网关 404。已通过管理 API 为 6 个可用 OAuth 账号追加两个同名映射，先备份原映射，并核对其他映射、凭据和客户端配置保持不变。没有全局放宽白名单，也未修改停用账号的映射。
- 使用已有管理员测试 Key 再次调用 `/v1/models` 和 `/v1/responses`：两个新模型均可见、HTTP 200、收到 `response.completed`，响应模型与所选模型一致。各 4397 输入、5 输出 token、无缓存命中的请求分别落账 `$0.008844` / `$0.0004422`，输入费、输出费、总费和实际费逐项吻合。线上冒烟验证标准档；Fast/Flex 与长上下文边界由本地计价回归覆盖。
- 仅统计新进程启动后的日志，6 批 OTLP 经账号代理收到 202 acknowledgement，未发现导出失败、HTTP/2 不支持错误、panic 或 fatal。202 只证明收集端接收，不证明后续内部处理。没有伪造桌面 UI、审批、rollout 或沙箱遥测。
- 二进制与定价资源的脚本备份、模型映射备份、部署前后检查及原始冒烟结果保留在私有归档中，可用于回滚和复核。

## 范围

这不是所有模型、推理档位和功能的笛卡尔积测试。未重跑全部旧模型矩阵，也未覆盖其余推理档位、快速模式全场景、云/远程项目、全部权限模式、音视频、连接器、真实业务文件、长时断网、429/5xx 和登录失效。代理记录的是应用协议语义；它不能直接证明未经过代理时的 TLS/HTTP2 指纹与网关一致。

原始流量、TLS 密钥、认证状态和详细本地路径保存在 Git 忽略的 `.codex-official-desktop/2026-09-23/` 下。归档仅保留白名单证据；采集结束后停止独立副本和代理，保留可重复启动的脚本。
