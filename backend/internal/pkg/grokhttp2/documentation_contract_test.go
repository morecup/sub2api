package grokhttp2

import (
	"strings"
	"testing"
)

var knownGo126HandlerBaselineFailures = []string{
	"TestGrokFastTransientPolicyAcrossHTTPHandlers/chat_completions_bridge",
	"TestGrokFastTransientPolicyAcrossHTTPHandlers/chat_completions_raw",
	"TestGrokFastTransientPolicyAcrossHTTPHandlers/media",
	"TestGrokFastTransientPolicyAcrossHTTPHandlers/messages",
	"TestGrokMedia429FailoverIsBounded/first_429_selects_one_healthy_followup",
	"TestGrokMedia429FailoverIsBounded/second_429_stops_without_sweeping_a_third_account",
	"TestGrokOAuthCredentialFailoverAcrossHTTPHandlers/chat_completions_raw_fallback_revoked_selects_healthy",
	"TestGrokOAuthCredentialFailoverAcrossHTTPHandlers/chat_completions_revoked_selects_healthy",
	"TestGrokOAuthCredentialFailoverAcrossHTTPHandlers/grok_media_revoked_selects_healthy",
	"TestGrokOAuthCredentialFailoverAcrossHTTPHandlers/messages_revoked_selects_healthy",
	"TestGrokOAuthMissingSelectedRowRetriesHealthyAccountWithoutMutation",
	"TestOpenAIGatewayHandlerImages_ServerErrorFailsOverAndReturnsClearErrorWhenExhausted",
	"TestOpenAIGatewayHandlerResponses_FailoverAbortsWhenClientDisconnected",
	"TestOpenAIGatewayHandlerResponses_FailoverContinuesForConnectedClient",
	"TestResponsesCredentialFailoverLoop/post-mapping_cancellation_stops_before_scheduler_mutation_or_reselection",
	"TestResponsesCredentialFailoverLoop/revoked_account_selects_healthy_account",
	"TestResponsesGrok402FailoverCooldown",
	"TestResponsesGrok429FailoverHandlesMixedStatuses/429_then_500_stops_after_the_bounded_followup",
	"TestResponsesGrok429FailoverHandlesMixedStatuses/500_then_429_permits_one_healthy_followup",
	"TestResponsesGrok429FailoverHandlesMixedStatuses/OAuth_429_then_API-key_failure_cannot_bypass_the_bound",
	"TestResponsesGrok429FailoverIsBounded/first_rate_limited_account_selects_healthy_account",
	"TestResponsesGrok429FailoverIsBounded/two_rate_limited_accounts_stop_without_sweeping_the_pool",
	"TestResponsesGrokFastTransientRetryPolicy/capacity_recovery_on_the_third_retry_never_switches_account",
	"TestResponsesGrokFastTransientRetryPolicy/capacity_retries_same_account_three_times_then_one_different_account",
	"TestResponsesGrokFastTransientRetryPolicy/connection_503_uses_the_same_bounded_immediate_retry_sequence",
	"TestResponsesGrokFastTransientRetryPolicy/two_capacity_accounts_stop_after_exactly_one_followup_attempt",
	"TestResponsesWebSocketCredentialFailoverLoop/capacity_response_retries_in_place_then_selects_one_healthy_account",
	"TestResponsesWebSocketCredentialFailoverLoop/revoked_account_selects_healthy_account",
	"TestResponsesWebSocketCredentialFailoverLoop/two_capacity_accounts_close_after_the_single_followup",
}

var knownGo126ServiceBaselineFailures = []string{
	"TestGetModelPricing_OpenAICompactAliasesFallback/gpt5.5",
	"TestComputeFinalAnthropicBeta_APIKeyHaiku_StillUsesAPIKeyBetas",
	"TestComputeFinalCountTokensAnthropicBeta_OAuthTransparent_NoClientBetaInjectsDefault",
	"TestBuildUpstreamRequest_APIKeyHaiku_RemainsUnmimicked",
	"TestNormalizeOpenAIResponsesLiteTools_StripsImageDetailsOnlyFromSupportedContent",
	"TestRateLimitService_HandleUpstreamError_OAuth401NoRefreshTokenIgnoredByDefault/antigravity_no_refresh_token_sets_error",
}

