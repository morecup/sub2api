# Claude Desktop 本机路径探测记录

本文档记录本机 Claude 桌面端的安装位置、运行入口、用户数据目录、集成路径和后续可继续分析的代码入口。探测时间：2026-06-29，系统用户：`Administrator`。

## 结论摘要

本机安装的官方 Claude 桌面端是 Windows MSIX 包，不是 npm 全局安装的 `@anthropic-ai/claude-code` 那个 `claude.exe`。

桌面端主链路更接近 Electron + `claude.ai` Web App：窗口进程加载 `app.asar`，用户数据落在 MSIX 虚拟化目录，另有 `CoworkVMService` 本地服务、`claude://` 协议入口、浏览器扩展 native host 和 Office add-in 相关能力。

当前本机 Claude Desktop 处于未登录状态。日志里只看到登录页和“无账号上下文”类报错；虽然存在 `Network\Cookies` 等浏览器存储数据库，但本文档不展开任何 cookie、token、设备标识或加密密钥值。

## 官方安装包

Appx 包信息：

- 包名：`Claude`
- PackageFullName：`Claude_1.15962.1.0_x64__pzs8sxrjxfjjc`
- PackageFamilyName：`Claude_pzs8sxrjxfjjc`
- 版本：`1.15962.1.0`
- Publisher：`Anthropic, PBC`
- InstallLocation：`C:\Program Files\WindowsApps\Claude_1.15962.1.0_x64__pzs8sxrjxfjjc`

关键文件：

| 文件 | 作用 | 大小 | SHA256 |
| --- | --- | ---: | --- |
| `C:\Program Files\WindowsApps\Claude_1.15962.1.0_x64__pzs8sxrjxfjjc\app\Claude.exe` | Electron 主程序 | 232347472 | `cd2a1d22b37bb6eba21b28304a4ca12367ea0adcf9dcd1b4b2cb4f66550b8912` |
| `C:\Program Files\WindowsApps\Claude_1.15962.1.0_x64__pzs8sxrjxfjjc\app\resources\app.asar` | 桌面端 JS 主体 | 36685074 | `72bacf24536704c8f80960d1d1b1bf2f4d63aef8e36b83ba052bf13c16420c6f` |
| `C:\Program Files\WindowsApps\Claude_1.15962.1.0_x64__pzs8sxrjxfjjc\app\resources\cowork-svc.exe` | 本地服务组件 | 12662608 | `49eb25bdb6b24df6b3171133d93af50804060342d364812c3ce720ffd68bb3ec` |
| `C:\Program Files\WindowsApps\Claude_1.15962.1.0_x64__pzs8sxrjxfjjc\app\resources\chrome-native-host.exe` | 浏览器扩展 native host | 1016144 | `118438cea83d482f155dfe086fe021e832f00b4187f9aad0bfc2104c6ec8fd3d` |

`resources` 下还存在：

- `app.asar.unpacked\node_modules\@ant\claude-native\claude-native-binding.node`
- `resources\office365-mcp\office365-mcp.mjs`
- `resources\office365-mcp\msalruntime.dll`
- `resources\office365-mcp\msal-node-runtime.node`
- `resources\smol-bin.x64.vhdx`
- `resources\ion-dist\...`

## 运行中进程

当前运行中可见：

- `claude.exe` 主进程
- `claude.exe --type=utility --utility-sub-type=network.mojom.NetworkService`
- 多个 `claude.exe --type=renderer`
- `claude.exe --type=gpu-process`
- `claude.exe --type=crashpad-handler`
- `cowork-svc.exe`

进程参数显示：

- Electron 版本：`42.4.0`
- app path：`C:\Program Files\WindowsApps\Claude_1.15962.1.0_x64__pzs8sxrjxfjjc\app\resources\app.asar`
- user data 参数表面写为：`C:\Users\Administrator\AppData\Roaming\Claude`
- 真实用户数据由 MSIX 虚拟化到：`C:\Users\Administrator\AppData\Local\Packages\Claude_pzs8sxrjxfjjc\LocalCache\Roaming\Claude`

## 系统入口

`AppxManifest.xml` 声明了以下入口。

### 协议入口

注册了 `claude://` 协议：

