# Claude Code OAuth 出站请求 Profile 归一指导

本文是后续实现网关出站请求对齐的指导文档。目标是：项目最终发往 Claude OAuth 的请求，尽量与官方 Claude Code CLI 抓包请求一致，包括请求头、请求体、顶层 `system`、前置 `<system-reminder>`、工具列表和动态字段。

本文先以 Claude Code `2.1.201` 的 `ANTHROPIC_AUTH_TOKEN` 交互 CLI 抓包为基准，优先落地 `cli main ToolSearch off` profile，再扩展到 ToolSearch on、title、SDK/headless 和后续工具轮次。

## 目标边界

目标不是把任意请求转成 Claude API 可接受的形状，而是把已识别为 Claude Code family 的入站请求归一成官方 Claude Code wire profile。

核心流程：

```text
入站请求
  -> Claude Code family 识别
  -> 请求类别分类
  -> 选择 official profile
  -> 提取动态字段
  -> 缺失字段使用 profile 默认值
  -> 重建或归一 headers/body/system/reminders/tools
  -> 白名单透传允许字段
  -> 出站到 Claude OAuth
```

不要用一套通用修补逻辑打所有请求。每个请求类别都应有自己的识别规则、动态字段规则和允许透传字段。

## 证据文档

当前可用抓包与分析：

- `captures/2.1.201_vps-linux_20260704-062834/CLASSIFICATION.md`
- `captures/2.1.201_vps-linux_20260704-062834/FP_CCH_RECALC.md`
- `captures/2.1.201_vps-linux-tty-auth-token_20260704-071859_slim/AUTH_TOKEN_TTY_SUMMARY.md`
- `captures/2.1.201_windows-tty-auth-token_20260704-074947/WINDOWS_TTY_AUTH_TOKEN_SUMMARY.md`
- `captures/TOOLSEARCH_CCH_MATRIX_2.1.201.md`
- `captures/DYNAMIC_FIELDS_2.1.201.md`
- `captures/MESSAGE_PREFIX_REMINDERS_2.1.201.md`
- `captures/PHASE1_CLI_TTY_FOLLOWUPS_2.1.201.md`
- `captures/PHASE2_PHASE3_CLI_TTY_EXPANDED_2.1.201.md`
- `captures/SDK_LIBRARY_AND_INTERACTIVE_CLI_COVERAGE_2.1.201.md`
- `captures/MODEL_MATRIX_CLI_SDK_2.1.201.md`
- `captures/2.1.201_vps-linux-cli-tty-phase1-followups_20260705_slim/`
- `captures/2.1.201_windows-cli-tty-phase1-followups_20260705_slim/`
- `captures/2.1.201_vps-linux-cli-tty-phase2-expanded_20260705_slim/`
- `captures/2.1.201_windows-cli-tty-phase2-expanded_20260705_slim/`
- `captures/2.1.201_vps-linux-cli-tty-phase3-more-axes_20260705_slim/`
- `captures/2.1.201_windows-cli-tty-phase3-more-axes_20260705_slim/`
- `captures/2.1.201_sdk-0.3.201_windows-sdk-library_20260705_slim/`
- `captures/2.1.201_sdk-0.3.201_vps-linux-sdk-library_20260705_slim/`
- `captures/2.1.201_windows-cli-tty-model-matrix_20260705_slim/`
- `captures/2.1.201_vps-linux-cli-tty-model-matrix_20260705_slim/`
- `captures/2.1.201_sdk-0.3.201_windows-sdk-library-model-matrix_20260705_slim/`
- `captures/2.1.201_sdk-0.3.201_vps-linux-sdk-library-model-matrix_20260705_slim/`
- `captures/system-originals-2.1.201-vps-linux-cli/`
- `captures/message-prefix-originals-2.1.201/`

这些文档是 profile 默认值和动态字段判断的来源。实现时不要从当前 Codex/运行环境现查 Claude Code 配置来代替入站请求。

## 抓包基准机器

Linux 抓包基准来自 SSH MCP 服务器 `vps服务器`：

```text
ssh_mcp_server: vps服务器
host: racknerd-75391c4
os: Ubuntu 24.04 LTS x86_64
node: v22.23.1
npm: 10.9.8
claude_code_version: 2.1.201 (Claude Code)
real_binary: /usr/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe
binary_sha256: a34809a6839fdefff21b9347d7fb5b6b58e6a9cc208a5e62853f29c83eb107a3
```

