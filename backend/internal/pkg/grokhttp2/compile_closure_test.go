package grokhttp2

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	localhttp2 "github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http2"
)

const localImportPrefix = "github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2"

var copiedPackageDirs = []string{
	"http2",
	filepath.Join("http2", "hpack"),
	filepath.Join("http", "httpguts"),
	filepath.Join("internal", "httpcommon"),
	filepath.Join("internal", "httpsfv"),
}

var declaredLocalOnlyFiles = map[string][]string{
	"http2": {
		"header_order_capability_legacy.go",
		"header_order_capability_wrap.go",
	},
}

func TestCompileClosureArtifactsExist(t *testing.T) {
	t.Helper()

	root := "."
	required := []string{
		"LICENSE",
		"PATENTS",
		"README.md",
		"SOURCE.md",
		"SYNC.md",
		filepath.Join("http2", "transport.go"),
		filepath.Join("http2", "server.go"),
		filepath.Join("http2", "write.go"),
		filepath.Join("http2", "hpack", "encode.go"),
		filepath.Join("internal", "httpcommon", "request.go"),
		filepath.Join("internal", "httpsfv", "httpsfv.go"),
		filepath.Join("http", "httpguts", "guts.go"),
	}

	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("required artifact %s missing: %v", rel, err)
		}
	}
}

func TestCompileClosureMatchesUpstreamNonTestFileSet(t *testing.T) {
	upstreamRoot := upstreamModuleRoot(t)
	for _, dir := range copiedPackageDirs {
		want := expectedGoFilesForDir(t, filepath.Join(upstreamRoot, dir), dir)
		got := collectGoFiles(t, dir)
		if diff := diffLists(want, got); diff != "" {
			t.Fatalf("copied package %s file set mismatch (-want +got):\n%s", dir, diff)
		}
	}
}

func TestForkedSourceFilesOnlyContainDeclaredRewrites(t *testing.T) {
	upstreamRoot := upstreamModuleRoot(t)
	for _, dir := range copiedPackageDirs {
		files := collectGoFiles(t, dir)
		for _, rel := range files {
			localPath := filepath.Join(dir, rel)
			localBody, err := os.ReadFile(localPath)
			if err != nil {
				t.Fatalf("read local %s: %v", localPath, err)
			}
			if expected, ok := expectedLocalOnlyFile(localPath); ok {
				if !bytes.Equal(localBody, []byte(expected)) {
					t.Fatalf("unexpected local-only file content in %s", localPath)
				}
				continue
			}
			upstreamPath := filepath.Join(upstreamRoot, dir, rel)
			upstreamBody, err := os.ReadFile(upstreamPath)
			if err != nil {
				t.Fatalf("read upstream %s: %v", upstreamPath, err)
			}
			expected := expectedForkFile(localPath, upstreamBody)
			if !bytes.Equal(localBody, expected) {
				t.Fatalf("unexpected local rewrite in %s; fork differs from upstream beyond declared rewrites\n%s", localPath, firstSourceDifference(expected, localBody))
			}
		}
	}
}

func firstSourceDifference(want, got []byte) string {
	wantLines := bytes.Split(want, []byte("\n"))
	gotLines := bytes.Split(got, []byte("\n"))
	limit := min(len(wantLines), len(gotLines))
	for i := range limit {
		if !bytes.Equal(wantLines[i], gotLines[i]) {
			return fmt.Sprintf("first difference at line %d:\n-want %s\n+got  %s", i+1, wantLines[i], gotLines[i])
		}
	}
	return fmt.Sprintf("line count differs: want %d, got %d", len(wantLines), len(gotLines))
}

func TestLicenseAndPatentsMatchUpstreamExactly(t *testing.T) {
	upstreamRoot := upstreamModuleRoot(t)
	for _, rel := range []string{"LICENSE", "PATENTS"} {
		got, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("read local %s: %v", rel, err)
		}
		want, err := os.ReadFile(filepath.Join(upstreamRoot, rel))
		if err != nil {
			t.Fatalf("read upstream %s: %v", rel, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s does not match upstream byte-for-byte", rel)
		}
	}
}

func TestProvenanceDocsRecordRealBoundaryFacts(t *testing.T) {
	readme := readTextFile(t, "README.md")
	source := readTextFile(t, "SOURCE.md")
	sync := readTextFile(t, "SYNC.md")
	upstreamRoot := upstreamModuleRoot(t)

	assertContainsAll(t, "README.md", readme, []string{
		"golang.org/x/net v0.56.0",
		"Go 以 package 为编译单元",
		"server.go",
		"write.go",
		"writesched",
		"http2legacy",
		"go1.27",
		"`http2/transport_common.go`",
		"`http2/transport.go`",
		"`internal/httpcommon/request.go`",
		"`EncodeHeadersParam` 新增可选 `HeaderOrder`",
		"`:method,:scheme,:authority,:path`",
	})

	assertContainsAll(t, "SOURCE.md", source, []string{
		"Copy date: `2026-07-28`",
		"本地改动仅限",
		"导入路径重写",
		"http2/http2.go",
		"version.go",
		"fork_boundary_test.go",
		"compile_closure_test.go",
		"`http2/header_order_capability_legacy.go`",
		"`http2/header_order_capability_wrap.go`",
		"`http2/transport_common.go`",
		"`http2/transport.go`",
		"`internal/httpcommon/request.go`",
		"`HeaderOrder`",
		"http2legacy",
		"`http2/hpack/encode.go`",
		"`NewGrokClientEncoder`",
		"`SOURCE-ALIGNED / WIRE-UNVERIFIED`",
	})

	assertContainsAll(t, "SYNC.md", sync, []string{
		"独立比对",
		"LICENSE",
		"PATENTS",
		"逐文件",
		"只允许做导入路径重写",
		"http2legacy",
		"`http2/transport_common.go`",
		"`http2/transport.go`",
		"`internal/httpcommon/request.go`",
		"go test ./internal/pkg/grokhttp2/... -count=1",
		"go build ./internal/pkg/grokhttp2/...",
	})

	if strings.Contains(sync, "仍需后续 provenance 代理补齐独立复核") {
		t.Fatalf("SYNC.md still claims provenance audit is pending")
	}

	wantCopiedPackages := copiedPackageDocEntries()
	assertListEqual(t, "README.md 已复制 package 边界", wantCopiedPackages, markdownBacktickBulletsBetween(t, readme, "已复制 package 边界：", "未复制但仍由"))
	assertListEqual(t, "SOURCE.md 已复制的上游 package", copiedUpstreamPackagePaths(), markdownBacktickBulletsBetween(t, source, "已复制的上游 package：", "未复制、继续直接引用上游 module 的依赖："))

	wantExternalDeps := actualExternalXNetDeps(t)
	assertListEqual(t, "README.md 未复制但仍由上游提供的依赖", wantExternalDeps, markdownBacktickBulletsBetween(t, readme, "未复制但仍由 `golang.org/x/net v0.56.0` 提供的依赖：", "未复制的上游子包："))
	assertListEqual(t, "SOURCE.md 未复制、继续直接引用上游 module 的依赖", wantExternalDeps, markdownBacktickBulletsBetween(t, source, "未复制、继续直接引用上游 module 的依赖：", "未复制的上游相邻子包："))

	wantOmittedSubpackages := omittedHTTP2Subpackages(t, upstreamRoot)
	assertListEqual(t, "README.md 未复制的上游子包", wantOmittedSubpackages, markdownBacktickBulletsBetween(t, readme, "未复制的上游子包：", "保留 `server.go`"))
	assertListEqual(t, "SOURCE.md 未复制的上游相邻子包", prefixEach("golang.org/x/net/", wantOmittedSubpackages), markdownBacktickBulletsBetween(t, source, "未复制的上游相邻子包：", "本地改动仅限："))

	wantClosureFiles := actualClosureFiles()
	assertListEqual(t, "SOURCE.md 当前闭包文件", wantClosureFiles, markdownBacktickBulletsBetween(t, source, "当前闭包文件：", ""))

	assertListEqual(t, "SYNC.md 独立比对 package 列表", wantCopiedPackages, indentedBacktickBulletsBetween(t, sync, "2. 对已复制 package 做独立比对：", "3. 逐文件核对"))
	assertListEqual(t, "SYNC.md 验证命令列表", []string{
		"go test ./internal/pkg/grokhttp2/... -count=1",
		"go build ./internal/pkg/grokhttp2/...",
		"provenance 测试会再次做独立比对、逐文件内容校验，以及 LICENSE/PATENTS 一致性校验。",
	}, normalizeMarkdownCodeLiterals(indentedBulletsBetween(t, sync, "10. 同步完成后运行：", "")))
}

