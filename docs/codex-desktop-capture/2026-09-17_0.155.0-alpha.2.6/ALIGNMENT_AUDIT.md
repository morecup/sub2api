# 非遥测部分的对齐复核（2026-09-18）

本文保留修复前的审计证据与离线失败结果。随后按“除限流与重试外尝试修复”的要求进行了代码修改，当前状态见 [修复记录](REPAIRS_2026-09-18.md)，不能把下表当成修复后的状态。

当前实现存在请求生命周期与会话状态方面的明确差异。此前完成的五模型抓包矩阵、字段回归和短时发布检查，不能证明网关在长时间运行中完整复现了官方客户端行为。本次只读分析既有抓包与代码，并以合成数据做离线复现；没有向官方发送验证请求。

## 已确认差异

| 项目 | 抓包／代码证据 | 当前实现与影响范围 |
|---|---|---|
| 同一 turn 的工具续接 | 202 条模型请求中，24 组普通用户 turn 在多次请求中复用 turn_id，共 120 条请求；24 组的开始时间均保持不变，均包含工具输出续接。其中 HTTP 10 组、WS 14 组 | HTTP 每次重建请求时生成新的 turn_id 和 turn_started_at_unix_ms，丢失同一用户 turn 的关联语义 |
| 压缩后的上下文窗口 | 14 次窗口变化均发生在 compaction → turn；HTTP 6 次、WS 8 次。window_number 从 0→1 或 1→2，window_id 与 context_window_id 同时改变，后续输入带 compaction 项 | metadata 生成器把 window_number 固定为 0，window_id 固定为 session:0，context_window_id 仅由 session 派生；压缩后仍表示初始窗口 |
| 固定上游会话模式 | 代码显式优先采用账号固定 session，不再按下游会话种子隔离 | 开启这个配置时，不同下游任务可共用上游 session/thread/context 标识。这是配置引入的行为差异，不应与默认任务隔离混为一谈 |
| HTTP Cookie 生命周期 | 本轮 80 次 HTTP 模型请求全部携带 Cookie | 模型转发只读取账号凭据中预存的 Cookie；共享 http.Client 未设置 CookieJar，未发现该路径消费 Set-Cookie 并更新后续请求的逻辑。是否有 Cookie 取决于导入的数据；不能据此推定 Cookie 是模型请求的必需条件或处罚依据 |
| 限流处理的自定义分支 | 代码存在跳过 429 冷却、额度条件下添加 noop 工具调用及结果的分支 | 这些行为没有本轮官方抓包证据支持，不能纳入“已对齐”的结论。分支是否触发需看具体账号和错误记录；开关启用不等于实际执行 |
| 部分管理接口的代理失败处理 | CodexDesktopAPIService.resolveProxyURL 在配置了代理但查询失败／记录不存在时返回空字符串，调用者继续创建客户端 | 邀请及额度重置积分相关接口可能从指定代理退回直连。它与模型／遥测链路的代理处理不是同一条路径，不能用后者的验证替代前者；此处未进行故障注入 |

主要代码定位：

- [turn 与窗口 metadata](../../../backend/internal/service/openai_codex_mimic.go)：`buildCodexTurnMetadata`、`buildCodexCompactionMetadata`、`generateCodexContextWindowUUID`、`applyCodexOAuthMimicHeaders`。
- [固定会话优先级](../../../backend/internal/service/openai_codex_mimic.go)：`resolveCodexSessionUUID`。
- [预存 Cookie](../../../backend/internal/service/codex_desktop_webview_profile.go)：`codexDesktopOptionalCookie`；[HTTP 客户端](../../../backend/internal/repository/http_upstream.go)。
- [限流自定义分支](../../../backend/internal/service/openai_codex_tool_frame.go)：`shouldSuppressOpenAI429AccountCooldown`、`appendCodexToolFrameIfNeeded`。
- [管理接口代理解析](../../../backend/internal/service/codex_desktop_api_service.go)：`resolveProxyURL`。

## 已发生异常，但具体转换缺陷尚未定位

- 事件账号有 6 次 `No tool call found for function call output`，证明上游拒绝了工具历史配对；错误快照已裁剪，不能确认是下游原始历史、代理转换还是压缩造成。应先补齐不含正文的入站／出站配对摘要，再针对合成复现修复，不能任意删除工具输出。
- 718 次 response→account 绑定告警发生在请求 context 已取消时写 Redis。进程内映射先于 Redis 写入，因此“Redis 写失败”不等于“全部会话映射丢失”。需要验证重启、多实例及取消后的状态持久化；没有证据把它与六次 400 或停用关联。

## 尚未充分验证

- **TLS / HTTP2 传输画像**：当前 profile 的来源仍是 0.151 / App 26.825 基线。0.155 的应用层 MITM 结果不能证明直连 ClientHello、HTTP2 设置、连接复用及重连行为一致；旧基线本身也不能证明新版本一定不同。
- **WS 与 HTTP 回退生命周期**：官方样本包含原生 WS、受控回退、同连接续接和模型切换。事件账号关闭了 WS，全部用量记录为 HTTP，未复现该 WS 生命周期。HTTP 本身也是官方支持的已观察路径，不能仅因此认定错误。
- **启动与辅助 API**：抓包含 models、usage、me 等请求；项目也有相应接口实现或透传，但未完成同一任务从启动、查询、执行、取消、压缩到重连的端到端顺序验证。接口存在不等于生命周期一致，也不应凭空生成用户未执行的管理操作。
- **模型与工作负载维度**：[核心矩阵](MATRIX.md)主要使用 low 推理强度；其他推理档位、速度设置、子代理、并发工具、超长工具输出、429/5xx、长时间断线及长时间持续运行仍有验证空白。

## 离线复现结果

使用位于 Git 忽略目录中的合成测试文件，通过 Go overlay 加载；不修改生产实现，不使用真实账号凭据。

| 测试 | 当前结果 |
|---|---|
| TestCaptureAlignmentAudit_HTTPPreservesTurnContinuation | FAIL：替换入站 turn_id，同 turn 的两次请求获得不同 ID，并重置 turn 开始时间 |
| TestCaptureAlignmentAudit_HTTPPreservesCompactedWindow | FAIL：窗口编号回到 0、窗口 ID 回到初始窗口、context_window_id 被替换 |

复现命令（仓库 backend 目录，Windows PowerShell）：

```powershell
go test '-overlay=D:/language/ai/sub2api/.codex-official-desktop/2026-09-18/alignment-audit-overlay.json' ./internal/service -run '^TestCaptureAlignmentAudit_' -count=1
```

测试失败是本次审计观察到的现有差异，不是已修复的验证结果。测试与 overlay 位于本机私有目录，不会纳入常规测试集。

建议优先处理 turn／压缩窗口生命周期、工具配对和任务隔离，再核对 Cookie、辅助接口代理与异常恢复，最后补全实际网关的场景验证。所有这些差异均不能单独证明账号停用原因，也不能构成修复后不会停用的保证。
