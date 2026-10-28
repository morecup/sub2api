# 2026-10-28：0.165.0-alpha.1.2 全模型抓包

本轮使用官方桌面应用的独立副本，对账号目录中 8 个可选模型逐个完成 WebSocket 和 HTTP SSE 实测。基础双轮文本矩阵共 16 组；图片、工具、压缩和停止恢复矩阵另有 16 组。所有结论按请求和响应中的 model、实际传输、响应状态和本地验证结果确认。

## 版本与隔离

| 组件 | 上次基准 | 本轮 |
|---|---|---|
| Windows 包 | 26.928.1915.0 | 26.1023.6123.0 |
| App / Electron release | 26.928.20755 | 26.1023.50603 |
| bundled codex.exe | 0.159.0 | 0.165.0-alpha.1.2 |
| WebView Chromium | 154.0.0.0 | 156.0.0.0 |
| Rust OTel SDK | 0.31.0 | 0.31.0 |

应用内更新检查返回 `up_to_date`；本轮从已安装官方包复制运行文件，并显式选择其中的 bundled CLI。未修改官方 `app.asar`。独立副本以 `Start-Process -WindowStyle Hidden` 后台启动，使用单独 profile、CODEX_HOME、代理和调试端口。通过 CDP 操作真实界面；模型推理由官方客户端发出，没有手工构造推理请求代替 UI 测试。

独立副本只复用现有认证文件，未复制历史对话。测试读写限定在合成 fixture。原始流量、认证、TLS 密钥、完整模型指令、调试日志及启动脚本保存在 Git 忽略目录 `.codex-official-desktop/2026-10-28/`。本目录仅提交白名单脱敏证据。

收尾已关闭独立客户端与代理，调试和抓包端口已释放；核对原主进程启动时间、安装包 ASAR 哈希与系统代理，均保持不变。

## 测试矩阵

每个模型、每种传输都执行：

1. 两轮文本对话，核对返回标记与 `17 + 25 = 42`。
2. 上传合成图片，识别从左到右的红色正方形、蓝色圆形、黄色三角形。
3. 读取 CSV，执行故意返回 exit 7 的命令，恢复后写 JSON；独立核对总额 5900、3 项与图形列表，并检查工具校验输出。
4. 通过 `/compact` 手动压缩，观察实际 `request_kind=compaction` 成功完成；压缩后继续读写同一个 JSON。
5. 在主任务收到文本增量后点击停止，再发送恢复消息；同时核对被中断响应和后续完整响应。

HTTP 组通过对独立实例的 WS upgrade 临时返回 426 触发官方回退，首轮后撤去拦截。后台 `thread_sample` 可以重新走 WS，因此标题和回复建议请求单独统计，不混入主任务传输结果。

| 模型 | 默认强度 | Desktop context / max | WS 完整流程 | HTTP 完整流程 | 深度请求数 WS / HTTP |
|---|---|---|---|---|---|
| `gpt-6.1-sol` | low | 500,000 / 872,000 | 通过 | 通过 | 13 / 10 |
| `gpt-6-astra` | medium | 272,000 / 872,000 | 通过 | 通过 | 11 / 10 |
| `gpt-6.1-terra` | low | 500,000 / 872,000 | 通过 | 通过 | 12 / 12 |
| `gpt-6-sol` | medium | 272,000 / 872,000 | 通过 | 通过 | 11 / 12 |
| `gpt-6-luna` | medium | 272,000 / 872,000 | 通过 | 通过 | 13 / 13 |
| `gpt-5.6-sol` | low | 272,000 / 872,000 | 通过 | 通过 | 15 / 13 |
| `gpt-5.6-terra` | medium | 272,000 / 872,000 | 通过 | 通过 | 13 / 14 |
| `gpt-5.6-luna` | medium | 272,000 / 872,000 | 通过 | 通过 | 14 / 13 |

请求数包括主任务预热、工具续接、压缩、取消及恢复。两种传输的数量不同不表示漏测。表中默认强度取自模型目录，实测强度和档位以每条 `samples.effort` / `service_tier` 为准；UI 会跨模型保留所选强度。自动化调试及重复尝试保留在本地原始记录中；`matrix-evidence.json` 只保留每个用例最后一次实测窗口。最初仅 Terra 执行深度流程，随后已将相同流程补齐到其余 7 个模型。

