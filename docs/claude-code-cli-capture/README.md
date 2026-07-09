# Claude Code CLI 实测抓包与对齐工作流

本文档记录此前围绕本机与 SSH MCP VPS 的 Claude Code CLI 做过的实际请求分析、抓包、还原和对齐流程。后续如果在新的会话里切到目标服务器，需要先确认目标机器身份，再按这里的步骤重新采集、复核和迁移结论。

## 目标边界

本流程解决三个问题：

1. 确认当前机器实际运行的是哪个 Claude Code CLI 二进制，而不是只看 npm 包名或 wrapper。
2. 用可复现的本地 mock 和 HTTPS/TLS 抓包，得到真实 wire 请求的 headers、body、system、ToolSearch 轮次和 transport 指纹。
3. 把抓包结论转成项目里的 body profile、header policy、metadata、CCH、TLS/JA3/ALPN 对齐规则，并用回放测试验证。

不把 token、账号 UUID、真实用户消息、代理凭据写进文档或仓库。抓包产物进入仓库前必须脱敏；原始未脱敏文件只留在临时分析目录。

## 已知本机样本位置

Windows 本机曾使用过这些路径：

- Claude Code exe：
  `C:\Users\Administrator\AppData\Roaming\npm\node_modules\@anthropic-ai\claude-code\bin\claude.exe`
- 旧分析目录：
  `C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis`
- 旧 bundle：
  `C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\claude-2.1.191-bun-cjs-bundle.js`
- 当前机器曾确认版本：
  `2.1.199 (Claude Code)`
- 旧基准版本：
  `2.1.191`
- ToolSearch 新抓包目录：
  `C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\captures\raw_toolsearch_20260704-112420`

后续在 VPS 上不要假设路径一致。先确认 `which claude`、`npm root -g`、实际 bin 指向、`claude --version` 和二进制 hash。

## 2026-07-04 VPS Linux 实测摘要

目标 SSH MCP 服务器：`vps服务器`。本轮 VPS Linux 抓包基准机器是 host `racknerd-75391c4`，不是洛杉矶建站机。

本轮抓包产物已经放入当前仓库：