func TestCompileClosureExportsUpstreamSurface(t *testing.T) {
	tr := &localhttp2.Transport{}
	if tr == nil {
		t.Fatal("transport should compile")
	}
}

func upstreamModuleRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMODCACHE")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env GOMODCACHE: %v", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "golang.org", "x", "net@v0.56.0")
}

func collectGoFiles(t testingTB, root string) []string {
	t.Helper()
	var files []string
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dir %s: %v", root, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.ToSlash(name))
	}
	sort.Strings(files)
	return files
}

func expectedGoFilesForDir(t testingTB, upstreamDir, localDir string) []string {
	t.Helper()
	files := collectGoFiles(t, upstreamDir)
	files = append(files, declaredLocalOnlyFiles[filepath.ToSlash(localDir)]...)
	sort.Strings(files)
	return files
}

func expectedForkFile(rel string, upstreamBody []byte) []byte {
	if expected, ok := expectedLocalOnlyFile(rel); ok {
		return []byte(expected)
	}
	text := string(upstreamBody)
	text = strings.ReplaceAll(text, "golang.org/x/net/http/httpguts", localImportPrefix+"/http/httpguts")
	text = strings.ReplaceAll(text, "golang.org/x/net/http2/hpack", localImportPrefix+"/http2/hpack")
	text = strings.ReplaceAll(text, "golang.org/x/net/internal/httpcommon", localImportPrefix+"/internal/httpcommon")
	text = strings.ReplaceAll(text, "golang.org/x/net/internal/httpsfv", localImportPrefix+"/internal/httpsfv")
	if filepath.ToSlash(rel) == "http2/http2.go" {
		text = strings.Replace(text, "package http2 // import \"golang.org/x/net/http2\"\n", "package http2\n", 1)
	}
	switch filepath.ToSlash(rel) {
	case "http2/transport_common.go":
		text = applyDeclaredPhase4TransportCommonPatch(text)
	case "http2/transport.go":
		text = applyDeclaredPhase4TransportPatch(text)
		text = applyDeclaredPhase8TransportPatch(text)
	case "http2/hpack/encode.go":
		text = applyDeclaredPhase8HPACKEncoderPatch(text)
	case "internal/httpcommon/request.go":
		text = applyDeclaredPhase4RequestPatch(text)
	}
	return []byte(text)
}

func expectedLocalOnlyFile(rel string) (string, bool) {
	switch filepath.ToSlash(rel) {
	case "http2/header_order_capability_legacy.go":
		return "//go:build !(go1.27 && !http2legacy)\n\npackage http2\n\n// SupportsHeaderOrder reports whether this build of the Grok HTTP/2 fork can\n// honor Transport.HeaderOrder at the real request-encoding seam.\nfunc SupportsHeaderOrder() bool {\n\treturn true\n}\n", true
	case "http2/header_order_capability_wrap.go":
		return "//go:build go1.27 && !http2legacy\n\npackage http2\n\n// SupportsHeaderOrder reports whether this build of the Grok HTTP/2 fork can\n// honor Transport.HeaderOrder at the real request-encoding seam.\nfunc SupportsHeaderOrder() bool {\n\treturn false\n}\n", true
	default:
		return "", false
	}
}

func applyDeclaredPhase4TransportCommonPatch(text string) string {
	text = strings.Replace(text, "\t\"time\"\n\n\t\"golang.org/x/net/idna\"\n", "\t\"time\"\n\n\t\"github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/internal/httpcommon\"\n\t\"golang.org/x/net/idna\"\n", 1)
	text = strings.Replace(text, "\tDisableCompression bool\n\n\t// AllowHTTP, if true, permits HTTP/2 requests using the insecure,\n", "\tDisableCompression bool\n\n\t// HeaderOrder optionally overrides request pseudo-header and ordinary\n\t// header emission order. Nil keeps the upstream golang.org/x/net v0.56.0\n\t// request enumeration behavior unchanged.\n\tHeaderOrder *HeaderOrder\n\n\t// AllowHTTP, if true, permits HTTP/2 requests using the insecure,\n", 1)
	text = strings.Replace(text, "\t// Internal state, differs between wrapped and non-wrapped implementations.\n\ttransportInternal\n}\n", "\t// Internal state, differs between wrapped and non-wrapped implementations.\n\ttransportInternal\n}\n\n// HeaderOrder is the transport-visible alias of the request header ordering\n// override implemented in the forked internal/httpcommon package.\ntype HeaderOrder = httpcommon.HeaderOrder\n", 1)
	return text
}

func applyDeclaredPhase4TransportPatch(text string) string {
	text = strings.Replace(text, "encodeRequestHeaders(req, cs.requestedGzip, cc.peerMaxHeaderListSize, func(name, value string) {", "encodeRequestHeaders(req, cs.requestedGzip, cc.peerMaxHeaderListSize, cc.t.HeaderOrder, func(name, value string) {", 1)
	text = strings.Replace(text, "func encodeRequestHeaders(req *http.Request, addGzipHeader bool, peerMaxHeaderListSize uint64, headerf func(name, value string)) (httpcommon.EncodeHeadersResult, error) {", "func encodeRequestHeaders(req *http.Request, addGzipHeader bool, peerMaxHeaderListSize uint64, headerOrder *HeaderOrder, headerf func(name, value string)) (httpcommon.EncodeHeadersResult, error) {", 1)
	text = strings.Replace(text, "\t\tPeerMaxHeaderListSize: peerMaxHeaderListSize,\n\t\tDefaultUserAgent:      defaultUserAgent,\n\t}, headerf)\n", "\t\tPeerMaxHeaderListSize: peerMaxHeaderListSize,\n\t\tDefaultUserAgent:      defaultUserAgent,\n\t\tHeaderOrder:           headerOrder,\n\t}, headerf)\n", 1)
	return text
}