```text
HKEY_CLASSES_ROOT\claude\shell\open\command
"C:\Program Files\WindowsApps\Claude_1.15962.1.0_x64__pzs8sxrjxfjjc\app\Claude.exe" "%1"
```

### 本地服务

服务名：`CoworkVMService`

- 状态：`RUNNING`
- 启动类型：`AUTO_START`
- 启动账号：`LocalSystem`
- 依赖：`staterepository`
- 可执行文件：`C:\Program Files\WindowsApps\Claude_1.15962.1.0_x64__pzs8sxrjxfjjc\app\resources\cowork-svc.exe`
- Manifest 触发管道：`\pipe\cowork-vm-service`

### 启动任务

Manifest 声明了 `ClaudeStartup`：

- Executable：`app\Claude.exe`
- Enabled：`false`
- DisplayName：`Claude`

### 防火墙规则

Manifest 声明了 `app\Claude.exe` 与 `app\resources\cowork-svc.exe` 的 TCP 入站/出站规则，Profile 为 `all`。

## 用户数据目录

主用户数据目录：

```text
C:\Users\Administrator\AppData\Local\Packages\Claude_pzs8sxrjxfjjc\LocalCache\Roaming\Claude
```

该目录当前约 210 个文件，约 17.8 MB。关键文件和目录：

| 路径 | 说明 |
| --- | --- |
| `ant-did` | 本地设备/安装标识类文件，已确认存在但不展开内容 |
| `claude_desktop_config.json` | 桌面端偏好配置 |
| `config.json` | 版本、首启时间、locale、主题配置 |
| `git-worktrees.json` | 工作树状态，目前为空 |
| `Local State` | Electron/Chromium 本地状态，含加密密钥字段，已红acted |
| `Preferences` | Electron 偏好，含媒体设备 salt，已红acted |
| `Network\Cookies` | Chromium cookie DB，存在但不读取内容 |
| `Network\TransportSecurity` | Chromium HSTS/网络状态 |
| `Local Storage\leveldb` | Web localStorage |
| `Session Storage` | Web sessionStorage |
| `IndexedDB\https_claude.ai_0.indexeddb.leveldb` | `claude.ai` IndexedDB |
| `Partitions\cowork-file-preview\...` | 文件预览 partition 数据 |
| `logs\main.log` | 主进程日志 |
| `logs\claude.ai-web.log` | Web 层日志 |
| `logs\cowork_vm_node.log` | 本地服务/VM 相关日志 |
| `logs\ssh.log` | SSH 管理器日志 |

当前配置内容摘要：

- `claude_desktop_config.json`
  - `preferences.coworkHipaaRestricted=false`
  - `preferences.coworkWebSearchEnabled=true`
- `config.json`
  - `updaterLastSeenVersion=1.15962.1`
  - `locale=en-US`
  - `userThemeMode=system`
- `git-worktrees.json`
  - `worktrees={}`
  - `schemaVersion=2`

## MSIX 虚拟化例外路径

Manifest 明确配置了注册表和文件系统写入例外。

浏览器 native messaging host 注册表例外：

- `HKEY_CURRENT_USER\SOFTWARE\Google\Chrome\NativeMessagingHosts\com.anthropic.claude_browser_extension`
- `HKEY_CURRENT_USER\SOFTWARE\BraveSoftware\Brave-Browser\NativeMessagingHosts\com.anthropic.claude_browser_extension`
- `HKEY_CURRENT_USER\SOFTWARE\Microsoft\Edge\NativeMessagingHosts\com.anthropic.claude_browser_extension`
- `HKEY_CURRENT_USER\SOFTWARE\Chromium\NativeMessagingHosts\com.anthropic.claude_browser_extension`
- `HKEY_CURRENT_USER\SOFTWARE\ArcBrowser\Arc\NativeMessagingHosts\com.anthropic.claude_browser_extension`
- `HKEY_CURRENT_USER\SOFTWARE\Vivaldi\NativeMessagingHosts\com.anthropic.claude_browser_extension`
- `HKEY_CURRENT_USER\SOFTWARE\Opera Software\Opera Stable\NativeMessagingHosts\com.anthropic.claude_browser_extension`

Office add-in 注册表例外：

- `HKEY_CURRENT_USER\SOFTWARE\Microsoft\Office\16.0\WEF\TrustedCatalogs`

文件系统例外：

