#!/usr/bin/env python3
"""Capture official and local TLS 1.3 session-resumption hello shapes.

The CONNECT proxy never terminates TLS. It observes only the plaintext
ClientHello and ServerHello records, relays every byte unchanged, and keeps
ticket identities in memory. The derived report cannot reconstruct a ticket,
credential, hostname, request, or response.
"""

from __future__ import annotations

import argparse
import collections
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import threading
import time
from dataclasses import dataclass, field
from typing import Any, Callable

from capture import (
    CaptureError,
    file_sha256,
    read_connect_request,
    run_two_prompts,
    safe_write_report,
    sha256_hex,
    short_bound_hash,
)


TLS_CONTENT_HANDSHAKE = 22
TLS_HANDSHAKE_CLIENT_HELLO = 1
TLS_HANDSHAKE_SERVER_HELLO = 2
TLS_EXTENSION_SERVER_NAME = 0
TLS_EXTENSION_SUPPORTED_GROUPS = 10
TLS_EXTENSION_EC_POINT_FORMATS = 11
TLS_EXTENSION_SIGNATURE_ALGORITHMS = 13
TLS_EXTENSION_ALPN = 16
TLS_EXTENSION_SUPPORTED_VERSIONS = 43
TLS_EXTENSION_PSK_MODES = 45
TLS_EXTENSION_KEY_SHARE = 51
TLS_EXTENSION_PRE_SHARED_KEY = 41
MAX_TLS_RECORD = (1 << 14) + 2048
MAX_TLS_HANDSHAKE = 1 << 20


@dataclass(frozen=True)
class TLSExtension:
    extension_id: int
    data: bytes


@dataclass(frozen=True)
class TLSClientHello:
    raw_length: int
    legacy_version: int
    session_id: bytes
    cipher_suites: list[int]
    compression_methods: list[int]
    extensions: list[TLSExtension]

    def extension(self, extension_id: int) -> TLSExtension | None:
        return next((item for item in self.extensions if item.extension_id == extension_id), None)


@dataclass(frozen=True)
class TLSServerHello:
    raw_length: int
    legacy_version: int
    session_id: bytes
    cipher_suite: int
    compression_method: int
    extensions: list[TLSExtension]

    def extension(self, extension_id: int) -> TLSExtension | None:
        return next((item for item in self.extensions if item.extension_id == extension_id), None)


class TLSHelloStreamParser:
    def __init__(self, expected_type: int, callback: Callable[[bytes], None]) -> None:
        self.expected_type = expected_type
        self.callback = callback
        self.record_buffer = bytearray()
        self.handshake_buffer = bytearray()
        self.complete = False

    def feed(self, data: bytes) -> None:
        if self.complete:
            return
        self.record_buffer.extend(data)
        while len(self.record_buffer) >= 5:
            length = int.from_bytes(self.record_buffer[3:5], "big")
            if length > MAX_TLS_RECORD:
                raise CaptureError("TLS record exceeds capture limit")
            if len(self.record_buffer) < 5 + length:
                return
            content_type = self.record_buffer[0]
            payload = bytes(self.record_buffer[5 : 5 + length])
            del self.record_buffer[: 5 + length]
            if content_type != TLS_CONTENT_HANDSHAKE:
                continue
            self.handshake_buffer.extend(payload)
            if len(self.handshake_buffer) < 4:
                continue
            message_type = self.handshake_buffer[0]
            message_length = int.from_bytes(self.handshake_buffer[1:4], "big")
            if message_length > MAX_TLS_HANDSHAKE:
                raise CaptureError("TLS handshake exceeds capture limit")
            if message_type != self.expected_type:
                raise CaptureError("unexpected first TLS handshake message")
            if len(self.handshake_buffer) < 4 + message_length:
                continue
            message = bytes(self.handshake_buffer[: 4 + message_length])
            self.complete = True
            self.callback(message)
            return