func applyDeclaredPhase8TransportPatch(text string) string {
	text = strings.Replace(text,
		"\tcc.henc = hpack.NewEncoder(&cc.hbuf)\n\tcc.henc.SetMaxDynamicTableSizeLimit(conf.MaxEncoderHeaderTableSize)\n",
		"\tcc.henc = hpack.NewGrokClientEncoder(&cc.hbuf)\n",
		1,
	)
	text = strings.Replace(text,
		"\tcc.hbuf.Reset()\n\tres, err := encodeRequestHeaders",
		"\tcc.hbuf.Reset()\n\tcc.henc.BeginHeaderBlock()\n\tres, err := encodeRequestHeaders",
		1,
	)
	text = strings.Replace(text,
		"func (cc *ClientConn) encodeTrailers(trailer http.Header) ([]byte, error) {\n\tcc.hbuf.Reset()\n",
		"func (cc *ClientConn) encodeTrailers(trailer http.Header) ([]byte, error) {\n\tcc.hbuf.Reset()\n\tcc.henc.BeginHeaderBlock()\n",
		1,
	)
	return text
}

func applyDeclaredPhase8HPACKEncoderPatch(text string) string {
	text = strings.Replace(text,
		"const (\n\tuint32Max              = ^uint32(0)\n\tinitialHeaderTableSize = 4096\n)\n\ntype Encoder struct {",
		"const (\n\tuint32Max              = ^uint32(0)\n\tinitialHeaderTableSize = 4096\n)\n\ntype encoderMode uint8\n\nconst (\n\tencoderModeDefault encoderMode = iota\n\tencoderModeGrokClient\n)\n\ntype grokTableSizeUpdate struct {\n\tcount    uint8\n\tmin, max uint32\n}\n\ntype grokHeaderBlockState struct {\n\thasLast   bool\n\tname      string\n\tnameIndex uint64\n}\n\ntype Encoder struct {",
		1,
	)
	text = strings.Replace(text,
		"\tw               io.Writer\n\tbuf             []byte\n}",
		"\tw               io.Writer\n\tbuf             []byte\n\tmode            encoderMode\n\tgrokSizeUpdate  grokTableSizeUpdate\n\tgrokHeaderBlock grokHeaderBlockState\n}",
		1,
	)
	text = strings.Replace(text,
		"func NewEncoder(w io.Writer) *Encoder {\n\te := &Encoder{\n\t\tminSize:         uint32Max,\n\t\tmaxSizeLimit:    initialHeaderTableSize,\n\t\ttableSizeUpdate: false,\n\t\tw:               w,\n\t}\n\te.dynTab.table.init()\n\te.dynTab.setMaxSize(initialHeaderTableSize)\n\treturn e\n}",
		"func NewEncoder(w io.Writer) *Encoder {\n\treturn newEncoder(w, encoderModeDefault)\n}\n\n// NewGrokClientEncoder returns an Encoder whose wire decisions match the\n// crates.io h2 0.4.15 encoder used by the Grok client reference path.\nfunc NewGrokClientEncoder(w io.Writer) *Encoder {\n\treturn newEncoder(w, encoderModeGrokClient)\n}\n\nfunc newEncoder(w io.Writer, mode encoderMode) *Encoder {\n\tmaxSizeLimit := uint32(initialHeaderTableSize)\n\tif mode == encoderModeGrokClient {\n\t\tmaxSizeLimit = uint32Max\n\t}\n\te := &Encoder{\n\t\tminSize:         uint32Max,\n\t\tmaxSizeLimit:    maxSizeLimit,\n\t\ttableSizeUpdate: false,\n\t\tw:               w,\n\t\tmode:            mode,\n\t}\n\te.dynTab.table.init()\n\te.dynTab.setMaxSize(initialHeaderTableSize)\n\treturn e\n}\n\n// BeginHeaderBlock resets per-block state used by the Grok client encoder.\n// It is a no-op for the default encoder.\nfunc (e *Encoder) BeginHeaderBlock() {\n\tif e.mode == encoderModeGrokClient {\n\t\te.grokHeaderBlock = grokHeaderBlockState{}\n\t}\n}",
		1,
	)
	text = strings.Replace(text,
		"\tif e.tableSizeUpdate {\n\t\te.tableSizeUpdate = false",
		"\tif e.mode == encoderModeGrokClient {\n\t\te.appendGrokTableSizeUpdates()\n\t} else if e.tableSizeUpdate {\n\t\te.tableSizeUpdate = false",
		1,
	)
	text = strings.Replace(text,
		"\t}\n\n\tidx, nameValueMatch := e.searchTable(f)",
		"\t}\n\tif e.mode == encoderModeGrokClient && e.grokHeaderBlock.hasLast && e.grokHeaderBlock.name == f.Name {\n\t\tif e.grokHeaderBlock.nameIndex == 0 {\n\t\t\te.buf = appendNewName(e.buf, f, false, true)\n\t\t} else {\n\t\t\te.buf = appendIndexedName(e.buf, f, e.grokHeaderBlock.nameIndex, false, true)\n\t\t}\n\t\treturn e.writeBuf()\n\t}\n\n\tidx, nameValueMatch := e.searchTable(f)\n\tgrokNameIndex := idx",
		1,
	)
	text = strings.Replace(text,
		"\t\tif indexing {\n\t\t\te.dynTab.add(f)\n\t\t}\n",
		"\t\tif indexing {\n\t\t\te.dynTab.add(f)\n\t\t\tif e.mode == encoderModeGrokClient {\n\t\t\t\tgrokNameIndex = uint64(staticTable.len()) + 1\n\t\t\t}\n\t\t}\n",
		1,
	)
	text = strings.Replace(text,
		"\t\tif idx == 0 {\n\t\t\te.buf = appendNewName(e.buf, f, indexing)\n\t\t} else {\n\t\t\te.buf = appendIndexedName(e.buf, f, idx, indexing)\n\t\t}",
		"\t\tif idx == 0 {\n\t\t\te.buf = appendNewName(e.buf, f, indexing, e.mode == encoderModeGrokClient)\n\t\t} else {\n\t\t\te.buf = appendIndexedName(e.buf, f, idx, indexing, e.mode == encoderModeGrokClient)\n\t\t}",
		1,
	)
	text = strings.Replace(text,
		"\t}\n\tn, err := e.w.Write(e.buf)\n\tif err == nil && n != len(e.buf) {\n\t\terr = io.ErrShortWrite\n\t}\n\treturn err\n}\n\n// searchTable",
		"\t}\n\tif e.mode == encoderModeGrokClient {\n\t\te.grokHeaderBlock = grokHeaderBlockState{\n\t\t\thasLast:   true,\n\t\t\tname:      f.Name,\n\t\t\tnameIndex: grokNameIndex,\n\t\t}\n\t}\n\treturn e.writeBuf()\n}\n\nfunc (e *Encoder) writeBuf() error {\n\tn, err := e.w.Write(e.buf)\n\tif err == nil && n != len(e.buf) {\n\t\terr = io.ErrShortWrite\n\t}\n\treturn err\n}\n\n// searchTable",
		1,
	)
	text = strings.Replace(text,
		"\ti, nameValueMatch = staticTable.search(f)\n\tif nameValueMatch {",
		"\tif e.mode == encoderModeGrokClient {\n\t\ti, nameValueMatch = grokSearchStaticTable(f)\n\t} else {\n\t\ti, nameValueMatch = staticTable.search(f)\n\t}\n\tif nameValueMatch {",
		1,
	)
	text = strings.Replace(text,
		"\tif nameValueMatch {\n\t\treturn i, true\n\t}\n\n\tj, nameValueMatch := e.dynTab.table.search(f)",
		"\tif nameValueMatch {\n\t\treturn i, true\n\t}\n\tif e.mode == encoderModeGrokClient && (grokSkipValueIndex(f.Name) || e.grokFieldTooLarge(f)) {\n\t\treturn i, false\n\t}\n\n\tj, nameValueMatch := e.dynTab.table.search(f)",
		1,
	)
	text = strings.Replace(text,
		"\tj, nameValueMatch := e.dynTab.table.search(f)\n\tif nameValueMatch || (i == 0 && j != 0) {",
		"\tdynamicField := f\n\tif e.mode == encoderModeGrokClient {\n\t\tdynamicField.Sensitive = false\n\t}\n\tj, nameValueMatch := e.dynTab.table.search(dynamicField)\n\tif e.mode == encoderModeGrokClient && f.Sensitive && j != 0 {\n\t\treturn j + uint64(staticTable.len()), nameValueMatch\n\t}\n\tif nameValueMatch || (i == 0 && j != 0) {",
		1,
	)
	text = strings.Replace(text,
		"\n// SetMaxDynamicTableSize changes the dynamic header table size to v.\n",
		"\nfunc grokSearchStaticTable(f HeaderField) (uint64, bool) {\n\tswitch f.Name {\n\tcase \":authority\":\n\t\treturn 1, false\n\tcase \":method\":\n\t\tswitch f.Value {\n\t\tcase \"GET\":\n\t\t\treturn 2, true\n\t\tcase \"POST\":\n\t\t\treturn 3, true\n\t\tdefault:\n\t\t\treturn 2, false\n\t\t}\n\tcase \":scheme\":\n\t\tswitch f.Value {\n\t\tcase \"http\":\n\t\t\treturn 6, true\n\t\tcase \"https\":\n\t\t\treturn 7, true\n\t\tdefault:\n\t\t\treturn 6, false\n\t\t}\n\tcase \":path\":\n\t\tswitch f.Value {\n\t\tcase \"/\":\n\t\t\treturn 4, true\n\t\tcase \"/index.html\":\n\t\t\treturn 5, true\n\t\tdefault:\n\t\t\treturn 4, false\n\t\t}\n\tcase \":status\":\n\t\tswitch f.Value {\n\t\tcase \"200\":\n\t\t\treturn 8, true\n\t\tcase \"204\":\n\t\t\treturn 9, true\n\t\tcase \"206\":\n\t\t\treturn 10, true\n\t\tcase \"304\":\n\t\t\treturn 11, true\n\t\tcase \"400\":\n\t\t\treturn 12, true\n\t\tcase \"404\":\n\t\t\treturn 13, true\n\t\tcase \"500\":\n\t\t\treturn 14, true\n\t\tdefault:\n\t\t\treturn 8, false\n\t\t}\n\tcase \"accept-encoding\":\n\t\tif f.Value == \"gzip, deflate\" {\n\t\t\treturn 16, true\n\t\t}\n\tdefault:\n\t}\n\ti, _ := staticTable.search(f)\n\treturn i, false\n}\n\n// SetMaxDynamicTableSize changes the dynamic header table size to v.\n",
		1,
	)
	text = strings.Replace(text,
		"\tif v > e.maxSizeLimit {\n\t\tv = e.maxSizeLimit\n\t}\n\tif v < e.minSize {",
		"\tif v > e.maxSizeLimit {\n\t\tv = e.maxSizeLimit\n\t}\n\tif e.mode == encoderModeGrokClient {\n\t\te.queueGrokTableSizeUpdate(v)\n\t\treturn\n\t}\n\tif v < e.minSize {",
		1,
	)
	text = strings.Replace(text,
		"\te.tableSizeUpdate = true\n\te.dynTab.setMaxSize(v)\n}\n\n// MaxDynamicTableSize returns",
		"\te.tableSizeUpdate = true\n\te.dynTab.setMaxSize(v)\n}\n\nfunc (e *Encoder) queueGrokTableSizeUpdate(v uint32) {\n\tupdate := &e.grokSizeUpdate\n\tswitch update.count {\n\tcase 0:\n\t\tif v != e.dynTab.maxSize {\n\t\t\tupdate.count = 1\n\t\t\tupdate.max = v\n\t\t}\n\tcase 1:\n\t\told := update.max\n\t\tif v > old {\n\t\t\tif old > e.dynTab.maxSize {\n\t\t\t\tupdate.max = v\n\t\t\t} else {\n\t\t\t\tupdate.count = 2\n\t\t\t\tupdate.min = old\n\t\t\t\tupdate.max = v\n\t\t\t}\n\t\t} else {\n\t\t\tupdate.max = v\n\t\t}\n\tcase 2:\n\t\tif v < update.min {\n\t\t\tupdate.count = 1\n\t\t\tupdate.max = v\n\t\t} else {\n\t\t\tupdate.max = v\n\t\t}\n\t}\n}\n\nfunc (e *Encoder) appendGrokTableSizeUpdates() {\n\tupdate := e.grokSizeUpdate\n\te.grokSizeUpdate = grokTableSizeUpdate{}\n\tswitch update.count {\n\tcase 1:\n\t\te.dynTab.setMaxSize(update.max)\n\t\te.buf = appendTableSize(e.buf, update.max)\n\tcase 2:\n\t\te.dynTab.setMaxSize(update.min)\n\t\te.buf = appendTableSize(e.buf, update.min)\n\t\te.dynTab.setMaxSize(update.max)\n\t\te.buf = appendTableSize(e.buf, update.max)\n\t}\n}\n\n// MaxDynamicTableSize returns",
		1,
	)
	text = strings.Replace(text,
		"func (e *Encoder) SetMaxDynamicTableSizeLimit(v uint32) {\n\te.maxSizeLimit = v",
		"func (e *Encoder) SetMaxDynamicTableSizeLimit(v uint32) {\n\tif e.mode == encoderModeGrokClient {\n\t\te.maxSizeLimit = v\n\t\ttarget := e.dynTab.maxSize\n\t\tif e.grokSizeUpdate.count != 0 {\n\t\t\ttarget = e.grokSizeUpdate.max\n\t\t}\n\t\tif target > v {\n\t\t\te.SetMaxDynamicTableSize(v)\n\t\t}\n\t\treturn\n\t}\n\te.maxSizeLimit = v",
		1,
	)
	text = strings.Replace(text,
		"func (e *Encoder) shouldIndex(f HeaderField) bool {\n\treturn !f.Sensitive && f.Size() <= e.dynTab.maxSize\n}\n",
		"func (e *Encoder) shouldIndex(f HeaderField) bool {\n\tif e.mode == encoderModeGrokClient {\n\t\treturn !f.Sensitive && !grokSkipValueIndex(f.Name) && !e.grokFieldTooLarge(f)\n\t}\n\treturn !f.Sensitive && f.Size() <= e.dynTab.maxSize\n}\n\nfunc (e *Encoder) grokFieldTooLarge(f HeaderField) bool {\n\tsize := uint64(len(f.Name)) + uint64(len(f.Value)) + 32\n\treturn size*4 > uint64(e.dynTab.maxSize)*3\n}\n\nfunc grokSkipValueIndex(name string) bool {\n\tswitch name {\n\tcase \":path\",\n\t\t\"age\",\n\t\t\"authorization\",\n\t\t\"content-length\",\n\t\t\"etag\",\n\t\t\"if-modified-since\",\n\t\t\"if-none-match\",\n\t\t\"location\",\n\t\t\"cookie\",\n\t\t\"set-cookie\":\n\t\treturn true\n\tdefault:\n\t\treturn false\n\t}\n}\n",
		1,
	)
	text = strings.Replace(text,
		"func appendNewName(dst []byte, f HeaderField, indexing bool) []byte {\n\tdst = append(dst, encodeTypeByte(indexing, f.Sensitive))\n\tdst = appendHpackString(dst, f.Name)\n\treturn appendHpackString(dst, f.Value)\n}",
		"func appendNewName(dst []byte, f HeaderField, indexing, alwaysHuffman bool) []byte {\n\tdst = append(dst, encodeTypeByte(indexing, f.Sensitive))\n\tdst = appendHpackString(dst, f.Name, alwaysHuffman)\n\treturn appendHpackString(dst, f.Value, alwaysHuffman)\n}",
		1,
	)
	text = strings.Replace(text,
		"func appendIndexedName(dst []byte, f HeaderField, i uint64, indexing bool) []byte {",
		"func appendIndexedName(dst []byte, f HeaderField, i uint64, indexing, alwaysHuffman bool) []byte {",
		1,
	)
	text = strings.Replace(text,
		"\treturn appendHpackString(dst, f.Value)\n}\n\n// appendTableSize",
		"\treturn appendHpackString(dst, f.Value, alwaysHuffman)\n}\n\n// appendTableSize",
		1,
	)
	text = strings.Replace(text,
		"func appendHpackString(dst []byte, s string) []byte {\n\thuffmanLength := HuffmanEncodeLength(s)\n\tif huffmanLength < uint64(len(s)) {",
		"func appendHpackString(dst []byte, s string, alwaysHuffman bool) []byte {\n\thuffmanLength := HuffmanEncodeLength(s)\n\tif s != \"\" && (alwaysHuffman || huffmanLength < uint64(len(s))) {",
		1,
	)
	return text
}