这批 VPS Linux profile 结论指向 `vps服务器`，不是洛杉矶建站机。Windows 本机抓包只作为跨平台对照，用来识别 Windows 特有字段，例如 `Glob/Grep`、`win32`、gitStatus 等。

## 分层方法

本文把工作明确拆成两层，避免把“抓包场景”误当成最终归一化 profile：

1. **探索层**：尽可能多抓官方 Claude Code 真实请求，覆盖入口、模型、平台、工具状态、历史形态、系统参数和异常路径。探索层的名字可以很细，例如 `bash-followup`、`plain-git`、`effort-low`。
2. **归一化 profile 层**：把进入系统的请求归纳成少量 wire profile。每个 profile 必须定义识别规则、headers、body 白名单、`system` 模板、`<system-reminder>` 模板、动态字段抽取和 fallback。

最终分类器不能直接返回探索场景名。推荐返回：

```text
profile_id + system_profile + dimensions
```

其中 `profile_id` 表达入口和 wire shape 主类别，`system_profile` 表达 `system[2]` 的模板分支，`dimensions` 表达平台、模型、工具集、历史形态等轴。`ToolSearch`、普通工具 followup、多轮历史不应该拆成不同 `system_profile`；它们主要影响 headers、tools、messages 和 beta。

## 探索覆盖场景

下面这些是已经抓到的探索场景，作用是提供证据和发现分支；它们不等同于最终 profile：

| 探索场景组 | entrypoint | 状态 | 归入 profile / 维度 |
| --- | --- | --- | --- |
| title/structured-output | `cli` | 已抓 | `cli-title`。 |
| 普通交互 main ToolSearch off/on 首轮 | `cli` | 已抓 | `cli-main`，维度 `toolsearch_state=off/on_initial`。 |
| ToolSearch 后续轮次 | `cli` / `sdk-cli` / `sdk-ts` | 已抓 | `*-main`，维度 `history_shape=toolsearch_followup`。 |
| 普通工具后续轮次：Read/Bash/Edit/Write/WebFetch/Glob/Grep/multi-Read | `cli` / `sdk-ts` | 已抓 | `*-main`，维度 `history_shape=tool_followup`，工具名是子维度。 |
| 普通多轮对话 | `cli` | 已抓 | `cli-main`，维度 `history_shape=multiturn_plain`。 |
| plan/cron/task 特殊工具 | `cli` | 已抓 | `cli-main`，维度 `history_shape=tool_followup` + `tool_family=special`；不改变普通 main `system_profile`。 |
| agent/subagent | `cli` | 已抓 | 发起 agent 的主轮次仍是 `cli-main`；subagent 内部请求是 `system_profile=agent_subrequest`。 |
| git/non-git 工作区 | `cli` | 已抓 | 不是 profile；是 `system[2]` 的动态字段和 `workspace.git_state` 维度。 |
| append-system/system-prompt | `cli` / `sdk-cli` / `sdk-ts` | 已抓 | 拆到 `system_profile=append_system/replace_system`；SDK library 仍可能影响 `system_count`。 |
| tools none/read-only/disallowed | `cli` / `sdk-ts` | 已抓 | `toolset` 维度，会影响 tools 和前置 reminders。 |
| bare/safe | `cli` | 已抓 | 拆到 `system_profile=bare/safe`；不是普通 `toolset`。 |
| effort low/max | `cli` | 已抓 | `effort` 维度；是否允许由 model beta 决定。 |
| `/btw` side query | `cli` | 已抓 | 目前仍归 `cli-main`；只观察到 main，无 title。 |
| WebFetch 内部 summarization | `cli` | 已抓 | 独立 `cli-internal-summarization` 候选 profile。 |
| permission bypass noisy path | `cli` | Windows noisy / Linux root 拒绝 | 暂不归普通 main，保持未知/特殊 profile。 |
| 7 模型矩阵 | `cli` / `sdk-ts` | 已抓 | `model` 维度；影响 beta 和部分 body 字段。 |

## 归一化 profile 目录

当前建议落地的最终 profile 如下。实现时先识别 `profile_id`，再识别 `system_profile` 和其他 `dimensions`：

