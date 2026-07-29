# grokhttp2

此目录承载仅 Grok 指纹链路使用的仓库内 HTTP/2 fork。fork 以 `golang.org/x/net v0.56.0` 为上游基线，并已接入现有 `tlsfingerprint` / repository 传输接缝。

## 交付状态（2026-07-28）

- item 4/5：`COMPLETED`
  - 只有具备 Grok 专用头序能力的 profile 进入本 fork；普通 HTTP/2 profile 继续使用标准 `golang.org/x/net/http2`。
  - 伪头按 `:method,:scheme,:authority,:path`（`m,s,a,p`）发出；普通头按已固化的 18 头实抓顺序发出；未列头确定性落尾。
  - CONNECT、extended CONNECT、保留头过滤、重复值、cookie 拆分和 partial config 分支均有回归保护。
  - `GetBody` 驱动的 H1 fallback、代理、preface `WrapConn`、access-denied fallback、OpenAI `protocolMode` / cache 与 session 隔离均保持原语义。
- item 6：`OFFICIAL-WIRE-ALIGNED (DECLARED SCENARIO)`
  - 已用仅对子进程生效的临时 CA 与透明 CONNECT/TLS 中继取得官方 `grok 0.2.112 (9bbd559437)` 原始 H2 字节；中继只拆 TLS，不解码后重编码 H2。
  - 同一官方进程、session、TLS/H2 连接上的两次固定合成请求为 stream `3/5`；目标连接从 stream `1` 开始共 3 个 client header block，全部与 `NewGrokClientEncoder` 逐字节相等。
  - 目标块为 994/833 字节；非空 literal 全部 Huffman，第二块有 19 次动态索引引用，每块 3 个 skip 字段均为 without-index，均已在线上 wire 覆盖。
  - 常规 OAuth 样本没有 never-indexed 字段且只需单个 `HEADERS`；新增隔离 API-key 压力样本在同一 H2 连接的 stream `1/3` 均发出 `HEADERS + CONTINUATION`，两个完整块及帧切分都与本地逐字节相等。
  - OAuth 与 API-key 官方样本中的 Authorization 均为 `literal_without_indexing`、decoder sensitive=false；公开源码也未观察到 auth builder 调用敏感标记 API。因此 sensitive never-index 对当前观察到的官方 builder 归类为 `NOT-APPLICABLE`，本地 future-sensitive 支持继续保留。
- HPACK 源码行为：`SOURCE-ALIGNED`；声明场景：`OFFICIAL-WIRE-ALIGNED`
  - 官方二进制编译路径确认 `h2 0.4.15`；Grok client 运行时现已对齐该 crates.io 版本的非空 Huffman、skip 名单、3/4 索引阈值、sensitive never-index、静态 name-only 索引、普通静态表空值匹配和连接级动态表复用。
  - Rust `HeaderMap` 同名多值的后续项按 name-less iterator 分支编码：复用首值解析出的 name index、使用 without-index，并且不把后续值再次插入动态表；sensitive 新值也会优先复用已有动态 name index。
  - peer SETTINGS 可把 encoder 动态表调到 4096 以上；连续 table-size update 按 Rust pending 队列折叠，下降后回升时保留块首所需的最小值更新。
  - 锁定的 Rust raw-peer harness 在同一连接生成 stream `1/3`，并在独立连接生成 `HEADERS + CONTINUATION`；两次 `cargo run --locked --offline` 产物逐字节相同。
  - fork 的真实 `ClientConn` 已通过 stream `1/3` 动态表测试，并对 continuation 的 9 字节帧头与 payload 做逐帧 golden 比较。
  - 默认 `hpack.NewEncoder` 与 server 保持上游行为；仅 Grok client 显式使用 `hpack.NewGrokClientEncoder`。
- TLS session resumption：`OFFICIAL-TLS-RESUMPTION-STRUCTURALLY-ALIGNED`
  - 官方与本地 Grok profile 都在强制 TCP 重连后的恢复 ClientHello 发送 TLS 1.3 PSK，且服务端均选择 identity `0` 完成恢复。
  - 恢复 ClientHello 都以 `pre_shared_key(41)` 收尾，不再多发空 `session_ticket(35)`；rustls 每连接打乱其余扩展，因此按扩展集合和 PSK 必须最后等不变量比较，不要求随机线序逐项相同。
