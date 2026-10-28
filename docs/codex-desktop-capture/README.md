# Codex 桌面应用抓包归档

按版本归档 Codex 独立桌面应用（ChatGPT 账号登录态）的真实请求画像，作为
sub2api 伪装层（`backend/internal/service/openai_codex_mimic.go`）的对齐基准。

## 目录

| 目录 | 应用版本 | codex.exe | 抓包日期 | 说明 |
|---|---|---|---|---|
| `2026-06-21_0.142.0-alpha.6/` | 26.616.6631.0 | 0.142.0-alpha.6 | 2026-06-21 | 首轮抓包（WS 426 拦截 → HTTP SSE 回退） |
| `2026-07-21_0.145.0-alpha.27/` | 26.715.8383.0 | 0.145.0-alpha.27 | 2026-07-21/22 | 第二轮抓包，含 0.144→0.145 diff 与风控记录 |
| `2026-07-22_0.145.0-alpha.27/` | 26.715.8383.0 | 0.145.0-alpha.27 | 2026-07-22 | 补充抓包：`/wham/rate-limit-reset-credits` GET（实抓）与 consume（手工构造） |
| `2026-08-30_0.151.0-alpha.7.1/` | 26.825.41651 | 0.151.0-alpha.7.1 | 2026-08-30 | WS、HTTP 回退、Lite/非 Lite、附件与 compaction；仅提交脱敏结论 |
| `2026-09-17_0.155.0-alpha.2.6/` | 26.911.61220 | 0.155.0-alpha.2.6 | 2026-09-17 | [Work 模型矩阵](2026-09-17_0.155.0-alpha.2.6/MATRIX.md)、[本地项目专项](2026-09-17_0.155.0-alpha.2.6/WORK.md)：读写执行、压缩与停止恢复、Rust 遥测 |
| `2026-09-23_0.155.0-alpha.16/` | 26.917.51856 | 0.155.0-alpha.16 | 2026-09-23 | [GPT-6 Sol/Luna 本地 Work](2026-09-23_0.155.0-alpha.16/README.md)：WS/HTTP 四组核心流程、高强度快速模式、协议与遥测差异 |
| `2026-09-30_0.159.0/` | 26.928.20755 | 0.159.0 | 2026-09-30 | [GPT-6.1 Sol 本地 Work](2026-09-30_0.159.0/README.md)：WS/HTTP 核心流程、高强度快速模式、Work 来源、Chromium 154 与版本迁移 |
| `2026-10-28_0.165.0-alpha.1.2/` | 26.1023.50603 | 0.165.0-alpha.1.2 | 2026-10-28 | [全模型本地 Work](2026-10-28_0.165.0-alpha.1.2/README.md)：8 个可选模型 WS/HTTP、图片工具、压缩与停止恢复，Terra 与 Chromium 156 |

## 运行时出口地区

OpenAI OAuth 请求的语言和时区由 `CodexEgressProfileService` 按当前请求的代理出口解析。
探测通过该代理发出；无绑定代理时使用服务端直连出口。时区采用探测返回的 IANA 名称，
语言按国家的默认语言选择（例如美国 `en-US`、日本 `ja-JP`，香港/台湾使用 `zh-HK`/`zh-TW`，新加坡使用 `en-SG`）。
多语言地区的默认语言不代表用户个人偏好；只有国家信息时不猜测时区。

HTTP/WS attestation 和 WebView 的 `oai-language`、`accept-language` 共用请求级画像。
HTTP（含透传、compact）和 WS 请求正文已有的顶层 `timezone`、`timezone_offset_min` 会同步覆盖，
偏移按请求时刻计算夏令时。Codex 自动生成的 `input[].content[].text` 环境上下文中的
`<timezone>` 同样覆盖为出口时区，已有的 `<current_date>` 同步到该时区的请求当天；
WS 首轮、后续轮次和 HTTP → WS 转发共用此规则。仅匹配完整的 `<environment_context>` 文本块，
不修改普通对话、引用的示例、工具参数或输出，也不补写缺失的环境字段。
账号的设备/安装 ID、版本及持久化画像保持稳定，不需要迁移账号数据。