| profile_id | entrypoint | 识别核心 | renderer 接管内容 | 主要 dimensions |
| --- | --- | --- | --- | --- |
| `cli-title` | `cli` | tools=0，`thinking.type=disabled`，`structured-outputs`，title JSON schema，`messages[0]` 含 `<session>`。 | headers、title `system[0..2]`、title body 白名单。 | `platform`、`model`。 |
| `cli-main` | `cli` | `system_count=3`，`system[1]` 为 Claude Code CLI identity，main 请求能力字段，首个 user content 有 typed reminders。 | headers、`system[0..2]`、前置 reminders、tools、metadata、thinking/output_config 能力字段、顶层白名单。 | `system_profile`、`toolsearch_state`、`history_shape`、`platform`、`model`、`workspace`、`toolset`、`effort`。 |
| `cli-internal-summarization` | `cli` | WebFetch 等内部请求，已见 `system_count=2`、tools=0、不是 title JSON schema。 | 暂只分类和保守透传；未完成 renderer 前不要套 `cli-main`。 | `source_tool`、`platform`、`model`。 |
| `sdk-cli-main` | `sdk-cli` | UA/billing entrypoint 为 `sdk-cli`，headless `claude -p` 形态。 | 单独 SDK CLI headers/system/reminders/tools profile。 | `system_profile`、`toolsearch_state`、`history_shape`、`model`。 |
| `sdk-ts-main` | `sdk-ts` | UA 含 `agent-sdk/0.3.201`，billing entrypoint 为 `sdk-ts`；plain `system_count=2`。 | SDK library headers、Agent SDK identity、SDK reminders、tools。 | `system_profile`、`toolsearch_state`、`history_shape`、`platform`、`model`、`toolset`。 |
| `unknown-claude-code-family` | 任意 | 符合 Claude Code family 但不满足已知 profile。 | 不做 CLI main 强归一；记录样本，必要时透明/保守转发。 | 原始分类信号。 |

### `cli-main` system_profile 拆分

`cli-main` 下必须先拆 `system_profile`，再处理 ToolSearch、工具集和历史形态：

| system_profile | 抓包识别信号 | renderer 原则 |
| --- | --- | --- |
| `default` | `system[2]` 以交互 main prompt 开头，包含 `# auto memory`、`# Environment`、`# Context management`，git 仓库时末尾有 `gitStatus`。 | 使用普通 CLI main 固定模板；只替换 cwd、platform、shell、OS、model、gitStatus 等动态字段。 |
| `append_system` | 在 `default` 基础上，官方 main prompt 与 `gitStatus` 之间出现用户追加 system block。 | 先渲染普通 main 模板，再插入/保留追加 block，最后拼动态 gitStatus。 |
| `replace_system` | `system[2]` 不再是官方交互 main prompt，而是用户 replacement prompt；git 仓库时仍可能拼 `gitStatus`。 | 不套 default main prompt；只规范 billing/identity/cache/body 白名单，replacement 文本按可信入口透传或模板化。 |
| `bare` | `system[2]` 以 `CWD:`、`Date:` 开头，随后是 `gitStatus`；没有普通 main 大段 prompt。 | 单独最小 system 模板；前置 reminders 通常只有 currentDate/context。 |
| `safe` | `system[2]` 仍是交互 main prompt，但缺少 `# auto memory` 等记忆段，体量明显短于 default。 | 单独 safe 模板；不能用 default renderer 把 memory 段补回去。 |
| `agent_subrequest` | `system[0]` 含 `cc_is_subagent=true`，或 `system[2]` 以 `You are an agent for Claude Code` 开头。 | 单独 agent prompt、工具集和 reminder 顺序；不能归普通 main default。 |

`cli-main-toolsearch-off`、`cli-main-toolsearch-on-initial` 和 `cli-toolsearch-followup` 不再作为互相割裂的顶层 profile；它们是 `cli-main + system_profile=<具体分支>` 下的 `toolsearch_state/history_shape`。文档下方仍保留 ToolSearch off/on 小节，是因为它们是 `cli-main + system_profile=default` renderer 的第一批落地切片。

每个 renderer slice 必须写清楚以下契约：

