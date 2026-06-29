# Claude Code 请求 Profile 探究记录

本文档记录对本机 Claude Code `2.1.191` 的抓包、JS bundle 还原与 `claude.exe` 逆向分析结论。重点是 `/v1/messages` 请求的 body profile、`system` 结构、header 差异来源，以及后续实现 body-first 识别时应采用的判断依据。

## 资料来源

- 抓包与还原目录：`C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis`
- Claude Code exe：`C:\Users\Administrator\AppData\Roaming\npm\node_modules\@anthropic-ai\claude-code\bin\claude.exe`
- TTY canonical profile：
  - `C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\recovered\CANONICAL_TTY_PROFILE.json`
  - `C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\recovered\CANONICAL_TTY_MATRIX.md`
- 关键还原文件：
  - `recovered\exact_renamed\03_messages_request.exact-renamed.js`
  - `recovered\exact_renamed\05_system_prompt.exact-renamed.js`
  - `recovered\exact_renamed\06_identity_body_cache.exact-renamed.js`
  - `recovered\exact_renamed\07_fingerprint_headers_body.exact-renamed.js`
  - `recovered\auth_headers.recovered.js`
  - `recovered\headers_deep.recovered.js`

已解析 `captures\claude_2.1.191*.json` 中 84 个 `/v1/messages` body。实抓聚合结果：

| Profile | 数量 | 入口 |
| --- | ---: | --- |
| `tty_title` | 14 | `cc_entrypoint=cli` |
| `tty_main` | 14 | `cc_entrypoint=cli` |
| `sdk_title` | 20 | `cc_entrypoint=sdk-cli` |
| `sdk_main` | 36 | `cc_entrypoint=sdk-cli` |

注意：这些实抓覆盖了 TTY 与 SDK 主线，但不能代表 Claude Code 代码里所有 querySource 分支。更多细类需要结合 bundle 代码推导。

## 总体结论

不要把 Claude Code 请求简单分成 `main` 和 `title query` 两类。更准确的划分应先看请求构造器路径，再看 `system` 和 body 细节。

核心分层：

1. **body 构造器家族**：主 queryModel、CN direct side query、gF/pgt one-shot、z0/U4 forked query、maintenance/probe。
2. **system 生成分支**：billing block、identity selector、cache boundary 拆块。
3. **header profile**：不是每个 querySource 一套独立 header，而是由 entrypoint、provider/auth、body-derived betas、运行上下文共同决定。

## System 结构

### billing block

`system[0].text` 是最稳定的 Claude Code body family 锚点：

```text
x-anthropic-billing-header: cc_version=2.1.191.<fp>; cc_entrypoint=<entrypoint>; cch=<5hex>;
```

代码路径：

- `buildBillingHeaderSystemText(...)`
- `CLAUDE_CODE_ATTRIBUTION_HEADER` 为真时会禁用 billing block。
- `cc_entrypoint` 来自 `CLAUDE_CODE_ENTRYPOINT`，默认 `cli`。
- firstParty/Vertex 条件下 JS 层先生成 `cch=00000;`，native HTTP 发送层再写回非零 5 hex。CCH 细节见 `docs/CLAUDE_CODE_CCH_NATIVE.md`。
- 还可能附加 `cc_workload=...;`、`cc_is_subagent=true;`。当前代理会整块重写 `system[0]`，只从入站 billing 文本中白名单提取这两个可选字段：`cc_workload` 必须匹配 `[A-Za-z0-9_-]{1,128}`，`cc_is_subagent` 只接受精确的 `true`。

### identity selector

`system[1]` 不一定永远是 official CLI identity。选择逻辑：

| 条件 | identity |
| --- | --- |
| Vertex provider | `You are Claude Code, Anthropic's official CLI for Claude.` |
| interactive/default | `You are Claude Code, Anthropic's official CLI for Claude.` |
| non-interactive + `hasAppendSystemPrompt` | `You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.` |
| non-interactive + no append | `You are a Claude agent, built on Anthropic's Claude Agent SDK.` |

CN direct side query 如果传 `skipSystemPromptPrefix:true`，会跳过 identity prefix。此时 `system[0]` 仍可能是 billing block，但 `system[1]` 直接变成任务自己的 system prompt。

### cache boundary

system cache 拆分不只一种：

