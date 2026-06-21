#!/usr/bin/env python3
"""Build Sub2API with embedded frontend and deploy it to a VPS over SSH.

Default target matches the LA (高配置服务器) PM2 deployment:

    python tools/deploy_vps.py

Override target when needed:

    python tools/deploy_vps.py --host root@example.com --port 22
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tempfile
from typing import Iterable


REPO_ROOT = Path(__file__).resolve().parents[1]
FRONTEND_DIR = REPO_ROOT / "frontend"
BACKEND_DIR = REPO_ROOT / "backend"
FRONTEND_DIST = BACKEND_DIR / "internal" / "web" / "dist"
VERSION_FILE = BACKEND_DIR / "cmd" / "server" / "VERSION"


def info(message: str) -> None:
    print(f"[deploy] {message}", flush=True)


def fail(message: str, code: int = 1) -> None:
    print(f"[deploy] ERROR: {message}", file=sys.stderr, flush=True)
    raise SystemExit(code)


def which(name: str) -> str:
    path = shutil.which(name)
    if not path:
        fail(f"command not found: {name}")
    return path


def run(
    cmd: list[str],
    *,
    cwd: Path | None = None,
    env: dict[str, str] | None = None,
    input_text: str | None = None,
    capture: bool = False,
) -> subprocess.CompletedProcess[str]:
    display_cwd = f" (cwd={cwd})" if cwd else ""
    info("$ " + " ".join(shlex.quote(part) for part in cmd) + display_cwd)
    input_bytes = None
    if input_text is not None:
        input_bytes = input_text.replace('\r\n', '\n').encode('utf-8')
    result = subprocess.run(
        cmd,
        cwd=str(cwd) if cwd else None,
        env=env,
        input=input_bytes,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if capture else None,
        check=True,
    )
    if capture:
        stdout = result.stdout.decode('utf-8', errors='replace') if result.stdout else ''
        return subprocess.CompletedProcess(cmd, result.returncode, stdout, None)
    return subprocess.CompletedProcess(cmd, result.returncode, None, None)


def output(cmd: list[str], *, cwd: Path | None = None) -> str:
    result = run(cmd, cwd=cwd, capture=True)
    return (result.stdout or "").strip()


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def git_value(args: list[str], default: str) -> str:
    try:
        value = output([which("git"), *args], cwd=REPO_ROOT)
        return value or default
    except Exception:
        return default


def commit_label() -> str:
    short = git_value(["rev-parse", "--short", "HEAD"], "unknown")
    dirty = git_value(["status", "--porcelain", "--untracked-files=no"], "")
    if dirty and short != "unknown":
        return f"{short}-dirty"
    return short


def build_frontend(skip_install: bool) -> None:
    pnpm = which("pnpm")
    if not (FRONTEND_DIR / "node_modules").exists() and not skip_install:
        run([pnpm, "install", "--frozen-lockfile"], cwd=FRONTEND_DIR)
    run([pnpm, "build"], cwd=FRONTEND_DIR)
    index_html = FRONTEND_DIST / "index.html"
    if not index_html.exists():
        fail(f"frontend build did not create {index_html}")
    assets_dir = FRONTEND_DIST / "assets"
    if not assets_dir.exists() or not any(assets_dir.iterdir()):
        fail(f"frontend build did not create assets in {assets_dir}")


def build_backend(version: str | None, label: str) -> Path:
    go = which("go")
    version = (version or VERSION_FILE.read_text(encoding="utf-8").strip() or "0.0.0-dev")
    build_date = dt.datetime.now(dt.UTC).strftime("%Y-%m-%dT%H:%M:%SZ")

    out_dir = Path(tempfile.gettempdir()) / "sub2api-deploy"
    out_dir.mkdir(parents=True, exist_ok=True)
    out_path = out_dir / f"sub2api-linux-amd64-{label}"

    env = os.environ.copy()
    env.update({"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"})
    ldflags = (
        f"-s -w -X main.Version={version} -X main.Commit={label} "
        f"-X main.Date={build_date} -X main.BuildType=release"
    )
    run(
        [
            go,
            "build",
            "-tags",
            "embed",
            "-ldflags",
            ldflags,
            "-trimpath",
            "-o",
            str(out_path),
            "./cmd/server",
        ],
        cwd=BACKEND_DIR,
        env=env,
    )
    if not out_path.exists():
        fail(f"go build did not create {out_path}")
    return out_path


def ssh_base_args(args: argparse.Namespace) -> list[str]:
    out: list[str] = []
    if args.port:
        out.extend(["-p", str(args.port)])
    if args.identity_file:
        out.extend(["-i", str(args.identity_file)])
    for option in args.ssh_option or []:
        out.extend(["-o", option])
    return out


def scp_base_args(args: argparse.Namespace) -> list[str]:
    out: list[str] = []
    if args.port:
        out.extend(["-P", str(args.port)])
    if args.identity_file:
        out.extend(["-i", str(args.identity_file)])
    for option in args.ssh_option or []:
        out.extend(["-o", option])
    return out


def remote_exec(args: argparse.Namespace, script: str, *, sudo: bool = False, capture: bool = False) -> str:
    ssh = args.ssh_bin or which("ssh")
    remote_cmd = ["sudo", "bash", "-s"] if sudo else ["bash", "-s"]
    result = run(
        [ssh, *ssh_base_args(args), args.host, *remote_cmd],
        input_text=script,
        capture=capture,
    )
    return (result.stdout or "").strip()


def detect_remote_uid(args: argparse.Namespace) -> str:
    ssh = args.ssh_bin or which("ssh")
    try:
        return output([ssh, *ssh_base_args(args), args.host, "id", "-u"]).strip()
    except Exception as exc:
        fail(f"cannot connect to remote host {args.host!r}: {exc}")


def upload_file(args: argparse.Namespace, local_path: Path, remote_path: str) -> None:
    scp = args.scp_bin or which("scp")
    run([scp, *scp_base_args(args), str(local_path), f"{args.host}:{remote_path}"])


def q(value: str | Path) -> str:
    return shlex.quote(str(value))


def deploy(args: argparse.Namespace, binary_path: Path, digest: str, label: str) -> None:
    remote_stage_dir = f"/tmp/sub2api-deploy-{label}"
    remote_stage_path = f"{remote_stage_dir}/{binary_path.name}"

    remote_exec(
        args,
        f"set -euo pipefail\nmkdir -p {q(remote_stage_dir)}\nrm -f {q(remote_stage_path)}\n",
    )
    upload_file(args, binary_path, remote_stage_path)

    uid = detect_remote_uid(args)
    use_sudo = (uid != "0") and not args.no_sudo
    if use_sudo:
        info("remote user is not root; deployment will use sudo")

    if args.restart_method == "pm2":
        restart_stop = 'pm2 stop "$service_name" 2>&1 || true'
        restart_start = 'pm2 start "$service_name" 2>&1'
        restart_check = 'pm2 status "$service_name" 2>&1 | grep -E "online|stopped|errored" | tail -1'
        log_cmd = 'pm2 logs "$service_name" --nostream --lines 80 2>&1 || true'
    else:
        restart_stop = ""
        restart_start = 'systemctl restart "$service_name"'
        restart_check = 'systemctl is-active "$service_name"'
        log_cmd = 'journalctl -u "$service_name" --since "3 minutes ago" --no-pager -n 80 || true'

    remote_script = f"""set -euo pipefail
