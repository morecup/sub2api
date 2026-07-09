package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/cespare/xxhash/v2"
	"github.com/tidwall/gjson"
)

// fingerprintSalt 是计算 cc_version 后缀指纹的盐值。
//
// 来源：与 Parrot src/transform/cc_mimicry.py 的 FINGERPRINT_SALT 完全一致；
// 这是真实 Claude Code CLI 抓包推导出的常量，改动会导致 fp 与 CLI 不一致，
// 进一步触发 Anthropic 的第三方检测。
const fingerprintSalt = "59cf53e54c78"

const claudeCodeCCHSeed uint64 = 0x4d659218e32a3268

var (
	claudeCodeCCHSystemNeedle        = []byte(`"system":[`)
	claudeCodeCCHPlaceholderNeedle   = []byte(`cch=00000`)
	claudeCodeCCHModelNeedle         = []byte(`"model":"`)
	claudeCodeCCHMaxTokensNeedle     = []byte(`"max_tokens":`)
	claudeCodeCCHFallbacksNeedle     = []byte(`"fallbacks":[`)
	claudeCodeCCHFallbackTokenNeedle = []byte(`"fallback_credit_token":"`)
)

// computeClaudeCodeFingerprint 复刻真实 Claude Code CLI 的 cc_version 指纹算法：
//
//  1. 取 messages 中第一条 role=user 的非 meta 纯文本
//  2. 取该文本的第 4、7、20 字符（不足以 '0' 补齐）
//  3. SHA256(SALT + chars + cc_version) 取 hex 前 3 字符
//
// 算法来自 Parrot src/transform/cc_mimicry.py:compute_fingerprint，与官方 CLI 字节对齐。
// 任何偏差都会导致 cc_version=X.Y.Z.{fp} 在上游侧与真实 CLI 不一致。
func computeClaudeCodeFingerprint(body []byte, version string) string {
	firstText := extractFirstUserText(body)
	chars := claudeCodeFingerprintChars(firstText)
	sum := sha256.Sum256([]byte(fingerprintSalt + string(chars) + version))
	return hex.EncodeToString(sum[:])[:3]
}

func claudeCodeFingerprintChars(text string) string {
	units := utf16.Encode([]rune(text))
	indices := []int{4, 7, 20}
	chars := make([]uint16, 0, len(indices))
	for _, i := range indices {
		if i < len(units) {
			chars = append(chars, units[i])
		} else {
			chars = append(chars, '0')
		}
	}
	return string(utf16.Decode(chars))
}

// extractFirstUserText 提取 messages 中第一条 user 非 meta 文本内容。
// 兼容 string 和 []block 两种 content 格式；Claude Code 原始实现基于内部
// message.isMeta 跳过 meta user message，代理侧只能从已扁平化 body 中跳过
// 对应的 system-reminder 文本块。
func extractFirstUserText(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	first := ""
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			text := content.String()
			if isClaudeCodeMetaUserText(text) {
				return true
			}
			first = text
			return false
		}
		if content.IsArray() {
			found := false
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					text := block.Get("text").String()
					if isClaudeCodeMetaUserText(text) {
						return true
					}
					first = text
					found = true
					return false
				}
				return true
			})
			return !found
		}
		return true
	})
	return first
}

func isClaudeCodeMetaUserText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<system-reminder>")
}

// buildBillingAttributionText 构造 system 数组的 billing attribution 文本。
//
// 形态对齐真实 Claude Code CLI：
//
//	x-anthropic-billing-header: cc_version=2.1.191.{fp}; cc_entrypoint=cli; cch=00000;
//
// JS bundle 只写入 cch=00000 占位符；真正的非零 CCH 由 claude.exe native
// HTTP 发送层在最终 JSON body 上补写。代理侧先放置同样的占位符，随后在
// NewRequest 前由 applyClaudeCodeCCH 对最终 body 字节补写。
//
// 此 block 不带 cache_control（与真实 CLI 一致；cache breakpoint 由后续的
// Claude Code prompt block 承担）。
func buildBillingAttributionText(body []byte, cliVersion string) (string, error) {
	return buildBillingAttributionTextWithOptions(body, cliVersion, claudeCodeBillingAttributionOptions{})
}

