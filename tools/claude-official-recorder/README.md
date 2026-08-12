# 家宽 Windows：Claude Code 官方请求记录器

该工具已用于 Windows Server 上的官方 Claude Code CLI。Claude 继续使用原有 `firstParty` / `claude.ai` / Pro 登录态，不配置 API Key，也不设置 `ANTHROPIC_BASE_URL`。

实现方式：只向 Claude Code 的用户设置注入本机 HTTPS 抓包代理 `127.0.0.1:18080`，由常驻 mitmdump 记录 Claude 进程发出的全部 HTTP(S) 请求，再转发给官方目标。

## 远端路径

```text
C:\Users\Administrator\ClaudeCodeRequestRecorder\
├── bin\mitmdump.exe
├── bin\_internal\
├── mitm_request_recorder.py
├── run-proxy.ps1
├── configure-remote.ps1
├── inspect-latest.ps1
├── disable-recording.ps1
├── mitmproxy-home\
├── runtime\
└── records\YYYY-MM-DD\<每个请求目录>\
```

每个请求目录包含：

- `request.json`：URL、HTTP 版本、原始请求头（包括 Authorization/Cookie）
- `request-body.raw.bin`：完整请求体
- `request-body.txt`：JSON、文本、表单请求体的便于查看副本
- `response.json`：响应状态与响应头；响应正文为保持 SSE 流式体验而不落盘

WebSocket 客户端帧保存为 `websocket-client-*.bin`。

## 不脱敏警告

记录器明确配置为不脱敏。日志包含可复用的 OAuth Bearer、Cookie、提示词、代码、工具定义与工具结果。`records`、`mitmproxy-home`、`runtime` 已限制为当前 Administrator 和 SYSTEM 可读写。

不要把日志同步到 Git、网盘、聊天软件或公开日志系统。

## 查看

```powershell
powershell -ExecutionPolicy Bypass -File C:\Users\Administrator\ClaudeCodeRequestRecorder\inspect-latest.ps1
```

显示最近 20 条并输出文本请求体：

```powershell
powershell -ExecutionPolicy Bypass -File C:\Users\Administrator\ClaudeCodeRequestRecorder\inspect-latest.ps1 -Limit 20 -ShowBody
```

检查常驻任务和监听端口：

```powershell
Get-ScheduledTask -TaskName ClaudeCodeRequestRecorderProxy
Get-NetTCPConnection -State Listen -LocalPort 18080
```

## 停用

```powershell
powershell -ExecutionPolicy Bypass -File C:\Users\Administrator\ClaudeCodeRequestRecorder\disable-recording.ps1
```

停用脚本只删除本工具写入的 Claude proxy/CA 设置并移除常驻任务，历史记录保留。

## 技术边界

- 捕获的是官方 `claude.exe` 发给本地 MITM 的最终应用层请求，因此能看到 native 层写回后的非零 CCH 和完整头/body。
- Anthropic 上游看到的 TCP/TLS/HTTP 客户端是 mitmproxy，不再是 `claude.exe` 的原生 TLS/HTTP2 指纹；请求内容与官方 OAuth 登录态不变。
- 旧的、在启用记录器之前已经运行的 `claude.exe` 不会自动继承代理设置，必须正常退出后重新打开。
- 没有配置自动清理策略，日志会持续增长。