| 情况 | system block 形态 |
| --- | --- |
| 有 `SYSTEM_PROMPT_DYNAMIC_BOUNDARY` 且 prompt cache scope 生效 | billing 无 cache；identity 无 cache；boundary 前静态 prompt 为 `cache_control:{type:"ephemeral",scope:"global"}`；boundary 后动态 prompt 为普通 `cache_control:{type:"ephemeral"}` |
| tool-based prompt cache 且无 dynamic boundary | billing 无 cache；identity 与剩余 prompt 可能走 org scope，但 `makeCacheControl` 不显式写 `scope:"org"` |
| side query / title query | 常见 3 段，无 system cache；但 CN direct 可带调用方传入的自定义 `cache_control` |

普通 `{type:"ephemeral"}` 不默认补 `ttl:"5m"`。只有 1h cache 命中时，会对已有 cache_control 补 `ttl:"1h"`。

## Body 构造器家族

### 1. 主 queryModel / TTY main / SDK main

代码主线在 `messages_request` 的主请求构造器。典型 querySource：

- `repl_main_thread*`
- `sdk`
- `agent:*`
- `hook_agent`
- `session_search`
- `compact`
- `agent_summary`
- `away_summary`
- `prompt_suggestion`

最终 body 形态近似：

```text
model,
messages,
system,
tools,
tool_choice,
betas,
metadata,
max_tokens,
thinking,
temperature,
context_management,
context_hint / fallbacks / fallback_credit_token / extraBodyParams,
output_config,
speed,
diagnostics
```

关键条件：

- `thinking` 非 disabled 时通常不发送 `temperature`。
- `tool_choice:{type:"tool"}` 遇到 extended thinking 会降级为 `{type:"auto"}`。
- `context_management` 只有在有 thinking、body 构造允许、且 beta 包含 context-management 时发送。
- `output_config` 可能来自 effort、task_budget 或 structured output。
- `context_hint` 只对 `repl_main_thread*` 的 context hint controller 分支有效。
- fallback/credit 分支可能额外插入 `fallbacks`、`fallback_credit_token`。
- Bedrock/provider 分支会把部分 beta 变成 body 内 `anthropic_beta`。

TTY main 实抓：

- `system` 4 段。
- `system[0]` billing，`cc_entrypoint=cli`。
- `system[1]` official CLI identity。
- `system[2]` global cache。
- `system[3]` ordinary ephemeral cache。
- non-Haiku 有 `output_config.effort`。
- Haiku 无 `output_config`，`thinking:{type:"enabled",budget_tokens:31999}`。

SDK main 实抓：

- body/cache 边界和主请求类似。
- `system[1]` 是 Agent SDK identity。
- `cc_entrypoint=sdk-cli`。
- 工具数量、timeout、beta 顺序可能与 TTY 有差异。

### 2. CN direct side query

`CN(...)` 是独立 side query 构造器，不走主 queryModel 的完整 body 顺序。典型 querySource：

- `auto_mode`
- `auto_mode_critique`
- `context_tip_classifier`
- `context_tip_reception`
- `permission_explainer`
- 部分 classifier / memory selection 类 side query

body 顺序近似：

```text
model,
max_tokens,
system,
messages,
tools?,
tool_choice?,
output_config?,
temperature?,
stop_sequences?,
thinking?,
betas?,
metadata,
extraBodyParams
```

关键条件：

- `skipSystemPromptPrefix:true` 时不加 official CLI / SDK identity。
- `system` 可是字符串、数组或 block；调用方传入的 cache_control 会保留。
- `thinking:false` 会变成 `thinking:{type:"disabled"}`。
- `thinking:<number>` 会变成 `thinking:{type:"enabled",budget_tokens:min(thinking,max_tokens-1)}`。
- `output_format` 只在模型支持 structured outputs 时转成 `output_config:{format:...}`。
- `temperature` 只有模型支持时发送。
- `stop_sequences` 可由具体 classifier 分支携带，例如 auto_mode fast 阶段会带 `"</block>"`。

这类请求不能用 `system[1] == official CLI identity` 判断是否 Claude Code，因为它可能没有 identity。

### 3. gF / pgt one-shot structured query

`gF(...)`、`pgt(...)` 最终调用 `F8e(...)`，通常用于轻量 structured output 或一次性辅助任务。典型 querySource：

- `generate_session_title`
- `teleport_generate_title`
- `rename_generate_name`
- `mcp_datetime_parse`
- `insights`
- 部分 `hook_prompt`
- `agent_creation`

常见特征：

- `thinkingConfig:{type:"disabled"}`。
- `tools:[]`。
- 可能带 `outputFormat:{type:"json_schema",schema:...}`，最终成为 `output_config.format`。
- 通常不使用完整 TTY main 工具集。
- system 一般是 billing + identity + 任务 prompt；如果非交互，identity 可能是 SDK variant。