func buildBillingAttributionTextWithOptions(body []byte, cliVersion string, opts claudeCodeBillingAttributionOptions) (string, error) {
	if cliVersion == "" {
		return "", fmt.Errorf("cliVersion required")
	}
	fp := computeClaudeCodeFingerprint(body, cliVersion)
	entrypoint := normalizeClaudeCodeBillingEntrypoint(opts.Entrypoint)
	text := fmt.Sprintf(
		"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s;",
		cliVersion, fp, entrypoint,
	)
	if !opts.OmitCCH {
		text += " cch=00000;"
	}
	if opts.Workload != "" && claudeCodeBillingWorkloadValueRe.MatchString(opts.Workload) {
		text += " cc_workload=" + opts.Workload + ";"
	}
	if opts.IsSubagent {
		text += " cc_is_subagent=true;"
	}
	return text, nil
}

// applyClaudeCodeCCH 复刻 claude.exe native HTTP 发送层的 CCH 补写逻辑。
//
// 动态追踪位置：
//   - claude.exe RVA 0x4b4190：定位 /v1/messages body 中 system billing block 的
//     cch=00000 占位符；
//   - RVA 0x157ea40/0x157ea90/0x157ec50：seeded xxHash64 初始化、update、digest；
//   - RVA 0x4b5137/0x4b513c：将 digest 低 20 bit 写成 5 位小写 hex。
//
// Hash 输入为最终 JSON body 的原始 UTF-8 字节。native 会跳过 `"model":"..."`
// 的值，以及无空白形态的 fallback/max_tokens 相关字段；cch=00000 占位本身参与 hash。
func applyClaudeCodeCCH(body []byte) []byte {
	cch, digitsAt, ok := computeClaudeCodeCCH(body)
	if !ok {
		return body
	}
	out := append([]byte(nil), body...)
	copy(out[digitsAt:digitsAt+5], cch)
	return out
}

func computeClaudeCodeCCH(body []byte) (string, int, bool) {
	digitsAt := findClaudeCodeCCHDigits(body)
	if digitsAt < 0 {
		return "", -1, false
	}

	h := xxhash.NewWithSeed(claudeCodeCCHSeed)
	cursor := 0
	for _, r := range claudeCodeCCHSkipRanges(body) {
		if r.start < cursor || r.start > len(body) || r.end > len(body) || r.end < r.start {
			continue
		}
		_, _ = h.Write(body[cursor:r.start])
		cursor = r.end
	}
	_, _ = h.Write(body[cursor:])

	return fmt.Sprintf("%05x", h.Sum64()&0xfffff), digitsAt, true
}

func findClaudeCodeCCHDigits(body []byte) int {
	systemAt := bytes.Index(body, claudeCodeCCHSystemNeedle)
	if systemAt < 0 {
		return -1
	}
	windowEnd := systemAt + 0x12c
	if windowEnd > len(body) {
		windowEnd = len(body)
	}
	rel := bytes.Index(body[systemAt:windowEnd], claudeCodeCCHPlaceholderNeedle)
	if rel < 0 {
		return -1
	}
	return systemAt + rel + len("cch=")
}

type claudeCodeCCHSkipRange struct {
	start int
	end   int
}

func claudeCodeCCHSkipRanges(body []byte) []claudeCodeCCHSkipRange {
	var ranges []claudeCodeCCHSkipRange
	ranges = appendClaudeCodeCCHModelSkips(ranges, body)
	ranges = appendClaudeCodeCCHFallbacksSkips(ranges, body)
	ranges = appendClaudeCodeCCHFallbackTokenSkips(ranges, body)
	ranges = appendClaudeCodeCCHMaxTokensSkips(ranges, body)
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].start < ranges[j].start
	})
	return ranges
}