- `go1.27 && !http2legacy` wrapper 分支没有低侵入 request header-order hook，遇到 Grok 有序请求会显式报错；`http2legacy` 仅作为保留旧 fork 编码路径的 build escape hatch，不是默认长期方案。

| 范围 | 状态 | 运行时结论 |
| --- | --- | --- |
| HPACK/Huffman | `OFFICIAL-WIRE-ALIGNED` | 官方 stream `3/5` 目标块 994/833 字节，与本地 encoder 逐字节相等；非空 literal Huffman 已实测 |
| 动态表 | `OFFICIAL-WIRE-ALIGNED` | 目标连接 3 个连续块全部相等；第二目标块有 19 次动态引用 |
| skip | `OFFICIAL-WIRE-ALIGNED` | 每个目标块 3 个 skip 字段全部走 without-index，逐字节相等 |
| sensitive never-index | `NOT-APPLICABLE (OBSERVED AUTH BUILDERS)` | OAuth/API-key 官方 wire 均显示 Authorization 为 without-index 且非 sensitive；本地 never-index 实现仍由源码 fixture/单测保护 |
| `HEADERS/CONTINUATION` 合成 wire | `SOURCE-ALIGNED` | raw peer fixture 与 fork client 逐帧一致 |
| 官方 `CONTINUATION` | `OFFICIAL-WIRE-ALIGNED (STRESS SCENARIO)` | 官方 stream `1/3` 的 23126/23063 字节目标块均拆为 16384 字节 HEADERS fragment + CONTINUATION，并与本地完整块和切分逐字节一致 |
| 官方二进制 wire parity | `OFFICIAL-WIRE-ALIGNED (DECLARED SCENARIO)` | 同进程/session/连接 stream `3/5`，目标及其之前的连接级头块全部逐字节相等 |

定向审计结果、最终 QA 命令矩阵和基线失败分组见 `VERIFICATION.md`。

复制边界分为“已复制 package”与“仍引用上游 module 的外部依赖”两部分。

已复制 package 边界：

- `http2`
- `http2/hpack`
- `http/httpguts`
- `internal/httpcommon`
- `internal/httpsfv`

未复制但仍由 `golang.org/x/net v0.56.0` 提供的依赖：

- `golang.org/x/net/idna`

未复制的上游子包：

- `http2/h2c`
- `http2/h2i`

保留 `server.go`、`server_common.go`、`server_wrap.go`、`write.go`、`writesched*.go` 的真实原因：

- Go 以 package 为编译单元；fork 的目标不是“只保留 transport.go 单文件”，而是“保留 `http2` package 的真实非测试源码闭包”。
- `http2` 根 package 内的这些文件与 `transport.go` 共享同一组 package 级类型、常量、辅助函数、构建标签拆分文件和内部实现约束。
- 若删除这些 server 侧或写调度文件，就不再是“来源等价的最小 fork”，而是对上游包结构做再设计；那需要额外实现变更与新的编译/行为证明，不属于本阶段。

当前 provenance 断言由测试保护：

- 已复制 package 的非测试 `.go` 文件集合必须与 `golang.org/x/net@v0.56.0` 对应目录一致。
- 除已声明文件外，每个复制文件只允许有导入路径重写与 `http2/http2.go` import comment 移除这两类源码差异。
- 已声明的运行时源码补丁仅限：
  - `http2/header_order_capability_legacy.go` 与 `http2/header_order_capability_wrap.go`：按 build tag 报告当前分支是否真能在 request-encoding seam honor `HeaderOrder`
  - `http2/transport_common.go`：为 fork transport 新增可选 `HeaderOrder` 字段与类型别名，默认 `nil` 保持上游行为
  - `http2/transport.go`：把 transport 侧 `HeaderOrder` 透传到现有编码路径，并在 Grok client 连接创建处显式选择 `NewGrokClientEncoder`
  - `http2/hpack/encode.go`：新增内部两态编码模式；Grok client 对齐 crates.io `h2 0.4.15` 的 HPACK 决策，默认 encoder 保持上游语义
  - `internal/httpcommon/request.go`：`EncodeHeadersParam` 新增可选 `HeaderOrder`，允许按配置发出 Grok 的 `:method,:scheme,:authority,:path` 和 18 个普通头顺序；未列头按名称排序落尾并保留重复值顺序
