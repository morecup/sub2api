# Sub2API repository instructions

## Production build invariant

- Production uses Go 1.27. Every server binary that can route Codex or Grok traffic MUST be built with the `http2legacy` build tag. Go 1.27's default `x/net/http2` wrapper cannot honor the ordered HTTP/2 headers and HPACK path required by these fingerprints; a binary built with only `-tags embed` will fail requests before they reach the upstream.
- The release build with the embedded frontend is:

  ```bash
  cd backend
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -tags "embed,http2legacy" \
    -trimpath \
    -o sub2api \
    ./cmd/server
  ```

- For a backend-only local build, use `make -C backend build`; that target also includes `http2legacy`.
- Do not remove `http2legacy` from `tools/deploy_vps.py`, Dockerfiles, Makefiles, or GoReleaser configs unless the Go 1.27 wrapper path has gained a tested request-header ordering seam and the Grok wire-parity tests have been updated.

## Required verification for transport/build changes

Run these from the repository root before a production update:

```bash
bash deploy/tests/grok-http2-build-tags-test.sh
cd backend
go test ./internal/pkg/grokhttp2/... -count=1
go test -tags=http2legacy ./internal/pkg/grokhttp2/... -count=1
go test -tags=unit ./internal/repository -count=1
go test -tags="unit http2legacy" ./internal/repository -count=1
go build -tags="embed http2legacy" -trimpath -o /tmp/sub2api-release-check ./cmd/server
```

The default Go 1.27 tests and the `http2legacy` tests are both required: they protect the wrapper behavior and the production fork behavior separately.

## Production update procedure

- The current production target is SSH server `高配置服务器` (`bald@107.175.76.239:2222`). The PM2 application is `sub2api-pool-bald-app`, the application directory is `/home/bald/sub2api-pool-bald/app`, and the local health endpoint is `http://127.0.0.1:18082/health`.
- Use the repository deployment script from the repository root:

  ```bash
  python tools/deploy_vps.py --build-only  # optional preflight
  python tools/deploy_vps.py               # build frontend, build Linux binary, upload, back up, restart, health-check
  ```

- `tools/deploy_vps.py` is the canonical production path. Do not replace it with a bare `go build -tags embed`, an ad-hoc binary copy, or a PM2 restart of an old binary.
- The deployment script builds the current local repository working tree. Before running it, inspect `git status --short` and ensure the listed changes are the intended production contents. Never stash or discard another person's changes.
- Before replacement, record the current PM2 PID/restart count, binary SHA-256, configuration SHA-256, and health result. Preserve the script-created binary/pricing backups for rollback.
- After replacement, verify all of the following:
  - `pm2` reports `sub2api-pool-bald-app` as `online` with a new PID and only one expected restart.
  - The running binary SHA-256 matches the uploaded artifact.
  - `strings /home/bald/sub2api-pool-bald/app/sub2api | grep -- '-tags='` contains `embed,http2legacy`.
  - Both the local health endpoint and the external application endpoint return HTTP 200.
  - New logs contain no `ordered HTTP/2 fingerprint unsupported`, panic, or fatal errors.
- A passing health check proves process availability only. When the change targets an upstream protocol, separately verify the relevant account/request path when credentials and authorization are available.

## Rollback

- If restart, hash, health, or post-deploy checks fail, restore the most recent `sub2api.backup.*` from `/home/bald/sub2api-pool-bald/app/.deploy-tmp/`, restart only `sub2api-pool-bald-app`, and repeat the health/hash checks.
- Do not modify production configuration, account state, databases, Redis, or other PM2 services as part of a binary-only update unless the task explicitly requires it.
