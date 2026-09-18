# 0.155.0-alpha.2.6 脱敏抓包记录（2026-09-17）

本次重点为操作本地文件的 Work/Codex。请先阅读 [本地 Work 专项与历史对齐](WORK.md)：
已补齐本地项目的读取、修改、执行、连续任务和压缩后续接，并按 Rust 上报进程核实遥测归属。

逐模型补测及未覆盖项见 [Work 模型与场景覆盖矩阵](MATRIX.md)。首轮出现某个模型名不代表其用户任务已测试；自动标题和预热单独计数。

本文保留首轮 13 组带标记的测试及压缩、用量页、设置页记录。首轮有 21 个 Work 模型请求；
S11 的 1 个普通 Chat 请求仅为额外对照，不是主要采集对象，也不纳入 Work 协议基线。
模型链路总体延续 0.151，手动压缩 metadata 增加 `root_turn_id`。

采集阶段只新增抓包结论；随后按用户要求完成的网关请求与遥测修改见 [实现对齐说明](IMPLEMENTATION.md)。机器可读的首轮脱敏计数见 [evidence.json](evidence.json)。

## 版本与采集环境

| 项目 | 本次 | 2026-08-30 基线 |
|---|---|---|
| Store package | `26.911.7940.0` | `26.825.5331.0` |
| App 内部版本 | `26.911.61220` | `26.825.41651` |
| codex-rs | `0.155.0-alpha.2.6` | `0.151.0-alpha.7.1` |
| WebView UA 中的 Chrome | `153.0.0.0`，`CodexBrowser` 前缀 | Chrome 151，同样有该前缀 |
| 系统 | Windows `10.0.26100` x86_64 | 同一平台 |

- 使用复制的程序、独立用户目录和独立 `CODEX_HOME`，由用户手动登录；正在工作的原应用保持运行。
- 仅复制副本的单实例判断作等长补丁，原安装包未改动。通过该副本的环境变量和 Chromium 参数接入本地 mitmproxy；系统代理设置保持原值。
- 默认采集原生 WS。S08 短暂对目标 responses WS 握手注入 426，验证 HTTP 回退；共注入两次，完成后已移除开关。
- 模型请求与遥测正文保存在 Git 忽略目录 `.codex-official-desktop/2026-09-17/`；本文和 evidence 文件只包含人工筛选的字段、类型和计数。
- 这是应用层请求画像。MITM 会改变代理到服务器的 TLS 连接，不能用它证明原客户端的直连 TLS 指纹。

## 首轮覆盖场景（S01–S13）

| 标记/操作 | 场景与可观察结果 | 抓包证据 |
|---|---|---|
| S01 | 新建 Codex 对话，Sol 低强度；短答成功 | 用户 prewarm + turn；Luna 自动标题请求 |
| S02 | 同一对话连续追问成功 | 同一 WS 连接，增量 input + `previous_response_id` |
| S03 | 读取专用合成文本文件，返回标记和数值计算结果 | 工具调用及工具返回后的继续请求 |
| S04 | 通过文件选择器附加合成 PNG，识别颜色与形状成功 | `input_image`，后续模型切换时携带完整历史 |
| S05 | 同对话切到 Terra 高强度，短答成功 | 复用 WS，payload 模型改变，重新发送上下文 |
| 手动 `/compact` | WS 压缩成功，界面显示已压缩 | `compaction_trigger` 与 `response.compaction.compacting` |
| S06 | 压缩后追问文件标记和图形，回忆正确 | 新上下文含 `compaction` 项 |
| S07 | 切换到非 Lite 的 GPT-5.5，短答成功 | 无 WS Lite 标记；另抓到切换附近的自动压缩 |
| 用量 / 常规设置 | 打开页面并读取 | 额度、重置积分库存及页面初始化请求；未执行购买或积分消耗 |
| S08 | 新对话受到合成 426 后自动回退并完成短答 | HTTP/2 POST、zstd 请求、SSE 正文；用户和标题各一次回退 |
| HTTP `/compact` | HTTP 路径下压缩成功 | 同一 responses 端点，压缩请求返回完整 SSE |
| S09 | 思考期间点击停止，界面显示已停止 | 客户端 HTTP/2 CANCEL；此样本尚受记录器缓冲影响 |
| S10 | 修正记录器后，中断会话重新短答成功 | 流式 sidecar，`response.completed` |
| S11 | 切到普通 ChatGPT「聊天」，短答成功 | `/backend-api/f/conversation`，HTTP/2 200、delta SSE 与 `[DONE]` |
| S12 | 可见正文持续输出时点击停止 | 保留 438,206 字节、1,095 个文本 delta；CANCEL，无 `response.completed` |
| S13 | S12 后继续发送新指令，短答成功 | 流式响应正常结束，`response.completed` |

