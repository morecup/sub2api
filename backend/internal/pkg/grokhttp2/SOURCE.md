# Source Provenance

- Fork root: `backend/internal/pkg/grokhttp2`
- Upstream module: `golang.org/x/net`
- Upstream version: `v0.56.0`
- Copy date: `2026-07-28`
- Local module path prefix: `github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2`

已复制的上游 package：

- `golang.org/x/net/http2`
- `golang.org/x/net/http2/hpack`
- `golang.org/x/net/http/httpguts`
- `golang.org/x/net/internal/httpcommon`
- `golang.org/x/net/internal/httpsfv`

未复制、继续直接引用上游 module 的依赖：

- `golang.org/x/net/idna`

未复制的上游相邻子包：

- `golang.org/x/net/http2/h2c`
- `golang.org/x/net/http2/h2i`

复制自上游的运行时源码，本地改动仅限：

1. 将上游 `golang.org/x/net/...` 内部导入路径重写到本地 fork 前缀。
2. 移除 `http2/http2.go` 中的上游 import comment，避免本地包路径校验冲突。
3. `http2/transport_common.go` 引入最小 transport 接缝补丁：
   - 新增 `HeaderOrder` 类型别名，桥接 fork transport 与 forked `internal/httpcommon`
   - `Transport` 新增可选 `HeaderOrder` 字段
   - 默认为 `nil`，未配置时保持上游 `v0.56.0` 默认请求头枚举行为
4. `http2/transport.go` 引入最小透传补丁：
   - `encodeRequestHeaders` 新增 `headerOrder` 参数
   - 只把 transport 侧 `HeaderOrder` 透传到现有 `httpcommon.EncodeHeaders` 路径
   - Grok client 连接创建处显式使用 `hpack.NewGrokClientEncoder`
5. `internal/httpcommon/request.go` 引入头序实现补丁：
   - `EncodeHeadersParam` 新增可选 `HeaderOrder`
   - 允许按配置发出 Grok 伪头顺序 `:method,:scheme,:authority,:path`
   - 重复的伪头配置只发出一次，同时保留未配置的合法伪头
   - 允许按配置发出普通头顺序，未列头按名称排序落尾并保留重复值顺序
   - 未提供排序配置时保持上游 `v0.56.0` 默认枚举行为
6. `http2/hpack/encode.go` 引入 client-only HPACK 补丁：
   - 新增未导出的两态 mode 与唯一窄入口 `NewGrokClientEncoder`
   - Grok mode 对齐 crates.io `h2 0.4.15` 的非空 Huffman、skip 名单、3/4 阈值、sensitive、伪头 name-only 静态索引，以及普通静态表空值不作 exact match 的语义
   - Grok mode 的 encoder table-size 上限可随 peer SETTINGS 超过 4096；连续更新使用 Rust 式 pending 队列，相同 setting 保持 no-op
   - 每个 header block 显式重置同名续值状态；Rust `HeaderMap` 的 name-less 后续值复用首值 name index、保持 without-index 且不再次入动态表
   - Grok mode 的 dynamic exact lookup 不因 sensitive 标记而跳过；sensitive 新值在已有动态同名项时优先复用动态 name index
   - 默认 `NewEncoder` 语义不变
7. 不修改 server 逻辑；`server.go` 继续使用默认 `NewEncoder`。repository 与 service 集成位于 fork 目录外，不属于复制源码的 provenance 差异。

HPACK reference provenance 分为三组独立事实：

- 官方二进制：`grok 0.2.112 (9bbd559437)`，SHA-256 `2469bd182af212c7fcb84f2981999e4e8a6a7a2e4172bad3ae7f787a1f11407c`；完整扫描观察到 `reqwest 0.12.24/0.13.4`、`hyper 1.8.1`、`hyper-util 0.1.20`、唯一 `h2 0.4.15`、`http 0.2.12/1.4.0`。
- 公开快照：提交 `47348d13ec4508dcfe440e34c6d511bb02998fb2`，`Cargo.lock` SHA-256 `852e088a2b4ac3586142592a6c6bbd3f78b8446a8fa8a24b5131baa44b31fd38`。
- crates.io：`h2 0.4.15` checksum `6cb093c84e8bd9b188d4c4a8cb6579fc016968d14c99882163cd3ff402a4f155`。

二进制扫描得到 415 个 crates.io registry `h2-0.4.15` 源路径锚点，覆盖 31 个源码文件及 235 个行号锚点，全部可映射到标准 crates.io 源；没有发现 `git+.../h2` 替换标记。官方 live capture 又证明声明场景的连接级头块逐字节一致。因此当前结论是“未发现替换/patch 证据，声明场景不存在影响 wire 的 patch”，而不是“数学证明任何内部 patch 都不存在”。仍未证明内部 revision `9bbd559437` 等于公开提交 `47348d...`。Rust fixture 的证据等级固定为 `SOURCE-ALIGNED / WIRE-UNVERIFIED`，与独立的官方 live wire 证据分开。

本地新增的非上游运行时与验证辅助源码：

- `version.go`：固定上游版本元数据。
- `http2/header_order_capability_legacy.go`
- `http2/header_order_capability_wrap.go`
  - 两个 build-tag 文件显式声明当前分支能否在 request-encoding seam honor `HeaderOrder`。
  - `go1.27 && !http2legacy` 返回 unsupported；`http2legacy` 仅是保留旧编码路径的 build escape hatch。
