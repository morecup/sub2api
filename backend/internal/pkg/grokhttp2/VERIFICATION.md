# Verification

本文件只记录可复跑命令、审计状态和失败归因，不保存官方或私密原始帧、真实凭据、用户内容或可识别标识。仓库内 raw frame fixture 全部来自锁定 harness 的合成头。除明确标注的 harness 子目录命令外，所有命令均从 `backend` 目录执行。

## 状态边界

| 范围 | 状态 | 结论 |
| --- | --- | --- |
| item 4/5 | `COMPLETED` | 仅 Grok 进入 fork；伪头为 `m,s,a,p`；普通头采用已固化的 18 头顺序；外围 fallback / proxy / preface / cache / session 回归已锁定。 |
| item 6 official wire | `OFFICIAL-WIRE-ALIGNED (DECLARED SCENARIO)` | 官方同进程/session/连接 stream `3/5`；目标 994/833 字节头块及该连接前置块均与本地 encoder 逐字节相等。 |
| 官方 Huffman/动态表/skip | `WIRE-OBSERVED` | 非空 literal Huffman、连接级动态引用、每块 3 个 skip without-index 均已实测且相等。 |
| 官方 sensitive never-index | `NOT-APPLICABLE (OBSERVED AUTH BUILDERS)` | OAuth/API-key 官方 wire 中 Authorization 均为 without-index 且非 sensitive；本地 never-index 分支仍保留源码与合成测试证据。 |
| 官方 CONTINUATION | `OFFICIAL-WIRE-ALIGNED (STRESS SCENARIO)` | 官方 stream `1/3` 两个长头块均产生 CONTINUATION，完整块和帧切分与本地逐字节相等。 |
| TLS session resumption | `OFFICIAL-TLS-RESUMPTION-STRUCTURALLY-ALIGNED` | 官方与本地均提供并成功恢复 TLS 1.3 PSK；恢复 ClientHello 不含多余 session_ticket(35)，PSK 扩展保持最后。 |
| 多实例 turn index | `SHARED-MONOTONIC` | Redis Lua 原子保存 `max(current, derived)`，重试不自增；key 不含原始 conversation id，Redis 故障回退进程内单调值。 |
| 多实例 compaction catalog | `REMOTE-CATALOG-SHARED` | 每账号加载 `/v1/settings` 与 `/v1/models` 后写入 Redis；远端目录成功加载即为权威，控制面使用独立 transport。 |
| Phase 8 HPACK source behavior | `SOURCE-ALIGNED / WIRE-UNVERIFIED` | Rust 合成 fixture 的证据等级不变；它与独立官方 live wire 报告分开。 |

item 4/5 与 Phase 8 合成 fixture 本身不构成官方 wire 证据。三份官方报告分别覆盖常规 OAuth wire、隔离 API-key CONTINUATION 压力场景和一次 TLS 恢复场景。never-index 没有被官方 builder 触发，结论是当前观察范围内不适用而非该分支已线上 parity；其他未声明请求形状仍没有结论。

## TDD trace

下表按实施顺序记录先红后绿事实，不附会不存在的时间戳。红灯原因描述首次建立契约时缺少的行为，绿灯列记录实际用于收口的命令。