| 契约项 | 要求 |
| --- | --- |
| 识别规则 | 用多信号判定，不靠单个 header 或 tools 数量。 |
| headers | 固定字段使用抓包值；动态字段只从结构化位置提取；敏感凭据使用出站账号。 |
| body 顶层 | profile 白名单；抓包未出现字段默认丢弃或记录。 |
| `system[0]` | 固定 billing 模板，只替换允许动态字段，例如 `fp`。 |
| `system[1]` | 使用 profile 固定 identity。 |
| `system[2]` | 先按 `system_profile` 选择模板；路径、平台、shell、OS、gitStatus、model hints 等动态字段从入站请求提取，缺失用 profile 默认值。 |
| 前置 `<system-reminder>` | 按 reminder 类型生成或规范化；动态字段从入站结构提取，缺失用默认值。 |
| 历史 messages | 前置 reminders 之后的正常 user/assistant/tool_use/tool_result 历史按结构透传，不把普通用户文本当动态配置来源。 |
| tools | 按 profile + `toolset/toolsearch_state/platform` 渲染或白名单透传。 |
| fallback 日志 | 记录缺失动态字段、丢弃字段、最终 profile 和 dimensions。 |

待补抓类别：

- 真实 Claude Code 登录态或订阅账号路径，如果和 `ANTHROPIC_AUTH_TOKEN` mock bearer 不同，需要单独 profile。
- MCP server / plugin-dir / plugin-url / Chrome / IDE / remote-control / worktree / background/fork/resume/continue 等入口。
- 真实 WebSearch 服务端结果形态；当前只覆盖 WebFetch 客户端工具。
- permission mode 完整矩阵和拒绝/失败/权限提示后的 tool_result。
- `/compact`、`/review`、`ultrareview` 等会触发 summarization 或云服务的 slash command。
- Bedrock、Vertex、Foundry、真实 API key 上游等非 Claude OAuth provider。

## 模型维度与 beta 选择

`model` 必须纳入 dimensions。推荐按 `profile_id + dimensions` 选择 headers/body 模板，其中至少包含 `platform`、`request_kind`、`toolsearch_state` 和 `model`。不能只按 `entrypoint=cli` 或 `sdk-ts` 写一套固定 `anthropic-beta`。

当前 model matrix 的 main 请求结论：

| model | CLI main beta 差异 | SDK library main beta 差异 | body 注意事项 |
| --- | --- | --- | --- |
| `claude-haiku-4-5-20251001` | 无 `effort-2025-11-24`，无 `mid-conversation-system-2026-04-07`。 | 同样无 `effort-2025-11-24`，且 SDK 不带 `redact-thinking-2026-02-12`。 | 不要无条件补 `output_config.effort`。 |
| `claude-fable-5` | 含 `mid-conversation-system-2026-04-07`、`effort-2025-11-24`、`fallback-credit-2026-06-01`。 | 同 CLI main 的新增 beta 集合，但 SDK 仍不带 `redact-thinking-2026-02-12`。 | main messages 可出现额外 `role=system` 历史形态，不能按纯 user-only 断言。 |
| `claude-opus-4-6` | 含 `effort-2025-11-24`，无 `mid-conversation-system-2026-04-07`。 | 同左，SDK 不带 `redact-thinking-2026-02-12`。 | 可使用常规 effort profile。 |
| `claude-opus-4-7` | 含 `effort-2025-11-24`，无 `mid-conversation-system-2026-04-07`。 | 同左，SDK 不带 `redact-thinking-2026-02-12`。 | 可使用常规 effort profile。 |
| `claude-opus-4-8` | 含 `mid-conversation-system-2026-04-07` 与 `effort-2025-11-24`。 | 同左，SDK 不带 `redact-thinking-2026-02-12`。 | 需要 mid-conversation-system profile。 |
| `claude-sonnet-4-6` | 含 `effort-2025-11-24`，无 `mid-conversation-system-2026-04-07`。 | 同左，SDK 不带 `redact-thinking-2026-02-12`。 | 可使用常规 effort profile。 |
| `claude-sonnet-5` | 含 `effort-2025-11-24`，无 `mid-conversation-system-2026-04-07`。 | 同左，SDK 不带 `redact-thinking-2026-02-12`。 | 可使用常规 effort profile。 |

title/structured-output 请求还要再按 `request_kind=title` 分支：title beta 通常额外包含 `structured-outputs-2025-12-15`，不能把该 beta 加到普通 main。

## 分类器设计

分类器应使用多信号组合，不要只靠单个字段。

分类顺序：