缓存以完整代理 URL（含用于选择住宅出口的凭据）的 SHA-256 为键，成功结果缓存 10 分钟，
失败缓存 1 分钟。切换代理地址或凭据会使用新键；同一地址自动轮换的出口在缓存过期后重新探测。
首次解析最多等待 3 秒；缺少完整国家/时区、探测失败时回退到持久化画像。
未解析到出口时区时，请求体保留客户端原值。
现有代理检测缓存仅在路由指纹匹配且足够新时复用；`ipify`、`chatgpt-trace` 未返回时区时不能单独驱动覆盖。

## 抓包方法

1. 默认直接记录原生 WebSocket。需要测试 HTTP 回退时，mitmproxy 对
   `/backend-api/codex/responses` 的 WebSocket 升级请求临时返回 426；结束后撤去拦截。
   早期轮次主要使用强制 HTTP 回退方式（工具：`tmp/codex_capture/`）。
2. 关键点：codex.exe 的 WS 客户端**不走系统代理**（直连），但遵守
   `HTTP_PROXY`/`HTTPS_PROXY` 环境变量。有 TUN 类代理时直连会绕过抓包，
   需关 TUN 并设置上述环境变量指向 mitmproxy。
3. 本机无外网直连时，mitmdump 用 `--mode upstream:http://127.0.0.1:33210`
   链到 Clash mixed 端口。

## 跨版本要点

- 0.165 重抓当前 8 个可选模型；新增观察到 `history_initialization` 和后台 `thread_sample` 回复建议来源字段。6.1 Sol/Terra 桌面默认窗口为 500k，WebView 升至 Chromium 156。
- 0.159 Work 推理实抓 `originator=codex_work_desktop`；普通 composer turn 新观察到 `client_type=desktop_app`、`source=codex`，WebView 升至 Chromium 154。
- 0.155 手动 compaction 的 WS/HTTP metadata 增加 `root_turn_id`。
- 0.155 扩展采样后比旧样本多观察到 15 个 OTLP 指标名，其中 9 个在旧程序中未检出同名字符串，另外 6 个旧程序已有；详见新版归档。
- 0.155 普通 ChatGPT 走 `/backend-api/f/conversation`，Work/Codex 继续走 `/backend-api/codex/responses`。
- 0.151 新增 `x-codex-routing-hint: model=<实际模型>`。
- 0.151 普通 turn 恢复完整 `s=0 + token` attestation；0.145 的 `s=1` 已过时。
- 0.151 WS 的 Lite 标记位于 `response.create.client_metadata`，握手不发送 Lite 头。
- 0.151 turn metadata 新增 agent/window/context/root/trigger/sandbox-mode/auto-review/node-repl 字段。
- 0.151 WebView UA 新增 `CodexBrowser` 前缀并升级到 Chrome 151。
- 0.145 新增 `x-openai-internal-codex-responses-lite: true`（responses-lite 模型）。
- 0.145 turn POST 的 attestation 简化为 `{"v":1,"s":1}`（不带 CBOR token）；
  prewarm / compaction 仍带完整 token。
- 0.145 移除 `x-responsesapi-include-timing-metrics`（0.144 曾新增）。
- 0.145 prewarm 的 turn-metadata 增加 `thread_source` / `sandbox` / `workspaces`。
- 0.145 turn 区分用户 turn（`thread_source:"user"`，sandbox=none）与系统 turn
  （`thread_source:"system"`，sandbox=windows_elevated，无 workspaces）。
- 额度响应头（`x-codex-*`）字段集两轮一致。

## 注意

- 2026-08-30 起只提交脱敏结论；原始流量放在 Git 忽略目录，禁止提交或外发。
- 早期 0.142/0.145 归档仍包含**未脱敏**原始记录（含 JWT、cookie），不要外发整个目录。
- 两个抓包日期的账号均为临时测试号，其中 2026-07-21 的第一个账号
  （KarlRemien...）在登录约 2 分钟后被服务端吊销 token（详见 0.145 目录 README）。