func applyDeclaredPhase4RequestPatch(text string) string {
	text = strings.Replace(text, "\t// DefaultUserAgent is the User-Agent header to send when the request\n\t// neither contains a User-Agent nor disables it.\n\tDefaultUserAgent string\n}\n\n// EncodeHeadersResult", "\t// DefaultUserAgent is the User-Agent header to send when the request\n\t// neither contains a User-Agent nor disables it.\n\tDefaultUserAgent string\n\n\t// HeaderOrder optionally overrides request pseudo-header and ordinary\n\t// header emission order. When nil, request encoding remains upstream-\n\t// equivalent to golang.org/x/net v0.56.0.\n\tHeaderOrder *HeaderOrder\n}\n\n// HeaderOrder describes the optional request header ordering override used by\n// the Grok HTTP/2 fork. It is intentionally limited to pseudo-header order and\n// ordinary header order only.\ntype HeaderOrder struct {\n\tPseudo  []string\n\tRegular []string\n}\n\n// EncodeHeadersResult", 1)
	text = strings.Replace(text, "\tenumerateHeaders := func(f func(name, value string)) {\n\t\t// 8.1.2.3 Request Pseudo-Header Fields\n\t\t// The :path pseudo-header field includes the path and query parts of the\n\t\t// target URI (the path-absolute production and optionally a '?' character\n\t\t// followed by the query production, see Sections 3.3 and 3.4 of\n\t\t// [RFC3986]).\n\t\tf(\":authority\", host)\n\t\tm := req.Method\n\t\tif m == \"\" {\n\t\t\tm = \"GET\"\n\t\t}\n\t\tf(\":method\", m)\n\t\tif !isNormalConnect {\n\t\t\tf(\":path\", path)\n\t\t\tf(\":scheme\", req.URL.Scheme)\n\t\t}\n\t\tif protocol != \"\" {\n\t\t\tf(\":protocol\", protocol)\n\t\t}\n\t\tif trailers != \"\" {\n\t\t\tf(\"trailer\", trailers)\n\t\t}\n\n\t\tvar didUA bool\n\t\tfor k, vv := range req.Header {\n\t\t\tif asciiEqualFold(k, \"host\") || asciiEqualFold(k, \"content-length\") {\n\t\t\t\t// Host is :authority, already sent.\n\t\t\t\t// Content-Length is automatic, set below.\n\t\t\t\tcontinue\n\t\t\t} else if asciiEqualFold(k, \"connection\") ||\n\t\t\t\tasciiEqualFold(k, \"proxy-connection\") ||\n\t\t\t\tasciiEqualFold(k, \"transfer-encoding\") ||\n\t\t\t\tasciiEqualFold(k, \"upgrade\") ||\n\t\t\t\tasciiEqualFold(k, \"keep-alive\") {\n\t\t\t\t// Per 8.1.2.2 Connection-Specific Header\n\t\t\t\t// Fields, don't send connection-specific\n\t\t\t\t// fields. We have already checked if any\n\t\t\t\t// are error-worthy so just ignore the rest.\n\t\t\t\tcontinue\n\t\t\t} else if asciiEqualFold(k, \"user-agent\") {\n\t\t\t\t// Match Go's http1 behavior: at most one\n\t\t\t\t// User-Agent. If set to nil or empty string,\n\t\t\t\t// then omit it. Otherwise if not mentioned,\n\t\t\t\t// include the default (below).\n\t\t\t\tdidUA = true\n\t\t\t\tif len(vv) < 1 {\n\t\t\t\t\tcontinue\n\t\t\t\t}\n\t\t\t\tvv = vv[:1]\n\t\t\t\tif vv[0] == \"\" {\n\t\t\t\t\tcontinue\n\t\t\t\t}\n\t\t\t} else if asciiEqualFold(k, \"cookie\") {\n\t\t\t\t// Per 8.1.2.5 To allow for better compression efficiency, the\n\t\t\t\t// Cookie header field MAY be split into separate header fields,\n\t\t\t\t// each with one or more cookie-pairs.\n\t\t\t\tfor _, v := range vv {\n\t\t\t\t\tfor {\n\t\t\t\t\t\tp := strings.IndexByte(v, ';')\n\t\t\t\t\t\tif p < 0 {\n\t\t\t\t\t\t\tbreak\n\t\t\t\t\t\t}\n\t\t\t\t\t\tf(\"cookie\", v[:p])\n\t\t\t\t\t\tp++\n\t\t\t\t\t\t// strip space after semicolon if any.\n\t\t\t\t\t\tfor p+1 <= len(v) && v[p] == ' ' {\n\t\t\t\t\t\t\tp++\n\t\t\t\t\t\t}\n\t\t\t\t\t\tv = v[p:]\n\t\t\t\t\t}\n\t\t\t\t\tif len(v) > 0 {\n\t\t\t\t\t\tf(\"cookie\", v)\n\t\t\t\t\t}\n\t\t\t\t}\n\t\t\t\tcontinue\n\t\t\t} else if k == \":protocol\" {\n\t\t\t\t// :protocol pseudo-header was already sent above.\n\t\t\t\tcontinue\n\t\t\t}\n\n\t\t\tfor _, v := range vv {\n\t\t\t\tf(k, v)\n\t\t\t}\n\t\t}\n\t\tif shouldSendReqContentLength(req.Method, req.ActualContentLength) {\n\t\t\tf(\"content-length\", strconv.FormatInt(req.ActualContentLength, 10))\n\t\t}\n\t\tif param.AddGzipHeader {\n\t\t\tf(\"accept-encoding\", \"gzip\")\n\t\t}\n\t\tif !didUA {\n\t\t\tf(\"user-agent\", param.DefaultUserAgent)\n\t\t}\n\t}\n", "\tenumerateHeaders := func(f func(name, value string)) {\n\t\temitPseudoHeadersInUpstreamOrder(req, path, host, isNormalConnect, protocol, f)\n\t\temitRegularHeadersInUpstreamOrder(req, trailers, param, f)\n\t}\n\tif hasCustomHeaderOrder(param.HeaderOrder) {\n\t\tenumerateHeaders = func(f func(name, value string)) {\n\t\t\tenumerateHeadersWithCustomOrder(req, path, host, isNormalConnect, protocol, trailers, param, f)\n\t\t}\n\t}\n", 1)
	text = strings.Replace(text, "\n// IsRequestGzip reports whether we should add an Accept-Encoding: gzip header\n", "\ntype headerFieldEntry struct {\n\tname  string\n\tvalue string\n}\n\nfunc hasCustomHeaderOrder(order *HeaderOrder) bool {\n\treturn order != nil && (len(order.Pseudo) > 0 || len(order.Regular) > 0)\n}\n\nfunc emitPseudoHeadersInUpstreamOrder(req Request, path, host string, isNormalConnect bool, protocol string, f func(name, value string)) {\n\t// 8.1.2.3 Request Pseudo-Header Fields\n\t// The :path pseudo-header field includes the path and query parts of the\n\t// target URI (the path-absolute production and optionally a '?' character\n\t// followed by the query production, see Sections 3.3 and 3.4 of\n\t// [RFC3986]).\n\tf(\":authority\", host)\n\tmethod := req.Method\n\tif method == \"\" {\n\t\tmethod = \"GET\"\n\t}\n\tf(\":method\", method)\n\tif !isNormalConnect {\n\t\tf(\":path\", path)\n\t\tf(\":scheme\", req.URL.Scheme)\n\t}\n\tif protocol != \"\" {\n\t\tf(\":protocol\", protocol)\n\t}\n}\n\nfunc emitRegularHeadersInUpstreamOrder(req Request, trailers string, param EncodeHeadersParam, f func(name, value string)) {\n\tif trailers != \"\" {\n\t\tf(\"trailer\", trailers)\n\t}\n\n\tvar didUA bool\n\tfor k, vv := range req.Header {\n\t\tif asciiEqualFold(k, \"host\") || asciiEqualFold(k, \"content-length\") {\n\t\t\t// Host is :authority, already sent.\n\t\t\t// Content-Length is automatic, set below.\n\t\t\tcontinue\n\t\t} else if asciiEqualFold(k, \"connection\") ||\n\t\t\tasciiEqualFold(k, \"proxy-connection\") ||\n\t\t\tasciiEqualFold(k, \"transfer-encoding\") ||\n\t\t\tasciiEqualFold(k, \"upgrade\") ||\n\t\t\tasciiEqualFold(k, \"keep-alive\") {\n\t\t\t// Per 8.1.2.2 Connection-Specific Header\n\t\t\t// Fields, don't send connection-specific\n\t\t\t// fields. We have already checked if any\n\t\t\t// are error-worthy so just ignore the rest.\n\t\t\tcontinue\n\t\t} else if asciiEqualFold(k, \"user-agent\") {\n\t\t\t// Match Go's http1 behavior: at most one\n\t\t\t// User-Agent. If set to nil or empty string,\n\t\t\t// then omit it. Otherwise if not mentioned,\n\t\t\t// include the default (below).\n\t\t\tdidUA = true\n\t\t\tif len(vv) < 1 {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tvv = vv[:1]\n\t\t\tif vv[0] == \"\" {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t} else if asciiEqualFold(k, \"cookie\") {\n\t\t\t// Per 8.1.2.5 To allow for better compression efficiency, the\n\t\t\t// Cookie header field MAY be split into separate header fields,\n\t\t\t// each with one or more cookie-pairs.\n\t\t\tfor _, v := range vv {\n\t\t\t\tfor {\n\t\t\t\t\tp := strings.IndexByte(v, ';')\n\t\t\t\t\tif p < 0 {\n\t\t\t\t\t\tbreak\n\t\t\t\t\t}\n\t\t\t\t\tf(\"cookie\", v[:p])\n\t\t\t\t\tp++\n\t\t\t\t\t// strip space after semicolon if any.\n\t\t\t\t\tfor p+1 <= len(v) && v[p] == ' ' {\n\t\t\t\t\t\tp++\n\t\t\t\t\t}\n\t\t\t\t\tv = v[p:]\n\t\t\t\t}\n\t\t\t\tif len(v) > 0 {\n\t\t\t\t\tf(\"cookie\", v)\n\t\t\t\t}\n\t\t\t}\n\t\t\tcontinue\n\t\t} else if k == \":protocol\" {\n\t\t\t// :protocol pseudo-header was already sent above.\n\t\t\tcontinue\n\t\t}\n\n\t\tfor _, v := range vv {\n\t\t\tf(k, v)\n\t\t}\n\t}\n\tif shouldSendReqContentLength(req.Method, req.ActualContentLength) {\n\t\tf(\"content-length\", strconv.FormatInt(req.ActualContentLength, 10))\n\t}\n\tif param.AddGzipHeader {\n\t\tf(\"accept-encoding\", \"gzip\")\n\t}\n\tif !didUA {\n\t\tf(\"user-agent\", param.DefaultUserAgent)\n\t}\n}\n\nfunc enumerateHeadersWithCustomOrder(req Request, path, host string, isNormalConnect bool, protocol, trailers string, param EncodeHeadersParam, f func(name, value string)) {\n\tpseudoHeaders := configuredPseudoHeaders(req, path, host, isNormalConnect, protocol)\n\tif len(param.HeaderOrder.Pseudo) > 0 {\n\t\tfor _, field := range orderPseudoHeaders(pseudoHeaders, param.HeaderOrder) {\n\t\t\tf(field.name, field.value)\n\t\t}\n\t} else {\n\t\temitPseudoHeadersInUpstreamOrder(req, path, host, isNormalConnect, protocol, f)\n\t}\n\n\tif len(param.HeaderOrder.Regular) > 0 {\n\t\tfor _, field := range orderRegularHeaders(collectRegularHeaders(req, trailers, param), param.HeaderOrder) {\n\t\t\tf(field.name, field.value)\n\t\t}\n\t} else {\n\t\temitRegularHeadersInUpstreamOrder(req, trailers, param, f)\n\t}\n}\n\nfunc configuredPseudoHeaders(req Request, path, host string, isNormalConnect bool, protocol string) []headerFieldEntry {\n\tpseudoHeaders := []headerFieldEntry{\n\t\t{name: \":authority\", value: host},\n\t}\n\n\tmethod := req.Method\n\tif method == \"\" {\n\t\tmethod = \"GET\"\n\t}\n\tpseudoHeaders = append(pseudoHeaders, headerFieldEntry{name: \":method\", value: method})\n\n\tif !isNormalConnect {\n\t\tpseudoHeaders = append(pseudoHeaders,\n\t\t\theaderFieldEntry{name: \":path\", value: path},\n\t\t\theaderFieldEntry{name: \":scheme\", value: req.URL.Scheme},\n\t\t)\n\t}\n\tif protocol != \"\" {\n\t\tpseudoHeaders = append(pseudoHeaders, headerFieldEntry{name: \":protocol\", value: protocol})\n\t}\n\treturn pseudoHeaders\n}\n\nfunc orderPseudoHeaders(headers []headerFieldEntry, order *HeaderOrder) []headerFieldEntry {\n\tif len(headers) == 0 {\n\t\treturn nil\n\t}\n\tavailable := make(map[string]headerFieldEntry, len(headers))\n\tfor _, field := range headers {\n\t\tavailable[field.name] = field\n\t}\n\n\tvar ordered []headerFieldEntry\n\tseen := make(map[string]struct{}, len(headers))\n\tfor _, name := range order.Pseudo {\n\t\tif field, ok := available[name]; ok {\n\t\t\tordered = append(ordered, field)\n\t\t\tseen[name] = struct{}{}\n\t\t}\n\t}\n\tfor _, name := range []string{\":authority\", \":method\", \":path\", \":scheme\", \":protocol\"} {\n\t\tif _, ok := seen[name]; ok {\n\t\t\tcontinue\n\t\t}\n\t\tif field, ok := available[name]; ok {\n\t\t\tordered = append(ordered, field)\n\t\t}\n\t}\n\treturn ordered\n}\n\nfunc collectRegularHeaders(req Request, trailers string, param EncodeHeadersParam) []headerFieldEntry {\n\tvar didUA bool\n\tgrouped := make(map[string][]string, len(req.Header)+4)\n\n\tappendValue := func(name, value string) {\n\t\tgrouped[name] = append(grouped[name], value)\n\t}\n\n\tif trailers != \"\" {\n\t\tappendValue(\"trailer\", trailers)\n\t}\n\n\tfor k, vv := range req.Header {\n\t\tswitch {\n\t\tcase asciiEqualFold(k, \"host\") || asciiEqualFold(k, \"content-length\"):\n\t\t\tcontinue\n\t\tcase asciiEqualFold(k, \"connection\") ||\n\t\t\tasciiEqualFold(k, \"proxy-connection\") ||\n\t\t\tasciiEqualFold(k, \"transfer-encoding\") ||\n\t\t\tasciiEqualFold(k, \"upgrade\") ||\n\t\t\tasciiEqualFold(k, \"keep-alive\"):\n\t\t\tcontinue\n\t\tcase asciiEqualFold(k, \"user-agent\"):\n\t\t\tdidUA = true\n\t\t\tif len(vv) < 1 {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tif vv[0] == \"\" {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tappendValue(\"user-agent\", vv[0])\n\t\t\tcontinue\n\t\tcase asciiEqualFold(k, \"cookie\"):\n\t\t\tfor _, v := range vv {\n\t\t\t\tappendSplitCookieValues(grouped, v)\n\t\t\t}\n\t\t\tcontinue\n\t\tcase k == \":protocol\":\n\t\t\tcontinue\n\t\t}\n\n\t\tname, ascii := LowerHeader(k)\n\t\tif !ascii {\n\t\t\tcontinue\n\t\t}\n\t\tfor _, v := range vv {\n\t\t\tappendValue(name, v)\n\t\t}\n\t}\n\n\tif shouldSendReqContentLength(req.Method, req.ActualContentLength) {\n\t\tappendValue(\"content-length\", strconv.FormatInt(req.ActualContentLength, 10))\n\t}\n\tif param.AddGzipHeader {\n\t\tappendValue(\"accept-encoding\", \"gzip\")\n\t}\n\tif !didUA {\n\t\tappendValue(\"user-agent\", param.DefaultUserAgent)\n\t}\n\n\tentries := make([]headerFieldEntry, 0, len(grouped))\n\tfor name, values := range grouped {\n\t\tfor _, value := range values {\n\t\t\tentries = append(entries, headerFieldEntry{name: name, value: value})\n\t\t}\n\t}\n\treturn entries\n}\n\nfunc appendSplitCookieValues(grouped map[string][]string, value string) {\n\tfor {\n\t\tp := strings.IndexByte(value, ';')\n\t\tif p < 0 {\n\t\t\tbreak\n\t\t}\n\t\tgrouped[\"cookie\"] = append(grouped[\"cookie\"], value[:p])\n\t\tp++\n\t\tfor p+1 <= len(value) && value[p] == ' ' {\n\t\t\tp++\n\t\t}\n\t\tvalue = value[p:]\n\t}\n\tif len(value) > 0 {\n\t\tgrouped[\"cookie\"] = append(grouped[\"cookie\"], value)\n\t}\n}\n\nfunc orderRegularHeaders(headers []headerFieldEntry, order *HeaderOrder) []headerFieldEntry {\n\tif len(headers) == 0 {\n\t\treturn nil\n\t}\n\n\tgrouped := make(map[string][]string, len(headers))\n\tfor _, field := range headers {\n\t\tgrouped[field.name] = append(grouped[field.name], field.value)\n\t}\n\n\tvar ordered []headerFieldEntry\n\temit := func(name string) {\n\t\tvalues, ok := grouped[name]\n\t\tif !ok {\n\t\t\treturn\n\t\t}\n\t\tfor _, value := range values {\n\t\t\tordered = append(ordered, headerFieldEntry{name: name, value: value})\n\t\t}\n\t\tdelete(grouped, name)\n\t}\n\n\tfor _, rawName := range order.Regular {\n\t\tname, ascii := LowerHeader(rawName)\n\t\tif !ascii {\n\t\t\tcontinue\n\t\t}\n\t\temit(name)\n\t}\n\n\tvar tailNames []string\n\tfor name := range grouped {\n\t\ttailNames = append(tailNames, name)\n\t}\n\tsort.Strings(tailNames)\n\tfor _, name := range tailNames {\n\t\temit(name)\n\t}\n\treturn ordered\n}\n\n// IsRequestGzip reports whether we should add an Accept-Encoding: gzip header\n", 1)
	text = strings.Replace(text,
		"\tfor _, name := range order.Pseudo {\n\t\tif field, ok := available[name]; ok {",
		"\tfor _, name := range order.Pseudo {\n\t\tif _, ok := seen[name]; ok {\n\t\t\tcontinue\n\t\t}\n\t\tif field, ok := available[name]; ok {",
		1,
	)
	return text
}

func readTextFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func actualClosureFiles() []string {
	var files []string
	for _, dir := range copiedPackageDirs {
		for _, name := range collectGoFiles(nilSafeTestingT{}, dir) {
			files = append(files, filepath.ToSlash(filepath.Join(dir, name)))
		}
	}
	sort.Strings(files)
	return files
}

type nilSafeTestingT struct{}

func (nilSafeTestingT) Helper() {}
func (nilSafeTestingT) Fatalf(format string, args ...any) {
	panic(fmt.Sprintf(format, args...))
}

type testingTB interface {
	Helper()
	Fatalf(format string, args ...any)
}

func copiedPackageDocEntries() []string {
	return []string{
		"http2",
		"http2/hpack",
		"http/httpguts",
		"internal/httpcommon",
		"internal/httpsfv",
	}
}

func copiedUpstreamPackagePaths() []string {
	return []string{
		"golang.org/x/net/http2",
		"golang.org/x/net/http2/hpack",
		"golang.org/x/net/http/httpguts",
		"golang.org/x/net/internal/httpcommon",
		"golang.org/x/net/internal/httpsfv",
	}
}

func actualExternalXNetDeps(t *testing.T) []string {
	t.Helper()
	seen := map[string]struct{}{}
	for _, localPkg := range localCopiedPackageImportPaths() {
		for _, dep := range goListDeps(t, localPkg) {
			if !strings.HasPrefix(dep, "golang.org/x/net/") {
				continue
			}
			if isForkedUpstreamPackage(dep) {
				continue
			}
			seen[dep] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func localCopiedPackageImportPaths() []string {
	var paths []string
	for _, dir := range copiedPackageDirs {
		paths = append(paths, filepath.ToSlash(localImportPrefix+"/"+filepath.ToSlash(dir)))
	}
	return paths
}

func isForkedUpstreamPackage(dep string) bool {
	for _, pkg := range copiedUpstreamPackagePaths() {
		if dep == pkg {
			return true
		}
	}
	return false
}

func goListDeps(t *testing.T, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", pkg)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	lines := strings.Fields(string(out))
	sort.Strings(lines)
	return lines
}

func omittedHTTP2Subpackages(t *testing.T, upstreamRoot string) []string {
	t.Helper()
	http2Root := filepath.Join(upstreamRoot, "http2")
	entries, err := os.ReadDir(http2Root)
	if err != nil {
		t.Fatalf("read dir %s: %v", http2Root, err)
	}
	localRoot := filepath.Join(".", "http2")
	var omitted []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		upDir := filepath.Join(http2Root, name)
		if len(collectGoFiles(t, upDir)) == 0 {
			continue
		}
		if _, err := os.Stat(filepath.Join(localRoot, name)); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat local subdir http2/%s: %v", name, err)
		}
		omitted = append(omitted, filepath.ToSlash(filepath.Join("http2", name)))
	}
	sort.Strings(omitted)
	return omitted
}

func prefixEach(prefix string, items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, prefix+item)
	}
	return out
}