| 阶段 | 先红测试 | 红灯原因 | 绿灯命令 |
| --- | --- | --- | --- |
| Phase 2 | `TestUpstreamVersionPinnedToXNetV0560`、`TestCompileClosureArtifactsExist`、`TestCompileClosureMatchesUpstreamNonTestFileSet`、`TestForkedSourceFilesOnlyContainDeclaredRewrites`、`TestLicenseAndPatentsMatchUpstreamExactly`、`TestCompileClosureExportsUpstreamSurface` | fork/version/artifact 尚不存在，表现为缺失闭包、来源文件和可编译 surface；复制不完整时文件集合及逐字节 provenance 也失败。 | `go test ./internal/pkg/grokhttp2/... -count=1`；`go build ./internal/pkg/grokhttp2/...` |
| Phase 3 | `TestNewFingerprintHTTP2Transport_GrokProfileUsesFork`、`TestBuildUpstreamTransportWithTLSFingerprint_NonGrokAdvertisesHTTP2SyntheticProfileStaysOnStdTransport`、`TestBuildUpstreamTransportWithTLSFingerprint_GrokProfileCreatesForkOnlyOnGrokCapability` | Grok 正例仍返回标准 transport，尚未形成“仅 Grok 进入 fork、普通 H2 留在标准 transport”的边界。 | `go test -tags=unit ./internal/repository -run "Fingerprint\|Fork\|Grok" -count=1` |
| Phase 4 | `TestEncodeHeaders_GrokPseudoHeadersEncodeAsMSAP`、`TestEncodeHeaders_GrokOrdinaryHeadersEncodeInCaptured18HeaderOrder`、`TestEncodeHeaders_GrokUnlistedHeadersUseDeterministicTailPolicyAndPreserveDuplicates`、`TestEncodeRequestHeaders_RealGrokProfileOrderReachesTransportEncodingPath`、`TestEncodeHeaders_DuplicateConfiguredPseudoHeadersEmitOnceAndKeepUnconfigured` | 上游仍发 `a,m,p,s`，普通头仍由 map 枚举，未列头无确定尾排，真实 encoding seam 尚未接收配置；重复伪头配置还会破坏唯一发出语义。 | `go test ./internal/pkg/grokhttp2/... -count=1`；`go test -tags=unit ./internal/pkg/tlsfingerprint/... -count=1` |
| Phase 5 | 功能红灯 N/A：本阶段主体是纯既有回归网；新增保护为 `TestProfileCacheKeyIncludesGrokHTTP2ObservableConfiguration`。 | fallback、proxy、preface、cache、session 与 access-denied 均是既有行为；新增 Grok transport 能力与两组头序未进入 CacheKey 时，该测试先因 CacheKey 碰撞而红。 | `go test -tags=unit ./internal/pkg/tlsfingerprint/... -count=1`；`GOMAXPROCS=1 go test -p 1 -parallel 1 -tags=unit ./internal/repository -run "ALPN\|Fallback\|Proxy\|Cache\|Session\|WrapConn\|AccessDenied" -count=1` |
| Phase 6 | `TestHPACKEvidenceRejectsDecodedHeadersOnly`、`TestHPACKEvidenceRequiresSameConnectionTwoRequests`、`TestHPACKEvidenceRequiresRequestLifecycleBindings`、`TestHPACKEvidenceRequiresLifecycleMetadata`、`TestHPACKEvidenceRepositoryFixtureContainsOnlyDerivedData`、`TestEvidenceRejectsDirectJSONSerialization`、`TestHPACKAnalysisRejectsCallerClaimsAsIndeterminate` | validator 尚未存在或约束不足，无法拒绝仅解码头、跨连接请求、缺失 lifecycle、原始数据序列化及无证据 HPACK 结论，也不能保证仓库摘要只含派生数据。 | `go test ./internal/pkg/grokhttp2/... -count=1` |
| Phase 6 审计返修 | `TestHPACKEvidenceRejectsReservedAndRegressingClientStreamIDs`、`TestHPACKEvidenceRequiresH2ALPNAndSHA256CertificateHash`、`TestHPACKEvidenceRejectsWeakSettingsAndRawBlockSemantics`、`TestHPACKEvidenceAcceptsUniqueExtensionSettingsAndPreservesWireOrder` | 初版未完整拒绝 high-bit stream 与倒序 stream，未把 ALPN 限为 `h2`、未严格校验证书 SHA-256；SETTINGS 规则还需同时接受唯一扩展 SETTINGS、保留线序并继续拒绝重复项。 | `go test ./internal/pkg/grokhttp2/... -count=1`；`go vet ./internal/pkg/grokhttp2/...` |
| Go 1.27 边界 | 初始矩阵先出现 `undefined: encodeRequestHeaders`，且 `TestNewFingerprintHTTP2Transport_GrokProfileUsesFork` 的正向期望与 wrapper 无保序 hook 冲突；边界测试为 `TestSupportsHeaderOrder_Go127WrapperReportsUnsupported`、`TestNewFingerprintHTTP2Transport_GrokOrderedHeadersFailOnGo127WrapperBranch`。 | Go 1.27 wrapper 分支不再暴露本地 request encoder，不能静默声称头序能力；正向 fork 测试只适用于 legacy encoding 分支。 | Go 1.27 toolchain：`go test ./internal/pkg/grokhttp2/... -count=1`、`go test -tags=http2legacy ./internal/pkg/grokhttp2/... -count=1`、`go test -tags=unit ./internal/repository -run "Grok" -count=1`、`go test -tags="unit http2legacy" ./internal/repository -run "Grok" -count=1` |
| Phase 7 | `TestDocumentation_StatesHPACKStatusPrecisely`、`TestVerificationPlan_EnumeratesKnownSixExistingFailures`、`TestVerificationPlan_RecordsCleanHeadThirtyFiveLeafBaseline`、`TestVerificationPlan_RecordsPhaseTDDTrace` | README / VERIFICATION 缺失时先红；后续分别因 HPACK 状态不精确、基线失败未完整分组以及缺少本 TDD trace 而红。 | `go test ./internal/pkg/grokhttp2 -run "TestDocumentation_\|TestVerificationPlan_\|TestProvenanceDocs_" -count=1`；`go test ./internal/pkg/grokhttp2/... -count=1` |
| Phase 8 | `TestSourceAlignedRustH2FixtureContract`、`TestGrokClientEncoderMatchesRustH2SourceFixture`、`TestGrokClientEncoderThreeQuarterIndexThreshold`、`TestGrokClientEncoderUsesRustStaticFieldMatching`、`TestGrokClientEncoderUsesRustTableSizeUpdateQueue`、`TestGrokClientEncoderSensitiveFieldCanReuseExactDynamicEntry`、`TestGrokClientEncoderSensitiveFieldUsesDynamicNameBeforeStaticName`、`TestClientConnRuntimeUsesSourceAlignedGrokHPACK`、`TestClientConnRuntimeContinuationMatchesRustH2SourceFixture` | 第一阶段缺少 fixture；第二阶段缺少 `NewGrokClientEncoder`；真实 `ClientConn` 初始仍使用默认 Go encoder；相同 4096 SETTINGS 会多发 table-size update；普通静态表空值会被 Go 表误判为 exact；encoder 上限与连续 size update 尚未采用 Rust 队列语义；同名多值会被逐项查表并再次索引；sensitive 动态查表会错误退回静态 name index。 | 两次 `cargo run --locked --offline -- <output-path>` 逐字节一致；`go test ./internal/pkg/grokhttp2/... -count=1` |
| Official wire | `TestOfficialWireReportIsPinnedSafeAndScenarioAligned`、`test_tls_proxy_forwards_decrypted_h2_bytes_unchanged`、`test_safe_report_writer_rejects_credential_material` | 初始只有 keylog 失败结论；随后透明代理首次因严格证书缺 AKI 红灯，正式首轮 ACP 超时暴露诊断缺口，第二轮又因只取每轮首请求而选到非相邻 stream。 | `python -m unittest -v test_capture.py`；`python capture.py --run-official`；`go test ./internal/pkg/grokhttp2/... -count=1` |
| Official TLS resumption | `TestOfficialTLSResumptionReportIsPinnedSafeAndStructurallyAligned`、`test_fragmented_record_yields_resumed_client_hello`、`test_server_hello_reports_selected_identity` | 初始只具备本地恢复能力，未取得官方 resumed ClientHello；首版本地恢复形态还比官方多发空 `session_ticket(35)`。 | `python -m unittest -v test_resumption.py`；`python capture_resumption.py --run-official`；`go test ./internal/pkg/grokhttp2 -run TestOfficialTLSResumptionReport -count=1` |
| Official HPACK branches | `TestOfficialHPACKBranchesReportIsPinnedSafeAndAligned`、`test_long_path_produces_captured_continuation`、`test_bearer_auth_builder_is_not_a_sensitive_marking_api` | 常规样本未越过单帧上限；隔离压力抓取首轮又因官方在 stream `1/3` 发出两次目标请求而触发单请求断言。 | `python -m unittest -v test_hpack_branches.py`；`python capture_hpack_branches.py --run-official`；`go test ./internal/pkg/grokhttp2 -run TestOfficialHPACKBranchesReport -count=1` |

