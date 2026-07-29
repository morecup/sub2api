# Sync Notes

同步锚点固定为 `golang.org/x/net v0.56.0`。

后续若需要同步或裁剪，先重复以下步骤：

1. 用 `go env GOMODCACHE` 锁定本机 `golang.org/x/net@v0.56.0` 源码目录。
2. 对已复制 package 做独立比对：
   - `http2`
   - `http2/hpack`
   - `http/httpguts`
   - `internal/httpcommon`
   - `internal/httpsfv`
3. 逐文件核对各 package 目录下的非测试 `.go` 文件集合，确认本地与上游一致。
4. 逐文件内容比对，确认复制自上游的运行时源码只允许做导入路径重写、`http2/http2.go` 的 import comment 移除，以及已声明的 Phase 4 头序补丁与 Phase 8 client-only HPACK 补丁；若出现其他差异，必须先解释再修改。
5. 逐字节核对 `LICENSE` 与 `PATENTS`，确保与上游完全一致。
6. 若上游新增了 `http2` 根 package 非测试文件，默认视为真实 compile closure 的一部分；只有在新增证明说明其不属于 fork 边界时才允许不复制。
7. `http2/h2c`、`http2/h2i` 这类相邻子包只有在 fork 实际引入对应 import 时才纳入复制边界；否则继续保持未复制状态并在 `SOURCE.md` 中明示。
8. 对复制自上游的运行时源码，运行时行为差异仅允许以下已声明补丁，且必须先有独立红灯测试：
   - `http2/header_order_capability_legacy.go` 与 `http2/header_order_capability_wrap.go`：按 build tag 报告 HeaderOrder 能力；`http2legacy` 仅是 build escape hatch
   - `http2/transport_common.go`：新增可选 `HeaderOrder` transport 接缝
   - `http2/transport.go`：透传 `HeaderOrder`，并在 Grok client 创建处显式选择 `NewGrokClientEncoder`
   - `internal/httpcommon/request.go`：实现伪头与普通头顺序覆盖逻辑；重复的伪头配置只发出一次，同时保留未配置的合法伪头
   - `http2/hpack/encode.go`：仅 Grok mode 对齐 `h2 0.4.15` 的 Huffman、动态表、skip、sensitive、3/4 阈值、伪头 name-only 索引、普通静态表空值语义、`HeaderMap` 同名续值的 name-less 分支，以及支持超过 4096 的 pending table-size update 队列；默认 encoder 不变
   - `http2/transport.go`：每个请求头块与 trailer 头块在写入首字段前调用 `BeginHeaderBlock`，不得让同名续值状态跨 header block 泄漏
   - `version.go`、`evidence_validator.go`、本地测试以及 `README.md` / `SOURCE.md` / `SYNC.md` / `VERIFICATION.md` 是仓库本地辅助文件，应按 `SOURCE.md` 的独立分类同步，不得混入“上游运行时源码差异”清单。
   - `evidence_validator.go` 必须接受唯一扩展 SETTINGS、保留线序并拒绝重复 SETTINGS；同步时不得把“未知但唯一”的扩展 setting 误判为无效。
   - Rust fixture 始终只维持 `SOURCE-ALIGNED / WIRE-UNVERIFIED`，不得把它或 Go runtime golden 冒充官方原始 wire。独立官方证据来自 `official-wire-report.json`、`official-hpack-branches-report.json` 和 `official-tls-resumption-report.json`，分别限于常规 stream `3/5`、隔离 CONTINUATION stream `1/3` 与一次 TLS 恢复场景。never-index 对观察到的 auth builder 是不适用结论，不得改写成该分支已线上触发。
9. 若尝试剔除 `server.go`、`server_common.go`、`server_wrap.go`、`write.go`、`writesched*.go` 或其他文件，必须先证明不再复制 `http2` 根 package 的完整非测试文件集合，并补新的闭包边界测试。
10. 同步完成后运行：
   - `go test ./internal/pkg/grokhttp2/... -count=1`
   - `go build ./internal/pkg/grokhttp2/...`
   - provenance 测试会再次做独立比对、逐文件内容校验，以及 `LICENSE`/`PATENTS` 一致性校验。
11. 若更新 `h2` reference，必须在 `testdata/rust-h2-reference` 中执行两次 `cargo run --locked --offline -- <output-path>`，逐字节比较产物，并同步更新 harness `Cargo.lock` hash；禁止用未锁定或联网生成结果直接替换 fixture。
12. 若重跑官方证据，必须先通过 `python -m unittest -v test_capture.py test_resumption.py test_hpack_branches.py`；报告不得出现 raw/decoded headers、凭据、body、生产 endpoint、TLS ticket 或 binder 字节。重跑后同步更新对应报告固定 SHA，并再次验证用户配置、父进程代理环境和 Windows 根证书库均未改变。