def _read_vector(data: bytes, offset: int, length_bytes: int) -> tuple[bytes, int]:
    if length_bytes not in (1, 2, 3) or offset + length_bytes > len(data):
        raise CaptureError("truncated TLS vector length")
    length = int.from_bytes(data[offset : offset + length_bytes], "big")
    offset += length_bytes
    end = offset + length
    if end > len(data):
        raise CaptureError("truncated TLS vector")
    return data[offset:end], end


def _parse_extensions(data: bytes) -> list[TLSExtension]:
    result: list[TLSExtension] = []
    offset = 0
    while offset < len(data):
        if offset + 4 > len(data):
            raise CaptureError("truncated TLS extension")
        extension_id = int.from_bytes(data[offset : offset + 2], "big")
        length = int.from_bytes(data[offset + 2 : offset + 4], "big")
        offset += 4
        end = offset + length
        if end > len(data):
            raise CaptureError("truncated TLS extension payload")
        result.append(TLSExtension(extension_id, data[offset:end]))
        offset = end
    return result


def parse_client_hello(message: bytes) -> TLSClientHello:
    if len(message) < 4 or message[0] != TLS_HANDSHAKE_CLIENT_HELLO:
        raise CaptureError("not a ClientHello")
    body = message[4:]
    if len(body) < 34:
        raise CaptureError("truncated ClientHello")
    legacy_version = int.from_bytes(body[:2], "big")
    offset = 34
    session_id, offset = _read_vector(body, offset, 1)
    cipher_bytes, offset = _read_vector(body, offset, 2)
    if len(cipher_bytes) % 2:
        raise CaptureError("invalid ClientHello cipher suite vector")
    ciphers = [int.from_bytes(cipher_bytes[i : i + 2], "big") for i in range(0, len(cipher_bytes), 2)]
    compression_bytes, offset = _read_vector(body, offset, 1)
    extension_bytes, offset = _read_vector(body, offset, 2)
    if offset != len(body):
        raise CaptureError("unexpected bytes after ClientHello extensions")
    return TLSClientHello(
        raw_length=len(message),
        legacy_version=legacy_version,
        session_id=session_id,
        cipher_suites=ciphers,
        compression_methods=list(compression_bytes),
        extensions=_parse_extensions(extension_bytes),
    )


def parse_server_hello(message: bytes) -> TLSServerHello:
    if len(message) < 4 or message[0] != TLS_HANDSHAKE_SERVER_HELLO:
        raise CaptureError("not a ServerHello")
    body = message[4:]
    if len(body) < 38:
        raise CaptureError("truncated ServerHello")
    legacy_version = int.from_bytes(body[:2], "big")
    offset = 34
    session_id, offset = _read_vector(body, offset, 1)
    if offset + 3 > len(body):
        raise CaptureError("truncated ServerHello selection")
    cipher_suite = int.from_bytes(body[offset : offset + 2], "big")
    compression_method = body[offset + 2]
    offset += 3
    extension_bytes, offset = _read_vector(body, offset, 2)
    if offset != len(body):
        raise CaptureError("unexpected bytes after ServerHello extensions")
    return TLSServerHello(
        raw_length=len(message),
        legacy_version=legacy_version,
        session_id=session_id,
        cipher_suite=cipher_suite,
        compression_method=compression_method,
        extensions=_parse_extensions(extension_bytes),
    )


@dataclass
class TLSConnectionRecord:
    phase: str
    hostname: str
    port: int
    started_at: float
    client_hello: TLSClientHello | None = None
    server_hello: TLSServerHello | None = None
    closed: bool = False
    errors: list[str] = field(default_factory=list)
    sockets: list[socket.socket] = field(default_factory=list, repr=False)

    def target_key(self) -> tuple[str, int]:
        return self.hostname, self.port


