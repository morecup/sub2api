#!/usr/bin/env python3
"""Capture official Grok CONTINUATION and auth HPACK branch evidence safely."""

from __future__ import annotations

import argparse
import io
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from typing import Any
import urllib.request

from h2.config import H2Configuration
from h2.connection import H2Connection
from h2.events import RequestReceived, StreamEnded

import capture


LONG_PATH_CHARS = 14_000
DEFAULT_MAX_FRAME_SIZE = 16_384
SYNTHETIC_KEY_PREFIX = "xai-synthetic-hpack-"
PUBLIC_ARCHIVE_MAX_BYTES = 96 * 1024 * 1024


def h2_response(status: int, body: bytes) -> tuple[list[tuple[bytes, bytes]], bytes]:
    headers = [
        (b":status", str(status).encode("ascii")),
        (b"content-type", b"application/json"),
        (b"content-length", str(len(body)).encode("ascii")),
    ]
    return headers, body


class LocalOnlyConnectProxy:
    """CONNECT tunnel that refuses every destination except one local server."""

    def __init__(self, target_port: int) -> None:
        self.target_port = target_port
        self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen(32)
        self.listener.settimeout(0.25)
        self.port = self.listener.getsockname()[1]
        self.blocked_count = 0
        self.accepted_count = 0
        self.errors: list[str] = []
        self._stop = threading.Event()
        self._lock = threading.Lock()
        self._threads: list[threading.Thread] = []
        self._sockets: set[socket.socket] = set()
        self._accept_thread: threading.Thread | None = None

    def start(self) -> None:
        self._accept_thread = threading.Thread(target=self._accept_loop, daemon=True)
        self._accept_thread.start()

    def stop(self) -> None:
        self._stop.set()
        try:
            self.listener.close()
        except OSError:
            pass
        with self._lock:
            sockets = list(self._sockets)
        for item in sockets:
            try:
                item.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                item.close()
            except OSError:
                pass
        if self._accept_thread is not None:
            self._accept_thread.join(timeout=3)
        for thread in list(self._threads):
            thread.join(timeout=3)

    def _accept_loop(self) -> None:
        while not self._stop.is_set():
            try:
                client, _ = self.listener.accept()
            except TimeoutError:
                continue
            except OSError:
                break
            thread = threading.Thread(target=self._handle, args=(client,), daemon=True)
            self._threads.append(thread)
            thread.start()

    def _handle(self, client: socket.socket) -> None:
        upstream: socket.socket | None = None
        try:
            hostname, port = capture.read_connect_request(client)
            allowed_host = hostname.lower().strip("[]") in {"localhost", "127.0.0.1", "::1"}
            if not allowed_host or port != self.target_port:
                with self._lock:
                    self.blocked_count += 1
                client.sendall(b"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
                return
            upstream = socket.create_connection(("127.0.0.1", self.target_port), timeout=10)
            client.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            with self._lock:
                self.accepted_count += 1
                self._sockets.update((client, upstream))
            reverse = threading.Thread(target=self._relay, args=(upstream, client), daemon=True)
            reverse.start()
            self._relay(client, upstream)
            reverse.join(timeout=3)
        except Exception as error:  # noqa: BLE001 - only the type is retained
            if not self._stop.is_set():
                with self._lock:
                    self.errors.append(type(error).__name__)
        finally:
            for item in (client, upstream):
                if item is None:
                    continue
                with self._lock:
                    self._sockets.discard(item)
                try:
                    item.close()
                except OSError:
                    pass

    def _relay(self, source: socket.socket, destination: socket.socket) -> None:
        try:
            while not self._stop.is_set():
                data = source.recv(64 * 1024)
                if not data:
                    return
                destination.sendall(data)
        except OSError:
            return
        finally:
            try:
                destination.shutdown(socket.SHUT_WR)
            except OSError:
                pass


class LocalH2CaptureServer:
    def __init__(self, directory: Path, synthetic_key: str) -> None:
        self.synthetic_key = synthetic_key.encode("ascii")
        self.authority = capture.CertificateAuthority(directory, "sub2api local HPACK evidence")
        self.context = self.authority.server_context("localhost", "h2")
        self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen(16)
        self.listener.settimeout(0.25)
        self.port = self.listener.getsockname()[1]
        self.connections: list[capture.ConnectionCapture] = []
        self.auth_matches: list[bool] = []
        self.target_seen = threading.Event()
        self.errors: list[str] = []
        self._stop = threading.Event()
        self._lock = threading.Lock()
        self._threads: list[threading.Thread] = []
        self._sockets: set[socket.socket] = set()
        self._accept_thread: threading.Thread | None = None

    def start(self) -> None:
        self._accept_thread = threading.Thread(target=self._accept_loop, daemon=True)
        self._accept_thread.start()

    def stop(self) -> None:
        self._stop.set()
        try:
            self.listener.close()
        except OSError:
            pass
        with self._lock:
            sockets = list(self._sockets)
        for item in sockets:
            try:
                item.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                item.close()
            except OSError:
                pass
        if self._accept_thread is not None:
            self._accept_thread.join(timeout=3)
        for thread in list(self._threads):
            thread.join(timeout=3)

    def _accept_loop(self) -> None:
        while not self._stop.is_set():
            try:
                raw, _ = self.listener.accept()
            except TimeoutError:
                continue
            except OSError:
                break
            thread = threading.Thread(target=self._handle, args=(raw,), daemon=True)
            self._threads.append(thread)
            thread.start()

    def _handle(self, raw: socket.socket) -> None:
        tls: ssl.SSLSocket | None = None
        connection_capture: capture.ConnectionCapture | None = None
        try:
            tls = self.context.wrap_socket(raw, server_side=True)
            if tls.selected_alpn_protocol() != "h2":
                raise capture.CaptureError("local evidence client did not negotiate h2")
            connection_capture = capture.ConnectionCapture(
                f"local-evidence-{id(tls)}-{time.monotonic_ns()}",
                capture.sha256_hex(self.authority.cert.public_bytes(capture.serialization.Encoding.DER)),
                "h2",
            )
            with self._lock:
                self.connections.append(connection_capture)
                self._sockets.add(tls)

            protocol = H2Connection(config=H2Configuration(client_side=False, header_encoding=None))
            protocol.initiate_connection()
            self._send_protocol_bytes(tls, protocol, connection_capture)
            requests: dict[int, list[tuple[bytes, bytes]]] = {}

            while not self._stop.is_set():
                data = tls.recv(64 * 1024)
                if not data:
                    break
                connection_capture.feed_client(data)
                for event in protocol.receive_data(data):
                    if isinstance(event, RequestReceived):
                        requests[event.stream_id] = [
                            (bytes(name), bytes(value)) for name, value in event.headers
                        ]
                    elif isinstance(event, StreamEnded):
                        headers = requests.get(event.stream_id, [])
                        self._respond(protocol, event.stream_id, headers)
                self._send_protocol_bytes(tls, protocol, connection_capture)
        except Exception as error:  # noqa: BLE001 - only the type is retained
            if not self._stop.is_set():
                with self._lock:
                    self.errors.append(type(error).__name__)
        finally:
            if connection_capture is not None:
                connection_capture.mark_closed()
            for item in (tls, raw):
                if item is None:
                    continue
                with self._lock:
                    self._sockets.discard(item)
                try:
                    item.close()
                except OSError:
                    pass

    def _respond(
        self,
        protocol: H2Connection,
        stream_id: int,
        headers: list[tuple[bytes, bytes]],
    ) -> None:
        values = {name.lower(): value for name, value in headers}
        path = values.get(b":path", b"")
        if path.endswith(b"/settings"):
            response = h2_response(200, b'{"auto_compact_threshold_percent":80}')
        elif path.endswith(b"/models"):
            response = h2_response(
                200,
                b'{"object":"list","data":[{"id":"grok-4.5","model":"grok-4.5",'
                b'"contextWindow":500000,"apiBackend":"responses",'
                b'"compactionAtTokens":true,"compactionsRemaining":true}]}',
            )
        elif path.endswith(b"/responses"):
            auth_value = values.get(b"authorization", b"")
            with self._lock:
                self.auth_matches.append(auth_value == b"Bearer " + self.synthetic_key)
            self.target_seen.set()
            response = h2_response(
                401,
                b'{"error":{"type":"invalid_request_error","message":"synthetic capture stop"}}',
            )
        else:
            response = h2_response(404, b'{"error":{"message":"not available in capture"}}')
        response_headers, body = response
        protocol.send_headers(stream_id, response_headers)
        protocol.send_data(stream_id, body, end_stream=True)

    @staticmethod
    def _send_protocol_bytes(
        tls: ssl.SSLSocket,
        protocol: H2Connection,
        connection_capture: capture.ConnectionCapture,
    ) -> None:
        outgoing = protocol.data_to_send()
        if outgoing:
            connection_capture.feed_server(outgoing)
            tls.sendall(outgoing)


def initialize_params() -> dict[str, Any]:
    return {
        "protocolVersion": 1,
        "clientCapabilities": {
            "fs": {"readTextFile": False, "writeTextFile": False},
            "terminal": False,
        },
        "_meta": {
            "startupHints": {
                "nonInteractive": True,
                "skipGitStatus": True,
                "skipProjectLayout": True,
            },
            "clientType": "sub2api-hpack-evidence",
            "clientVersion": "1",
        },
    }


def run_official_api_key_request(
    grok_binary: Path,
    temp_dir: Path,
    server: LocalH2CaptureServer,
    proxy: LocalOnlyConnectProxy,
    synthetic_key: str,
) -> tuple[int, list[str]]:
    grok_home = temp_dir / "ephemeral-grok-home"
    workspace = temp_dir / "empty-workspace"
    grok_home.mkdir()
    workspace.mkdir()
    (grok_home / "config.toml").write_text(
        '[auth]\npreferred_method = "api_key"\n', encoding="ascii", newline="\n"
    )

    environment = os.environ.copy()
    proxy_url = f"http://127.0.0.1:{proxy.port}"
    for key in ("HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"):
        environment[key] = proxy_url
    for key in ("NO_PROXY", "no_proxy"):
        environment[key] = ""
    environment["SSL_CERT_FILE"] = str(server.authority.ca_path)
    environment["GROK_HOME"] = str(grok_home)
    environment["XAI_API_KEY"] = synthetic_key
    environment.pop("GROK_CODE_XAI_API_KEY", None)

    long_prefix = "~" * LONG_PATH_CHARS
    xai_base = f"https://localhost:{server.port}/{long_prefix}/v1"
    control_base = f"https://localhost:{server.port}/v1"
    command = [
        str(grok_binary),
        "agent",
        "--always-approve",
        "--no-leader",
        "--model",
        "grok-4.5",
        "--xai-api-base-url",
        xai_base,
        "--cli-chat-proxy-base-url",
        control_base,
        "stdio",
    ]
    client = capture.ACPClient(command, environment, workspace)
    methods: list[str] = []
    try:
        initialized = client.request(1, "initialize", initialize_params(), timeout=120)
        methods = capture.auth_method_ids(initialized)
        if "xai.api_key" not in methods:
            raise capture.CaptureError("official binary did not advertise synthetic API-key auth")
        client.request(
            2,
            "authenticate",
            {"methodId": "xai.api_key", "_meta": {"headless": True}},
            timeout=60,
        )
        session = client.request(
            3,
            "session/new",
            {"cwd": str(workspace), "mcpServers": [], "_meta": {"yoloMode": True}},
            timeout=120,
        )
        session_id = session.get("sessionId", "")
        if not isinstance(session_id, str) or not session_id:
            raise capture.CaptureError("ACP session/new returned no session id")
        try:
            client.request(
                4,
                "session/prompt",
                {
                    "sessionId": session_id,
                    "prompt": [{"type": "text", "text": "Synthetic HPACK branch capture."}],
                },
                timeout=120,
            )
        except capture.CaptureError:
            # The local endpoint deliberately returns 401 after receiving the
            # complete request; the wire evidence is the terminal condition.
            if not server.target_seen.wait(timeout=5):
                raise
        if not server.target_seen.wait(timeout=10):
            raise capture.CaptureError("official binary did not send the target request")
        return client.pid, methods
    finally:
        client.close()


def field_representations(record: capture.HeaderBlockRecord) -> list[capture.Representation]:
    return [item for item in record.representations if item.kind != "table_size_update"]


def select_targets(
    connections: list[capture.ConnectionCapture],
) -> tuple[capture.ConnectionCapture, list[capture.HeaderBlockRecord]]:
    matches: list[tuple[capture.ConnectionCapture, capture.HeaderBlockRecord]] = []
    for connection in connections:
        with connection.lock:
            records = list(connection.records)
        for record in records:
            values = {field.name.lower(): field.value for field in record.fields}
            if (
                values.get(b":method") == b"POST"
                and values.get(b":path", b"").endswith(b"/responses")
                and b"authorization" in values
            ):
                matches.append((connection, record))
    if len(matches) != 2:
        raise capture.CaptureError(f"expected two synthetic target requests, observed {len(matches)}")
    connections_seen = {id(connection) for connection, _ in matches}
    if len(connections_seen) != 1:
        raise capture.CaptureError("synthetic target requests did not share one HTTP/2 connection")
    connection = matches[0][0]
    records = sorted((record for _, record in matches), key=lambda item: item.stream_id)
    if [record.stream_id for record in records] != [1, 3]:
        raise capture.CaptureError(
            f"expected synthetic target streams [1, 3], observed {[record.stream_id for record in records]}"
        )
    return connection, records


def analyze_target(record: capture.HeaderBlockRecord) -> dict[str, Any]:
    representations = field_representations(record)
    if len(representations) != len(record.fields):
        raise capture.CaptureError("target field/representation count mismatch")
    auth_indexes = [
        index for index, field in enumerate(record.fields) if field.name.lower() == b"authorization"
    ]
    if len(auth_indexes) != 1:
        raise capture.CaptureError("target did not contain exactly one auth header")
    auth_index = auth_indexes[0]
    auth_representation = representations[auth_index]
    auth_field = record.fields[auth_index]
    fragment_lengths = [len(frame.fragment) for frame in record.frames]
    local_fragment_lengths = [
        min(DEFAULT_MAX_FRAME_SIZE, record.local_length - offset)
        for offset in range(0, record.local_length, DEFAULT_MAX_FRAME_SIZE)
    ]
    return {
        "stream_id": record.stream_id,
        "path_length": next(
            len(field.value) for field in record.fields if field.name.lower() == b":path"
        ),
        "header_count": len(record.fields),
        "header_block_length": len(record.block),
        "header_block_sha256": capture.sha256_hex(record.block),
        "local_header_block_length": record.local_length,
        "local_header_block_sha256": record.local_sha256,
        "byte_equal": record.byte_equal,
        "frame_types": [frame.frame_type for frame in record.frames],
        "frame_flags": [frame.flags for frame in record.frames],
        "frame_fragment_lengths": fragment_lengths,
        "local_frame_fragment_lengths": local_fragment_lengths,
        "frame_split_equal": fragment_lengths == local_fragment_lengths,
        "continuation_count": sum(frame.frame_type == "CONTINUATION" for frame in record.frames),
        "auth_header_representation": auth_representation.kind,
        "auth_header_decoder_sensitive": auth_field.sensitive,
        "auth_value_huffman": auth_representation.value_huffman,
        "never_index_count": sum(item.kind == "literal_never_indexed" for item in representations),
    }


def source_uses_sensitive_marking(counts: dict[str, int]) -> bool:
    return bool(counts["set_sensitive_call_count"] or counts["header_sensitive_call_count"])


def fetch_public_source_audit() -> dict[str, Any]:
    url = f"https://api.github.com/repos/xai-org/grok-build/tarball/{capture.PUBLIC_SYNC_COMMIT}"
    request = urllib.request.Request(url, headers={"User-Agent": "sub2api-hpack-evidence"})
    with urllib.request.urlopen(request, timeout=60) as response:
        archive = response.read(PUBLIC_ARCHIVE_MAX_BYTES + 1)
    if len(archive) > PUBLIC_ARCHIVE_MAX_BYTES:
        raise capture.CaptureError("public source archive exceeded audit limit")
    patterns = {
        "set_sensitive_call_count": b"set_sensitive(",
        "header_sensitive_call_count": b"header_sensitive(",
        "bearer_auth_call_count": b"bearer_auth(",
    }
    counts = {name: 0 for name in patterns}
    rust_files = 0
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as source:
        for member in source.getmembers():
            if not member.isfile() or not member.name.endswith(".rs"):
                continue
            extracted = source.extractfile(member)
            if extracted is None:
                continue
            data = extracted.read()
            rust_files += 1
            for name, pattern in patterns.items():
                counts[name] += data.count(pattern)
    return {
        "commit": capture.PUBLIC_SYNC_COMMIT,
        "archive_sha256": capture.sha256_hex(archive),
        "rust_files_scanned": rust_files,
        **counts,
        "sensitive_marking_api_observed": source_uses_sensitive_marking(counts),
    }


def oauth_baseline(script_dir: Path) -> dict[str, Any]:
    path = script_dir / "official-wire-report.json"
    raw = path.read_bytes()
    report = json.loads(raw)
    never_count = sum(int(item["sensitive_never_index_count"]) for item in report["requests"])
    return {
        "report_sha256": capture.sha256_hex(raw),
        "request_count": len(report["requests"]),
        "never_index_count": never_count,
        "all_blocks_byte_equal": bool(report["parity"]["all_header_blocks_on_target_connection_equal"]),
    }


def build_report(
    grok_binary: Path,
    version: str,
    process_id: int,
    methods: list[str],
    proxy: LocalOnlyConnectProxy,
    server: LocalH2CaptureServer,
    connection: capture.ConnectionCapture,
    targets: list[capture.HeaderBlockRecord],
    source_audit: dict[str, Any],
    oauth: dict[str, Any],
    parent_env_unchanged: bool,
    user_config_unchanged: bool,
) -> dict[str, Any]:
    target_reports = [analyze_target(target) for target in targets]
    with connection.lock:
        records = list(connection.records)
        connection_errors = list(connection.errors)
    continuation_aligned = (
        bool(target_reports)
        and all(target["byte_equal"] for target in target_reports)
        and all(target["frame_split_equal"] for target in target_reports)
        and all(target["continuation_count"] >= 1 for target in target_reports)
        and all(target["frame_types"][0] == "HEADERS" for target in target_reports)
        and all(
            all(item == "CONTINUATION" for item in target["frame_types"][1:])
            for target in target_reports
        )
    )
    never_index_not_applicable = (
        bool(target_reports)
        and all(
            target["auth_header_representation"] == "literal_without_indexing"
            for target in target_reports
        )
        and all(not target["auth_header_decoder_sensitive"] for target in target_reports)
        and all(target["never_index_count"] == 0 for target in target_reports)
        and oauth["never_index_count"] == 0
        and not source_audit["sensitive_marking_api_observed"]
    )
    aligned = (
        continuation_aligned
        and never_index_not_applicable
        and all(record.byte_equal for record in records)
        and bool(server.auth_matches)
        and all(server.auth_matches)
        and not connection_errors
        and not server.errors
        and not proxy.errors
    )
    return {
        "schema_version": 1,
        "status": (
            "OFFICIAL-CONTINUATION-ALIGNED_SENSITIVE-NEVER-INDEX-NOT-APPLICABLE"
            if aligned
            else "OFFICIAL-HPACK-BRANCH-REVIEW-REQUIRED"
        ),
        "official_binary": {
            "version": version,
            "sha256": capture.file_sha256(grok_binary),
            "process_hash": capture.short_bound_hash(os.urandom(32), str(process_id)),
        },
        "scope": {
            "synthetic_api_key_auth_advertised": "xai.api_key" in methods,
            "temporary_grok_home": True,
            "local_endpoint_only": True,
            "target_request_count": len(targets),
            "target_request_streams": [target.stream_id for target in targets],
            "all_synthetic_auth_values_matched_in_memory": bool(server.auth_matches)
            and len(server.auth_matches) == len(targets)
            and all(server.auth_matches),
        },
        "transport": {
            "alpn": connection.alpn,
            "client_preface_exact": connection.preface == capture.CLIENT_PREFACE,
            "settings_ack": connection.settings_ack,
            "connection_header_blocks_compared": len(records),
            "all_connection_header_blocks_equal": all(record.byte_equal for record in records),
            "targets": target_reports,
            "continuation_aligned": continuation_aligned,
        },
        "sensitive_branch": {
            "api_key_live_never_index_count": sum(
                target["never_index_count"] for target in target_reports
            ),
            "oauth_baseline": oauth,
            "public_source_audit": source_audit,
            "official_auth_builders_mark_sensitive": False,
            "classification": "NOT_APPLICABLE_FOR_OBSERVED_OFFICIAL_GROK_AUTH_BUILDERS",
            "not_applicable_evidence_complete": never_index_not_applicable,
        },
        "network_isolation": {
            "accepted_local_connect_count": proxy.accepted_count,
            "blocked_nonlocal_connect_count": proxy.blocked_count,
            "nonlocal_connect_allowed": False,
        },
        "safety": {
            "raw_headers_persisted": False,
            "decoded_headers_persisted": False,
            "authorization_persisted": False,
            "request_or_response_body_persisted": False,
            "production_endpoint_persisted": False,
            "synthetic_key_persisted_outside_temporary_home": False,
            "user_auth_file_read": False,
            "user_auth_file_modified": False,
            "user_config_unchanged": user_config_unchanged,
            "parent_proxy_environment_unchanged": parent_env_unchanged,
            "windows_root_store_modified": False,
        },
        "limitations": {
            "private_binary_source_is_not_available": True,
            "public_source_commit_differs_from_installed_private_revision": True,
            "never_index_support_remains_in_local_encoder_for_future_sensitive_inputs": True,
            "conclusion_is_scoped_to_current_official_oauth_and_api_key_request_builders": True,
        },
    }


def run_capture(args: argparse.Namespace) -> int:
    script_dir = Path(__file__).resolve().parent
    backend = script_dir.parents[4]
    grok_binary = Path(args.grok_binary).resolve()
    config_path = Path.home() / ".grok" / "config.toml"
    if not grok_binary.is_file():
        raise capture.CaptureError("official Grok binary is missing")
    config_hash_before = capture.file_sha256(config_path) if config_path.is_file() else ""
    parent_keys = (
        "HTTPS_PROXY",
        "HTTP_PROXY",
        "https_proxy",
        "http_proxy",
        "NO_PROXY",
        "no_proxy",
        "SSL_CERT_FILE",
        "GROK_HOME",
        "XAI_API_KEY",
    )
    parent_env_before = {key: os.environ.get(key) for key in parent_keys}
    version_run = subprocess.run(
        [str(grok_binary), "--version"],
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=30,
        check=False,
    )
    if version_run.returncode != 0:
        raise capture.CaptureError("unable to read official binary version")
    version = version_run.stdout.strip().splitlines()[0]
    if capture.file_sha256(grok_binary) != capture.EXPECTED_BINARY_SHA256:
        raise capture.CaptureError("installed official binary hash changed")

    synthetic_key = SYNTHETIC_KEY_PREFIX + os.urandom(24).hex()
    with tempfile.TemporaryDirectory(prefix="sub2api-grok-hpack-branches-") as temp_name:
        temp_dir = Path(temp_name)
        server_dir = temp_dir / "server"
        server_dir.mkdir()
        server = LocalH2CaptureServer(server_dir, synthetic_key)
        server.start()
        proxy = LocalOnlyConnectProxy(server.port)
        proxy.start()
        try:
            process_id, methods = run_official_api_key_request(
                grok_binary, temp_dir, server, proxy, synthetic_key
            )
            time.sleep(0.5)
            connection, targets = select_targets(server.connections)
            capture.compare_connection(connection, backend, temp_dir)
            source_audit = fetch_public_source_audit()
            oauth = oauth_baseline(script_dir)
        finally:
            proxy.stop()
            server.stop()

        config_hash_after = capture.file_sha256(config_path) if config_path.is_file() else ""
        parent_env_after = {key: os.environ.get(key) for key in parent_keys}
        report = build_report(
            grok_binary,
            version,
            process_id,
            methods,
            proxy,
            server,
            connection,
            targets,
            source_audit,
            oauth,
            parent_env_before == parent_env_after,
            config_hash_before == config_hash_after,
        )
        output = Path(args.output).resolve()
        capture.safe_write_report(output, report)
        if report["status"] != "OFFICIAL-CONTINUATION-ALIGNED_SENSITIVE-NEVER-INDEX-NOT-APPLICABLE":
            raise capture.CaptureError("official HPACK branch evidence did not align")
        print(f"status={report['status']}")
        print(
            "continuations="
            + ",".join(
                str(target["continuation_count"]) for target in report["transport"]["targets"]
            )
        )
        print(
            "auth_representations="
            + ",".join(
                target["auth_header_representation"]
                for target in report["transport"]["targets"]
            )
        )
        print(f"report_sha256={capture.file_sha256(output)}")
    return 0


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-official", action="store_true")
    parser.add_argument(
        "--grok-binary",
        default=str(Path.home() / ".grok" / "bin" / "grok.exe"),
    )
    parser.add_argument(
        "--output",
        default=str(Path(__file__).resolve().parent / "official-hpack-branches-report.json"),
    )
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    if not args.run_official:
        print("Refusing to run the official binary without --run-official", file=sys.stderr)
        return 2
    try:
        return run_capture(args)
    except capture.CaptureError as error:
        print(f"capture failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
