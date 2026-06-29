# Claude Code Body Profile 与 Header Policy

本文档把 `/v1/messages` 请求拆成可实现的 body-first 分类规则，并给出对应的安全 header 生成策略。目标是做兼容转发和路由判断，不把调用方 body 当成伪造官方客户端网络指纹的依据。

## 资料来源

- 抓包、bundle 还原、exe 逆向资料根目录：`C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis`
- 本机 Claude Code exe：`C:\Users\Administrator\AppData\Roaming\npm\node_modules\@anthropic-ai\claude-code\bin\claude.exe`
- `docs/CLAUDE_CODE_REQUEST_PROFILES.md`
- `docs/CLAUDE_CODE_TTY_COMPAT_PLAN.md`
- `C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\recovered\CANONICAL_TTY_MATRIX.md`
- `C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\recovered\exact_renamed\03_messages_request.exact-renamed.js`
- `C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\recovered\exact_renamed\04_headers_deep.exact-renamed.js`
- `C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\recovered\exact_renamed\05_system_prompt.exact-renamed.js`
- `C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\recovered\exact_renamed\06_identity_body_cache.exact-renamed.js`

## 总原则

入站 header 不可信。是否像 Claude Code，应优先看 body 的结构特征，尤其是 `system` 数组和 `system[0].text` 的 billing attribution。

不要按 `main`、`title` 两类硬分。Claude Code 至少有主请求、CN side query、structured one-shot、forked query、maintenance/probe、count_tokens 这些 body 家族。

header 不按每个 `querySource` 硬编码一套模板。实际 header 是几个轴组合出来的：provider/auth、endpoint、body-derived beta、运行上下文、动态 request id。

body profile 可以决定 API 兼容所需的业务 header，例如 `anthropic-beta`，也可以约束 `X-Claude-Code-Session-Id` 与 metadata 的会话一致性。`x-client-request-id` 由发送层每次生成。body 不应反推伪造 `User-Agent`、TLS/JA3、HTTP2 指纹、transport header 或不可验证的 runtime header。

## 分类入口

先按 endpoint 分流：

| Endpoint | 分类入口 | 说明 |
| --- | --- | --- |
| `/v1/messages?beta=true` | body profile classifier | 常规 messages、main、side query、title、probe 都在这里 |
| `/v1/messages/count_tokens?beta=true` | count_tokens profile | 不套 main/title 规则，header 追加 token counting beta |
| 其他 Anthropic beta endpoint | 不进入本分类器 | sessions、skills、files 等另走对应 endpoint policy |

对 `/v1/messages`，先提取这些通用信号：

| 信号 | 判断 |
| --- | --- |
| `system` | 是否为数组、字符串、缺失 |
| billing block | `system[0].text` 是否以 `x-anthropic-billing-header:` 开头，是否含 `cc_version=`、`cc_entrypoint=`、`cch=` |
| identity block | `system[1].text` 是否是 CLI identity、Agent SDK identity，或缺失 |
| structured output | 是否存在 `output_config.format` 或旧的 `output_format` |
| thinking | `thinking.type` 是 `adaptive`、`enabled`、`disabled`，或缺失 |
| tools | 缺失、空数组、完整工具数组、classifier 小工具数组 |
| context_management | 是否存在，是否为 `clear_thinking_20251015` |
| max_tokens | 1、64/256、1024、8192、32000/64000 等区间 |
| metadata | `metadata.user_id` 是否为 JSON 字符串形态，是否含 `device_id/account_uuid/session_id` |

## Body Profiles

### 1. `cc_main_tty`

这是交互 TTY 主对话形态。

| 项 | 特征 |
| --- | --- |
| system | 常见 4 段：billing、official CLI identity、global cache block、ordinary ephemeral block |
| billing | `cc_entrypoint=cli` |
| body | `model,messages,system,tools,tool_choice?,metadata,max_tokens,thinking,context_management?,output_config?,stream` |
| tools | 常见完整 TTY 工具集，但不能把工具集当硬条件，因为用户和环境会影响工具 |
| thinking | Sonnet/Opus 常见 `adaptive`；Haiku 抓包为 `enabled` |
| output_config | 非 Haiku 常见 `effort`，Haiku 常见缺失 |
| confidence | billing + `cc_entrypoint=cli` + official identity + main 字段组合时为高 |

Header policy：