首轮统计快照包含 21 次 Codex 模型请求：14 个 WS `response.create` 和 7 个 HTTP POST。
其中包括 prewarm、标题生成、工具续接和压缩，不能解释成 21 次用户提问。
14 个 WS 请求均观察到 `response.completed`；HTTP 中两次为完成前主动停止。
S13 还在收到完成事件后出现客户端 CANCEL，属于完成后的关闭记录，不能计作一次失败。
普通 ChatGPT 请求单独计数，不混入 Codex 模型请求统计。

首轮模型/文件测试使用无项目任务、默认审批配置和合成资料。本地项目读写已在 W01–W03 补齐，
见 [Work 专项记录](WORK.md)。语音、外部应用授权、支付、真实业务项目改动和长时间断网重连未覆盖。

## 0.151 → 0.155 请求差异

| 项目 | 本次观察 | 与基线的关系 |
|---|---|---|
| Rust UA | `Codex Desktop/0.155.0-alpha.2.6 (Windows 10.0.26100; x86_64) dumb (Codex Desktop; 26.911.61220)` | 版本更新 |
| WS | `GET /backend-api/codex/responses`，HTTP/1.1 升级 101 | 延续 |
| WS beta | `responses_websockets=2026-02-06` | 本次实抓值 |
| Codex beta features | `realtime_conversation,remote_compaction_v2` | 本次实抓值 |
| Routing hint | `model=<握手或 POST 的模型>` | 延续；WS 复用后以每条 payload 的 model 为准 |
| Attestation | `v=1, s=0` 且包含 token | 延续；不归档 token 值 |
| Cookie | WS 握手不带，HTTP 回退 POST 带会话 Cookie | 延续 |
| Lite | 首轮 Sol/Terra/Luna 的 WS 标记在 `client_metadata`；HTTP Sol/Luna 带 Lite 头 | 扩展矩阵又覆盖 Astra，并单测 GPT-5.5 的 WS/HTTP 非 Lite，见 [矩阵](MATRIX.md) |
| GPT-5.5 | WS payload 不含 Lite 标记 | 与基线一致 |
| `access_programs` | 普通请求出现 `{"cyber":"<string>"}`，标题请求未见 | 旧实抓未见，但新旧二进制都含该字段名，不能认定为 0.155 新实现 |
| 手动压缩 metadata | WS 和 HTTP 均包含 `root_turn_id` | 旧 WS/HTTP 手动压缩均无此字段，是实抓差异 |
| 自动压缩 | `trigger=auto`、`reason=comp_hash_changed`、`phase=pre_turn` | 本轮补充样本，旧场景覆盖不足，不据此认定新增 |
| `reasoning.context` | `all_turns` | 旧原始样本已有 |
| `stream_options.reasoning_summary_delivery` | `sequential_cutoff` | 旧原始样本已有 |

普通 turn metadata 的键集合没有发现新的通用键；首轮无项目任务使用
`workspace_kind=projectless`、`sandbox=windows_elevated`、`sandbox_mode=workspace-write`。
旧测试的项目任务为另一组权限设置且带 `workspaces`，这里的缺失不能视为版本删字段。
W01–W03 项目补测已确认 `workspaces` 与 `workspace_kind=project` 均正常出现。

手动压缩仍使用：

```json
{"trigger":"manual","reason":"user_requested","implementation":"responses_compaction_v2","phase":"standalone_turn","strategy":"memento"}
```

WS 手动压缩以 `previous_response_id` 关联历史，input 只含压缩触发项；HTTP 压缩发送所需历史。
自动压缩样本还包含 `turn_trigger`、`workspace_kind`，不要把手动压缩模板套给所有压缩场景。

## 遥测：新增证据与采样差异

首轮快照解码 OTLP JSON 得到 77 个不同指标名，旧样本为 68 个。
其中 11 个名字在旧抓包中未出现。进一步按精确字符串比较两个版本的 `codex.exe`：

| 本次新增观察的指标 | 旧程序是否含同名字符串 | 判断 |
|---|---|---|
| `codex.app_server.codex_home.size_bytes` | 否 | 有新版新增埋点的静态证据 |
| `codex.plugins.loaded_cache.event` | 否 | 同上 |
| `codex.plugins.loaded_cache.load.duration_ms` | 否 | 同上 |
| `codex.plugins.loaded_cache.request` | 否 | 同上 |
| `codex.plugins.loaded_cache.wait.duration_ms` | 否 | 同上 |
| `codex.thread_history.sqlite_projection` | 否 | 同上 |
| `codex.windows_mxc.available` | 否 | 同上 |
| `codex.windows_sandbox.private_desktop` | 否 | 同上 |
| `codex.windows_system_config.namespace_squatting_probe` | 否 | 同上 |
| `codex.hooks.run` | 是 | 旧实现已存在，本次才观察到上报 |
| `codex.hooks.run.duration_ms` | 是 | 同上 |