1. **Claude Code family 识别**：确认这是 Claude Code/Claude Agent SDK family 请求，否则不要进入 official profile renderer。
2. **entrypoint 识别**：从 UA 和 `system[0].text` 的 `cc_entrypoint` 判定 `cli`、`sdk-cli`、`sdk-ts`。
3. **profile_id 识别**：判定 `cli-title`、`cli-main`、`cli-internal-summarization`、`sdk-cli-main`、`sdk-ts-main` 或 `unknown-claude-code-family`。
4. **system_profile 识别**：在 main profile 内判定 `default`、`append_system`、`replace_system`、`bare`、`safe`、`agent_subrequest` 等 system 模板分支。
5. **dimensions 提取**：在已识别 profile 内提取 `toolsearch_state`、`history_shape`、`platform`、`model`、`workspace`、`toolset`、`effort` 等维度。
6. **renderer 选择**：用 `profile_id + system_profile + dimensions` 渲染 official wire body/header；无法识别关键维度时使用 profile 默认值并记录 fallback。

通用 Claude Code family 信号：

- path 为 `/v1/messages?beta=true`。
- `User-Agent` 形如 `claude-cli/<version> (external, cli)`、`claude-cli/<version> (external, sdk-cli)`，或 SDK library 的 `claude-cli/<version> (external, sdk-ts, agent-sdk/<sdk-version>, client-app/<app>)`。
- body 顶层存在 `system` 数组。
- `system[0].text` 以 `x-anthropic-billing-header:` 开头。
- `system[0].text` 包含 `cc_version=<version>.<fp>; cc_entrypoint=<entrypoint>;`，其中已见 entrypoint 包括 `cli`、`sdk-cli`、`sdk-ts`。
- `system[1].text` 是 Claude Code/Claude Agent SDK identity。
- `metadata.user_id` 是 JSON 字符串，包含 `device_id`、`account_uuid`、`session_id`。

### `cli-title`

识别信号：

- `cc_entrypoint=cli`。
- tools 数量为 0。
- `thinking.type=disabled`。
- `anthropic-beta` 包含 `structured-outputs-2025-12-15`。
- `output_config.format.type=json_schema`，schema 含 `title` 字段。
- `system[2]` 是标题生成 prompt。
- `messages[0].content[0].text` 包含 `<session>...</session>`。

### `cli-main` with `system_profile=default` and `toolsearch_state=off`

识别信号：

- `cc_entrypoint=cli`。
- `User-Agent` 为 `external, cli`。
- tools 非空，且不包含 `ToolSearch`。
- `anthropic-beta` 不包含 `advanced-tool-use-2025-11-20`。
- `thinking.type=adaptive`。
- `messages[0].content` 前置 3 个 `<system-reminder>`：
  - agent types reminder
  - skills reminder
  - currentDate/context reminder
- 真正用户输入在前置 reminder 之后。

### `cli-main` with `system_profile=default` and `toolsearch_state=on_initial`

识别信号：

- `cc_entrypoint=cli`。
- tools 包含 `ToolSearch`。
- `anthropic-beta` 包含 `advanced-tool-use-2025-11-20`。
- `messages[0].content[0]` 是 deferred tools reminder。
- 后续依次为 agent types、skills、currentDate/context reminder。
- 当前已抓样本是首轮请求，不含 ToolSearch 后续 `tool_result` 历史。

## 归一 pipeline

建议实现为 5 个独立阶段。

1. **Classify**
   - 识别 Claude Code family。
   - 选择 `profile_id` 并提取 dimensions。
   - 无法分类时进入兼容路径，不要套用 CLI profile。

2. **Extract**
   - 从入站请求提取动态字段。
   - 只从结构化位置提取，不从普通用户自然语言中猜。
   - 对 `<system-reminder>` 使用类型识别，不单纯依赖下标。

3. **Render**
   - 用 profile 模板渲染 headers/body/system/reminders/tools。
   - 缺失动态字段使用 profile 默认值。

4. **Whitelist Pass-through**
   - 对该 profile 允许的字段，从入站透传。
   - 对 profile 接管字段，不透传入站原值。
   - 未知字段默认丢弃或记录日志。

5. **Validate**
   - 校验出站请求是否符合 profile。
   - 检查 `system_count`、reminder 顺序、tools 集、beta、metadata、fp。
   - 记录分类结果、动态字段来源、fallback 字段。

## Renderer slice: `cli-main` + `system_profile=default` + `toolsearch_state=off`