func TestDocumentation_StatesHPACKStatusPrecisely(t *testing.T) {
	readme := readTextFile(t, "README.md")
	verification := readTextFile(t, "VERIFICATION.md")

	assertContainsAll(t, "README.md", readme, []string{
		"item 4/5：`COMPLETED`",
		"item 6：`OFFICIAL-WIRE-ALIGNED (DECLARED SCENARIO)`",
		"HPACK 源码行为：`SOURCE-ALIGNED`；声明场景：`OFFICIAL-WIRE-ALIGNED`",
		"默认 `hpack.NewEncoder` 与 server 保持上游行为",
		"Rust `HeaderMap` 同名多值的后续项按 name-less iterator 分支编码",
		"目标连接从 stream `1` 开始共 3 个 client header block，全部与 `NewGrokClientEncoder` 逐字节相等",
		"sensitive never-index 对当前观察到的官方 builder 归类为 `NOT-APPLICABLE`",
		"官方 `CONTINUATION` | `OFFICIAL-WIRE-ALIGNED (STRESS SCENARIO)`",
		"TLS session resumption：`OFFICIAL-TLS-RESUMPTION-STRUCTURALLY-ALIGNED`",
	})
	assertContainsAll(t, "VERIFICATION.md", verification, []string{
		"166131 字节",
		"1650c6ef13cf0c01d8bd737d4809de42ccb95879f4cf18af6ab4072450a89dcf",
		"ab4332a2b2bb7d0411c73cbf92c502ff3ef96881871704079ba8fdd12fd20729",
		"dca49d2d1f67bed9e8ad15d22bad1dadc95bafb0280124b1f18186df31e0e3b6",
		"2372f64ff14f2d51b3526d20fa77adec01fdd615a30841128e4f838e79539c9b",
		"Redis Lua 原子保存 `max(current, derived)`",
		"`HTTPUpstreamProfileGrokControlPlane` 的独立连接池、HPACK encoder 与 TLS session cache",
	})
}

func TestVerificationPlan_EnumeratesKnownSixExistingFailures(t *testing.T) {
	verification := readTextFile(t, "VERIFICATION.md")
	section := markdownSection(t, verification, "#### internal/service：6 项", "### Go 1.27 wrapper 基线：3 项")
	got := markdownTestBullets(section)
	if diff := diffLists(knownGo126ServiceBaselineFailures, got); diff != "" {
		t.Fatalf("Go 1.26 baseline failure list mismatch (-want +got):\n%s", diff)
	}
	assertContainsAll(t, "VERIFICATION.md", section, []string{
		"干净 HEAD",
		"非本任务新增",
	})
}

func TestVerificationPlan_RecordsCleanHeadThirtyFiveLeafBaseline(t *testing.T) {
	verification := readTextFile(t, "VERIFICATION.md")
	handlerSection := markdownSection(t, verification, "#### internal/handler：29 项", "#### internal/service：6 项")
	if diff := diffLists(knownGo126HandlerBaselineFailures, markdownTestBullets(handlerSection)); diff != "" {
		t.Fatalf("Go 1.26 handler baseline failure list mismatch (-want +got):\n%s", diff)
	}

	assertContainsAll(t, "VERIFICATION.md", verification, []string{
		"35 = 29 + 6",
		"`go1.26.5 windows/amd64`",
		"`go test -json -tags=unit ./... -count=1`",
		"当前工作树的 29 个 handler 失败叶子与干净 HEAD 集合完全相同",
		"旧测试桩只实现 `Do`",
		"nil `HTTPUpstream`",
		"`DoWithTLS`",
		"完全不进入 repository / fork",
		"无持久新增失败",
	})
}