candidate_tmp={q(remote_stage_path)}
app_dir={q(args.remote_app_dir)}
service_name={q(args.service)}
health_url={q(args.health_url)}
expected_sha={q(digest)}

target="$app_dir/sub2api"
deploy_tmp="$app_dir/.deploy-tmp"
mkdir -p "$deploy_tmp"

candidate="$deploy_tmp/$(basename "$candidate_tmp")"
install -m 755 "$candidate_tmp" "$candidate"

actual_sha="$(sha256sum "$candidate" | awk '{{print $1}}')"
printf 'candidate_sha=%s\\n' "$actual_sha"
if [ "$actual_sha" != "$expected_sha" ]; then
  printf 'sha256 mismatch: expected=%s actual=%s\\n' "$expected_sha" "$actual_sha" >&2
  exit 1
fi

"$candidate" --version 2>&1 || true

old_commit="unknown"
if [ -x "$target" ]; then
  old_commit="$("$target" --version 2>&1 | sed -n 's/.*commit: \\([^,)]*\\).*/\\1/p' | head -n1 || true)"
  old_commit="${{old_commit:-unknown}}"
fi

backup="$deploy_tmp/sub2api.backup.$(date -u +%Y%m%dT%H%M%SZ).pre-$old_commit"
if [ -e "$target" ]; then
  cp -a "$target" "$backup"