这是第一优先级落地切片，也是普通交互 `cli-main` renderer 的基准实现。它只覆盖 `system_profile=default`；`append_system` 可以在此基础上扩展，`replace_system`、`bare`、`safe`、`agent_subrequest` 必须走单独分支。

### Headers

应对齐抓包：

| Header | 规则 |
| --- | --- |
| `Authorization` | 使用 OAuth 出站凭据，不从入站 dummy token 透传。 |
| `User-Agent` | `claude-cli/2.1.201 (external, cli)`，版本随目标 profile 升级。 |
| `Content-Type` | `application/json`。 |
| `Accept` | `application/json`。 |
| `anthropic-version` | `2023-06-01`。 |
| `anthropic-beta` | 按 model matrix 选择；`claude-code-20250219,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,effort-2025-11-24` 仅是常规 Sonnet/Opus main 示例。 |
| `anthropic-dangerous-direct-browser-access` | `true`。 |
| `x-app` | `cli`。 |
| `X-Claude-Code-Session-Id` | 从入站 header 或 metadata 提取；缺失则生成稳定 UUID。 |
| `x-claude-remote-session-id` | 从入站提取；缺失用 profile 默认或生成。 |
| `X-Stainless-OS` | 从入站提取；缺失按目标平台默认，Linux profile 默认为 `Linux`。 |
| `X-Stainless-Arch` | 优先入站；Linux/Windows x64 默认 `x64`。 |
| `X-Stainless-Lang` | `js`。 |
| `X-Stainless-Package-Version` | 当前抓包为 `0.94.0`。 |
| `X-Stainless-Runtime` | `node`。 |
| `X-Stainless-Runtime-Version` | 当前抓包为 `v26.3.0`，版本升级时需重抓确认。 |
| `X-Stainless-Retry-Count` | `0`，除非明确是重试请求。 |
| `X-Stainless-Timeout` | `600`。 |

`Host`、`Content-Length`、`Connection`、`Accept-Encoding` 由 HTTP client/transport 负责，不作为业务 profile 强行设置。

### Body top-level 白名单

当前 `cli-main` + `system_profile=default` + `toolsearch_state=off` 可接受的顶层字段：

```text
model
messages
system
tools
metadata
max_tokens
thinking
output_config
stream
```

规则：

- `system`、`messages[0].content` 前置 reminders、`metadata`、`thinking`、`tools`、`anthropic-beta` 由 profile 接管或校正。
- `model`、`max_tokens`、`stream` 可按 profile 白名单透传，但必须符合抓包形态。
- 抓包未出现的未知字段先丢弃或记录，不直接出站。

### `system`

`cli` 交互 main default 顶层 `system` 固定 3 段。

#### `system[0]`

模板：

```text
x-anthropic-billing-header: cc_version=2.1.201.<fp>; cc_entrypoint=cli;
```

规则：

- `<fp>` 动态计算。
- 当前 2.1.201 `ANTHROPIC_AUTH_TOKEN` CLI 样本没有 `cch=`，不要补。
- 不从入站透传已有 `cc_version` 后缀，避免 fp 错误。

fp 规则沿用当前项目算法：

```text
fp = sha256("59cf53e54c78" + fp_chars + cli_version).hex()[0:3]
```

`fp_chars` 取首个非 meta 用户文本的 UTF-16 code unit 下标 `4, 7, 20`，不足补 `0`。首个非 meta 用户文本必须跳过前置 `<system-reminder>`。

#### `system[1]`

固定值：

```text
You are Claude Code, Anthropic's official CLI for Claude.
```

#### `system[2]`

使用官方 main 模板，模板原文参考：

```text
captures/system-originals-2.1.201-vps-linux-cli/main_entry1_system_toolsearch_off.txt
```

动态插值字段：