目录另有隐藏的 `gpt-reserve` 和 `codex-auto-review`，它们没有普通模型选择入口，仅记录能力，不计入上述 8 个可选模型的推理覆盖。未遍历每种 reasoning effort、Ultra 委派、长上下文、所有套餐或所有错误码。

Fast 对 8 个模型的 WS/HTTP 分别采样，共 16 组：请求 `service_tier=priority`，服务端样本回报 `default`。这证明开关到请求字段的映射，不能据此认定获得了 Priority 服务或确定其延迟。

## 协议和目录变化

- 普通请求、预热和手动压缩包含 `history_initialization=new`。回复建议使用 `thread_source=thread_sample`、`history_initialization=warm_fork` 与 `forked_from_thread_id`；实际预测请求附带 `turn_trigger=composer_predictions`、两个 prediction source ID 和 `prediction_trigger=live`。
- 普通 Work 来源仍为 `originator=codex_work_desktop`。HTTP Lite 标记在请求头；WS Lite 标记仍位于 `response.create.client_metadata`，不放进 WS 握手。
- 8 个可选模型均支持 text/image 与 Responses Lite。6.1 Sol/Terra 的桌面默认窗口为 500,000，其他可选模型为 272,000；最大窗口均为 872,000。这是账号桌面目录值，与公开 API context 上限分别处理。
- Terra 默认 low，支持 low/medium/high/xhigh/max/ultra，multi_agent_version=v2、multi_agent_reasoning_effort=xhigh。
- 未观察到新的顶层请求字段或 client_metadata 字段。相对于旧采样，新出现的 metadata/telemetry 名称不自动等于该版本首次引入。

本轮观察到 85 个 OTLP 指标名，比旧采样多 14 个，详见 `protocol-evidence.json`。旧抓包运行目录下未找到旧版 CLI 二进制，因此旧版字符串存在性记录为 null，不声称这些指标全部是新代码。没有为未在网关发生的本地编辑器操作伪造遥测事件。

代理记录的是应用协议；不能用 MITM 数据证明官方直连的 TLS 指纹或 HTTP/2 HPACK 完全相同。后续已另用本次内置 CLI、空 CODEX_HOME、假认证和本地 TLS 服务采集 HTTP/WS ClientHello 与 HTTP/2 连接前导，见 [transport-evidence.json](transport-evidence.json)。该离线测试阻断外部转发；HTTP 测试响应完成，WS 在记录 TLS 后由测试服务返回 400，不作为真实 WS 推理成功证据。

## 仓库对齐与验证

已补齐 Terra 的目录、别名、Lite/图像能力、默认强度、上下文和定价，更新桌面 CLI/App/WebView 版本，加入新 metadata 的受限解析。迁移 242 仅升级精确匹配旧默认版本组合的 profile，保留设备/安装 ID、环境和自定义画像，发送对应 scheduler outbox 失效事件。