class PassthroughConnectProxy:
    def __init__(self) -> None:
        self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen(64)
        self.listener.settimeout(0.25)
        self.port = self.listener.getsockname()[1]
        self.connections: list[TLSConnectionRecord] = []
        self.errors: list[str] = []
        self._phase = "official"
        self._stop = threading.Event()
        self._lock = threading.RLock()
        self._threads: list[threading.Thread] = []
        self._accept_thread: threading.Thread | None = None

    def set_phase(self, phase: str) -> None:
        with self._lock:
            self._phase = phase

    def start(self) -> None:
        self._accept_thread = threading.Thread(target=self._accept_loop, name="tls-pass-accept", daemon=True)
        self._accept_thread.start()

    def drop_active(self, phase: str) -> None:
        with self._lock:
            sockets = [sock for item in self.connections if item.phase == phase and not item.closed for sock in item.sockets]
        for sock in sockets:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                sock.close()
            except OSError:
                pass

    def stop(self) -> None:
        self._stop.set()
        try:
            self.listener.close()
        except OSError:
            pass
        self.drop_active("official")
        self.drop_active("local")
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
                return
            thread = threading.Thread(target=self._handle, args=(client,), daemon=True)
            self._threads.append(thread)
            thread.start()

    def _handle(self, client: socket.socket) -> None:
        upstream: socket.socket | None = None
        record: TLSConnectionRecord | None = None
        try:
            hostname, port = read_connect_request(client)
            upstream = socket.create_connection((hostname, port), timeout=20)
            client.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            with self._lock:
                phase = self._phase
            record = TLSConnectionRecord(phase, hostname, port, time.monotonic())
            record.sockets.extend((client, upstream))
            client_parser = TLSHelloStreamParser(
                TLS_HANDSHAKE_CLIENT_HELLO,
                lambda raw: setattr(record, "client_hello", parse_client_hello(raw)),
            )
            server_parser = TLSHelloStreamParser(
                TLS_HANDSHAKE_SERVER_HELLO,
                lambda raw: setattr(record, "server_hello", parse_server_hello(raw)),
            )
            with self._lock:
                self.connections.append(record)

            reverse = threading.Thread(
                target=self._relay,
                args=(upstream, client, server_parser.feed, record),
                daemon=True,
            )
            reverse.start()
            self._relay(client, upstream, client_parser.feed, record)
            reverse.join(timeout=3)
        except Exception as error:  # noqa: BLE001 - only the type is retained
            with self._lock:
                self.errors.append(type(error).__name__)
        finally:
            if record is not None:
                record.closed = True
            for sock in (client, upstream):
                if sock is None:
                    continue
                try:
                    sock.close()
                except OSError:
                    pass

    def _relay(
        self,
        source: socket.socket,
        destination: socket.socket,
        observer: Callable[[bytes], None],
        record: TLSConnectionRecord,
    ) -> None:
        try:
            while not self._stop.is_set():
                data = source.recv(64 << 10)
                if not data:
                    return
                observer(data)
                destination.sendall(data)
        except (OSError, CaptureError) as error:
            if not self._stop.is_set():
                record.errors.append(type(error).__name__)
        finally:
            try:
                destination.shutdown(socket.SHUT_WR)
            except OSError:
                pass


def _u16_list(data: bytes) -> list[int]:
    if len(data) < 2:
        raise CaptureError("truncated uint16 TLS vector")
    length = int.from_bytes(data[:2], "big")
    payload = data[2 : 2 + length]
    if len(payload) != length or length % 2:
        raise CaptureError("invalid uint16 TLS vector")
    return [int.from_bytes(payload[i : i + 2], "big") for i in range(0, len(payload), 2)]


def _u8_list(data: bytes) -> list[int]:
    if not data or len(data) != data[0] + 1:
        raise CaptureError("invalid uint8 TLS vector")
    return list(data[1:])


def _alpn_protocols(data: bytes) -> list[str]:
    protocols, end = _read_vector(data, 0, 2)
    if end != len(data):
        raise CaptureError("invalid ALPN extension")
    result: list[str] = []
    offset = 0
    while offset < len(protocols):
        item, offset = _read_vector(protocols, offset, 1)
        result.append(item.decode("ascii"))
    return result