| 字段 | 提取来源 | fallback |
| --- | --- | --- |
| memory path | 入站 `system[2]` 中 auto memory path | 根据 `CLAUDE_CONFIG_DIR`、workdir、project slug 生成 profile 默认。 |
| primary working directory | 入站 `system[2]` Environment 块 | Linux 默认当前隔离 workspace 或 `/tmp/.../workspace` 形态。 |
| is git repository | 入站 `system[2]` 或实际工作目录元信息 | 默认 `false`。 |
| platform | 入站 `system[2]` 或 `X-Stainless-OS` | Linux profile 默认 `linux`。 |
| shell | 入站 `system[2]` | Linux profile 默认 `unknown`。 |
| OS version | 入站 `system[2]` | profile 默认 `Linux 6.8.0-124-generic`，升级后重抓。 |
| gitStatus block | 入站 `system[2]` 末尾 gitStatus | 缺失时不生成；如果 `is git repository=true` 但无可用状态，可生成空/保守状态并记录 fallback。 |
| model hints | 入站同字段 | 缺失用抓包默认：Sonnet 5 / `claude-sonnet-5`。 |

同一 OS 内，普通 TTY、ToolSearch off、ToolSearch on 的 `system[2]` 在归一化路径/环境后没有 diff。ToolSearch 不改变顶层 `system[2]`。

### 前置 `<system-reminder>`

普通 main ToolSearch off 的 `messages[0].content` 顺序：

```text
content[0] agent types reminder
content[1] skills reminder
content[2] currentDate/context reminder
content[3] user text
```

不要硬编码“用户文本总是 content[3]”。实现应扫描并跳过所有前置 `<system-reminder>`，第一个非 reminder text 才是真正用户输入。

#### agent types reminder

处理规则：

1. 入站已有合法 agent types reminder 时，优先保留原文或轻度规范化。
2. 合法性校验：
   - 外层完整 `<system-reminder>...</system-reminder>`。
   - 第二行是 `Available agent types for the Agent tool:`。
   - agent 行符合 `- name: description (Tools: ...)` 形态。
   - agent 名称来自已知白名单或可信配置来源。
3. 缺失或非法时按 profile 默认生成。

当前默认：

| profile | 差异 |
| --- | --- |
| `cli/linux` | 包含 `claude-code-guide`，tools 为 `Bash, Read, WebFetch, WebSearch`。 |
| `cli/windows` | 包含 `claude-code-guide`，tools 为 `Glob, Grep, Read, WebFetch, WebSearch`。 |
| `sdk-cli` | 不包含 `claude-code-guide`。 |

在 `system_profile=default` 内，ToolSearch on/off 不改变 agent types reminder。

#### skills reminder

处理规则：

1. 入站已有合法 skills reminder 时，优先保留原文。
2. 合法性校验：
   - 外层完整 `<system-reminder>...</system-reminder>`。
   - 第二行是 `The following skills are available for use with the Skill tool:`。
   - skill 项为 `- name: description` 形态。
3. 缺失或非法时使用抓包默认 skills block。
4. 不从当前项目运行环境现查 skills 来生成。目标是模拟入站 Claude Code 请求，而不是当前 Codex 环境。

当前默认 skills：

```text
deep-research
dataviz
update-config
keybindings-help
verify
code-review
simplify
fewer-permission-prompts
loop
claude-api
run
init
review
security-review
```

#### currentDate/context reminder

处理规则：

- 入站已有合法 context reminder 时，保留模板并更新日期为出站处理日期，除非明确要求保持入站日期。
- 缺失时按 profile 生成。
- 日期不能固定抓包日期。运行时应使用当前处理日期，例如 `Today's date is 2026-07-05.`。

模板：

```text
<system-reminder>
As you answer the user's questions, you can use the following context:
# currentDate
Today's date is <YYYY-MM-DD>.

      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>
```

### 历史 messages

规则：

- 只处理首个 user message 的前置 reminders。
- 正常历史 user/assistant/tool_use/tool_result 透传，除非后续 profile 证明需要重写。
- 不把历史中的 `<system-reminder>` 当真实用户文本参与 fp。
- 多轮历史、工具后续轮次尚需补抓后再细化。

### tools

VPS Linux `cli-main + system_profile=default + toolsearch_state=off` 默认 27 个工具：

```text
Agent
AskUserQuestion
Bash
CronCreate
CronDelete
CronList
Edit
EnterPlanMode
EnterWorktree
ExitPlanMode
ExitWorktree
NotebookEdit
Read
ReportFindings
ScheduleWakeup
SendMessage
Skill
TaskCreate
TaskGet
TaskList
TaskOutput
TaskStop
TaskUpdate
WebFetch
WebSearch
Workflow
Write
```

规则：

