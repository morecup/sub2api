# 家宽 Windows：Claude Desktop 官方请求记录器

该工具用于同一台远端家宽 Windows Server 上的官方 Claude Desktop。它不搭建 API Key 中转，也不修改 Anthropic API Base URL；Claude Desktop 仍使用原来的官方登录态和官方目标地址。

实现方式：用独立入口为官方 `Claude.exe` 加入 Electron/Chromium 的进程级代理参数，同时仅给该进程设置代理环境变量。常驻 mitmdump 在 `127.0.0.1:18081` 记录 Desktop 发出的 HTTP(S) 请求并转发到原官方目标。没有设置 Windows 全局代理，因此不会抓取其他应用。

## 远端目录

```text
C:\Users\Administrator\ClaudeDesktopRequestRecorder\
├── bin\mitmdump.exe
├── bin\_internal\
├── mitm_request_recorder.py
├── run-proxy.ps1
├── launch-recorded.ps1
├── restart-recorded.ps1
├── configure-remote.ps1
├── inspect-latest.ps1
├── verify-recording.ps1
├── audit-recording.ps1
├── disable-recording.ps1
├── mitmproxy-home\
├── runtime\
└── records\YYYY-MM-DD\<每个请求目录>\
```

Desktop 使用独立端口 `18081`、独立 CA、独立日志目录和独立计划任务，不影响 Claude Code 使用的 `18080`。

每个请求目录包含：

- `request.json`：官方 URL、HTTP 版本、原始请求头，包括 Authorization、Cookie 和 OAuth Bearer
- `request-body.raw.bin`：完整原始请求体
- `request-body.txt`：JSON、文本、表单等请求体的便于查看副本
- `response.json`：响应状态和响应头；为保证 SSE/下载流式体验，响应正文不落盘
- `websocket-client-*.bin`：客户端发出的 WebSocket 帧

## 启动方式

部署后会创建两个入口：

- 桌面：`Claude (Recorded)`
- 开始菜单：`Claude (Recorded)`

同时创建 `ClaudeDesktopRecordedLaunch` 登录任务，以便 Administrator 登录服务器后首先启动带记录参数的 Claude Desktop。Electron 是单实例应用；只要这个带代理的主实例仍在运行，之后从普通 Claude 图标唤醒窗口时，请求仍由带代理的主实例发出并被记录。

如果普通 Claude Desktop 已先启动，记录入口会拒绝混用并提示先彻底退出普通实例。这是为了避免看似开启记录、实际主进程却没有代理参数。

需要明确重启为记录模式时，可先正常关闭，超时后只终止官方 Desktop 完整路径下的进程：

```powershell
powershell -ExecutionPolicy Bypass -File C:\Users\Administrator\ClaudeDesktopRequestRecorder\restart-recorded.ps1 -Force
```

## 查看和验收

检查任务、代理、证书、Desktop 主进程参数和最近请求：

```powershell
powershell -ExecutionPolicy Bypass -File C:\Users\Administrator\ClaudeDesktopRequestRecorder\verify-recording.ps1
```

对全部日志重新计算请求正文 SHA-256，并核对未脱敏凭据头、ACL、任务和两个记录器端口：

```powershell
powershell -ExecutionPolicy Bypass -File C:\Users\Administrator\ClaudeDesktopRequestRecorder\audit-recording.ps1
```

显示最近 20 条并输出文本请求体：

```powershell
powershell -ExecutionPolicy Bypass -File C:\Users\Administrator\ClaudeDesktopRequestRecorder\inspect-latest.ps1 -Limit 20 -ShowBody
```

## 不脱敏警告

记录器明确不做脱敏。日志会保存可复用的 OAuth Bearer、Cookie、提示词、附件元数据、工具定义和工具结果。`records`、`mitmproxy-home`、`runtime` 目录只允许当前 Administrator 和 SYSTEM 读写。

不要把这些目录同步到 Git、网盘、聊天软件或公共日志平台。

## 停用

先完全退出 Claude Desktop，再运行：

```powershell
powershell -ExecutionPolicy Bypass -File C:\Users\Administrator\ClaudeDesktopRequestRecorder\disable-recording.ps1
```

如果需要脚本同时终止当前带记录参数的 Desktop 进程：

```powershell
powershell -ExecutionPolicy Bypass -File C:\Users\Administrator\ClaudeDesktopRequestRecorder\disable-recording.ps1 -StopClaude
```

停用会删除两个计划任务、专用快捷方式，以及仅属于该目录 CA 的 `LocalMachine\Root` 信任项。历史记录和 CA 文件仍保留。

## 技术边界

- 捕获的是官方 Claude Desktop 发给本机 MITM 的最终应用层请求，官方登录态、URL、请求头和请求体保持不变。
- 为让 Electron/Chromium 信任动态站点证书，Desktop 专用 CA 会加入 `LocalMachine\Root`；没有设置系统代理，其他程序不会被送入该抓包端口。
- Anthropic 上游看到的 TCP/TLS/HTTP 客户端是 mitmproxy，不再是 Electron 的原生 TLS/HTTP2 指纹。
- 只有由带代理参数的 Desktop 主实例发出的请求才能记录。记录入口和登录任务会尽量确保它成为 Electron 单实例主进程。
- 没有自动清理策略，日志会持续增长。