- `anthropic-beta` 按模型 main profile 生成，或从 body `betas` 经白名单校验、去重后转 header。
- 如果 body 有 `context_management`，最终 beta 必须含 `context-management-2025-06-27`，否则按 provider policy 删除该字段。
- 如果 body 有 `output_config.effort`，最终 beta 应含 `effort-2025-11-24`。
- `X-Claude-Code-Session-Id` 从当前账号生成的 `metadata.user_id.session_id` 同源设置。
- `User-Agent`、`x-app`、`X-Stainless-*` 由当前 outbound client profile 决定，不从入站 body 反推。

### 2. `cc_main_sdk`

这是 Agent SDK 或 `sdk-cli` 主线形态。

| 项 | 特征 |
| --- | --- |
| system | 与 main 类似，但 identity 可能是 Agent SDK 文案 |
| billing | 常见 `cc_entrypoint=sdk-cli`，也可能带 agent/workload 标记 |
| body | 主 queryModel 字段顺序，和 TTY main 近似 |
| tools | 工具数量和 timeout 可能不同于 TTY |
| confidence | billing + SDK identity 或 `cc_entrypoint=sdk-cli` 时为高 |

Header policy：

- 保留其作为 Claude Code family 的识别结果，不默认改写为 TTY profile。
- `anthropic-beta` 仍按 body/model/context/output_config 推导。
- 如果产品策略要求统一走 TTY 兼容，应显式配置，而不是由入站 body 自动升级。

### 3. `cc_title_structured`

这是标题生成、rename、teleport title 这类 structured output 请求。

| 项 | 特征 |
| --- | --- |
| system | 常见 3 段：billing、identity、任务 prompt；通常无 system cache_control |
| body | `model,messages,system,tools?,metadata,max_tokens,thinking,temperature,output_config,stream?` |
| output_config | `format.type=json_schema`，schema 常含 `title`，teleport 还可能含 `branch` |
| thinking | 通常 `disabled` |
| temperature | 常见 `1` |
| tools | 缺失或空数组 |
| confidence | `output_config.format` + title-like schema + thinking disabled 为高 |

Header policy：

- `anthropic-beta` 必须包含 `structured-outputs-2025-12-15`。
- 其余 beta 从 body 原始 `betas` 或模型基础 beta 来，不能套 main 工具/effort 模板。
- 不主动为普通用户主对话补发 title 请求。

### 4. `cc_cn_side_query`

这是 `CN(...)` direct side query。它不走主 queryModel 的完整字段顺序。

| 项 | 特征 |
| --- | --- |
| system | 可能是数组、字符串或 block；`skipSystemPromptPrefix:true` 时可能没有 identity |
| body | `model,max_tokens,system,messages,tools?,tool_choice?,output_config?,temperature?,stop_sequences?,thinking?,metadata,extraBodyParams` |
| thinking | `false` 转 `disabled`；数字转 `enabled` 且 budget 不超过 `max_tokens-1` |
| output_config | 由 `output_format` 条件转换 |
| temperature | 只有模型支持时发送 |
| confidence | billing 存在但 identity 缺失，且字段顺序/小 max_tokens/side query 字段吻合时为中高 |

Header policy：

- `anthropic-beta` 从 body `betas` 和实际字段推导，不套 TTY main 全量 beta。
- 如果有 `output_config.format`，追加 structured output beta。
- 如果有 `context_management`，按 context-management beta 规则处理。
- 保留 `stop_sequences`，不要因它不像主请求而删除。

### 5. `cc_auto_mode_classifier`

这是 CN side query 的 auto mode 子类。

| 项 | 特征 |
| --- | --- |
| querySource 线索 | 代码中为 `auto_mode`，body 通常没有显式 querySource |
| system | 常见 `skipSystemPromptPrefix:true`，可能无 identity |
| max_tokens | fast 阶段常见较小值，stage2 可更大 |
| messages | 用户内容常追加 classifier XML/规则判断 prompt |
| stop_sequences | 非 fast 阶段可能带 `"</block>"` |
| temperature | classifier 专用值，可能不同于主请求 |
| confidence | body-only 只能中等置信，需要结合 system/task prompt 关键词 |

Header policy：

- 只根据实际 body 字段生成 beta。
- 不用 main profile 补 tools、tool_choice、output_config 或 temperature。

### 6. `cc_permission_or_context_tip_side_query`

