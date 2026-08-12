"""Persist every HTTP(S) request received by the dedicated Claude Code capture proxy.

This recorder intentionally performs no redaction. The log contains reusable auth
credentials, cookies, URLs, headers, prompts, code, tool definitions, and tool output.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
from datetime import datetime
from pathlib import Path
from typing import Any

from mitmproxy import http


TOOL_ROOT = Path(__file__).resolve().parent
RECORD_ROOT = TOOL_ROOT / "records"
RECORD_ROOT.mkdir(parents=True, exist_ok=True)


def _now() -> datetime:
    return datetime.now().astimezone()


def _write_json(path: Path, value: Any) -> None:
    path.write_text(
        json.dumps(value, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


def _slug(value: str, fallback: str) -> str:
    normalized = re.sub(r"[^A-Za-z0-9._-]+", "_", value).strip("_")[:72]
    return normalized or fallback


def _flow_dir(flow: http.HTTPFlow) -> Path:
    stored = flow.metadata.get("claude_recorder_dir")
    if stored:
        return Path(stored)

    now = _now()
    request = flow.request
    directory = (
        RECORD_ROOT
        / now.strftime("%Y-%m-%d")
        / (
            f"{now.strftime('%H%M%S-%f')}_"
            f"{_slug(flow.id, 'flow')}_"
            f"{_slug(request.method, 'METHOD')}_"
            f"{_slug(request.host, 'host')}_"
            f"{_slug(request.path.split('?', 1)[0], 'root')}"
        )
    )
    directory.mkdir(parents=True, exist_ok=False)
    flow.metadata["claude_recorder_dir"] = str(directory)
    return directory


def _headers(headers: Any) -> list[list[str]]:
    return [[name, value] for name, value in headers.items(multi=True)]


def _append_index(day_dir: Path, value: dict[str, Any]) -> None:
    day_dir.mkdir(parents=True, exist_ok=True)
    with (day_dir / "index.jsonl").open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(value, ensure_ascii=False) + "\n")


def request(flow: http.HTTPFlow) -> None:
    captured_at = _now()
    request = flow.request
    directory = _flow_dir(flow)
    raw_body = request.raw_content or b""

    if raw_body:
        (directory / "request-body.raw.bin").write_bytes(raw_body)
        content_type = request.headers.get("content-type", "")
        if re.search(r"json|text|xml|javascript|x-www-form-urlencoded|graphql", content_type, re.I):
            (directory / "request-body.txt").write_bytes(raw_body)

    metadata = {
        "schema_version": 1,
        "flow_id": flow.id,
        "captured_at": captured_at.isoformat(),
        "client_address": list(flow.client_conn.peername) if flow.client_conn.peername else None,
        "method": request.method,
        "url": request.pretty_url,
        "scheme": request.scheme,
        "host": request.host,
        "port": request.port,
        "path": request.path,
        "http_version": request.http_version,
        "headers": _headers(request.headers),
        "body": {
            "bytes": len(raw_body),
            "sha256": hashlib.sha256(raw_body).hexdigest(),
            "raw_file": "request-body.raw.bin" if raw_body else None,
            "text_file": "request-body.txt" if raw_body and (directory / "request-body.txt").exists() else None,
        },
        "redacted": False,
        "warning": "Contains raw authentication credentials and private request content.",
    }
    _write_json(directory / "request.json", metadata)
    _append_index(
        directory.parent,
        {
            "event": "request",
            "at": captured_at.isoformat(),
            "flow_id": flow.id,
            "method": request.method,
            "url": request.pretty_url,
            "bytes": len(raw_body),
            "directory": directory.name,
        },
    )


def responseheaders(flow: http.HTTPFlow) -> None:
    if not flow.response:
        return
    # Preserve Claude's SSE UX while leaving request bodies buffered and recordable.
    flow.response.stream = True
    directory = _flow_dir(flow)
    response = flow.response
    _write_json(
        directory / "response.json",
        {
            "captured_at": _now().isoformat(),
            "status_code": response.status_code,
            "reason": response.reason,
            "http_version": response.http_version,
            "headers": _headers(response.headers),
            "body_recorded": False,
            "note": "Response body is streamed through without buffering; request recording is complete.",
        },
    )


def error(flow: http.HTTPFlow) -> None:
    directory = _flow_dir(flow)
    _write_json(
        directory / "error.json",
        {
            "captured_at": _now().isoformat(),
            "message": str(flow.error) if flow.error else "unknown proxy error",
        },
    )


def websocket_message(flow: http.HTTPFlow) -> None:
    if not flow.websocket or not flow.websocket.messages:
        return
    message = flow.websocket.messages[-1]
    # A client-originated WebSocket message is an outbound Claude request payload.
    if not message.from_client:
        return
    directory = _flow_dir(flow)
    index = sum(1 for _ in directory.glob("websocket-client-*.bin")) + 1
    payload = message.content
    filename = f"websocket-client-{index:05d}.bin"
    (directory / filename).write_bytes(payload)
    _append_index(
        directory.parent,
        {
            "event": "websocket_client_message",
            "at": _now().isoformat(),
            "flow_id": flow.id,
            "bytes": len(payload),
            "file": str(Path(directory.name) / filename),
        },
    )