func TestVerificationPlan_RecordsFinalQAWithoutClaimingFullGreen(t *testing.T) {
	verification := readTextFile(t, "VERIFICATION.md")
	assertContainsAll(t, "VERIFICATION.md", verification, []string{
		"完整全仓 unit 门禁：`RED`",
		"`go build -p 1 ./...`",
		"`go vet -p 1 ./internal/...`",
		"`go test -p 1 -parallel 1 -tags=unit ./internal/repository/ -count=1`",
		"`TestTokenRefreshService_SaturatedProviderPreservesConcurrencyAndActualQPSStartSpacing`",
		"默认调度 20/20",
		"单 P 20/20",
		"`TestOllamaCloudUsageRefreshSingleflightAndRunnerDeduplicateSharedGroup`",
		"强制全仓 `-parallel 1`",
		"不是 CI 参数",
	})
	if strings.Contains(verification, "全仓测试全部通过") {
		t.Fatal("VERIFICATION.md must not claim the still-red full unit gate is green")
	}
}

func TestVerificationPlan_RecordsPhaseTDDTrace(t *testing.T) {
	verification := readTextFile(t, "VERIFICATION.md")
	trace := markdownSection(t, verification, "## TDD trace", "## 已独立审计通过的定向命令")
	assertContainsAll(t, "VERIFICATION.md TDD trace", trace, []string{
		"| Phase 2 |",
		"`TestUpstreamVersionPinnedToXNetV0560`",
		"`TestCompileClosureArtifactsExist`",
		"`TestCompileClosureMatchesUpstreamNonTestFileSet`",
		"缺失闭包",
		"| Phase 3 |",
		"`TestNewFingerprintHTTP2Transport_GrokProfileUsesFork`",
		"`TestBuildUpstreamTransportWithTLSFingerprint_NonGrokAdvertisesHTTP2SyntheticProfileStaysOnStdTransport`",
		"仍返回标准 transport",
		"| Phase 4 |",
		"`TestEncodeHeaders_GrokPseudoHeadersEncodeAsMSAP`",
		"`TestEncodeHeaders_GrokOrdinaryHeadersEncodeInCaptured18HeaderOrder`",
		"`TestEncodeHeaders_GrokUnlistedHeadersUseDeterministicTailPolicyAndPreserveDuplicates`",
		"`TestEncodeRequestHeaders_RealGrokProfileOrderReachesTransportEncodingPath`",
		"真实 encoding seam",
		"| Phase 5 |",
		"功能红灯 N/A",
		"`TestProfileCacheKeyIncludesGrokHTTP2ObservableConfiguration`",
		"CacheKey 碰撞",
		"| Phase 6 |",
		"`TestHPACKEvidenceRejectsDecodedHeadersOnly`",
		"`TestHPACKEvidenceRequiresSameConnectionTwoRequests`",
		"`TestHPACKEvidenceRepositoryFixtureContainsOnlyDerivedData`",
		"`TestHPACKAnalysisRejectsCallerClaimsAsIndeterminate`",
		"| Phase 6 审计返修 |",
		"`TestHPACKEvidenceRejectsReservedAndRegressingClientStreamIDs`",
		"`TestHPACKEvidenceRequiresH2ALPNAndSHA256CertificateHash`",
		"`TestHPACKEvidenceAcceptsUniqueExtensionSettingsAndPreservesWireOrder`",
		"high-bit stream",
		"| Go 1.27 边界 |",
		"undefined: encodeRequestHeaders",
		"`TestSupportsHeaderOrder_Go127WrapperReportsUnsupported`",
		"`TestNewFingerprintHTTP2Transport_GrokOrderedHeadersFailOnGo127WrapperBranch`",
		"| Phase 7 |",
		"`TestDocumentation_StatesHPACKStatusPrecisely`",
		"`TestVerificationPlan_EnumeratesKnownSixExistingFailures`",
		"README / VERIFICATION 缺失",
		"| Phase 8 |",
		"`TestSourceAlignedRustH2FixtureContract`",
		"`TestGrokClientEncoderMatchesRustH2SourceFixture`",
		"`TestGrokClientEncoderSensitiveFieldCanReuseExactDynamicEntry`",
		"`TestGrokClientEncoderSensitiveFieldUsesDynamicNameBeforeStaticName`",
		"`TestClientConnRuntimeUsesSourceAlignedGrokHPACK`",
		"相同 4096 SETTINGS",
		"| Official wire |",
		"`TestOfficialWireReportIsPinnedSafeAndScenarioAligned`",
		"`test_tls_proxy_forwards_decrypted_h2_bytes_unchanged`",
		"`TestOfficialTLSResumptionReportIsPinnedSafeAndStructurallyAligned`",
		"`TestOfficialHPACKBranchesReportIsPinnedSafeAndAligned`",
		"go test ./internal/pkg/grokhttp2/... -count=1",
	})
}