func markdownBacktickBulletsBetween(t *testing.T, body, start, end string) []string {
	t.Helper()
	section := sliceBetween(t, body, start, end)
	return extractBullets(t, section, `(?m)^- `+"`"+`([^`+"`"+`]+)`+"`"+`$`)
}

func indentedBacktickBulletsBetween(t *testing.T, body, start, end string) []string {
	t.Helper()
	section := sliceBetween(t, body, start, end)
	return extractBullets(t, section, `(?m)^   - `+"`"+`([^`+"`"+`]+)`+"`"+`$`)
}

func indentedBulletsBetween(t *testing.T, body, start, end string) []string {
	t.Helper()
	section := sliceBetween(t, body, start, end)
	return extractBullets(t, section, `(?m)^   - (.+)$`)
}

func sliceBetween(t *testing.T, body, start, end string) string {
	t.Helper()
	startIdx := strings.Index(body, start)
	if startIdx < 0 {
		t.Fatalf("missing section start %q", start)
	}
	section := body[startIdx+len(start):]
	if end != "" {
		endIdx := strings.Index(section, end)
		if endIdx < 0 {
			t.Fatalf("missing section end %q after %q", end, start)
		}
		section = section[:endIdx]
	}
	return section
}

func extractBullets(t *testing.T, body, pattern string) []string {
	t.Helper()
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		t.Fatalf("no bullets matched pattern %q", pattern)
	}
	var items []string
	for _, match := range matches {
		items = append(items, strings.TrimSpace(match[1]))
	}
	return items
}

func assertContainsAll(t *testing.T, name, body string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Fatalf("%s missing required text %q", name, want)
		}
	}
}

func assertListEqual(t *testing.T, name string, want, got []string) {
	t.Helper()
	if diff := diffLists(want, got); diff != "" {
		t.Fatalf("%s mismatch (-want +got):\n%s", name, diff)
	}
}

func normalizeMarkdownCodeLiterals(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, strings.ReplaceAll(item, "`", ""))
	}
	return out
}

func diffLists(want, got []string) string {
	var b strings.Builder
	wantSet := make(map[string]struct{}, len(want))
	gotSet := make(map[string]struct{}, len(got))
	for _, item := range want {
		wantSet[item] = struct{}{}
	}
	for _, item := range got {
		gotSet[item] = struct{}{}
	}
	for _, item := range want {
		if _, ok := gotSet[item]; !ok {
			fmt.Fprintf(&b, "- %s\n", item)
		}
	}
	for _, item := range got {
		if _, ok := wantSet[item]; !ok {
			fmt.Fprintf(&b, "+ %s\n", item)
		}
	}
	return strings.TrimSpace(b.String())
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
