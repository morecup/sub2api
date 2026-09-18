# 本地 Work 专项抓包与历史对齐

重点是能读取、修改和执行本地文件的 Work/Codex。2026-08-30 历史模型样本确实全部来自这条链路。
9 月 17 日首轮主要也在测 Work，但采用无项目任务，只覆盖了本地读取；普通 Chat 的 S11 是额外对照。
本次补齐绑定本地 Git 项目的读、写、执行、连续任务和压缩后续接，并按上报进程重新确认遥测归属。

## 与历史原始记录核对

| 样本 | Work 模型请求 | 普通 Chat 模型请求 | 普通任务上下文 |
|---|---:|---:|---|
| 2026-08-30 旧原始样本 | 22 | 0 | `workspace_kind=project`，带 `workspaces` |
| 2026-09-17 首轮 S01–S13 | 21 | 1（S11） | `workspace_kind=projectless` |
| 2026-09-17 补测 W01–W03 + compact | 26 | 0 | `workspace_kind=project`，带 `workspaces` |

Work 端点均为 `/backend-api/codex/responses`。
普通 Chat 对照端点为 `/backend-api/f/conversation`，不纳入 Work 协议基线。
这里统计包括预热、标题生成、工具续接与压缩，不等于用户发送的消息数。

## 实际本地文件测试

通过 Computer Use 在独立抓包副本里添加专用本地项目，界面运行位置为「本地」，分支为 `main`。
项目仅包含合成数据和 Python 标准库代码；测试基线预先提交在该独立夹具的 Git 仓库中。
随后文件修复和报告生成由被测 Work 的工具执行，外部复核阶段只读取结果并运行验证。

| 操作 | Work 实际行为 | 独立验证 |
|---|---|---|
| W01 | 读取 CSV、代码及测试；运行失败测试；修正漏乘数量的错误；新建 Markdown 报告 | 代码从仅求单价和改为数量 × 单价；合计 5200 分；2 项测试均通过；测试文件与基线相同 |
| W02 | 从磁盘重新读取；CSV 新增商品；执行脚本；更新原报告；新建 JSON 结果 | 3 种商品，合计 5900 分；CSV、报告、JSON 和脚本输出一致 |
| `/compact` | 压缩已经执行过文件工具的项目任务 | UI 显示上下文已压缩；WS 有压缩事件和完成事件 |
| W03 | 压缩后再次读取本地 CSV、JSON、报告并执行脚本；追加验证行 | 仍为 5900 分、3 种商品；前两轮标记保留，末尾验证行已落盘 |

补测产生两条 responses WS 连接、26 个 `response.create`，26 个均有 `response.completed`。
捕获到 19 个完成的 `exec` 工具调用，其封装内容包含 `exec_command`、`apply_patch` 及工作环境依赖查询。
工具结果回传后的模型续接、文件改动 UI 和磁盘结果均有对应证据。
首轮已覆盖的 WS/HTTP 回退、Lite/非 Lite、图片和停止恢复仍然属于 Work 测试，无需因 S11 对照样本而作废。

本轮初次执行遇到默认 Python 别名不可用，Work 查询本机运行时后继续成功；因此工具执行次数包含环境探测。

## 项目 metadata 对齐结果

普通项目 turn 恢复了历史中的完整核心字段集合：`workspaces`、`workspace_kind=project`、
`root_turn_id`、`turn_trigger`、`turn_started_at_unix_ms` 等。
后续逐模型矩阵进一步发现：125 条普通项目请求中有 29 条省略 `workspaces`，其余 96 条携带非空映射；均保持 `workspace_kind=project`。上述字段集合描述典型样本，不代表每次请求都必须带目录明细。
典型项目字段的脱敏结构为：

```json
{
  "workspace_kind": "project",
  "workspaces": {
    "<local-project-path>": {
      "latest_git_commit_hash": "<hash>",
      "has_changes": false
    }
  }
}
```

- 第一轮从干净 Git 基线开始，`has_changes=false`；后续用户 turn 观察到 `has_changes=true`。
- 该值在同一 turn 的工具续接中保持，不应把它解释为每个工具调用后即时刷新。
- 旧项目有 1 个 remote，因此带 `associated_remote_urls`；新夹具没有 remote，未带该字段。这是项目条件差异。
- 当前 UI 使用默认「请求批准」：`sandbox=windows_elevated`、`sandbox_mode=workspace-write`。
  旧样本是 `sandbox=none`、`sandbox_mode=danger-full-access`，不能归因为升级改变了默认协议值。
- 手动 compact 不带 `workspaces` / `workspace_kind`，本地项目中也如此；新版带 `root_turn_id`，旧版手动压缩没有。
- 压缩后恢复项目 turn 时，项目 metadata 再次出现；input 含压缩上下文，然后继续实际工具调用。

## 只归属于 Work 的遥测结论

对 OTLP 请求的 resource 属性检查得到：

```json
{
  "service.name": "codex-app-server",
  "service.version": "0.155.0-alpha.2.6",
  "telemetry.sdk.language": "rust",
  "telemetry.sdk.name": "opentelemetry"
}
```

首轮多观察到的 11 个指标，全部能归属到这个 Rust Work 后端；并非普通 Chat WebView 指标。
后续逐模型矩阵又观察到 4 个旧程序已有同名字符串的指标；扩展计数与证据见 [主报告遥测补充](README.md#遥测新增证据与采样差异) 和 [matrix-telemetry.json](matrix-telemetry.json)。
其中以下 9 个在新程序中有同名字符串，旧程序未检出：

- `codex.app_server.codex_home.size_bytes`
- `codex.plugins.loaded_cache.event`
- `codex.plugins.loaded_cache.load.duration_ms`
- `codex.plugins.loaded_cache.request`
- `codex.plugins.loaded_cache.wait.duration_ms`
- `codex.thread_history.sqlite_projection`
- `codex.windows_mxc.available`
- `codex.windows_sandbox.private_desktop`
- `codex.windows_system_config.namespace_squatting_probe`

`codex.hooks.run` 和 `codex.hooks.run.duration_ms` 旧程序已有，因此只属于本轮新增观察。
新旧二进制的精确字符串差异支持新增埋点判断，不等于完整实现审计。

`/ces/v1/{i,p,t,m}`、`/ces/statsc/flush`、ChatGPT WebView intake 等通用端点，
从 Work 新增遥测结论中排除，只作为原始会话中的背景流量留档。
即使 UI 只操作 Work，后台 WebView 仍可能上传日志；不能只按时间窗口就断言所有请求都由 Work 功能产生。

## 证据文件

- [work-evidence.json](work-evidence.json)：补测时间窗口、模型请求、工作目录结构、工具种类、完成事件和 Rust 遥测归属；不含真实路径、凭据或请求正文。
- [首轮完整记录](README.md)：保留首轮传输与模型矩阵，普通 Chat 部分仅为对照附录。
- [旧 Work 基线](../2026-08-30_0.151.0-alpha.7.1/README.md)：历史项目任务字段与采集范围。

原始流量、夹具、文件改动和验证输出保留在本机 Git 忽略目录。
当前工作实例保持运行，独立抓包副本保留在本地项目的 Work/Codex 视图。