func TestVerificationPlan_RecordsNonUnitQAWithoutLowConcurrencyFalsePass(t *testing.T) {
	verification := readTextFile(t, "VERIFICATION.md")
	assertContainsAll(t, "VERIFICATION.md", verification, []string{
		"`go test ./internal/service/ ./internal/handler/... -count=1 -timeout 12m`",
		"96.4s",
		"`go test -p 1 -parallel 1 ./internal/service/ ./internal/handler/... -count=1 -timeout 12m`",
		"连续失败于 `TestOllamaCloudUsageRefreshSingleflightAndRunnerDeduplicateSharedGroup`",
		"不是只在 unit",
	})
	forbiddenPass := "| PASS | 非 unit service/handler | `go test -p 1 -parallel 1 ./internal/service/ ./internal/handler/... -count=1 -timeout 12m` |"
	if strings.Contains(verification, forbiddenPass) {
		t.Fatal("VERIFICATION.md incorrectly records the low-concurrency non-unit command as PASS")
	}
}

func TestProvenanceDocs_RecordFinalP2Corrections(t *testing.T) {
	for _, name := range []string{"SOURCE.md", "SYNC.md"} {
		body := readTextFile(t, name)
		assertContainsAll(t, name, body, []string{
			"重复的伪头配置只发出一次",
			"保留未配置的合法伪头",
			"接受唯一扩展 SETTINGS",
			"拒绝重复 SETTINGS",
		})
	}
}

func TestVerificationPlan_GrokForkDoesNotClaimUncoveredBranches(t *testing.T) {
	readme := readTextFile(t, "README.md")
	verification := readTextFile(t, "VERIFICATION.md")

	assertContainsAll(t, "README.md", readme, []string{
		"| HPACK/Huffman | `OFFICIAL-WIRE-ALIGNED` |",
		"| 动态表 | `OFFICIAL-WIRE-ALIGNED` |",
		"| skip | `OFFICIAL-WIRE-ALIGNED` |",
		"| sensitive never-index | `NOT-APPLICABLE (OBSERVED AUTH BUILDERS)` |",
		"| 官方 `CONTINUATION` | `OFFICIAL-WIRE-ALIGNED (STRESS SCENARIO)` |",
		"| 官方二进制 wire parity | `OFFICIAL-WIRE-ALIGNED (DECLARED SCENARIO)` |",
	})
	assertContainsAll(t, "VERIFICATION.md", verification, []string{
		"never-index 没有被官方 builder 触发",
		"NOT_APPLICABLE_FOR_OBSERVED_OFFICIAL_GROK_AUTH_BUILDERS",
		"`SOURCE-ALIGNED / WIRE-UNVERIFIED`",
		"未证明 `9bbd559437 == 47348d...`",
		"NO_PATCH_EVIDENCE_AND_DECLARED_SCENARIO_WIRE_ALIGNED",
	})

	combined := readme + "\n" + verification
	for _, forbidden := range []string{
		"HPACK 已与官方完全一致",
		"数学证明任何内部 patch 都不存在",
		"sensitive never-index 已在线上覆盖",
	} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("documentation makes unsupported parity claim %q", forbidden)
		}
	}
}

func markdownSection(t *testing.T, body, start, end string) string {
	t.Helper()
	startIndex := strings.Index(body, start)
	if startIndex < 0 {
		t.Fatalf("documentation missing section %q", start)
	}
	body = body[startIndex+len(start):]
	endIndex := strings.Index(body, end)
	if endIndex < 0 {
		t.Fatalf("documentation missing section %q", end)
	}
	return body[:endIndex]
}

func markdownTestBullets(section string) []string {
	var tests []string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- `Test") || !strings.HasSuffix(line, "`") {
			continue
		}
		tests = append(tests, strings.TrimSuffix(strings.TrimPrefix(line, "- `"), "`"))
	}
	return tests
}