## Phase 8 可复现证据

- 官方二进制外部观察：`grok 0.2.112 (9bbd559437)`，SHA-256 `2469bd182af212c7fcb84f2981999e4e8a6a7a2e4172bad3ae7f787a1f11407c`；二进制编译路径报告 `reqwest 0.12.24`、`hyper 1.8.1`、`h2 0.4.15`、`http 1.4.0`。
- 公开 `0.2.112` 快照：`47348d13ec4508dcfe440e34c6d511bb02998fb2`，公开 `Cargo.lock` SHA-256 `852e088a2b4ac3586142592a6c6bbd3f78b8446a8fa8a24b5131baa44b31fd38`。
- crates.io `h2 0.4.15` checksum：`6cb093c84e8bd9b188d4c4a8cb6579fc016968d14c99882163cd3ff402a4f155`。
- harness `Cargo.lock` SHA-256：`5f70259f7963478a0ecc247ae036ff78f600f3af7223e4b15106b5fdbf9cedca`。
- 两次离线输出均为 166131 字节，SHA-256 均为 `1650c6ef13cf0c01d8bd737d4809de42ccb95879f4cf18af6ab4072450a89dcf`，逐字节相同。
- stateful case 为一个连接、一个 preface、stream `1/3`；continuation case 使用独立连接，完整 header block 为 16623 字节并拆为一个 `HEADERS` 和一个 `CONTINUATION`。
- stateful case 还覆盖 Rust `HeaderMap` 同名多值的 name-less 后续项，以及 sensitive 新值优先复用动态 name index；定向边界测试另行锁定 sensitive exact dynamic reuse、普通静态表空值匹配和超过 4096 的连续 table-size update 队列。
- 上述测试均来自 crates.io `h2 0.4.15` 源码语义和合成 raw peer，不冒充官方原始头块。
- 内部 revision 与公开提交是独立 provenance；未证明 `9bbd559437 == 47348d...`，也未证明不存在内部 patch。