这是 `context_tip_classifier`、`context_tip_reception`、`permission_explainer` 等 side query。

| 项 | 特征 |
| --- | --- |
| body | CN-like 字段顺序 |
| tools | 常有小型工具或单一工具 |
| tool_choice | 常显式指定工具 |
| temperature | 常见 `0` |
| max_tokens | 通常小于主对话 |
| confidence | tools/tool_choice + 小模型/小 token + side system prompt 时为中高 |

Header policy：

- 透传工具和 `tool_choice`。
- `anthropic-beta` 不套 main 工具集模板，只从 body 需要的能力推导。

### 7. `cc_structured_one_shot`

这是 `gF(...)`、`pgt(...)`、`F8e(...)` 一次性 structured query。

| 项 | 特征 |
| --- | --- |
| system | billing + identity + 任务 prompt，非交互时 identity 可为 SDK variant |
| body | `messages` 简短，`tools:[]` 或无工具 |
| thinking | `disabled` |
| output_config | 常有 JSON schema |
| 典型用途 | `generate_session_title`、`mcp_datetime_parse`、`insights`、`hook_prompt`、`agent_creation` |
| confidence | structured output + disabled thinking + 空工具为高 |

Header policy：

- 追加 structured output beta。
- 不按 main profile 补 `context_management`、effort 或完整工具相关 beta。

### 8. `cc_forked_main_derived`

这是 `z0(...)`、`U4(...)` forked query，包括 compact、agent summary、away summary、prompt suggestion、session search、hook agent。

| 项 | 特征 |
| --- | --- |
| body | 可能很像 main queryModel，因为它继承 cacheSafeParams |
| messages | 常含 summary/search/hook/fork context |
| tools | compact 可能禁用工具，session_search 可能带 Grep/Read 类工具 |
| system | 可能继承主会话 system，也可能是 summary prompt |
| confidence | body-only 很难精确识别，只能判为 main-derived subquery |

Header policy：

- 如果 body 本身带 `betas`，以 body betas 为主。
- 如果不能高置信识别具体子类，不要补 title 或 main 之外的专项 beta。
- 不凭 prompt 文案强行重写 system。

### 9. `cc_count_tokens`

这是 `/v1/messages/count_tokens?beta=true`。

| 项 | 特征 |
| --- | --- |
| endpoint | `/v1/messages/count_tokens?beta=true` |
| body | 不应带 `betas`，不套 messages create 的 stream/main 规则 |
| system | 可缺失或不完整 |
| confidence | endpoint 直接决定 |

Header policy：

- `anthropic-beta` 在已有 beta 基础上追加 `token-counting-2024-11-01`。
- 不补 main 默认值，不补 `max_tokens`、`temperature`、`context_management`。

### 10. `cc_probe_or_maintenance`

这是 quota check、verify API key、Haiku `max_tokens:1` probe 等。

| 项 | 特征 |
| --- | --- |
| max_tokens | 常见 `1` |
| messages | 常见 `quota`、`test`、`count` 这类最小内容 |
| system | 可能缺失 |
| metadata | 可能有，也可能很简化 |
| confidence | 如果缺 billing，只能判为维护请求，不能判为完整 Claude Code family |

Header policy：

- 不套 main/title system。
- 只生成 auth、基础 API header、必要 beta。

### 11. `generic_anthropic_messages`

不满足 Claude Code body family 的普通 Anthropic 请求。

| 项 | 特征 |
| --- | --- |
| system | 缺失、字符串、自定义数组，且无 billing attribution |
| body | 普通 Anthropic Messages schema |
| metadata | 调用方自定义或缺失 |
| confidence | 默认落点 |

Header policy：

- 按项目普通 Anthropic 转发策略处理。
- 如果使用 Claude OAuth 账号，`metadata.user_id` 必须由当前账号重建，`account_uuid` 缺失应报错。
- 不从调用方 header 或 body 推导 Claude Code 身份。

## 字段处理矩阵