Terra 标准公开 API 单价（美元 / 百万 tokens）：输入 1、输出 5、缓存读取 0.05、30 分钟缓存写入 1.25；Fast 2 倍，Flex/Batch 半价。输入超过 272k 后，整次请求输入/缓存 2 倍、输出 1.5 倍。依据 [官方模型文档](https://developers.openai.com/api/docs/models/gpt-6.1-terra)，本地覆盖了定价表与两种回退来源、别名、档位和长上下文边界。

验证清单、构建哈希与隔离检查见 [validation-evidence.json](validation-evidence.json)。完成 default/http2legacy 传输和 repository 检查、相关 service 回归、模型目录测试、前端 60 项测试和类型检查。迁移在隔离本地 PostgreSQL 上验证了 17 个升级样本、8 个排除样本、身份/环境保留和幂等性。发布构建使用 Go 1.27、`embed,http2legacy`，并执行 canonical `tools/deploy_vps.py --build-only`。

本地验证后，已按用户要求部署生产并完成下文的真实转发验证。相关 service 测试通过不代表整个 service 包全量测试通过。

## 请求对齐补查与修复

补查直接使用上述 8 个模型的完整抓包，另补采内置 CLI 的直连 TLS/H2 结构。下列差异已落到请求构造和传输代码，不只是更新版本常量。

| 环节 | 抓包或审计发现 | 修复与验证 |
|---|---|---|
| 时间上下文 | user 的 `environment_context` 与 developer 的 `codex_apps_client_time_context` 同时存在；原逻辑漏改后者 | 两种独立时间块均按所选账号出口时区更新日期；跨日、DST、压缩、HTTP、HTTP→WS、两种 WS 入站模式、续轮和切账号路径覆盖。引用示例、工具输出和其他角色保持原文 |
| HTTP 推理头 | Work 的 `originator` 在窗口元数据之前，续轮 `x-codex-turn-state` 紧随 beta features | 更新有序 HTTP/2 profile；生产 `http2legacy` transport 经本地服务器解码实际 HEADERS，与独立抓包字段序列比较 |
| WS 握手 | `cookie` 在 WebSocket key 后，`originator` 在 beta features 后 | 更新握手排序器；完整字段序列与本次抓包对照；保持 Lite 只出现在帧元数据中 |
| TLS | HTTP/WS 签名算法尾部新增 `0x0904`、`0x0905`、`0x0906` | 更新 Codex profile；测试实际生成的 ClientHello。cipher suites、groups、ALPN、H2 SETTINGS 和窗口值与旧基线一致 |
| 原生模型目录 | UA 没有推理 wrapper 的 App 版本后缀，Accept 为 `*/*`；原请求绕过了统一 TLS 上游 | 独立 Native UA 与目录头顺序，默认 OAuth 路径接入 `DoWithTLS`；保留插件接管、自定义 profile 与 API Key 逻辑 |
| 用量查询 | 增加 `oai-did`、`sec-ch-ua*`、pricing chooser、window type、web frontend 头 | `/wham/usage` 使用账号设备 ID 与浏览器版本；修正 WebView 顺序。未将 usage 专属字段加到 reset-credit 请求 |
| Desktop 业务代理 | 入站旧 browser hints、baggage release 可能与已更新 UA 冲突 | 已存在的对应字段按账号 profile 同步；区分带 App 后缀的 Electron UA 与 `CodexBrowser` UA；locale 与出口快照一致 |
| WS→HTTP 回退 | 旧帧可能残留 WS 时间戳与 Lite 元数据 | HTTP 清除这两个传输专用字段；元数据同步局部改写 JSON，避免将工具参数/历史中的大整数转成 float64 |

新增 [request-contract.json](request-contract.json) 保存脱敏头字段、固定值与时间块角色；回归对 8 个模型分别构造 HTTP 和 WS 请求，并核对压缩正文、头/正文 metadata、Lite 位置与 session/cache 标识的一致性。完整 service 回归所选范围在 default 和 `http2legacy` 下均通过；Grok 构建标签、两套 grokhttp2/tlsfingerprint 与 repository 测试通过。首次扩大回归发现两处旧测试假设（目录 UA、OAuth 测试桩绕过上游抽象），调整后重跑通过。

补查后的本地预构建通过 canonical `tools/deploy_vps.py --build-only --skip-frontend-build`，SHA-256 为 `96f71f6488d630a6bc1af9ea45daccae1aff34ae30e22448dee0af29627464a7`。随后正式部署重新构建前端和服务，生产制品见下一节。

当前证据仍不表示所有端点逐字节等同官方客户端：浏览器 TLS 未重新直接采集，完整 HPACK 动态表演进未做等同性证明，14 个新增观察到的本地编辑器指标没有在网关伪造。

## 生产部署与全部模型实测

2026-10-28 17:01（Asia/Shanghai）通过 canonical `python tools/deploy_vps.py` 发布到 `高配置服务器` 的 `sub2api-pool-bald-app`。正式构建使用 Go 1.27、`embed,http2legacy`、CGO=0、linux/amd64、trimpath；运行文件与上传制品 SHA-256 均为 `531502269d266c6a8f6a3fb38d4e909895b00bf9820e1a9e11c567bf99925882`。PID 从 3213824 变为 1186944，仅一次预期 stop/start，自动重启计数保持 0。二进制与 pricing 回滚备份保留在服务的 `.deploy-tmp/` 中。

迁移将 1916 条匹配旧默认版本的画像升级到 CLI `0.165.0-alpha.1.2`、App `26.1023.50603`、Browser `156.0.0.0`；画像中其他字段的摘要保持一致。测试组两条现有可用路线补上 `gpt-6.1-terra` 自映射，原映射、凭据和画像经前后校验保持一致，映射备份已保存。生产配置、分组计费策略及其他 PM2 进程保持不变。

所有测试通过公网 `https://bald.lulucoding.com/v1/responses`，使用已有管理员测试 key，每个模型进行 low 强度文本首轮和带历史续轮。生产账号的 WS 模式为 off，因此此处验证的是实际 HTTP 流式转发路径。

| 模型 | 首轮 | 续轮 | 返回模型名 | 出站头与时区 |
|---|---|---|---|---|
| gpt-6.1-sol | 复测通过 | 通过 | 一致 | 通过 |
| gpt-6-astra | 通过 | 通过 | 一致 | 通过 |
| gpt-6.1-terra | 通过 | 通过 | 一致 | 通过 |
| gpt-6-sol | 通过 | 通过 | 一致 | 通过 |
| gpt-6-luna | 通过 | 通过 | 一致 | 通过 |
| gpt-5.6-sol | 通过 | 通过 | 一致 | 通过 |
| gpt-5.6-terra | 通过 | 通过 | 一致 | 通过 |
| gpt-5.6-luna | 通过 | 通过 | 一致 | 通过 |

合计 17 次网关请求、16 次完成、16 条 usage 记录。6.1 Sol 第一次上游返回 429 `turn_rate_limit_exceeded`（Codex is at capacity），网关流中返回 `gateway_concurrency_limit`；稍后两轮复测完成。此失败保留在证据中，不将最初的 HTTP 200 误算为推理成功。

本次新增默认关闭、仅管理员测试 key 可启用的脱敏诊断，在真实上游调用后检查出站请求对象，记录 18 份快照。逐份验证了版本和 UA 的 CLI/App 部分、originator、beta features、HTTP Lite、zstd、头/正文 turn metadata、session/cache 对应关系，以及 HTTP 中没有 WS 专属 metadata。账号原有 Windows build `10.0.26200` 保留；官方采样主机是 `10.0.26100`，不为追求字符串一致而改写账号 OS 身份。诊断头不转发给上游，令牌、Cookie 值和提示正文不写入快照。

独立探测确认测试请求所用代理出口为美国 `America/New_York`；虽然账号持久化时区及入站样本为 `Asia/Shanghai`，实际出站 locale 为 `en-US`，user `environment_context`、developer `codex_apps_client_time_context` 和 attestation 的区域字段均一致。样本中故意传入的 `2001-01-01` 已替换为出口当地日期 `2026-10-28`。这验证了请求级覆盖，未改写账号保存的环境。

内网及公网健康/管理页面均 HTTP 200。生产文件日志关闭，改查实时 `ops_system_logs` 和日志写入健康：发布后 93 条索引日志中，无 `ordered HTTP/2 fingerprint unsupported`、panic 或 fatal，日志队列无丢弃和写入失败。诊断快照保存到本地忽略目录后清理服务器副本，回滚备份保留。详细机器可读证据见 [production-validation.json](production-validation.json)。

生产出站快照证明实际传输边界处的头字段和正文；HTTP/2 顺序核对的是所选传输 profile，实际 HEADERS 编码另有本地 wire 回归，不声称本轮对生产 TLS 做了 MITM 抓包。本节不覆盖生产 WS、每个账号或所有强度/工具/图像/压缩组合。

## 真实线上请求内容审计（19:43）

2026-10-28 19:43:11（Asia/Shanghai）从生产服务端口 18082 被动取得一条完整 HTTP 入站请求，随后停止监听。初次仅监听 loopback 未取到流量；检查发现实际连接来自远端代理，改为仅过滤服务端口后取得样本。该请求自报 Mac OS 27.0.1 / arm64、Codex CLI `0.165.0-alpha.1.2`、App `26.1023.50603`，模型 `gpt-6.1-sol`、reasoning=max，正文 472,407 字节，包含 42 条 input 项和 9 组工具调用/结果。

认证、Cookie、API key 和转发客户端 IP 头在导出前丢弃。完整内容保存在 Git 忽略目录，用同版本 `Forward` 路径分别套用生产账号 1958 / 1961 的客户端画像与已探测出口时区，末端上游适配器在本地截取出站内容。没有重新调用模型或执行历史工具。这是完整请求构造的本地回放，不是对原请求生产出站 TLS 明文的抓取，也没有将它归入上文 16 次生产成功请求。

| 字段/内容 | 实际回放结果 | 处理结论 |
|---|---|---|
| 两种生成的时间上下文 | `Asia/Shanghai` 分别改为 `America/New_York` / `America/Los_Angeles`；这次日期相同 | 改写生效；其余文本保持一致 |
| UA、版本、attestation | UA 使用账号 Windows 画像；CBOR 内 locale/timezone 与出口一致，屏幕参数与账号画像一致 | 账号画像与区域字段一致 |
| installation/session/thread/window 与 prompt_cache_key | 安装 ID 取自账号；会话/线程/窗口改写；头与正文一致 | 已改写并保持关联 |
| turn_id、root_turn_id、context_window_id | 两次账号回放均保留客户端值 | 当前仍可关联；若需要跨账号隔离，应稳定映射并同步根轮次、fork/prediction 引用，不能逐次随机生成 |
| workspace 路径中的原会话 UUID | 保留在真实工作目录路径内 | 属于工具运行环境，不应全局替换为出站会话 ID |
| 工具调用 ID | 9 对调用/结果 ID 按类型归一化，前后一一对应 | 关联完整；工具参数、结果、schema 和 encrypted_content 保持不变 |
| turn_trigger | `send_user_message_async_question` 被旧逻辑误写为 `composer` | 新增受限枚举保留；默认、标题及 prediction 的既有语义保持不变 |

`turn_trigger` 问题已在本地修复，并用完整真实样本重新回放确认；该枚举也存在于本次官方桌面包的应用代码中。新增回归覆盖异步问题、缺省来源、非法触发值、标题优先级、prediction 优先级以及头/正文同步。**这次小修复尚未部署；线上仍是上节 SHA-256 对应的版本。**

真实 developer 时间块明确要求模型用客户端时区回答用户日期、时间和日程，因此当前将其改为出口时区也会改变这类回答的时间语义。这一字段不只是传输指纹；工作目录、shell、任务文字则继续表达真实客户端环境，不能为了 UA 是 Windows 就把 Mac 的 zsh/路径改成 Windows。

脱敏结果见 [real-request-audit.json](real-request-audit.json)。本节只代表一条真实请求和两条账号画像回放，不代表所有客户端与历史形态均已覆盖。

## 证据文件

- [model-catalog.json](model-catalog.json)：本账号模型目录白名单字段。
- [matrix-evidence.json](matrix-evidence.json)：每个用例的主任务、阶段、传输、完成状态与本地验证；`coverage_kind` 区分基础、深度和 Fast 专项，未执行的阶段不作为失败项。
- [protocol-evidence.json](protocol-evidence.json)：版本、头字段名、metadata 枚举及遥测差异；没有认证或正文。
- [validation-evidence.json](validation-evidence.json)：本地测试、构建及原客户端隔离检查。
- [request-contract.json](request-contract.json)：HTTP/WS/用量查询/原生模型目录的脱敏请求约定。
- [transport-evidence.json](transport-evidence.json)：新版内置 CLI 的离线 TLS/H2 直采结构。
- [production-validation.json](production-validation.json)：正式部署核验、全部 8 模型首轮/续轮结果与真实出站头、时区核对。
- [real-request-audit.json](real-request-audit.json)：一条真实线上长请求的本地完整回放、身份/时区差异与工具关联验证。