## Official live wire evidence

- 官方二进制：`grok 0.2.112 (9bbd559437)`，SHA-256 `2469bd182af212c7fcb84f2981999e4e8a6a7a2e4172bad3ae7f787a1f11407c`。
- 捕获绑定：一个官方进程、一个 ACP session、一个 TLS/H2 connection；目标 stream `3/5`，client preface 精确，SETTINGS 顺序为 `2=0,4=2097152,5=16384,6=16384`，ACK 与连接 close 均观察到。
- 目标头块：stream 3 为 994 字节，stream 5 为 833 字节；两块官方 SHA-256 分别与本地 encoder SHA-256 相同，byte equality 均为 true。
- 动态状态：目标连接共编码并比较 3 个 client header block，全部相等；第二目标块有 19 次 dynamic reference，证明同连接动态表实际复用。
- 分支：所有非空 literal 均 Huffman；两块各有 3 个 skip 字段，均为 without-index。官方样本 never-index count 为 0，两个块都没有 CONTINUATION。
- 派生报告：`testdata/official-wire-capture/official-wire-report.json`，6178 字节，SHA-256 `ab4332a2b2bb7d0411c73cbf92c502ff3ef96881871704079ba8fdd12fd20729`。
- 安全复核：报告为纯 ASCII，凭据/endpoint denylist 无命中；raw/decoded headers、Authorization、body 均未落盘；`config.toml` 前后 SHA-256 均为 `bf00790f1a9edbbe24e2380bc9459fbc57e6e0144911294680c297fcb715d1f4`；父进程代理环境未变，Windows 根证书库未安装临时 CA。