### 4. z0 / U4 forked query

`z0(...)` 是 forked agent/query 包装，内部走 `U4(...)` 主循环。典型 querySource：

- `compact`
- `agent_summary`
- `away_summary`
- `prompt_suggestion`
- `session_search`
- `hook_agent`

常见特征：

- 带 `forkLabel`、`maxTurns`、`skipTranscript`、`skipCacheWrite` 等语义。
- 可从 `cacheSafeParams` 继承主会话 system、toolUseContext、fork context messages。
- 可能走主 queryModel body 形态，而不是 CN direct side query。
- `compact` 会禁止工具，并用 summary prompt；`session_search` 会带 Grep/Read 类工具；`hook_agent` 会启用非交互/structured output 逻辑。

### 5. maintenance / probe

维护或探测请求不一定有 Claude Code system profile。

已见代码路径：

- `quota_check`
- `verify_api_key`
- `count_tokens`
- Haiku `max_tokens:1` probe

常见特征：

- `max_tokens:1`。
- 可能没有 top-level `system`。
- `messages` 常是 `test`、`count` 这类最小内容。
- `count_tokens` 走 `/v1/messages/count_tokens?beta=true`，header beta 会附加 `token-counting-2024-11-01`。

这类请求不能按 main/title 规则强行套 system。

## querySource 枚举

从 `modules\*.js` 中扫到的显式 querySource：

| querySource | 主要构造路径 | 备注 |
| --- | --- | --- |
| `agent_creation` | `F8e` | 生成 agent config，thinking disabled，tools 空 |
| `agent_summary` | `z0` | 后台 agent summary |
| `auto_mode` | CN-like classifier wrapper | `skipSystemPromptPrefix:true`，可能分 fast/stage2 |
| `auto_mode_critique` | `CN` | 分析 auto mode rules |
| `away_summary` | `z0` | 用户离开后的简短 recap |
| `bash_extract_prefix` | policy/classifier wrapper | Bash prefix 分类 |
| `compact` | `z0` / main fork | 压缩会话上下文 |
| `context_tip_classifier` | `CN` | tools + tool_choice，temperature 0 |
| `context_tip_reception` | `CN` | tools + tool_choice，temperature 0 |
| `generate_session_title` | `gF` | JSON schema title |
| `hook_agent` | `U4` | 非交互 agent hook |
| `hook_prompt` | `F8e` | JSON schema hook 判定 |
| `insights` | `pgt` | insight summarization |
| `mcp_datetime_parse` | `gF` | 日期解析 |
| `permission_explainer` | `CN` | tools + tool_choice |
| `prompt_suggestion` | `z0` | prompt suggestion |
| `rename_generate_name` | `gF` / `z0` | session rename |
| `sdk` | `U4` / main | Agent SDK 用户输入主线 |
| `session_search` | `U4` | 带 Grep/Read 工具 |
| `teleport_generate_title` | `gF` | title + branch JSON schema |

## Header profile

header 不应理解为“每个 querySource 一套模板”。还原代码显示，它主要由 Anthropic SDK client 统一合成，再由 auth/provider 与本次 body 所需 beta 修正。

### 通用 header skeleton

典型 TTY/SKD firstParty OAuth `/v1/messages?beta=true` 顺序：

1. `Accept`
2. `Authorization`
3. `Content-Type`
4. `User-Agent`
5. `X-Claude-Code-Session-Id`
6. `X-Stainless-Arch`
7. `X-Stainless-Lang`
8. `X-Stainless-OS`
9. `X-Stainless-Package-Version`
10. `X-Stainless-Retry-Count`
11. `X-Stainless-Runtime`
12. `X-Stainless-Runtime-Version`
13. `X-Stainless-Timeout`
14. `anthropic-beta`
15. `anthropic-dangerous-direct-browser-access`
16. `anthropic-version`
17. `x-app`
18. `x-client-request-id`
19. `Connection`
20. `Host`
21. `Accept-Encoding`
22. `Content-Length`

SDK merge order：

```text
idempotency
-> SDK defaults
-> auth headers
-> Claude Code defaultHeaders
-> body headers
-> per-request headers
```

### 实抓 header 差异