def _key_shares(data: bytes, client: bool) -> list[dict[str, int]]:
    payload = data
    if client:
        payload, end = _read_vector(data, 0, 2)
        if end != len(data):
            raise CaptureError("invalid key-share extension")
    result: list[dict[str, int]] = []
    offset = 0
    while offset < len(payload):
        if offset + 4 > len(payload):
            raise CaptureError("truncated key share")
        group = int.from_bytes(payload[offset : offset + 2], "big")
        length = int.from_bytes(payload[offset + 2 : offset + 4], "big")
        offset += 4
        if offset + length > len(payload):
            raise CaptureError("truncated key-share bytes")
        result.append({"group": group, "key_exchange_length": length})
        offset += length
    return result


def _psk_client_details(data: bytes, run_nonce: bytes) -> dict[str, Any]:
    identities, offset = _read_vector(data, 0, 2)
    binders, offset = _read_vector(data, offset, 2)
    if offset != len(data):
        raise CaptureError("unexpected bytes after PSK vectors")
    identity_reports: list[dict[str, Any]] = []
    cursor = 0
    while cursor < len(identities):
        identity, cursor = _read_vector(identities, cursor, 2)
        if cursor + 4 > len(identities):
            raise CaptureError("truncated PSK ticket age")
        age = int.from_bytes(identities[cursor : cursor + 4], "big")
        cursor += 4
        identity_reports.append(
            {
                "identity_length": len(identity),
                "identity_bound_hash": sha256_hex(run_nonce + identity)[:24],
                "obfuscated_ticket_age": age,
            }
        )
    binder_lengths: list[int] = []
    cursor = 0
    while cursor < len(binders):
        binder, cursor = _read_vector(binders, cursor, 1)
        binder_lengths.append(len(binder))
    return {"identities": identity_reports, "binder_lengths": binder_lengths}


def _server_names(data: bytes) -> dict[str, Any]:
    names, end = _read_vector(data, 0, 2)
    if end != len(data):
        raise CaptureError("invalid server-name extension")
    result: list[dict[str, int]] = []
    offset = 0
    while offset < len(names):
        if offset >= len(names):
            raise CaptureError("truncated server-name type")
        name_type = names[offset]
        name, offset = _read_vector(names, offset + 1, 2)
        result.append({"type": name_type, "length": len(name)})
    return {"names": result}


def client_hello_report(hello: TLSClientHello, run_nonce: bytes) -> dict[str, Any]:
    details: list[dict[str, Any]] = []
    for extension in hello.extensions:
        item: dict[str, Any] = {"id": extension.extension_id, "length": len(extension.data)}
        if extension.extension_id == TLS_EXTENSION_SERVER_NAME:
            item.update(_server_names(extension.data))
        elif extension.extension_id == TLS_EXTENSION_SUPPORTED_GROUPS:
            item["groups"] = _u16_list(extension.data)
        elif extension.extension_id == TLS_EXTENSION_EC_POINT_FORMATS:
            item["formats"] = _u8_list(extension.data)
        elif extension.extension_id == TLS_EXTENSION_SIGNATURE_ALGORITHMS:
            item["algorithms"] = _u16_list(extension.data)
        elif extension.extension_id == TLS_EXTENSION_ALPN:
            item["protocols"] = _alpn_protocols(extension.data)
        elif extension.extension_id == TLS_EXTENSION_SUPPORTED_VERSIONS:
            item["versions"] = _u8_prefixed_u16(extension.data)
        elif extension.extension_id == TLS_EXTENSION_PSK_MODES:
            item["modes"] = _u8_list(extension.data)
        elif extension.extension_id == TLS_EXTENSION_KEY_SHARE:
            item["shares"] = _key_shares(extension.data, client=True)
        elif extension.extension_id == TLS_EXTENSION_PRE_SHARED_KEY:
            item.update(_psk_client_details(extension.data, run_nonce))
        details.append(item)
    order = [item.extension_id for item in hello.extensions]
    return {
        "handshake_length": hello.raw_length,
        "legacy_version": hello.legacy_version,
        "session_id_length": len(hello.session_id),
        "cipher_suites": hello.cipher_suites,
        "compression_methods": hello.compression_methods,
        "extension_order": order,
        "extensions": details,
        "pre_shared_key_present": TLS_EXTENSION_PRE_SHARED_KEY in order,
        "pre_shared_key_last": bool(order) and order[-1] == TLS_EXTENSION_PRE_SHARED_KEY,
    }