## Official HPACK branch evidence

- 使用锁定官方二进制、临时 `GROK_HOME`、随机合成 API key、本地 TLS/H2 端点及只允许 localhost 的 CONNECT proxy；用户 `auth.json` 未读取或修改。
- 官方目标请求位于同一 H2 连接的 stream `1/3`，完整块为 23126/23063 字节；两块均拆成一个 16384 字节 HEADERS fragment 与一个 CONTINUATION，完整块、SHA-256 和 fragment 切分均与 `NewGrokClientEncoder` 相等。
- 两次 API-key Authorization 都是 `literal_without_indexing`、Huffman=true、decoder sensitive=false、never-index count=0；既有 OAuth 报告的两次请求同样为 0 个 never-index。
- 公开 `47348d...` 快照的 2365 个 Rust 文件中未出现 `set_sensitive(` 或 `header_sensitive(`。这只支持 `NOT_APPLICABLE_FOR_OBSERVED_OFFICIAL_GROK_AUTH_BUILDERS`，不把本地 never-index 分支冒充为官方已触发分支。
- 派生报告：`testdata/official-wire-capture/official-hpack-branches-report.json`，4566 字节，SHA-256 `dca49d2d1f67bed9e8ad15d22bad1dadc95bafb0280124b1f18186df31e0e3b6`。

## Official TLS resumption evidence

- 同一官方进程/session 的两次 prompt 之间强制断开 TCP；官方恢复 ClientHello 提供 PSK，服务端通过 `selected_identity=0` 接受。随后用本地 Grok profile 重放同一场景，也成功恢复。
- 官方和本地恢复 ClientHello 的 cipher suites、TLS versions、groups、signature algorithms、key share、ALPN、PSK modes、ticket/binder 长度等必需结构全部相等；`pre_shared_key(41)` 均为最后一个扩展。
- rustls 会按连接随机排列非 PSK 扩展，因此报告按集合和不变量比较，不宣称随机扩展线序逐项相同。当前仅有一组官方恢复样本。
- 派生报告：`testdata/official-wire-capture/official-tls-resumption-report.json`，12498 字节，SHA-256 `2372f64ff14f2d51b3526d20fa77adec01fdd615a30841128e4f838e79539c9b`。

## Multi-instance Grok session state

- turn index：可选 `GrokTurnIndexStore` 由生产 `gatewayCache` 实现，Redis Lua 在 12 小时 TTL 内原子写入 `max(current, derived)`；账号与 conversation id 一起 SHA-256 后才进入 key。相同请求重试保持同一值，所有成功读取的共享最大值会回灌本地 tracker，Redis 暂时故障也不会让本进程倒退。
- turn index 的输入仍是请求体中可观察到的 user turn 数量。若调用方完全裁掉旧历史且没有任何可区分的新轮次信号，relay 无法无歧义区分“同请求重试”和“正文完全相同的新一轮”；实现选择不猜测自增，保证跨实例一致、单调和重试幂等。
- compaction：首次 OAuth 推理按账号读取 `/v1/settings` 与 `/v1/models`，Redis L2 TTL 为 6 小时，进程 L1 TTL 为 5 分钟并用 singleflight 合并并发首次加载。模型目录成功加载后，即使为空或缺少 `grok-4.5` 也不再使用静态值；只有目录抓取失败才回退官方实抓的 `grok-4.5=400000`。
- `compactionAtTokens` 与 `compactionsRemaining` 独立解析和发头，支持 bool/固定数值、legacy `sendCompactionsRemaining` 及官方 `_meta` 回退。`/settings`、`/models` 使用 `HTTPUpstreamProfileGrokControlPlane` 的独立连接池、HPACK encoder 与 TLS session cache，不污染推理连接状态。

## Official binary patch audit

