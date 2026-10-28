# GPT-6.1 Sol 桌面 Work 抓包与 Sub2API 对齐

抓包日期：2026-09-30。对比基线：[2026-09-23](../2026-09-23_0.155.0-alpha.16/README.md)。

## 客户端与隔离方式

| 项目 | 本次实测 | 前次基线 |
|---|---|---|
| Windows Store 包 | 26.928.1915.0 | 26.917.6896.0 |
| App | 26.928.20755 | 26.917.51856 |
| 捆绑 codex.exe | 0.159.0 | 0.155.0-alpha.16 |
| WebView Chromium | 154.0.0.0 | 153.0.0.0 |
| Rust OpenTelemetry SDK | 0.31.0 | 0.31.0 |

运行中的主应用已经是更新检查确认的当前版本。本次复制官方安装目录，使用独立 user-data、CODEX_HOME、调试端口及进程级代理启动另一个可见窗口；只复用认证，不复制本地历史。显式指定副本内的官方 CLI，避免 Store 包身份探测改变 CLI 选择。

新版启用了 ASAR 完整性检查，旧的单实例补丁会导致启动失败。恢复官方 ASAR 后，独立数据目录即可成功并行启动，无需修改 ASAR。采集结束已停止副本、CLI 子进程及代理并释放端口，原主进程启动时间、安装 ASAR SHA-256、系统代理配置均与开始时一致。

## 实际 UI 验证

所有模型请求均由独立应用的 ChatGPT Work 本地项目界面发起。合成夹具包括一张红方块、蓝圆、黄三角图片及三行 CSV；金额合计 5900 分。没有业务文件或子代理参与。

| 路径 | 主模型请求数 | 核心验证 |
|---|---:|---|
| 原生 WebSocket，low、标准档 | 22，含 1 次 prewarm | 文本、图片、文件读写、执行、故意 exit 7 后恢复、手动压缩、压缩后验证、流式停止及继续 |
| HTTP SSE 回退，low、标准档 | 12 | 同上；文件验证与压缩后的落盘内容均通过 |
| HTTP SSE，high、快速模式 | 1 | 实际 `reasoning.effort=high`、`service_tier=priority`、HTTP 200、完整固定回复 |

请求数包含工具续轮，不等同于用户消息数。另有 3 次 `gpt-6-luna` 标题请求及 11 次应用辅助 `gpt-5.6-luna` 请求，单独统计。HTTP 路径通过本地代理临时对主任务和标题的 WS 升级各返回一次 426，再撤去拦截；这两次是明确的故障注入，不是上游自然报错。

两条核心路径均在实抓文本增量出现后点击停止，再发送新消息收到完整恢复回复。执行器返回的嵌套 JSON 输出经解码后确认 `MATRIX_VERIFIED`，并独立读取结果文件确认字段及压缩后的 `after_compact=true`。

详见 [matrix-evidence.json](matrix-evidence.json)。

## 协议与模型能力

- Work 推理的 HTTP/WS `originator` 实测为 `codex_work_desktop`；模型目录和 WebView 请求仍是 `Codex Desktop`。网关保留已知 Work 来源，其余来源继续使用桌面默认值。
- 普通 composer 用户 turn 新观察到 `client_type=desktop_app`、`source=codex`。prewarm、标题和手动 compaction 不带这两个字段，部分排队续轮也不带；不能将这些字段描述为所有请求必需。
- 普通本地项目仍带 `workspaces`、`workspace_kind=project`；`mcp_attribution` 新出现在部分 client metadata 中，网关原样保留，不构造归属值。
- `remote_compaction_v2`、手动压缩 `root_turn_id`、完整 attestation、HTTP Lite 头、WS payload 中的 Lite 标记及握手无 Lite 头均延续之前行为。
- 手动压缩的 `compaction` 对象为 `manual/user_requested/responses_compaction_v2/standalone_turn/memento`，与现有网关默认画像一致。它在本次导出的对比字段中多出，不能据此称为新版新增。
- 实抓目录中的 GPT-6.1 Sol 默认 reasoning 为 `low`，支持 `low/medium/high/xhigh/max/ultra`、text/image、Responses Lite、WebSocket、搜索工具及 multi-agent v2，子代理 reasoning 为 `xhigh`。这里只读取能力，没有启动子代理。
- 桌面目录默认上下文为 272000、最大 872000；不能把官方 API 页面标注的 1050000 直接作为桌面默认值。

完整白名单字段见 [protocol-evidence.json](protocol-evidence.json)。原始认证、请求内容、设备标识、TLS 密钥和本地路径不进入该证据文件。

## 遥测比较

本次观察到 75 个 Rust OTLP 指标名，较前次样本多 5 个：`codex.sse_event`、`codex.sse_event.duration_ms`、`codex.startup_prewarm.websocket_check`、`codex.task.compact`、`codex.transport.fallback_to_http`。其中只有 `codex.startup_prewarm.websocket_check` 未在前次 CLI 二进制检出同名字符串；其余四个之前已存在，采样差异不能当作版本新增证明。

OTLP 仍使用 `ab.chatgpt.com/otlp/v1/metrics`，SDK 0.31.0，service.version 为 0.159.0。保留现有真实网关生命周期遥测并加入新模型识别，没有为无法观测的桌面 UI、审批或沙箱活动制造指标。收集端的 202 仅代表接收请求。