def _u8_prefixed_u16(data: bytes) -> list[int]:
    if not data:
        raise CaptureError("truncated supported-versions extension")
    length = data[0]
    payload = data[1:]
    if len(payload) != length or length % 2:
        raise CaptureError("invalid supported-versions extension")
    return [int.from_bytes(payload[i : i + 2], "big") for i in range(0, len(payload), 2)]


def server_hello_report(hello: TLSServerHello) -> dict[str, Any]:
    selected_version: int | None = None
    selected_identity: int | None = None
    extension_order = [item.extension_id for item in hello.extensions]
    for extension in hello.extensions:
        if extension.extension_id == TLS_EXTENSION_SUPPORTED_VERSIONS and len(extension.data) == 2:
            selected_version = int.from_bytes(extension.data, "big")
        elif extension.extension_id == TLS_EXTENSION_PRE_SHARED_KEY and len(extension.data) == 2:
            selected_identity = int.from_bytes(extension.data, "big")
    return {
        "handshake_length": hello.raw_length,
        "legacy_version": hello.legacy_version,
        "session_id_length": len(hello.session_id),
        "cipher_suite": hello.cipher_suite,
        "compression_method": hello.compression_method,
        "extension_order": extension_order,
        "selected_version": selected_version,
        "selected_psk_identity": selected_identity,
        "resumption_accepted": selected_identity is not None,
    }


def select_resumption_pair(
    connections: list[TLSConnectionRecord],
    phase: str,
) -> tuple[TLSConnectionRecord, TLSConnectionRecord]:
    grouped: dict[tuple[str, int], list[TLSConnectionRecord]] = collections.defaultdict(list)
    for item in connections:
        if item.phase == phase and item.client_hello is not None and item.server_hello is not None:
            grouped[item.target_key()].append(item)
    for records in grouped.values():
        records.sort(key=lambda item: item.started_at)
        for index, fresh in enumerate(records):
            if fresh.client_hello is None or fresh.client_hello.extension(TLS_EXTENSION_PRE_SHARED_KEY) is not None:
                continue
            for resumed in records[index + 1 :]:
                if resumed.client_hello is None or resumed.server_hello is None:
                    continue
                if resumed.client_hello.extension(TLS_EXTENSION_PRE_SHARED_KEY) is None:
                    continue
                if resumed.server_hello.extension(TLS_EXTENSION_PRE_SHARED_KEY) is None:
                    continue
                return fresh, resumed
    phase_records = [
        item
        for item in connections
        if item.phase == phase and item.client_hello is not None and item.server_hello is not None
    ]
    offered = sum(
        item.client_hello.extension(TLS_EXTENSION_PRE_SHARED_KEY) is not None
        for item in phase_records
        if item.client_hello is not None
    )
    accepted = sum(
        item.server_hello.extension(TLS_EXTENSION_PRE_SHARED_KEY) is not None
        for item in phase_records
        if item.server_hello is not None
    )
    raise CaptureError(
        f"no accepted TLS resumption pair found for {phase} "
        f"(complete={len(phase_records)}, offered={offered}, accepted={accepted})"
    )