- 二进制完整字符串扫描只发现 `h2 0.4.15`；共有 415 个 crates.io registry 源路径锚点，覆盖 31 个源文件和 235 个行号锚点，全部能映射到本机 checksum 对齐的 crates.io 源。
- 未发现 `git+.../h2` 替换标记；公开快照 Cargo.lock hash 与 h2 registry checksum 均精确匹配。
- 二进制同时包含多个其他 HTTP crate 版本（`reqwest 0.12.24/0.13.4`、`http 0.2.12/1.4.0`），不能把“存在某版本字符串”单独当作具体调用路径证明；live wire equality 才是声明场景的行为证据。
- 结论：`NO_PATCH_EVIDENCE_AND_DECLARED_SCENARIO_WIRE_ALIGNED`。stripped binary 不能数学证明未执行/未使用的私有改动绝对不存在，但声明场景没有观察到影响 wire 的 patch。

## 已独立审计通过的定向命令

为降低共享环境中的并发干扰，测试审计使用串行低并发设置：

```text
go test -p 1 -parallel 1 ./internal/pkg/grokhttp2/... -count=1
go build ./internal/pkg/grokhttp2/...
go vet ./internal/pkg/grokhttp2/...
go test ./internal/pkg/grokhttp2/http2/hpack -count=1
go test ./internal/pkg/grokhttp2/http2 -run "TestClientConnRuntime" -count=1
GOMAXPROCS=1 go test -p 1 -parallel 1 -tags=unit ./internal/pkg/tlsfingerprint/... -count=1
GOMAXPROCS=1 go test -p 1 -parallel 1 -tags=unit ./internal/repository -run "Fingerprint|Grok|Fork|Header|ALPN|Fallback|Proxy|Cache|Session|WrapConn|AccessDenied" -count=1
```

Go 1.27 RC wrapper 矩阵也已独立复核：默认 grokhttp2 与 repository 的 Grok 定向测试通过；`http2legacy` 下 grokhttp2 与 repository 的正向头序路径通过。默认 `go1.27 && !http2legacy` 对无法 honor 头序的 Grok 请求保持显式报错，不静默退化。

## 最终全仓 QA 结果

以下命令已实际通过：

| 结果 | 范围 | 命令 |
| --- | --- | --- |
| PASS | Rust fixture 可复现 | 两次 `cargo run --quiet --locked --offline -- <output-path>` 与仓库 golden 三者逐字节相同 |
| PASS | 官方透明捕获本地自测 | `python -m unittest -v test_capture.py`（5/5） |
| PASS | 官方证据 harness 本地自测 | `python -m unittest -v test_capture.py test_resumption.py test_hpack_branches.py`（12/12） |
| PASS | 官方 live wire | `python capture.py --run-official`；stream `3/5`，目标及连接级全部 byte-equal |
| PASS | 官方 HPACK branch live wire | `python capture_hpack_branches.py --run-official`；stream `1/3` 的 CONTINUATION 完整块及切分 byte-equal |
| PASS | 官方派生报告契约 | `go test ./internal/pkg/grokhttp2 -run TestOfficialWireReportIsPinnedSafeAndScenarioAligned -count=1` |
| PASS | 官方 TLS/HPACK 分支报告契约 | `go test ./internal/pkg/grokhttp2 -run "TestOfficial.*Report" -count=1` |
| PASS | HPACK source-aligned runtime | `go test ./internal/pkg/grokhttp2/http2/hpack -count=1`；`go test ./internal/pkg/grokhttp2/http2 -run "TestClientConnRuntime" -count=1` |
| PASS | 格式 | `gofmt -l internal/pkg/grokhttp2 internal/pkg/tlsfingerprint internal/repository`（无输出） |
| PASS | 补丁完整性 | `git diff --check` |
| PASS | 全仓编译 | `go build -p 1 ./...` |
| PASS | internal vet | `go vet -p 1 ./internal/...` |
| PASS | TLS 指纹 unit | `go test -p 1 -parallel 1 -tags=unit ./internal/pkg/tlsfingerprint/... -count=1` |
| PASS | repository unit | `go test -p 1 -parallel 1 -tags=unit ./internal/repository/ -count=1` |
| PASS | Grok/Fingerprint service unit | `go test -p 1 -parallel 1 -tags=unit ./internal/service/ -run "Grok|Fingerprint" -count=1 -timeout 8m` |
| PASS | fork 全包 | `go test -p 1 -parallel 1 ./internal/pkg/grokhttp2/... -count=1` |
| PASS | 非 unit service/handler | `go test ./internal/service/ ./internal/handler/... -count=1 -timeout 12m`（96.4s） |