## 本地实现与验证

模型目录、白名单预设、别名、测试账号请求、Lite/图片能力、遥测模型识别及模型定价均已加入 `gpt-6.1-sol`。按[官方模型文档](https://developers.openai.com/api/docs/models/gpt-6.1-sol)核对每百万 token 的标准价：输入 $2、缓存读取 $0.10、缓存写入 $2.50、输出 $10；priority 为 2 倍、flex 为 0.5 倍，超过 272000 输入 token 后整段输入/缓存 2 倍、输出 1.5 倍。资源表、动态价格缺失回退和硬编码回退均覆盖。

客户端默认配置升级为 `capture-20260930` / `windows-v3-*`。迁移 241 仅升级已知的 9 月 23 日默认版本组合，保持设备标识、OS、屏幕、地区及其他字段；自定义版本跳过。内存配置也支持已知 v1→v2→v3 连续升级。

- 前端模型/账号表单 60 项测试及 `vue-tsc --noEmit` 通过。
- 后端 Codex、GPT6、模型、计价、metadata、turn-state、WS、遥测针对性 service 测试，以及 OpenAI 包和管理员模型目录测试通过。
- 迁移 241 在临时 PostgreSQL 18 中通过 17 个升级样本、8 个排除样本、完整环境字段保持及幂等检查；临时数据库已停止。
- 仓库规定的 HTTP/2 构建标签检查、默认及 `http2legacy` 的 grokhttp2 测试、`unit` 及 `unit http2legacy` 的 repository 测试全部通过。
- Linux amd64、CGO=0、`embed,http2legacy` 发布构建及正式脚本 `--build-only` 通过。
- 没有宣称全量 service 测试通过：上次已在未修改基线复现的 `TestOpenAIGatewayServiceForward_ResponsesLiteNormalizationIsLiteOnly/managed` 空模拟响应 panic 仍是已知独立问题，本次使用针对性回归。

## 生产更新与验收

2026-09-30 12:54（Asia/Shanghai）使用 `tools/deploy_vps.py` 重新构建前后端并部署，仅 `sub2api-pool-bald-app` 发生一次预期 stop/start。PM2 新 PID 为 880834，守护日志确认一次启动；这种 stop/start 保留自动重启计数 0。上传、安装和运行中二进制 SHA-256 均为 `2c19079a789e294b8d6f40c67ca02193e618b63f5ad5e096d67810b03a623a5f`，包含 `embed,http2legacy`。

- 1913 条 OAuth 客户端配置升级至 CLI 0.159.0 / App 26.928.20755；排除 CLI/App/Chromium/template 四个升级字段后的整体环境摘要保持一致。配置文件、分组价格策略及其他 PM2 进程也保持一致。
- 为 3 个仍可用且已映射旧 Sol 的 OAuth 账号追加同名 `gpt-6.1-sol` 映射，使用管理 API，先备份后修改。原有映射、其他凭据和客户端配置逐项保持一致；未启用停用账号。
- 内网与外网 `/health`、`/admin/accounts` 均为 HTTP 200，前端引用资源返回 200。只检查新进程日志，未发现 ordered HTTP/2 unsupported、panic、fatal 或遥测导出失败，已观察到 OTLP 202 接收确认。
- 使用既有管理员测试 Key，`/v1/models` 可见新模型；标准档及 priority 请求均 HTTP 200、模型一致、收到 `response.completed` 及预期 `OK` 回复。

| 请求档位 | 非缓存输入 | 缓存读取 | 输出 | 实际记录费用 |
|---|---:|---:|---:|---:|
| 标准 | 4397 | 0 | 5 | $0.008844 |
| priority，Work originator | 557 | 3840 | 5 | $0.003096 |

按数据库 token 与每项价格重算均一致，第二条的缓存费用为 `3840 × $0.20 / 1M = $0.000768`。未在生产发送长上下文或 Flex 样本，这些边界由本地回归覆盖。

Fast 请求的最终响应 `service_tier` 在官方 UI 抓包及生产测试中都回显 `default`。这与现有 Codex OAuth 计费约定一致：保留实际发往上游的 priority 档，单独观察回显字段；不能把这个字段或一次请求成功当作实际加速效果的证明。公共 API 路由的按响应档位降价规则保持不变。

脚本生成的二进制及定价备份分别保留在生产 `.deploy-tmp/sub2api.backup.20260930T045429Z.pre-c52dfa425-dirty`、`model_prices_and_context_window.backup.20260930T045429Z.json`；模型映射备份为 `model-mapping-before-20260930-045501.json`。脱敏验收摘要见 [validation-evidence.json](validation-evidence.json)。

## 范围

这是新模型的两条核心路径和 high/priority 补充验证，不是全部模型、强度、权限与连接器的组合穷举。未覆盖所有长时断网、429/5xx、登录失效、云/远程项目及音视频情形。代理证据反映应用层语义，不能单独证明原生 TLS 或 HTTP/2 指纹。

原始抓包、认证、启动/停止脚本、测试日志及生产核验放在 Git 忽略的 `.codex-official-desktop/2026-09-30/`，只归档脱敏结论。