def _extension_detail(report: dict[str, Any], extension_id: int) -> dict[str, Any] | None:
    return next((item for item in report["extensions"] if item["id"] == extension_id), None)


def compare_hello_shapes(official: dict[str, Any], local: dict[str, Any]) -> dict[str, Any]:
    official_has_psk = official["pre_shared_key_present"]
    local_has_psk = local["pre_shared_key_present"]
    fields = {
        "legacy_version": official["legacy_version"] == local["legacy_version"],
        "session_id_length": official["session_id_length"] == local["session_id_length"],
        "cipher_suites": official["cipher_suites"] == local["cipher_suites"],
        "compression_methods": official["compression_methods"] == local["compression_methods"],
        "extension_set": set(official["extension_order"]) == set(local["extension_order"]),
        "pre_shared_key_last": (
            not official_has_psk and not local_has_psk
        ) or (
            official_has_psk and local_has_psk and official["pre_shared_key_last"] and local["pre_shared_key_last"]
        ),
    }
    for extension_id, name, keys in (
        (TLS_EXTENSION_SUPPORTED_GROUPS, "supported_groups", ("groups",)),
        (TLS_EXTENSION_EC_POINT_FORMATS, "ec_point_formats", ("formats",)),
        (TLS_EXTENSION_SIGNATURE_ALGORITHMS, "signature_algorithms", ("algorithms",)),
        (TLS_EXTENSION_ALPN, "alpn", ("protocols",)),
        (TLS_EXTENSION_SUPPORTED_VERSIONS, "supported_versions", ("versions",)),
        (TLS_EXTENSION_PSK_MODES, "psk_modes", ("modes",)),
        (TLS_EXTENSION_KEY_SHARE, "key_share", ("shares",)),
    ):
        official_item = _extension_detail(official, extension_id)
        local_item = _extension_detail(local, extension_id)
        fields[name] = official_item is not None and local_item is not None and all(
            official_item.get(key) == local_item.get(key) for key in keys
        )
    official_psk = _extension_detail(official, TLS_EXTENSION_PRE_SHARED_KEY)
    local_psk = _extension_detail(local, TLS_EXTENSION_PRE_SHARED_KEY)
    if official_psk is None and local_psk is None:
        fields["psk_identity_lengths"] = True
        fields["psk_binder_lengths"] = True
    else:
        fields["psk_identity_lengths"] = (
            official_psk is not None
            and local_psk is not None
            and [item["identity_length"] for item in official_psk["identities"]]
            == [item["identity_length"] for item in local_psk["identities"]]
        )
        fields["psk_binder_lengths"] = (
            official_psk is not None
            and local_psk is not None
            and official_psk["binder_lengths"] == local_psk["binder_lengths"]
        )
    return {"checks": fields, "all_required_equal": all(fields.values())}


def run_local_probe(backend: Path, temp_dir: Path, proxy_port: int, target: tuple[str, int]) -> None:
    suffix = ".exe" if os.name == "nt" else ""
    executable = temp_dir / f"grok-tls-resumption-probe{suffix}"
    package = "./internal/pkg/grokhttp2/testdata/official-wire-capture/tls-probe"
    build = subprocess.run(
        ["go", "build", "-o", str(executable), package],
        cwd=backend,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        timeout=180,
        check=False,
    )
    if build.returncode != 0:
        raise CaptureError("failed to build TLS resumption comparison helper")
    hostname, port = target
    address = f"[{hostname}]:{port}" if ":" in hostname else f"{hostname}:{port}"
    result = subprocess.run(
        [str(executable)],
        input=json.dumps({"address": address, "proxy_url": f"http://127.0.0.1:{proxy_port}"}),
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=90,
        check=False,
    )
    if result.returncode != 0:
        raise CaptureError("TLS resumption comparison helper failed")
    try:
        payload = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise CaptureError("invalid TLS resumption comparison response") from error
    if payload != {"ok": True}:
        raise CaptureError("TLS resumption comparison helper did not complete")