- `LICENSE` 与 `PATENTS` 必须与上游逐字节一致。

## Phase 6 official wire status (2026-07-28)

- `SSLKEYLOGFILE` 路线仍不可用；最终使用仅注入官方子进程的 `HTTPS_PROXY` / `SSL_CERT_FILE`，由临时 CA 终止 TLS 后原样中继解密的 H2 bytes，未修改 Windows 根证书库。
- `grok agent stdio` 在一个进程和 session 内完成两轮固定提示；目标请求绑定同一连接的 stream `3/5`，完整 client preface、SETTINGS/ACK、HEADERS、DATA END_STREAM 与 close 生命周期均已验证。
- 原始头块、解码字段、Authorization、请求/响应 body 和生产 endpoint 只在内存；仓库仅保存 6178 字节派生报告，SHA-256 `ab4332a2b2bb7d0411c73cbf92c502ff3ef96881871704079ba8fdd12fd20729`。
- 报告中的两个官方头块分别为 994/833 字节；本地 encoder hash 与长度逐项相等，目标连接共比较 3 个头块且全部相等。
- 常规 OAuth 样本没有 sensitive never-index，也没有产生 CONTINUATION。后续隔离压力捕获已用合成 API key 和约 14K 路径让官方 stream `1/3` 产生 CONTINUATION；两个完整 HPACK 块及 `[16384, 6742]` / `[16384, 6679]` fragment 切分均与本地一致。
- 两次 API-key Authorization 和既有两次 OAuth Authorization 都没有被官方标记 sensitive，公开快照未发现 `set_sensitive` / `header_sensitive` 调用；因此 never-index 对当前官方 auth builder 是 `NOT-APPLICABLE`，不能改写为“官方 never-index 分支已线上触发”。
- 分支派生报告为 `testdata/official-wire-capture/official-hpack-branches-report.json`，4566 字节，SHA-256 `dca49d2d1f67bed9e8ad15d22bad1dadc95bafb0280124b1f18186df31e0e3b6`。

## Phase 8 source alignment (2026-07-28)

- 官方二进制观察事实：`grok 0.2.112 (9bbd559437)`，SHA-256 `2469bd182af212c7fcb84f2981999e4e8a6a7a2e4172bad3ae7f787a1f11407c`；完整扫描还观察到 `reqwest 0.12.24/0.13.4`、`hyper 1.8.1`、`hyper-util 0.1.20`、唯一 `h2 0.4.15`、`http 0.2.12/1.4.0`。
- 公开快照事实：首个公开 `0.2.112` 同步提交为 `47348d13ec4508dcfe440e34c6d511bb02998fb2`，其 `Cargo.lock` SHA-256 为 `852e088a2b4ac3586142592a6c6bbd3f78b8446a8fa8a24b5131baa44b31fd38`。
- crates.io 事实：`h2 0.4.15` checksum 为 `6cb093c84e8bd9b188d4c4a8cb6579fc016968d14c99882163cd3ff402a4f155`。
- 二进制内共找到 415 个 `h2-0.4.15` crates.io registry 源路径锚点，覆盖 31 个源码文件和 235 个行号锚点；均能映射到本地校验过的 crates.io 源，且没有发现 `git+.../h2` 替换标记。
- 以上仍不能从 stripped binary 数学证明“任何未使用私有改动都不存在”；严谨结论是“未发现替换/patch 证据，且声明的官方 wire 场景不存在影响字节的 patch”。未证明 `9bbd559437` 等于 `47348d...`。
- source fixture 仍只证明其声明的合成分支；官方 wire 结论独立来自 `official-wire-report.json`、`official-hpack-branches-report.json` 与 `official-tls-resumption-report.json` 三份派生报告，各自只覆盖报告声明的场景。