| 字段 | 处理 |
| --- | --- |
| `model` | 按路由和模型映射处理，保留调用方语义；CCH 或 hash 类逻辑不应把模型值当身份凭据 |
| `messages` | 语义透传，仅做协议兼容转换、必要的 cache breakpoint 或安全过滤 |
| `system` | 已识别为 Claude Code family 时尽量保留；项目主动生成 Claude OAuth 兼容请求时才重建 |
| `tools` | 调用方传什么用什么；缺失时是否补 `[]` 只属于显式 TTY-compat 主请求策略 |
| `tool_choice` | side query 和 generic 请求透传；主请求只有在明确复刻 Claude Code thinking 降级规则时才改 |
| `betas` | 从 JSON body 移除，转为 `anthropic-beta` header；去重并稳定顺序 |
| `metadata` | 不透传 `metadata.user_id`；按当前选中账号生成 JSON 字符串形态，`account_uuid` 必须存在 |
| `max_tokens` | 默认透传；只有显式 TTY-compat 主请求策略才按模型默认补齐或 cap |
| `thinking` | 透传；`disabled` 可做官方同类 sanitization，去除多余 key |
| `temperature` | 默认透传；不要因为主请求抓包有某个值就全局固定 |
| `context_management` | 若保留该字段，最终 beta 必须包含 context-management；否则按 provider policy 删除 |
| `output_config` | 透传；有 `format` 时追加 structured output beta，有 `effort` 时追加 effort beta |
| `output_format` | 旧 structured output 形态，透传；存在时也追加 structured output beta，不强制转换成 `output_config.format` |
| `stop_sequences` | side query 常用字段，透传 |
| `stream` | 透传；stream helper header 由 SDK/transport 层决定 |
| `fallbacks` / `fallback_credit_token` | 仅在 body 已存在或项目 fallback 分支生成时保留，不凭 profile 伪造 |
| `speed` / `diagnostics` / `context_hint` | 只在 body 已存在或项目对应功能真实启用时保留 |
| 未知字段 | 默认丢弃或按白名单透传，避免把调用方任意字段送到上游 |

## System 形态矩阵

`system` 的判断不要只看 `system[1]`。最稳定的 Claude Code family 锚点是 `system[0].text` 的 billing attribution；`system[1]` identity、`system[2]`、`system[3]` 都会随构造器、interactive/non-interactive、side query、cache boundary、forked query 变化。

### 1. `cc_main_tty`

交互 TTY 主对话最典型是 4 段：

```text
system[0] = billing attribution
system[1] = official CLI identity
system[2] = static/global prompt, cache_control: {type:"ephemeral", scope:"global"}
system[3] = dynamic/environment prompt, cache_control: {type:"ephemeral"}
```

关键特征：

- `system[0].text` 形如 `x-anthropic-billing-header: cc_version=...; cc_entrypoint=cli; cch=...;`。
- `system[1].text` 常见为 `You are Claude Code, Anthropic's official CLI for Claude.`。
- `system[2]` 和 `system[3]` 是 dynamic boundary 生效后的 prompt cache 拆块。
- `system[2].cache_control.scope` 常见为 `global`，`system[3].cache_control` 常见只有普通 `ephemeral`。
- `system[3]` 使用 TTY 抓包动态块模板（`# Text output` / `# Environment` 等锚点），不再使用项目自写的单句占位；无法可靠来源于调用方会话的 git/user/status/recent-commits 子段直接省略，不用当前代理环境填充，也不写 `unknown` / `unavailable` 占位。

### 2. `cc_main_sdk`

SDK main 和 TTY main 结构接近，但 identity 和 entrypoint 不同：

```text
system[0] = billing attribution
system[1] = Agent SDK identity 或 Claude Code within Agent SDK identity
system[2] = static/cache prompt
system[3] = dynamic prompt
```

关键特征：

- billing 常见 `cc_entrypoint=sdk-cli`，也可能带 agent/workload 标记。
- `system[1]` 可能是 `You are a Claude agent, built on Anthropic's Claude Agent SDK.`。
- `system[1]` 也可能是 `You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.`。
- 不能只因为它不是 official CLI identity 就判定不是 Claude Code family。

### 3. `cc_title_structured`

title/rename/teleport title 这类 structured query 常见 3 段：

```text
system[0] = billing attribution
system[1] = identity
system[2] = title/rename/structured task prompt
```

关键特征：

- 通常不像 main 那样拆成 4 段 cache boundary。
- 常见无 `system[2].cache_control.scope="global"`。
- `system[2]` 的任务 prompt 与 `output_config.format` 的 JSON schema 对应，例如 schema 里要求 `title` 或 `branch`。

### 4. `cc_cn_side_query`