func appendClaudeCodeCCHModelSkips(ranges []claudeCodeCCHSkipRange, body []byte) []claudeCodeCCHSkipRange {
	offset := 0
	for offset < len(body) {
		at := bytes.Index(body[offset:], claudeCodeCCHModelNeedle)
		if at < 0 {
			return ranges
		}
		valueStart := offset + at + len(claudeCodeCCHModelNeedle)
		valueEnd := findJSONStringValueEnd(body, valueStart)
		if valueEnd < 0 {
			return ranges
		}
		ranges = append(ranges, claudeCodeCCHSkipRange{start: valueStart, end: valueEnd})
		offset = valueEnd + 1
	}
	return ranges
}

func appendClaudeCodeCCHFallbacksSkips(ranges []claudeCodeCCHSkipRange, body []byte) []claudeCodeCCHSkipRange {
	offset := 0
	for offset < len(body) {
		at := bytes.Index(body[offset:], claudeCodeCCHFallbacksNeedle)
		if at < 0 {
			return ranges
		}
		fieldStart := offset + at
		arrayEnd := findJSONArrayEnd(body, fieldStart+len(claudeCodeCCHFallbacksNeedle))
		if arrayEnd < 0 {
			return ranges
		}
		ranges = append(ranges, expandClaudeCodeCCHFieldSkip(body, fieldStart, arrayEnd))
		offset = arrayEnd
	}
	return ranges
}

func appendClaudeCodeCCHFallbackTokenSkips(ranges []claudeCodeCCHSkipRange, body []byte) []claudeCodeCCHSkipRange {
	offset := 0
	for offset < len(body) {
		at := bytes.Index(body[offset:], claudeCodeCCHFallbackTokenNeedle)
		if at < 0 {
			return ranges
		}
		fieldStart := offset + at
		valueStart := fieldStart + len(claudeCodeCCHFallbackTokenNeedle)
		valueEnd := findJSONStringValueEnd(body, valueStart)
		if valueEnd < 0 {
			return ranges
		}
		ranges = append(ranges, expandClaudeCodeCCHFieldSkip(body, fieldStart, valueEnd+1))
		offset = valueEnd + 1
	}
	return ranges
}

func appendClaudeCodeCCHMaxTokensSkips(ranges []claudeCodeCCHSkipRange, body []byte) []claudeCodeCCHSkipRange {
	offset := 0
	for offset < len(body) {
		at := bytes.Index(body[offset:], claudeCodeCCHMaxTokensNeedle)
		if at < 0 {
			return ranges
		}
		fieldStart := offset + at
		valueStart := fieldStart + len(claudeCodeCCHMaxTokensNeedle)
		valueEnd := valueStart
		for valueEnd < len(body) && body[valueEnd] >= '0' && body[valueEnd] <= '9' {
			valueEnd++
		}
		if valueEnd == valueStart {
			offset = valueStart
			continue
		}
		ranges = append(ranges, expandClaudeCodeCCHFieldSkip(body, fieldStart, valueEnd))
		offset = valueEnd
	}
	return ranges
}

func expandClaudeCodeCCHFieldSkip(body []byte, start, end int) claudeCodeCCHSkipRange {
	if end < len(body) && body[end] == ',' {
		end++
	} else if start > 0 && body[start-1] == ',' {
		start--
	}
	return claudeCodeCCHSkipRange{start: start, end: end}
}

func findJSONStringValueEnd(body []byte, start int) int {
	escaped := false
	for i := start; i < len(body); i++ {
		switch {
		case escaped:
			escaped = false
		case body[i] == '\\':
			escaped = true
		case body[i] == '"':
			return i
		}
	}
	return -1
}

func findJSONArrayEnd(body []byte, start int) int {
	depth := 1
	inString := false
	escaped := false
	for i := start; i < len(body); i++ {
		c := body[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}