def build_report(
    grok_binary: Path,
    version: str,
    process_id: int,
    session_id: str,
    run_nonce: bytes,
    official_pair: tuple[TLSConnectionRecord, TLSConnectionRecord],
    local_pair: tuple[TLSConnectionRecord, TLSConnectionRecord],
    config_hash_before: str,
    config_hash_after: str,
    parent_env_before: dict[str, str | None],
    parent_env_after: dict[str, str | None],
) -> dict[str, Any]:
    official_fresh, official_resumed = official_pair
    local_fresh, local_resumed = local_pair
    if any(
        item.client_hello is None or item.server_hello is None
        for item in (official_fresh, official_resumed, local_fresh, local_resumed)
    ):
        raise CaptureError("incomplete TLS hello pair")
    official_fresh_report = client_hello_report(official_fresh.client_hello, run_nonce)
    official_resumed_report = client_hello_report(official_resumed.client_hello, run_nonce)
    local_fresh_report = client_hello_report(local_fresh.client_hello, run_nonce)
    local_resumed_report = client_hello_report(local_resumed.client_hello, run_nonce)
    fresh_parity = compare_hello_shapes(official_fresh_report, local_fresh_report)
    resumed_parity = compare_hello_shapes(official_resumed_report, local_resumed_report)
    official_server = server_hello_report(official_resumed.server_hello)
    local_server = server_hello_report(local_resumed.server_hello)
    server_parity = {
        "selected_version": official_server["selected_version"] == local_server["selected_version"],
        "cipher_suite": official_server["cipher_suite"] == local_server["cipher_suite"],
        "selected_psk_identity": official_server["selected_psk_identity"] == local_server["selected_psk_identity"],
        "resumption_accepted": official_server["resumption_accepted"] and local_server["resumption_accepted"],
    }
    aligned = fresh_parity["all_required_equal"] and resumed_parity["all_required_equal"] and all(server_parity.values())
    return {
        "schema_version": 1,
        "status": "OFFICIAL-TLS-RESUMPTION-STRUCTURALLY-ALIGNED" if aligned else "TLS-RESUMPTION-REVIEW-REQUIRED",
        "official_binary": {
            "version": version,
            "sha256": file_sha256(grok_binary),
            "process_hash": short_bound_hash(run_nonce, str(process_id)),
        },
        "bindings": {
            "session_hash": short_bound_hash(run_nonce, session_id),
            "target_hash": short_bound_hash(run_nonce, f"{official_resumed.hostname}:{official_resumed.port}"),
        },
        "official": {
            "fresh_client_hello": official_fresh_report,
            "resumed_client_hello": official_resumed_report,
            "resumed_server_hello": official_server,
        },
        "local_grok_profile": {
            "fresh_client_hello": local_fresh_report,
            "resumed_client_hello": local_resumed_report,
            "resumed_server_hello": local_server,
        },
        "parity": {
            "fresh": fresh_parity,
            "resumed": resumed_parity,
            "server": server_parity,
            "required_checks_passed": aligned,
            "extension_order_exact_match_required": False,
            "extension_order_reason": "rustls shuffles non-PSK extensions per connection; PSK remains last",
        },
        "evidence": {
            "same_official_process": True,
            "same_official_session": True,
            "tcp_reconnect_forced_between_prompts": True,
            "official_psk_offered": official_resumed_report["pre_shared_key_present"],
            "official_psk_selected_by_server": official_server["resumption_accepted"],
            "local_psk_offered": local_resumed_report["pre_shared_key_present"],
            "local_psk_selected_by_server": local_server["resumption_accepted"],
        },
        "safety": {
            "tls_terminated_by_capture_proxy": False,
            "tls_bytes_modified": False,
            "ticket_identity_persisted": False,
            "ticket_identity_hash_is_run_salted": True,
            "hostname_persisted": False,
            "request_or_response_persisted": False,
            "config_unchanged": config_hash_before == config_hash_after,
            "parent_proxy_environment_unchanged": parent_env_before == parent_env_after,
            "windows_root_store_modified": False,
        },
        "limitations": {
            "single_official_resumption_sample": True,
            "randomized_extension_order_compared_by_set_and_invariants": True,
            "ticket_and_binder_bytes_are_intentionally_unavailable_from_report": True,
        },
    }