- 对 `cli/linux` ToolSearch off，默认使用抓包工具集合和 schema。
- 如果入站工具集合与 profile 相同，可保留 schema 中的动态描述差异，但应校验名称、顺序和关键 schema。
- 如果工具集合不匹配，应按 profile 重建或降级到未分类路径。
- Windows profile 允许额外 `Glob`、`Grep`。

### metadata

`metadata.user_id` 是 JSON 字符串。字段：

```json
{
  "device_id": "...",
  "account_uuid": "",
  "session_id": "..."
}
```

规则：

- `session_id` 与 `X-Claude-Code-Session-Id` 对齐。
- `device_id` 优先入站；缺失生成稳定 device id。
- `account_uuid` 对 dummy bearer 抓包为空；真实 OAuth 登录态如有 account uuid，需补抓确认，不要凭空填。

### thinking / output_config

普通 main：

- `thinking.type=adaptive`。
- `output_config` 在本批 main 请求中存在，按抓包 profile 校验并透传允许字段。

title 请求：

- `thinking.type=disabled`。
- `output_config.format` 为 title JSON schema。

不要把 title 的 output schema 套到 main。

## Renderer slice: `cli-main` + `system_profile=default` + `toolsearch_state=on_initial`

在 `cli-main` + `system_profile=default` + `toolsearch_state=off` 基准切片上调整：

- `anthropic-beta` 增加 `advanced-tool-use-2025-11-20`。
- tools 使用 ToolSearch on 窄集合。
- `messages[0].content[0]` 新增 deferred tools reminder。
- 顶层 `system[2]` 不因 ToolSearch 改变。
- `system[0]` 的 fp 仍按真实用户输入计算，跳过 4 个前置 reminder。

CLI deferred tools 默认列表：

```text
CronCreate
CronDelete
CronList
EnterPlanMode
EnterWorktree
ExitPlanMode
ExitWorktree
NotebookEdit
ScheduleWakeup
SendMessage
TaskCreate
TaskGet
TaskList
TaskOutput
TaskStop
TaskUpdate
WebFetch
WebSearch
```

SDK/headless ToolSearch on 少 `EnterPlanMode`、`ExitPlanMode`，不能混用。

## 默认值策略

默认值必须分级：

1. **入站可信字段**：来自明确结构位置，格式合法。
2. **profile 默认字段**：来自抓包文档和原文文件。
3. **运行时合理默认**：例如当前日期、生成 session id。

不要从普通用户文本推断 system/reminder 动态字段。任何 fallback 都应记录，便于后续发现需要补抓的新类别。

## 白名单策略

按 profile 定义：

- 允许透传字段。
- profile 接管字段。
- 必须丢弃字段。

建议日志：

```text
profile=<id>
classification_signals=<matched signals>
fallback_fields=<list>
dropped_fields=<list>
normalized_system=true/false
normalized_reminders=true/false
fp=<value>
cch=absent
```

## 测试策略

每个 profile 至少需要：

- 分类器单测：各种 entry/body 能分类到正确 profile。
- 动态字段提取单测：路径、gitStatus、agent types、skills、currentDate、用户文本。
- renderer golden 测试：输出 body 与抓包归一化后 diff 只剩预期动态字段。
- fp 测试：复现抓包中的 `cc_version` 后缀。
- ToolSearch on/off 回归：在同一个 `system_profile` 内，ToolSearch 只改变 beta/tools/messages，不改变顶层 `system[2]` 模板。
- 未知字段测试：抓包未出现字段不能无条件透传。

## 实施顺序

建议顺序：

1. 实现 Claude Code family 识别。
2. 实现 `cli-main` 的 `system_profile` 分类：`default`、`append_system`、`replace_system`、`bare`、`safe`、`agent_subrequest`。
3. 实现 `cli-main + system_profile=default + toolsearch_state=off` 基准切片、动态提取和 renderer。
4. 增加 golden 测试，对齐 VPS Linux 抓包。
5. 加入 Windows profile 分支，处理 `Glob/Grep` 和 `gitStatus`。
6. 实现 `cli-main + system_profile=default + toolsearch_state=on_initial`。
7. 基于已补抓产物实现 ToolSearch followup 和普通工具后续轮次。
8. 再实现 `append_system`、`replace_system`、`bare`、`safe`、`agent_subrequest` 的独立 renderer。
9. 最后处理 SDK/headless 和 title 请求。

这个顺序能先把最大流量的普通交互 main 请求对齐，再逐步覆盖特殊路径。