- 解压目录：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux_20260704-062834/`
- 归档文件：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux_20260704-062834.tar.gz`
- 请求分类总结：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux_20260704-062834/CLASSIFICATION.md`
- fp/CCH 重算记录：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux_20260704-062834/FP_CCH_RECALC.md`
- `ANTHROPIC_AUTH_TOKEN` 交互 TTY slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux-tty-auth-token_20260704-071859_slim/`
- `ANTHROPIC_AUTH_TOKEN` 交互 TTY 总结：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux-tty-auth-token_20260704-071859_slim/AUTH_TOKEN_TTY_SUMMARY.md`
- Windows 本机 latest `ANTHROPIC_AUTH_TOKEN` 交互 TTY 产物：`docs/claude-code-cli-capture/captures/2.1.201_windows-tty-auth-token_20260704-074947/`
- Windows 本机 latest `ANTHROPIC_AUTH_TOKEN` 交互 TTY 总结：`docs/claude-code-cli-capture/captures/2.1.201_windows-tty-auth-token_20260704-074947/WINDOWS_TTY_AUTH_TOKEN_SUMMARY.md`
- Claude Code OAuth 出站请求 profile 归一指导：`docs/claude-code-cli-capture/PROFILE_NORMALIZATION_GUIDE.md`
- `ANTHROPIC_AUTH_TOKEN` 交互 TTY ToolSearch 开/关 CCH 矩阵：`docs/claude-code-cli-capture/captures/TOOLSEARCH_CCH_MATRIX_2.1.201.md`
- Claude Code 2.1.201 动态字段研究：`docs/claude-code-cli-capture/captures/DYNAMIC_FIELDS_2.1.201.md`
- Claude Code 2.1.201 前置 system-reminder 研究：`docs/claude-code-cli-capture/captures/MESSAGE_PREFIX_REMINDERS_2.1.201.md`
- VPS Linux ToolSearch off slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux-tty-auth-token-toolsearch-off_20260704-075758_slim/`
- VPS Linux ToolSearch on slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux-tty-auth-token-toolsearch-on_20260704-075838_slim/`
- Windows 本机 ToolSearch off 产物：`docs/claude-code-cli-capture/captures/2.1.201_windows-tty-auth-token-toolsearch-off_20260704-075929/`
- Windows 本机 ToolSearch on 产物：`docs/claude-code-cli-capture/captures/2.1.201_windows-tty-auth-token-toolsearch-on_20260704-075947/`
- 第一阶段 CLI TTY follow-up 补抓报告：`docs/claude-code-cli-capture/captures/PHASE1_CLI_TTY_FOLLOWUPS_2.1.201.md`
- VPS Linux 第一阶段 CLI TTY follow-up slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux-cli-tty-phase1-followups_20260705_slim/`
- Windows 第一阶段 CLI TTY follow-up slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_windows-cli-tty-phase1-followups_20260705_slim/`
- Phase 2/3 CLI TTY 扩展补抓报告：`docs/claude-code-cli-capture/captures/PHASE2_PHASE3_CLI_TTY_EXPANDED_2.1.201.md`
- VPS Linux Phase 2 CLI TTY expanded slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux-cli-tty-phase2-expanded_20260705_slim/`
- Windows Phase 2 CLI TTY expanded slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_windows-cli-tty-phase2-expanded_20260705_slim/`
- VPS Linux Phase 3 CLI TTY more-axes slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux-cli-tty-phase3-more-axes_20260705_slim/`
- Windows Phase 3 CLI TTY more-axes slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_windows-cli-tty-phase3-more-axes_20260705_slim/`
- 交互式 CLI 与 SDK library 覆盖总表：`docs/claude-code-cli-capture/captures/SDK_LIBRARY_AND_INTERACTIVE_CLI_COVERAGE_2.1.201.md`
- Windows SDK library slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_sdk-0.3.201_windows-sdk-library_20260705_slim/`
- VPS Linux SDK library slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_sdk-0.3.201_vps-linux-sdk-library_20260705_slim/`
- 不同模型 CLI/SDK 矩阵报告：`docs/claude-code-cli-capture/captures/MODEL_MATRIX_CLI_SDK_2.1.201.md`
- Windows CLI TTY model matrix slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_windows-cli-tty-model-matrix_20260705_slim/`
- VPS Linux CLI TTY model matrix slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_vps-linux-cli-tty-model-matrix_20260705_slim/`
- Windows SDK library model matrix slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_sdk-0.3.201_windows-sdk-library-model-matrix_20260705_slim/`
- VPS Linux SDK library model matrix slim 产物：`docs/claude-code-cli-capture/captures/2.1.201_sdk-0.3.201_vps-linux-sdk-library-model-matrix_20260705_slim/`

环境与安装结果：

```text
host: racknerd-75391c4
os: Ubuntu 24.04 LTS x86_64
node: v22.23.1
npm: 10.9.8
claude_code_version: 2.1.201 (Claude Code)
claude_bin: /usr/bin/claude
real_binary: /usr/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe
binary_sha256: a34809a6839fdefff21b9347d7fb5b6b58e6a9cc208a5e62853f29c83eb107a3
npm_package_dir: /usr/lib/node_modules/@anthropic-ai/claude-code
analysis_dir: /tmp/claude-code-cli-analysis/captures/2.1.201_vps-linux_20260704-062834
```

安装前通过 npm registry 确认 `@anthropic-ai/claude-code@latest = 2.1.201`，`engines.node >= 22.0.0`。VPS 原始 Ubuntu 源只提供 Node 18，因此使用 NodeSource 22.x 安装 Node 22 后再执行：

```bash
npm install -g @anthropic-ai/claude-code@latest
```

本轮已抓到：

- SDK/headless `claude -p`：`User-Agent: claude-cli/2.1.201 (external, sdk-cli)`，`X-Stainless-OS: Linux`，`X-Stainless-Runtime-Version: v26.3.0`。
- SDK/headless billing：`system[0].text` 形如 `cc_version=2.1.201.<fp>; cc_entrypoint=sdk-cli;`，本轮未携带 `cch=`。
- `--append-system-prompt`：仍保持 `system` 顶层后续 block/内容参与 fingerprint，未下沉到 messages。
- `ENABLE_TOOL_SEARCH=true`：首轮工具包含 `ToolSearch`，mock 返回 `ToolSearch` `tool_use` 后，第二轮 messages 含 assistant `tool_use` 与 user `tool_result`，tools 增加 `WebFetch`/`WebSearch`；system 未变化。
- TLS ClientHello：JA3 raw `771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-21,29-23-24,0`，JA3 hash `d871d02cecbde59abbf8f4806134addf`，ALPN `http/1.1`。

自动化 TTY 抓包备注：

- root 运行时 `--dangerously-skip-permissions` 被 2.1.201 拒绝。
- 非 root 临时用户 `cccapture` 可运行，但首次 TTY 自动化先后卡在主题页和 workspace trust TUI；预置 `theme`、`hasCompletedOnboarding`、`projects[workspace].hasTrustDialogAccepted=true` 后仍未成功提交交互消息。
- 后续已通过 `ANTHROPIC_AUTH_TOKEN` + 隔离环境补到 Linux/Windows 交互 TTY 主请求；详见上面的 TTY 总结与 ToolSearch 开/关矩阵。

## 阶段 1：定位真实 CLI 与版本

先明确 npm 安装物到底是什么。Windows 上曾遇到的情况是 npm 命令最终调用 `claude.exe`，而不是普通可直接阅读的 JS 入口。

检查项：

- `claude --version`
- `where claude` 或 Linux 上 `which -a claude`
- `npm root -g`
- `npm list -g @anthropic-ai/claude-code --depth=0`
- `sha256sum` / `Get-FileHash` 记录真实二进制 hash
- 确认 `bin/claude`、`bin/claude.exe`、npm shim 之间的调用关系

记录模板：

```text
host:
os:
node:
npm:
claude_code_version:
claude_bin:
real_binary:
binary_sha256:
npm_package_dir:
analysis_dir:
```

如果是 exe 或打包运行时，JS bundle 可见逻辑只是一部分；例如 CCH 的非零写回此前证明发生在 `claude.exe` native HTTP 发送层，而不是普通 JS `JSON.stringify` 分支。

## 阶段 2：准备隔离配置

抓包时不要污染真实用户配置，也不要让测试写入真实项目会话。

建议设置：

```text
CLAUDE_CONFIG_DIR=<临时目录>
ANTHROPIC_BASE_URL=<本地 mock 或 HTTPS 代理地址>
CLAUDE_CODE_REMOTE_SESSION_ID=<固定 UUID，用于稳定 session 相关字段>
```

如果要触发 ToolSearch：

```text
ENABLE_TOOL_SEARCH=true
```

如果要抓交互 TTY profile，必须让 Claude Code 认为自己处在交互终端中。Windows 之前用 `Start-Process cmd.exe` 打开真实 TTY；Linux/VPS 上应使用真实 SSH TTY、`script`、`tmux` 或交互 shell。不要用 `claude -p` 或纯 pipe 代替 TTY，否则会变成 headless/sdk-cli 口径。

## 阶段 3：本地 HTTP mock 抓 body 和业务 headers

第一轮先用本地 mock，不碰真实 Anthropic。mock 的作用是稳定记录 Claude Code 发出的 HTTP 请求，并返回最小合法响应或强制 tool_use。

需要记录：

- raw request line
- 原始 headers 和顺序
- 原始 body 字节
- pretty body
- body key 顺序
- system block 数量、每段 text hash、cache_control
- tools 数量、工具名、是否有 `defer_loading`
- messages 轮次和 content block 类型
- metadata.user_id 原始形态
- model、thinking、output_config、context_management、max_tokens、temperature

此前 ToolSearch 抓到的真实形态是：

- 初始请求仍是 `POST /v1/messages?beta=true`
- 不存在单独 ToolSearch HTTP endpoint
- 首轮 tools 里包含普通 `ToolSearch` 工具
- 模型返回 `ToolSearch` tool_use 后，下一轮 messages 包含：
  - assistant `tool_use`
  - user `tool_result`
  - `tool_result.content[0].type = "tool_reference"`
- 下一轮 `tools[]` 增加被加载工具，例如 `WebSearch`
- 新增工具可带 `defer_loading:true`
- ToolSearch 改 `messages/tools`，不改 `system`

这个结论很重要：如果项目里开启 ToolSearch 后触发整段 system 重建，优先检查请求进入网关时 `system[0]` billing 是否已经丢失，或者 `/v1/messages` 是否被 group platform 分流到了兼容路径。

## 阶段 4：区分 TTY、SDK、title、side query

不要只按 URL 判断 profile。Claude Code 的 `/v1/messages` 内部有多种 body 家族。

最稳定入口：

```text
system 是数组
system[0].text 以 x-anthropic-billing-header 开头
system[0].text 含 cc_version=
system[0].text 含 cc_entrypoint=
```

常见 entrypoint：

- `cc_entrypoint=cli`：交互 TTY 主口径
- `cc_entrypoint=sdk-cli`：SDK/headless/非交互口径

此前抓包结论：

- TTY main 常见 4-block system：billing、official CLI identity、global cache block、ordinary ephemeral block。
- SDK/headless main 常见 3-block system：billing、Agent SDK identity、大段 system prompt。
- title query 有结构化 output schema，常见 `output_config.format.schema.properties.title`。
- ToolSearch 轮次保持原 system，不因工具加载而变更。
- 自定义 `--system-prompt` 在真实 Claude Code 中仍位于顶层 `system` 后续 block，不是自动搬到 messages。

如果要判断是否应该整段重建 system，只看 body 是否已经是 Claude Code family；不要信任调用方 header。

## 阶段 5：还原 JS bundle 与 native 差异

JS/bundle 适合找：

- system prompt 分支
- identity selector
- body 字段构造顺序
- querySource / title / side query / main 分支
- beta tokens 由哪些 body 能力触发
- metadata.user_id 的 JSON 字符串结构
- ToolSearch 工具描述与请求体参与方式

native/exe 适合找：

- JS 层不可见的 HTTP 发送层补写
- CCH `00000` 到 5 hex 的写回
- TLS/ALPN/JA3 行为
- 打包运行时和 stock runtime 的差异

此前 CCH 结论归档在：

- `docs/CLAUDE_CODE_CCH_NATIVE.md`

关键结论：

- JS 可见路径只生成 `cch=00000` 占位。
- 非零 CCH 在 native HTTP 发送层写回。
- CCH 基于最终 wire body 原始字节，不做 JSON canonical。
- CCH 要放在所有 body sanitize、字段补齐、metadata 重写之后，`http.NewRequest` 之前。

## 阶段 6：HTTPS/TLS 抓包

本地 HTTP mock 只能捕获 body 和业务 headers，不能证明真实 TLS/JA3/ALPN/HTTP 协议指纹。

要补 HTTPS/TLS 时，需要：

- 让 Claude Code 连接 HTTPS mock 或可控代理。
- 捕获 ClientHello、JA3/JA3 hash、ALPN、SNI。
- 记录最终使用 HTTP/1.1 还是 HTTP/2。
- 和项目出站 transport 做对比。

当前项目已有 TLS 指纹实现位置：

- `backend/internal/pkg/tlsfingerprint/dialer.go`

此前项目注释里记录过 main `/v1/messages` 连接的 ALPN/JA3 基准。迁移 VPS 时必须重新实测，因为不同 Claude Code 版本、不同打包 runtime、不同系统 OpenSSL/BoringSSL/Bun 版本都可能导致差异。

## 阶段 7：回放验证

每次抓到新 profile，都要做两类测试。

第一类：分类测试。

- 把抓到的 raw body 作为输入。
- 验证 `classifyClaudeMessagesBody(body).isClaudeCodeFamily()` 是否为 true。
- 验证 profile 是否落到预期：TTY main、SDK main、title、side query、probe。
- 验证 ToolSearch 后续轮次仍保持 Claude Code family。

第二类：出站重建/转发测试。

- OAuth + 非 Claude Code 入站：应走主动 mimic，整段重建 system。
- OAuth + Claude Code family 入站：不应整段重建 system，只修正安全字段，例如 metadata、billing/CCH、beta。
- `/v1/responses` 和 `/v1/chat/completions` 兼容路径：按兼容策略处理，不能误认为原生 Claude Code。
- `/v1/messages` 但 group platform 是 OpenAI：会走 `OpenAIGateway.Messages`，不是原生 Anthropic `Gateway.Messages`。

调试开关：

```text
SUB2API_DEBUG_CLAUDE_MIMIC=true
```

重点看快照：

```text
CLIENT_ORIGINAL
UPSTREAM_FORWARD
inbound_claude_code
mimic_claude_code
```

如果 `CLIENT_ORIGINAL.system[0]` 已经没有 billing attribution，后续整段 system 重建是当前策略的预期结果；问题在客户端或进入主链路前的转换。

## 阶段 8：迁移到目标服务器的执行清单

在目标服务器上不要直接套 Windows 旧结论，也不要混用另一台机器的 TTY/system/profile 结论。按下面顺序跑：

1. 记录 VPS 基础环境：OS、架构、Node、npm、Claude Code 版本、真实二进制路径、hash。
2. 建立隔离 `CLAUDE_CONFIG_DIR`，不要复用生产配置。
3. 用本地 HTTP mock 抓 SDK/headless 请求。
4. 用真实交互 TTY 抓 TTY main 请求。
5. 开 `ENABLE_TOOL_SEARCH=true`，抓 ToolSearch 初始请求和 tool_result 后续请求。
6. 抓 title query：观察 structured output schema 和 header/beta。
7. 抓自定义 system prompt：确认 system 后续 block 是否保持顶层透传。
8. 抓 HTTPS/TLS：记录 JA3、ALPN、HTTP/1.1 或 HTTP/2。
9. 对每条 raw body 运行项目分类测试。
10. 对项目出站请求做 mock 对比：headers、body key 顺序、system block、metadata、CCH。
11. 如果目标是“对齐某个缓存版本”，优先以该缓存版本的 exe/hash 抓包为准，不混用另一个版本的 TTY/system/profile 结论。

## 产物命名建议

每次新抓包建独立目录：

```text
<analysis_dir>/captures/<version>_<host>_<profile>_<YYYYMMDD-HHMMSS>/
```

目录内建议包含：

```text
manifest.json
entry0_raw_http_request.txt
entry0_body_pretty.json
entry0_headers.json
entry0_summary.json
entry1_raw_http_request.txt
entry1_body_pretty.json
tls_clienthello.txt
ja3.txt
notes.md
```

`manifest.json` 至少记录：

```json
{
  "host": "",
  "os": "",
  "timezone": "",
  "claude_code_version": "",
  "binary_path": "",
  "binary_sha256": "",
  "entrypoint": "cli|sdk-cli",
  "tty": true,
  "enable_tool_search": false,
  "base_url": "mock",
  "capture_time": ""
}
```

## 和当前项目实现的对应关系

关键代码位置：

- body profile：
  `backend/internal/service/claude_code_body_profile.go`
- 整段 system 重建：
  `backend/internal/service/gateway_service.go`
  `forceRewriteSystemForNonClaudeCodeWithPromptBlocks`
- billing attribution 与 CCH：
  `backend/internal/service/gateway_billing_header.go`
  `backend/internal/service/gateway_billing_block.go`
- OAuth 出站 request 构造：
  `backend/internal/service/gateway_service.go`
  `buildUpstreamRequest`
- OpenAI/Responses 兼容入口：
  `backend/internal/service/gateway_forward_as_responses.go`
  `backend/internal/service/gateway_forward_as_chat_completions.go`
  `backend/internal/service/openai_gateway_messages.go`
- `/v1/messages` 路由分流：
  `backend/internal/server/routes/gateway.go`
- TLS 指纹：
  `backend/internal/pkg/tlsfingerprint/dialer.go`

判定原则：

- Claude Code family 入站：`system[0]` billing attribution 是锚点。
- 非 Claude Code 入站 + Claude OAuth 出站：允许主动重建 system。
- 已是 Claude Code family 入站：不应整段重建 system；只做账号相关字段、CCH、beta/header 等出站修正。
- ToolSearch 不是独立 endpoint；它不应单独导致 system 重建。

## 常见误区

- 只看 URL 是 `/v1/messages` 不够；group platform 可能把它分流到 OpenAI gateway。
- 只看 `User-Agent` 不够；Claude Code family 判断以 body 为准。
- 只抓 HTTP mock 不够；TLS/JA3/ALPN 需要 HTTPS 抓包。
- 只看 JS bundle 不够；CCH 非零写回在 native 发送层。
- TTY 和 `claude -p` 不是一个 profile；后者通常是 sdk-cli/headless。
- ToolSearch 后续请求的 system 应保持和首轮一致；变化的是 messages/tools。
- `cch` 必须最后按最终 body 覆盖；提前计算会被后续 metadata/model/body sanitize 改坏。