| Profile | User-Agent | x-app | Timeout | 主要差异 |
| --- | --- | --- | --- | --- |
| TTY title | `claude-cli/2.1.191 (external, cli)` | `cli` | `600` | `anthropic-beta` 为 title/structured output 组合 |
| TTY main | `claude-cli/2.1.191 (external, cli)` | `cli` | `600` | beta 随模型、thinking、context-management、effort 变化 |
| SDK title | `claude-cli/2.1.191 (external, sdk-cli)` | `cli` | 多数 `600`，少量 `300` | beta 为 title/structured output 组合 |
| SDK main | `claude-cli/2.1.191 (external, sdk-cli)` | `cli` | `600` 或 `300` | beta 随模型/effort/1h cache/fallback 变化 |

### header 差异轴

1. **entrypoint**
   - UA 形如 `claude-cli/2.1.191 (external, <entrypoint>[, agent-sdk/...][, client-app/...][, workload/...])`。
   - `cli`、`sdk-cli` 是当前抓包覆盖的主轴。
   - 代码还支持 `claude-vscode`、`remote*`、`mcp`、`claude-code-github-action`、`local-agent`、Slack/Teams 等 entrypoint。

2. **provider/auth**
   - OAuth：`Authorization: Bearer ...`，并可能带 OAuth beta。
   - API key：`x-api-key`。
   - Bedrock/Vertex/Gateway/Mantle/Foundry 走各自 SDK/provider 路径，header/body beta 位置会不同。

3. **body-derived beta**
   - title/structured output：带 `structured-outputs-2025-12-15`。
   - main：带 `claude-code`、thinking、context-management、prompt-caching-scope、advanced-tool-use、effort 等。
   - Opus 4.8：额外有 `mid-conversation-system-2026-04-07`。
   - count_tokens：带 `token-counting-2024-11-01`。
   - 1h cache/context hint/diagnostics/fallback 可能追加额外 beta。

4. **运行上下文**
   - 后台 session：`x-app` 可为 `cli-bg`。
   - remote/container：可能有 `x-claude-remote-container-id`、`x-claude-remote-session-id`。
   - Agent context：可能有 `x-claude-code-agent-id`、`x-claude-code-parent-agent-id`。
   - `CLAUDE_AGENT_SDK_CLIENT_APP`：会加入 `x-client-app`，UA 也可能带 client-app 后缀。

5. **动态请求值**
   - `X-Claude-Code-Session-Id` 与 `metadata.user_id.session_id` 同源。
   - `x-client-request-id` 每次请求/重试不同。
   - `Content-Length`、`Host`、`Connection`、`Accept-Encoding` 由 transport 决定。

## 识别与实现建议

### body-first classifier

调用方 header 不可信，识别真实 Claude Code 请求应以 body 特征为主：

1. 判断 `system` 是否为数组。
2. 判断 `system[0].text` 是否是 billing attribution。
3. 从 billing 中解析 `cc_version`、`cc_entrypoint`、`cch`、可选 workload/subagent 标记。
4. 再按 body 构造器家族分类：
   - 主 queryModel：`model,messages,system,tools,...`，常见 4 段 system/cache boundary。
   - CN direct：`model,max_tokens,system,messages,...`，可能无 identity。
   - structured one-shot：`thinking disabled`、`tools:[]`、`output_config.format`。
   - forked query：有 fork/summary/search/hook 形态，可能继承主 system。
   - probe/count_tokens：可能无 system，不套 main profile。
5. header 只作为诊断和生成上游请求时的 profile 参考，不作为可信入站判断条件。

### system 处理原则

- 已识别为 Claude Code body family 的请求，不应只因为 `system[1]` 不是 official CLI 就重写。
- `skipSystemPromptPrefix:true` 的 CN direct side query 是合法 Claude Code 形态，可能没有 identity。
- 未识别为 Claude Code body family、但需要按当前账号走 Claude OAuth 时，再按目标 profile 重建 system。
- `metadata.user_id` 不透传，必须按当前选中账号稳定生成；账号缺少 `account_uuid` 应直接报错。

### header 生成原则

不要按 19 个 querySource 硬编码 19 套 header。应组合生成：

```text
基础 SDK header skeleton
+ entrypoint profile
+ provider/auth profile
+ body-derived anthropic-beta
+ optional runtime/agent/remote headers
+ dynamic request ids / transport headers
```

TTY 兼容实现的默认基线仍应使用 `cc_entrypoint=cli`、`User-Agent: claude-cli/2.1.191 (external, cli)`、`x-app: cli`。SDK/Agent 请求应作为可识别和保留的 Claude Code family，而不是 TTY 默认伪装基线。