- `C:\Users\Administrator\AppData\Local\Microsoft\Office\16.0\WEF`
- `C:\Users\Administrator\AppData\Local\Claude-3p`

当前检查结果：

- 浏览器 native messaging host 注册表键未发现已注册值。
- `C:\Users\Administrator\AppData\Local\Microsoft\Office\16.0\WEF` 存在但为空。
- `C:\Users\Administrator\AppData\Local\Claude-3p` 存在但为空。

## asar 代码入口

`app.asar` 解析结果：

- top-level：`.vite`、`node_modules`、`package.json`、`resources`
- 包名：`@ant/desktop`
- app 版本：`1.15962.1`
- main：`.vite/build/index.pre.js`
- 文件数量：112
- header data base：`30656`

关键 JS / 资源：

| asar 内路径 | 说明 |
| --- | --- |
| `.vite/build/index.js` | 最大主 bundle，包含 API/OAuth/集成逻辑 |
| `.vite/build/index.pre.js` | main 入口 preload/启动相关代码 |
| `.vite/build/mainWindow.js` | 主窗口逻辑 |
| `.vite/build/mainView.js` | main view 逻辑 |
| `.vite/build/mcp-runtime/directMcpHost.js` | MCP runtime |
| `.vite/build/mcp-runtime/nodeHost.js` | MCP node host |
| `resources/office365-mcp/office365-mcp.mjs` | Office 365 MCP 逻辑 |
| `node_modules/@ant/claude-native/index.js` | native binding JS 包装 |

字符串搜索确认 `.vite/build/index.js` 同时包含：

- `https://api.anthropic.com`
- `https://api-staging.anthropic.com`
- `https://platform.claude.com/oauth/authorize`
- `https://claude.com/cai/oauth/authorize`
- `https://platform.claude.com/v1/oauth/token`
- `https://api.anthropic.com/api/oauth/claude_cli/create_api_key`
- `https://api.anthropic.com/api/oauth/claude_cli/roles`
- `https://mcp-proxy.anthropic.com`
- `https://claude.ai`
- `https://claude.ai/desktop/callback`
- `http://localhost:4000/desktop/callback`
- `http://localhost:3000/oauth/code/callback`
- `/v1/messages`
- `/v1/models?limit=1000`
- `/api/organizations`
- `anthropic-version`
- `anthropic-beta`
- `x-app`
- `NativeMessagingHosts`
- `chrome-native-host`

初步判断：桌面端包里包含 Claude Code OAuth/API key 辅助流程和 Anthropic SDK 代码，但 GUI 主体仍是 `claude.ai` Web/Electron 形态；不能直接把它等同为 Claude Code CLI 的 TTY `/v1/messages` 请求 profile。

## 其他 Claude 相关本机路径

这些路径也被本机扫描命中，但不属于官方 Claude Desktop 主包：

- `C:\Users\Administrator\AppData\Local\auto-claude-ui-updater`
- `C:\Users\Administrator\AppData\Roaming\auto-claude-ui`
- `C:\Users\Administrator\AppData\Local\claude-cli-nodejs`
- `C:\Users\Administrator\AppData\Roaming\npm\claude*`
- `C:\Program Files\Orchids\resources\claude-code`
- `C:\Users\Administrator\AppData\Local\Cockpit Tools\scripts\claude-desktop-auth-helper.cjs`

开始菜单/桌面当前只看到 `Auto-Claude.lnk`，未看到官方 Claude Desktop 的普通 `.lnk` 快捷方式；官方 MSIX 入口主要由 Appx 包和协议注册提供。

## 后续可分析方向

如果要继续像之前分析 Claude Code exe 一样拆桌面端，优先级如下：

1. 以 `app.asar` 为主，还原 `.vite/build/index.js` 中的 API wrapper、OAuth、session/cookie、`net.fetch` 和 SDK 调用路径。
2. 单独拆 `chrome-native-host.exe`，确认浏览器扩展 native messaging 的 manifest 生成逻辑、host 进程参数和消息协议。
3. 单独拆 `cowork-svc.exe`，确认 `CoworkVMService`、命名管道和 `smol-bin.x64.vhdx` 的作用边界。
4. 登录后只做受控抓包，区分 `claude.ai` Web API、`api.anthropic.com` SDK/API key 流程、OAuth 辅助流程三类请求，不把它们混为同一种 profile。
