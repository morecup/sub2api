#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

fail() {
  printf 'Grok HTTP/2 build-tag test failed: %s\n' "$1" >&2
  exit 1
}

assert_contains() {
  file=$1
  value=$2
  grep -Fq -- "$value" "$file" || fail "$file is missing required build tags: $value"
}

# Go 1.27's default x/net wrapper does not expose the request-encoding seam
# used by the Grok header-order and HPACK fingerprint. Every distributable or
# documented server build must therefore select the repository's legacy fork.
assert_contains Dockerfile '-tags "embed http2legacy"'
assert_contains deploy/Dockerfile '-tags "embed http2legacy"'
assert_contains backend/Dockerfile 'go build -tags http2legacy'
assert_contains backend/Makefile 'go build -tags http2legacy'
assert_contains deploy/Makefile 'go build -tags http2legacy'
assert_contains deploy/Makefile 'go build -tags "embed http2legacy"'
assert_contains .goreleaser.yaml '-tags=embed,http2legacy'
assert_contains .goreleaser.simple.yaml '-tags=embed,http2legacy'
assert_contains README.md '-tags "embed http2legacy"'
assert_contains README.md 'go run -tags http2legacy ./cmd/server'
assert_contains README_CN.md '-tags "embed http2legacy"'
assert_contains README_CN.md 'go run -tags http2legacy ./cmd/server'
assert_contains README_JA.md '-tags "embed http2legacy"'
assert_contains README_JA.md 'go run -tags http2legacy ./cmd/server'
assert_contains DEV_GUIDE.md 'go run -tags http2legacy ./cmd/server/'

printf 'Grok HTTP/2 build-tag test passed\n'