def run_capture(args: argparse.Namespace) -> int:
    script_dir = Path(__file__).resolve().parent
    backend = script_dir.parents[4]
    grok_binary = Path(args.grok_binary).resolve()
    config_path = Path.home() / ".grok" / "config.toml"
    if not grok_binary.is_file() or not config_path.is_file():
        raise CaptureError("official binary or config file is missing")
    config_hash_before = file_sha256(config_path)
    parent_keys = ("HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy", "NO_PROXY", "no_proxy", "SSL_CERT_FILE")
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
        raise CaptureError("unable to read official binary version")
    version = version_run.stdout.strip().splitlines()[0]
    run_nonce = os.urandom(32)

    with tempfile.TemporaryDirectory(prefix="sub2api-grok-resumption-") as temp_name:
        temp_dir = Path(temp_name)
        workspace = temp_dir / "empty-workspace"
        workspace.mkdir()
        proxy = PassthroughConnectProxy()
        proxy.start()

        def force_reconnect(_: int) -> None:
            time.sleep(0.5)
            proxy.drop_active("official")
            time.sleep(0.5)

        try:
            process_id, session_id, _ = run_two_prompts(
                grok_binary,
                proxy,
                workspace,
                between_prompts=force_reconnect,
            )
            time.sleep(1)
            official_pair = select_resumption_pair(proxy.connections, "official")
            proxy.set_phase("local")
            run_local_probe(backend, temp_dir, proxy.port, official_pair[1].target_key())
            time.sleep(1)
            local_pair = select_resumption_pair(proxy.connections, "local")
        finally:
            proxy.stop()

        config_hash_after = file_sha256(config_path)
        parent_env_after = {key: os.environ.get(key) for key in parent_keys}
        if config_hash_before != config_hash_after:
            raise CaptureError("official config changed during capture")
        if parent_env_before != parent_env_after:
            raise CaptureError("parent proxy environment changed during capture")
        report = build_report(
            grok_binary,
            version,
            process_id,
            session_id,
            run_nonce,
            official_pair,
            local_pair,
            config_hash_before,
            config_hash_after,
            parent_env_before,
            parent_env_after,
        )
        output = Path(args.output).resolve()
        safe_write_report(output, report)
        if not report["parity"]["required_checks_passed"]:
            failed = [
                f"{section}.{name}"
                for section in ("fresh", "resumed")
                for name, passed in report["parity"][section]["checks"].items()
                if not passed
            ]
            failed.extend(
                f"server.{name}"
                for name, passed in report["parity"]["server"].items()
                if not passed
            )
            raise CaptureError("official and local TLS resumption structures differ: " + ",".join(failed))
        print(f"status={report['status']}")
        print(f"official_psk_selected={report['evidence']['official_psk_selected_by_server']}")
        print(f"local_psk_selected={report['evidence']['local_psk_selected_by_server']}")
        print(f"report_sha256={file_sha256(output)}")
    return 0


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-official", action="store_true", help="perform the live logged-in capture")
    parser.add_argument(
        "--grok-binary",
        default=str(Path.home() / ".grok" / "bin" / "grok.exe"),
        help="path to the official Grok executable",
    )
    parser.add_argument(
        "--output",
        default=str(Path(__file__).resolve().parent / "official-tls-resumption-report.json"),
        help="derived report output path",
    )
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    if not args.run_official:
        print("Refusing live account traffic without --run-official", file=sys.stderr)
        return 2
    try:
        return run_capture(args)
    except CaptureError as error:
        print(f"capture failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