else
  backup=""
fi

owner="sub2api:sub2api"
if [ -e "$target" ]; then
  owner="$(stat -c '%U:%G' "$target" 2>/dev/null || printf 'sub2api:sub2api')"
fi
owner_user="${{owner%%:*}}"
owner_group="${{owner#*:}}"
if ! id -u "$owner_user" >/dev/null 2>&1 || ! getent group "$owner_group" >/dev/null 2>&1; then
  owner_user=root
  owner_group=root
fi

# Stop service before replacing binary to avoid "Text file busy"
{restart_stop}
install -o "$owner_user" -g "$owner_group" -m 755 "$candidate" "$target"
{restart_start}
sleep 2
{restart_check}
"$target" --version 2>&1 || true

http_code="$(curl -sS -o /tmp/sub2api-health.html -w '%{{http_code}}' "$health_url" || true)"
printf 'health_http=%s\\n' "$http_code"
if [ "$http_code" != "200" ]; then
  printf 'health check failed: %s\\n' "$http_code" >&2
  {log_cmd}
  exit 1
fi

if [ -n "$backup" ]; then
  printf 'backup=%s\\n' "$backup"
fi
rm -rf "$(dirname "$candidate_tmp")"
"""
    remote_exec(args, remote_script, sudo=use_sudo)


def parse_args(argv: Iterable[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Build frontend, embed it into the Linux backend binary, upload to VPS, restart service.",
    )
    parser.add_argument("--host", default=os.getenv("SUB2API_DEPLOY_HOST", "bald@107.175.76.239"), help="SSH host")
    parser.add_argument("--port", type=int, default=int(os.getenv("SUB2API_DEPLOY_PORT", "2222")), help="SSH port")
    parser.add_argument("--identity-file", default=os.getenv("SUB2API_DEPLOY_IDENTITY_FILE", str(Path.home() / ".ssh" / "id_rsa")), help="SSH private key")
    parser.add_argument("--ssh-option", action="append", help="Extra ssh/scp -o option, can be repeated")
    parser.add_argument("--ssh-bin", default=os.getenv("SUB2API_SSH_BIN"), help="Path to ssh executable")
    parser.add_argument("--scp-bin", default=os.getenv("SUB2API_SCP_BIN"), help="Path to scp executable")
    parser.add_argument("--remote-app-dir", default=os.getenv("SUB2API_REMOTE_APP_DIR", "/home/bald/sub2api-pool-bald/app"))
    parser.add_argument("--service", default=os.getenv("SUB2API_SERVICE", "sub2api-pool-bald-app"))
    parser.add_argument("--health-url", default=os.getenv("SUB2API_HEALTH_URL", "http://127.0.0.1:18082/health"))
    parser.add_argument("--restart-method", default=os.getenv("SUB2API_RESTART_METHOD", "pm2"), choices=["pm2", "systemd"], help="Restart method: pm2 or systemd")
    parser.add_argument("--version", help="Override version injected into the binary")
    parser.add_argument("--skip-frontend-build", action="store_true", help="Do not run pnpm build")
    parser.add_argument("--no-install", action="store_true", help="Do not auto-run pnpm install when node_modules is missing")
    parser.add_argument("--build-only", action="store_true", help="Only build the Linux binary; do not upload or restart")
    parser.add_argument("--no-sudo", action="store_true", default=True, help="Do not use sudo for remote install/restart (default)")
    parser.add_argument("--use-sudo", action="store_true", help="Use sudo for remote install/restart")
    return parser.parse_args(list(argv))


def main(argv: Iterable[str] = sys.argv[1:]) -> int:
    args = parse_args(argv)
    if args.use_sudo:
        args.no_sudo = False

    if not args.skip_frontend_build:
        build_frontend(skip_install=args.no_install)
    else:
        info("skipping frontend build by request")
        if not (FRONTEND_DIST / "index.html").exists():
            fail(f"embedded frontend dist is missing: {FRONTEND_DIST}")

    label = commit_label()
    binary_path = build_backend(args.version, label)
    digest = sha256_file(binary_path)
    info(f"built {binary_path}")
    info(f"sha256 {digest}")

    if args.build_only:
        info("build-only mode; deployment skipped")
        return 0

    deploy(args, binary_path, digest, label)
    info("deployment completed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