- `evidence_validator.go`：验证 item 6 证据结构并生成仅含派生 hash、计数和长度的仓库安全摘要；接受唯一扩展 SETTINGS、保留其线序并拒绝重复 SETTINGS；它不参与 HTTP/2 请求运行时编码。
- `testdata/rust-h2-reference`：锁定 `h2 0.4.15` 的 raw-peer fixture harness；只使用合成头，不读取 Grok 凭据或会话数据。
- `testdata/h2-0.4.15-source-aligned.json`：`SOURCE-ALIGNED / WIRE-UNVERIFIED` 合成 golden，不属于官方 evidence validator 可接受的抓包。
- `testdata/official-wire-capture/capture.py`：opt-in 透明 CONNECT/TLS/H2 取证与二进制审计；临时 CA 只通过子进程 `SSL_CERT_FILE` 生效，不修改 Grok 配置或系统根证书库。
- `testdata/official-wire-capture/capture_resumption.py`：直通 TLS 的官方/本地恢复握手取证；只保存结构摘要与 run-salted ticket identity hash，不保存票据或 binder 字节。
- `testdata/official-wire-capture/capture_hpack_branches.py`：使用临时 `GROK_HOME`、合成 API key、本地 H2 端点和 localhost-only proxy 触发官方 CONTINUATION，不读取用户 `auth.json`。
- `testdata/official-wire-capture/go-encoder`：通过匿名管道接收内存字段，调用 `NewGrokClientEncoder` 做连接级逐块比对；不保存比较字节。
- `testdata/official-wire-capture/official-wire-report.json`：官方同进程/session/连接 stream `3/5` 的派生报告；只含 hash、长度、flags、SETTINGS、分支摘要和 equality，文件 SHA-256 `ab4332a2b2bb7d0411c73cbf92c502ff3ef96881871704079ba8fdd12fd20729`。
- `testdata/official-wire-capture/official-hpack-branches-report.json`：官方 stream `1/3` CONTINUATION 压力场景与 observed auth builder 分类报告，SHA-256 `dca49d2d1f67bed9e8ad15d22bad1dadc95bafb0280124b1f18186df31e0e3b6`。
- `testdata/official-wire-capture/official-tls-resumption-report.json`：官方与本地 TLS 1.3 PSK 恢复结构报告，SHA-256 `2372f64ff14f2d51b3526d20fa77adec01fdd615a30841128e4f838e79539c9b`。

本地新增的测试文件（均不属于复制的上游运行时源码）：

- `fork_boundary_test.go`
- `compile_closure_test.go`
- `evidence_validator_test.go`
- `documentation_contract_test.go`
- `http2/header_order_capability_legacy_test.go`
- `http2/header_order_capability_wrap_test.go`
- `http2/transport_header_order_integration_test.go`
- `http2/transport_hpack_source_aligned_test.go`
- `http2/hpack/grok_encoder_test.go`
- `hpack_source_aligned_test.go`
- `official_wire_report_test.go`
- `official_hpack_branches_report_test.go`
- `official_tls_resumption_report_test.go`
- `internal/httpcommon/request_header_order_test.go`

本地 provenance 与交接文档辅助文件：

- `README.md`
- `SOURCE.md`
- `SYNC.md`
- `VERIFICATION.md`
- `LICENSE` 与 `PATENTS`：从上游逐字节复制，不属于本地文本改写。

以上本地新增文件与复制自上游的运行时源码差异分开维护；不能把 validator、测试或文档辅助文件误计为上游源码补丁。

保留 server 侧与写调度文件的真实原因：

- 此 fork 复制的是 `http2` package 的真实非测试源码闭包，而不是只摘取 client transport 相关单文件。
- `server.go`、`server_common.go`、`server_wrap.go`、`write.go`、`writesched*.go` 与同 package 其他文件共享 package 级符号和构建拆分，删除它们会把“来源镜像”变成“本地重构”。
- 因此本阶段把它们视为来源闭包的一部分；是否裁剪，必须以后续单独编译证明和行为证明为前提。

当前闭包文件：

- `http2/ascii.go`
- `http2/ciphers.go`
- `http2/client_conn_pool.go`
- `http2/client_priority_go126.go`
- `http2/client_priority_go127.go`
- `http2/clientconn.go`
- `http2/config.go`
- `http2/config_go125.go`
- `http2/config_go126.go`
- `http2/databuffer.go`
- `http2/errors.go`
- `http2/flow.go`
- `http2/frame.go`
- `http2/gotrack.go`
- `http2/header_order_capability_legacy.go`
- `http2/header_order_capability_wrap.go`
- `http2/http2.go`
- `http2/pipe.go`
- `http2/server.go`
- `http2/server_common.go`
- `http2/server_wrap.go`
- `http2/transport.go`
- `http2/transport_common.go`
- `http2/transport_wrap.go`
- `http2/unencrypted.go`
- `http2/write.go`
- `http2/writesched.go`
- `http2/writesched_common.go`
- `http2/writesched_priority_rfc7540.go`
- `http2/writesched_priority_rfc9218.go`
- `http2/writesched_random.go`
- `http2/writesched_roundrobin.go`
- `http2/hpack/encode.go`
- `http2/hpack/gen.go`
- `http2/hpack/hpack.go`
- `http2/hpack/huffman.go`
- `http2/hpack/static_table.go`
- `http2/hpack/tables.go`
- `http/httpguts/guts.go`
- `http/httpguts/httplex.go`
- `internal/httpcommon/ascii.go`
- `internal/httpcommon/headermap.go`
- `internal/httpcommon/request.go`
- `internal/httpsfv/httpsfv.go`