CN direct side query 的 `system` 变化最大。基础构造接近：

```text
system[0] = billing attribution
system[1] = identity? 或 side-query task prompt
system[2]? = 调用方传入的额外 side-query system
```

当 `skipSystemPromptPrefix:true` 生效时，identity 会被跳过：

```text
system[0] = billing attribution
system[1] = side-query task prompt
```

关键特征：

- `system` 可是数组、字符串或 text block，最终 body 形态比 main 更灵活。
- 调用方传入的 `cache_control` 可能保留。
- 这类请求不能用 `system[1] == official CLI identity` 判断，因为 `system[1]` 可能直接是任务 prompt。

### 5. `cc_auto_mode_classifier`

auto mode classifier 是 CN side query 子类，常见形态更接近：

```text
system[0] = billing attribution
system[1] = auto-mode/classifier task prompt
```

关键特征：

- 通常可能无 official CLI identity。
- `system[1]` 内容偏分类、规则判断、安全判断，而不是主对话身份。
- 可能配合 `stop_sequences:["</block>"]`、较小 `max_tokens`、classifier 专用 temperature。
- body-only 只能中等置信识别，不能强行当作 main。

### 6. `cc_permission_or_context_tip_side_query`

permission explainer、context tip classifier/reception 这类也是 CN-like：

```text
system[0] = billing attribution
system[1] = identity 或 permission/context-tip task prompt
system[2]? = permission/context-tip 专用 prompt
```

如果跳过 prefix，也可能是：

```text
system[0] = billing attribution
system[1] = permission/context-tip task prompt
```

关键特征：

- `system` 重点是解释权限、判断 context tip、接收 context tip，不是主对话身份。
- 常和小工具集、显式 `tool_choice`、`temperature=0` 一起出现。
- 不应删除这类请求自带的 task system 或 `tool_choice`。

### 7. `cc_structured_one_shot`

`gF(...)`、`pgt(...)`、`F8e(...)` 一次性 structured query 常见：

```text
system[0] = billing attribution
system[1] = identity
system[2] = one-shot task prompt
```

关键特征：

- 和 title structured 很像，但 `system[2]` 的任务可能是日期解析、insights、hook 判定、agent config 生成等。
- 常配合 `thinking.type="disabled"`、`tools:[]` 或无工具、`output_config.format`。
- 非交互路径下 `system[1]` 可能是 SDK variant。

### 8. `cc_forked_main_derived`

forked query 可能继承主会话 system，也可能替换为 summary/search/hook 专用 prompt。

继承主会话时可能像 main：

```text
system[0] = billing attribution
system[1] = main identity
system[2] = inherited static/global prompt
system[3] = inherited dynamic prompt
```

专用 fork 任务时可能像 structured/side query：

```text
system[0] = billing attribution
system[1] = identity
system[2] = compact/summary/search/hook task prompt
```

关键特征：

- compact、agent_summary、away_summary、prompt_suggestion、session_search、hook_agent 都可能落到这一类。
- 它看起来可能像 main，但语义是分叉/后台任务。
- 不要只因为 `system` 像 main 就强行按普通 TTY main 重写。

### 9. `cc_probe_or_maintenance`

probe/maintenance 请求可能没有完整 `system`：

```text
system = 缺失
```

也可能只有极简测试/探测 prompt：

```text
system = 少量 probe/maintenance prompt
```

关键特征：

- 常见 `max_tokens=1`。
- messages 内容可能是 `quota`、`test`、`count` 这类极短内容。
- 不一定有 billing attribution，也不一定有完整 metadata。
- 不能套 main/title 的 system 重建规则。

### 10. `generic_anthropic_messages`

普通 Anthropic Messages 请求不属于 Claude Code system family：

```text
system = 调用方字符串
```

或：

```text
system = 调用方自定义数组
```

或：

```text
system = 缺失
```

关键特征：

- 没有 `system[0]` billing attribution。
- 即使 `system` 文案看起来类似，也不应仅凭相似 prose 判为完整 Claude Code family。
- 如果使用 Claude OAuth 账号，项目可以按当前账号重建 metadata；是否重建 system 取决于明确的兼容策略。

### System 判断结论

稳定性从高到低：

```text
system[0] billing attribution > system 数组结构 > identity 类型 > cache_control boundary > 任务 prompt 文案
```

实现时应遵循：