这为其中 9 个指标名提供了“新版程序存在、旧版程序未检出”的额外证据。
精确字符串差异不等于完整代码审计，也不能单独说明服务端如何使用这些指标。
补测复核了这 11 个指标的 OTLP resource：均来自 Rust `codex-app-server`，归属 Work 后端。

扩展模型矩阵后又观察到 `codex.mcp.call`、`codex.mcp.call.duration_ms`、`codex.mcp.call.error`、
`codex.rollout.size_bytes`，指标总数达到 81 个，相比旧样本共有 15 个此前未观察到的名字。
这 4 个名字在新旧程序中均存在，也均来自 Rust `codex-app-server`；不能据此算作新版新增埋点。
更新后的白名单证据见 [matrix-telemetry.json](matrix-telemetry.json)。首轮的 77/11 计数保留为阶段快照。

端点按覆盖情况分组：

| 端点 | 观察 |
|---|---|
| `chat.openai.com/ces/v1/telemetry/intake` | 新旧均有；NDJSON 应用日志 |
| `chatgpt.com/ces/v1/rgstr` | 新旧均有；gzip JSON，包含实验曝光及 UI/对话事件 |
| `ab.chatgpt.com/v1/initialize` | 新旧均有；实验配置初始化 |
| `ab.chatgpt.com/otlp/v1/metrics` | 新旧均有；OTLP 指标，当前样本均为 202 |
| `chatgpt.com/ces/statsc/flush` | 本轮新增观察；计数器、直方图 |
| `chatgpt.com/ces/v1/{i,p,t}` | 本轮新增观察；身份、页面、事件结构，含匿名/用户标识字段 |
| `chatgpt.com/ces/v1/m` | 本轮新增观察；series 结构 |
| `chatgpt.com/ces/v1/projects/oai/settings` | 本轮新增观察；采集相关设置请求 |
| `chatgpt.com/ces/v1/telemetry/intake` | 本轮新增观察；ChatGPT WebView 日志 |
| `o33249.ingest.us.sentry.io/api/<project>/envelope/` | 本轮新增观察；2 次请求，分别 200 与 429 |

后六组包含登录页和普通 ChatGPT WebView 的覆盖差异，不据此宣称这些端点由此次升级首次引入，
也不将其计入 Work 新增遥测结论。
`rgstr` 中还观察到页面访问、会话开始/完成、流完成、首屏绘制耗时等事件。

已解码的应用日志包含版本、平台、窗口、插件、模型、token 计数、请求耗时和会话关联字段。
`executablePath` 与 `spawnCommand` 在本次样本中确实包含本地绝对路径。
对已解码遥测正文检查若干合成测试标记，未检出这些标记；该检查不是“绝不上传正文”的证明，
没有涵盖所有混淆、二进制或嵌套编码内容。

## 对照附录：普通 ChatGPT

S11 的普通聊天请求经过 `/backend-api/f/conversation/prepare` 和
`/backend-api/f/conversation`，模型标识为 `gpt-5-6`。
正文包含 `action`、`messages`、`model`、`local_function_signatures`、
`supported_encodings`、`client_prepare_state`、时区等字段。

响应为 HTTP/2 200，包含 `delta_encoding` / `delta` 事件及 `[DONE]`；界面显示预期短答。
此后记录到客户端 CANCEL，但它发生在已收到 `[DONE]` 的链路上，不能把所有 CANCEL 都归类为模型失败。
Work 流式停止的 S12 则没有完成事件，是不同情况。

## 记录器修正与证据边界

本轮 Codex HTTP Lite 响应没有 `Content-Type`，实际正文却是 SSE。
记录器原先只按响应 `Content-Type` 判断流式，导致 S08、HTTP 压缩与 S09 有缓冲效应；
这些样本可用于请求字段与正文结构，不能用于原生首包/增量时序。

随后增加请求 `Accept: text/event-stream` 判定。S10、S12、S13 重新验证了逐块转发、
部分流保留和中断恢复；S12 在真实可见输出后停止，修复后的数据足以验证主动取消行为。
普通 ChatGPT 的流也保存为 sidecar。开放中的 WS 和被取消的 SSE 应以增量 sidecar 为准，
不能只读最终完成的 mitm flow。

采集器默认不改请求正文、不屏蔽遥测。S08 的合成 426 有独立事件标记，已结束；
同一 Codex 会话随后继续走 HTTP，因此撤去拦截后仍出现 HTTP 是本轮观察到的客户端行为。

## 归档约束

原始日志、账号状态、Cookie、Authorization、attestation token、TLS key log、
请求 ID、本地路径和对话附件只保留在本机忽略目录。此目录只提交上述脱敏结论与计数。
后续实现对齐应按模型、传输与请求种类分别处理，避免把账号/配置条件字段硬编码成所有请求的默认值。