低并发非 unit 变体 `go test -p 1 -parallel 1 ./internal/service/ ./internal/handler/... -count=1 -timeout 12m` 连续失败于 `TestOllamaCloudUsageRefreshSingleflightAndRunnerDeduplicateSharedGroup`。该同步竞态不是只在 unit，也不是原计划或 CI 的参数，因此此变体不列为 PASS，不能覆盖上表原始命令的 96.4s PASS 结果。

完整全仓 unit 门禁：`RED`。精确门禁 `go test -json -tags=unit ./... -count=1` 仍命中干净 HEAD 已存在的 35 个稳定失败叶子，不能写成全仓全绿。当前工作树无持久新增失败，详细归因如下。

## 基线失败归因

### Go 1.26.5 全仓 unit 基线：35 个叶子

- 固定干净 HEAD：`fcf649a72`
- 环境：`go1.26.5 windows/amd64`
- 精确命令：`go test -json -tags=unit ./... -count=1`
- 结果：仅 2 个包、35 个失败叶子，即 **35 = 29 + 6**。

当前工作树的 29 个 handler 失败叶子与干净 HEAD 集合完全相同；使用真实 CI 参数随后重跑完整 service unit，也只剩以下原有 6 项。因此完整门禁虽仍为红色，但没有持久新增失败。

#### internal/handler：29 项

- `TestGrokFastTransientPolicyAcrossHTTPHandlers/chat_completions_bridge`
- `TestGrokFastTransientPolicyAcrossHTTPHandlers/chat_completions_raw`
- `TestGrokFastTransientPolicyAcrossHTTPHandlers/media`
- `TestGrokFastTransientPolicyAcrossHTTPHandlers/messages`
- `TestGrokMedia429FailoverIsBounded/first_429_selects_one_healthy_followup`
- `TestGrokMedia429FailoverIsBounded/second_429_stops_without_sweeping_a_third_account`
- `TestGrokOAuthCredentialFailoverAcrossHTTPHandlers/chat_completions_raw_fallback_revoked_selects_healthy`
- `TestGrokOAuthCredentialFailoverAcrossHTTPHandlers/chat_completions_revoked_selects_healthy`
- `TestGrokOAuthCredentialFailoverAcrossHTTPHandlers/grok_media_revoked_selects_healthy`
- `TestGrokOAuthCredentialFailoverAcrossHTTPHandlers/messages_revoked_selects_healthy`
- `TestGrokOAuthMissingSelectedRowRetriesHealthyAccountWithoutMutation`
- `TestOpenAIGatewayHandlerImages_ServerErrorFailsOverAndReturnsClearErrorWhenExhausted`
- `TestOpenAIGatewayHandlerResponses_FailoverAbortsWhenClientDisconnected`
- `TestOpenAIGatewayHandlerResponses_FailoverContinuesForConnectedClient`
- `TestResponsesCredentialFailoverLoop/post-mapping_cancellation_stops_before_scheduler_mutation_or_reselection`
- `TestResponsesCredentialFailoverLoop/revoked_account_selects_healthy_account`
- `TestResponsesGrok402FailoverCooldown`
- `TestResponsesGrok429FailoverHandlesMixedStatuses/429_then_500_stops_after_the_bounded_followup`
- `TestResponsesGrok429FailoverHandlesMixedStatuses/500_then_429_permits_one_healthy_followup`
- `TestResponsesGrok429FailoverHandlesMixedStatuses/OAuth_429_then_API-key_failure_cannot_bypass_the_bound`
- `TestResponsesGrok429FailoverIsBounded/first_rate_limited_account_selects_healthy_account`
- `TestResponsesGrok429FailoverIsBounded/two_rate_limited_accounts_stop_without_sweeping_the_pool`
- `TestResponsesGrokFastTransientRetryPolicy/capacity_recovery_on_the_third_retry_never_switches_account`
- `TestResponsesGrokFastTransientRetryPolicy/capacity_retries_same_account_three_times_then_one_different_account`
- `TestResponsesGrokFastTransientRetryPolicy/connection_503_uses_the_same_bounded_immediate_retry_sequence`
- `TestResponsesGrokFastTransientRetryPolicy/two_capacity_accounts_stop_after_exactly_one_followup_attempt`
- `TestResponsesWebSocketCredentialFailoverLoop/capacity_response_retries_in_place_then_selects_one_healthy_account`
- `TestResponsesWebSocketCredentialFailoverLoop/revoked_account_selects_healthy_account`
- `TestResponsesWebSocketCredentialFailoverLoop/two_capacity_accounts_close_after_the_single_followup`