- `system[0]` 是最稳定锚点。
- `system[1]` 不是稳定锚点，side query 可能没有 identity。
- `system[2]` / `system[3]` 在 main 里常表示 cache boundary，但在 forked/structured/side query 里可能只是任务 prompt。
- probe/maintenance 可能没有 system。
- generic Anthropic system 完全由调用方决定。

## Header 生成策略

基础 header 由当前出站账号和 HTTP client 生成：

- `Authorization` 或 `x-api-key` 来自当前选中账号。
- `Accept`、`Content-Type`、`anthropic-version` 按 Anthropic API 要求生成。
- `Content-Length`、`Host`、`Connection`、`Accept-Encoding` 由 transport 生成。
- `x-client-request-id` 每个 request/attempt 新生成。
- `X-Claude-Code-Session-Id` 与生成后的 `metadata.user_id.session_id` 保持一致。

实现边界：入站是否 Claude Code 只看 body family；出站是否 Claude OAuth 只看当前账号。只要走 Claude OAuth 的 Claude Code 兼容路径，`system[0]` 都按当前账号整体重写，`cc_entrypoint` 固定为 TTY 抓包口径的 `cli`，`User-Agent` 固定为 `claude-cli/<version> (external, cli)`；入站的 `cc_entrypoint` 不可信、不继承。入站不是 Claude Code family 时，system 重建是强制逻辑，不再受 `enable_claude_oauth_system_prompt_injection` 开关阻止；该设置最多只提供自定义 prompt/blocks 模板。入站已经是 Claude Code family 时，只保留 `system[1+]` 等后续 profile 内容，并继续按当前账号重写 metadata、刷新 billing/CCH、从 body profile 生成 beta。

`anthropic-beta` 是唯一应主要由 body profile 反推的业务 header：

| 条件 | beta 处理 |
| --- | --- |
| body 含 `betas` | 从 body 移除，转 header，去重 |
| `output_config.format` 或旧 `output_format` | 追加 `structured-outputs-2025-12-15` |
| count_tokens endpoint | 追加 `token-counting-2024-11-01` |
| `context_management` 存在 | 确保有 `context-management-2025-06-27`，否则删除 body 字段 |
| `output_config.effort` 存在 | 确保有 `effort-2025-11-24` |
| Haiku main profile | 使用 Haiku main beta 顺序 |
| Opus 4.8 main profile | 在 main beta 中加入 `mid-conversation-system-2026-04-07` |
| 普通 Sonnet/Opus main profile | 使用 main beta 顺序 |
| side query / structured one-shot | 只加实际字段需要的 beta，不套 main 全量 beta |

可选 header 只能来自真实运行上下文：

| Header | 规则 |
| --- | --- |
| `traceparent` | 只有当前服务端 tracing context 存在并允许传播时带 |
| `x-cc-atis` | 只有当前账号/session 真实有 atis 上下文时带 |
| `x-stainless-helper` | SDK helper 符号或工具 runner 真实产生时带；body-only classifier 不能凭空合成 |
| remote/container/agent headers | 只有当前运行上下文真实存在时带 |
| custom headers | 只允许受信配置注入，不接受调用方任意覆盖 |

## 实现建议

新增一个独立 classifier，不复用现有 UA-first validator：

```text
ClassifyClaudeMessagesBody(endpoint, body) -> {
  family,
  confidence,
  billingEntryPoint,
  hasBilling,
  hasStructuredOutput,
  hasContextManagement,
  hasEffort,
  isCountTokens,
  betaHints,
  systemPolicy
}
```

推荐优先级：

1. endpoint 判定 count_tokens。
2. 解析 `system[0].text` billing attribution。
3. 根据 `system` 数量、identity、structured output、tools、thinking、max_tokens、context_management 分类。
4. 只用分类结果生成 `anthropic-beta`、metadata/session 一致性和 body normalization。
5. UA、TLS、HTTP2、transport header 不进入 body classifier。

需要补的测试：

- TTY main Sonnet/Opus/Haiku 分类。
- Opus 4.8 beta 额外项。
- title structured output 分类。
- CN side query 无 identity 分类。
- auto_mode 带 `stop_sequences` 分类。
- count_tokens 不补 main 默认值。
- generic Anthropic 请求不误判为 Claude Code。
- metadata 缺 `account_uuid` 时返回明确错误。