这 29 项的共同根因是旧测试桩只实现 `Do`，并嵌入 nil `HTTPUpstream` 来满足接口；被测 service 路径调用提升出来的 `DoWithTLS` 时会解引用 nil。测试桩是 handler 内存替身，调用完全不进入 repository / fork，因此这组失败没有覆盖本任务修改的 transport 或 `grokhttp2`。

#### internal/service：6 项

以下 6 项在干净 HEAD 可复现，均与 Grok HTTP/2 fork 无关、非本任务新增：

- `TestGetModelPricing_OpenAICompactAliasesFallback/gpt5.5`
- `TestComputeFinalAnthropicBeta_APIKeyHaiku_StillUsesAPIKeyBetas`
- `TestComputeFinalCountTokensAnthropicBeta_OAuthTransparent_NoClientBetaInjectsDefault`
- `TestBuildUpstreamRequest_APIKeyHaiku_RemainsUnmimicked`
- `TestNormalizeOpenAIResponsesLiteTools_StripsImageDetailsOnlyFromSupportedContent`
- `TestRateLimitService_HandleUpstreamError_OAuth401NoRefreshTokenIgnoredByDefault/antigravity_no_refresh_token_sets_error`

### Go 1.27 wrapper 基线：3 项

以下 3 项仅在 Go 1.27 wrapper 测试矩阵出现，已在未应用本任务改动的干净 HEAD 独立复现；它们不是上述 Go 1.26.5 的 35 项，也非本任务新增：

- `TestEnableOpenAIHTTP2KeepAlive_EnablesPingHealthCheck`
- `TestBuildUpstreamTransport_OpenAIH2_EnablesPingHealthCheck`
- `TestBuildUpstreamTransport_OpenAIH2_WithHTTPProxy_EnablesKeepAlive`

## 两项时序残余风险

1. `TestTokenRefreshService_SaturatedProviderPreservesConcurrencyAndActualQPSStartSpacing`
   - 一次完整全仓 unit 运行出现启动间距断言失败。
   - 隔离复跑默认调度 20/20 通过，单 P 20/20 通过；随后按真实 CI 参数运行完整 service unit 时只剩原有 6 项。
   - 该叶子当前不是持久新增失败，但高负载调度下的实际启动间距仍是残余风险。
2. `TestOllamaCloudUsageRefreshSingleflightAndRunnerDeduplicateSharedGroup`
   - 强制全仓 `-parallel 1` 会暴露测试同步竞态；低并发非 unit 变体也连续失败，因此该风险不是只在 unit。
   - `-parallel 1` 不是 CI 参数，不能用该变体替代精确门禁归因，也不能把它列作非 unit PASS。
   - 测试未确认第二个调用已加入 singleflight 就释放首请求，单 P 调度可能使第二次调用成为独立刷新并命中手动刷新限流。
   - 这是测试确定性风险，与 repository / fork 路径无关。

最终结论是：完整全仓 unit 门禁仍因干净 HEAD 的 35 个稳定叶子保持红色；两项时序风险需后续单独治理，但当前没有持久新增失败。
