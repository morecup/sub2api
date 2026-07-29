#!/usr/bin/env python3
"""Capture official Grok HTTP/2 request headers without persisting secrets.

The proxy terminates TLS on both sides but forwards decrypted application bytes
unchanged. Header blocks, decoded values, request bodies, and credentials exist
only in process memory. The only writable artifact is a derived JSON report.
"""

from __future__ import annotations

import argparse
import base64
import collections
import datetime as dt
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import queue
import re
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import tomllib
from dataclasses import dataclass, field
from typing import Any, Callable
import urllib.request

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID
from hpack import Decoder


CLIENT_PREFACE = b"PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
FRAME_DATA = 0x0
FRAME_HEADERS = 0x1
FRAME_SETTINGS = 0x4
FRAME_GOAWAY = 0x7
FRAME_CONTINUATION = 0x9
FLAG_END_STREAM = 0x1
FLAG_ACK = 0x1
FLAG_END_HEADERS = 0x4
FLAG_PADDED = 0x8
FLAG_PRIORITY = 0x20
SETTING_HEADER_TABLE_SIZE = 0x1
HPACK_STATIC_TABLE_LENGTH = 61
MAX_CONNECT_HEADER = 64 * 1024
MAX_H2_FRAME = 16 * 1024 * 1024
PUBLIC_SYNC_COMMIT = "47348d13ec4508dcfe440e34c6d511bb02998fb2"
PUBLIC_LOCK_SHA256 = "852e088a2b4ac3586142592a6c6bbd3f78b8446a8fa8a24b5131baa44b31fd38"
H2_CRATE_SHA256 = "6cb093c84e8bd9b188d4c4a8cb6579fc016968d14c99882163cd3ff402a4f155"
EXPECTED_BINARY_SHA256 = "2469bd182af212c7fcb84f2981999e4e8a6a7a2e4172bad3ae7f787a1f11407c"
SKIP_VALUE_INDEX = {
    b":path",
    b"age",
    b"authorization",
    b"content-length",
    b"etag",
    b"if-modified-since",
    b"if-none-match",
    b"location",
    b"cookie",
    b"set-cookie",
}


class CaptureError(RuntimeError):
    pass


def sha256_hex(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def json_line(value: Any) -> str:
    return json.dumps(value, ensure_ascii=True, separators=(",", ":"))


def short_bound_hash(run_nonce: bytes, value: str) -> str:
    return sha256_hex(run_nonce + value.encode("utf-8"))[:24]


@dataclass(frozen=True)
class H2Frame:
    header: bytes
    frame_type: int
    flags: int
    stream_id: int
    payload: bytes


class H2FrameParser:
    def __init__(
        self,
        expect_preface: bool,
        on_preface: Callable[[bytes], None],
        on_frame: Callable[[H2Frame], None],
    ) -> None:
        self._expect_preface = expect_preface
        self._on_preface = on_preface
        self._on_frame = on_frame
        self._buffer = bytearray()

    def feed(self, data: bytes) -> None:
        self._buffer.extend(data)
        if self._expect_preface:
            if len(self._buffer) < len(CLIENT_PREFACE):
                return
            preface = bytes(self._buffer[: len(CLIENT_PREFACE)])
            del self._buffer[: len(CLIENT_PREFACE)]
            if preface != CLIENT_PREFACE:
                raise CaptureError("invalid HTTP/2 client preface")
            self._expect_preface = False
            self._on_preface(preface)

        while len(self._buffer) >= 9:
            length = int.from_bytes(self._buffer[:3], "big")
            if length > MAX_H2_FRAME:
                raise CaptureError("HTTP/2 frame exceeds capture limit")
            if len(self._buffer) < 9 + length:
                return
            header = bytes(self._buffer[:9])
            payload = bytes(self._buffer[9 : 9 + length])
            del self._buffer[: 9 + length]
            stream_id = int.from_bytes(header[5:9], "big") & 0x7FFF_FFFF
            self._on_frame(H2Frame(header, header[3], header[4], stream_id, payload))


@dataclass(frozen=True)
class DecodedField:
    name: bytes
    value: bytes
    sensitive: bool


@dataclass(frozen=True)
class Representation:
    kind: str
    index: int = 0
    name_index: int = 0
    name_huffman: bool | None = None
    value_huffman: bool | None = None
    encoded_name_length: int = 0
    encoded_value_length: int = 0


@dataclass
class CapturedHeaderFrame:
    frame_type: str
    flags: int
    stream_id: int
    payload: bytes
    fragment: bytes


@dataclass
class HeaderBlockRecord:
    stream_id: int
    started_at: float
    settings_version: int
    frames: list[CapturedHeaderFrame]
    block: bytes
    fields: list[DecodedField]
    representations: list[Representation]
    completed: bool
    local_length: int = 0
    local_sha256: str = ""
    byte_equal: bool = False

    def is_inference_request(self) -> bool:
        values: dict[bytes, list[bytes]] = collections.defaultdict(list)
        for item in self.fields:
            values[item.name.lower()].append(item.value)
        method = values.get(b":method", [b""])[0]
        path = values.get(b":path", [b""])[0]
        names = set(values)
        return (
            method == b"POST"
            and path == b"/v1/responses"
            and b"authorization" in names
            and b"x-grok-client-version" in names
        )


@dataclass
class PendingHeaderBlock:
    stream_id: int
    started_at: float
    settings_version: int
    end_stream: bool
    frames: list[CapturedHeaderFrame] = field(default_factory=list)
    fragments: list[bytes] = field(default_factory=list)


def decode_integer(data: bytes, offset: int, prefix_bits: int) -> tuple[int, int]:
    if offset >= len(data):
        raise CaptureError("truncated HPACK integer")
    mask = (1 << prefix_bits) - 1
    value = data[offset] & mask
    offset += 1
    if value < mask:
        return value, offset
    shift = 0
    while True:
        if offset >= len(data) or shift > 56:
            raise CaptureError("invalid HPACK integer")
        current = data[offset]
        offset += 1
        value += (current & 0x7F) << shift
        if current & 0x80 == 0:
            return value, offset
        shift += 7


def skip_hpack_string(data: bytes, offset: int) -> tuple[bool, int, int]:
    if offset >= len(data):
        raise CaptureError("truncated HPACK string")
    huffman = bool(data[offset] & 0x80)
    length, next_offset = decode_integer(data, offset, 7)
    end = next_offset + length
    if end > len(data):
        raise CaptureError("truncated HPACK string bytes")
    return huffman, length, end


def analyze_hpack(block: bytes) -> list[Representation]:
    result: list[Representation] = []
    offset = 0
    while offset < len(block):
        first = block[offset]
        if first & 0x80:
            index, offset = decode_integer(block, offset, 7)
            result.append(Representation("indexed", index=index))
            continue
        if first & 0x40:
            kind, prefix = "literal_incremental", 6
        elif first & 0x20:
            size, offset = decode_integer(block, offset, 5)
            result.append(Representation("table_size_update", index=size))
            continue
        elif first & 0x10:
            kind, prefix = "literal_never_indexed", 4
        else:
            kind, prefix = "literal_without_indexing", 4

        name_index, offset = decode_integer(block, offset, prefix)
        name_huffman: bool | None = None
        name_length = 0
        if name_index == 0:
            name_huffman, name_length, offset = skip_hpack_string(block, offset)
        value_huffman, value_length, offset = skip_hpack_string(block, offset)
        result.append(
            Representation(
                kind,
                name_index=name_index,
                name_huffman=name_huffman,
                value_huffman=value_huffman,
                encoded_name_length=name_length,
                encoded_value_length=value_length,
            )
        )
    return result


def header_fragment(frame: H2Frame) -> bytes:
    if frame.frame_type == FRAME_CONTINUATION:
        return frame.payload
    if frame.frame_type != FRAME_HEADERS:
        raise CaptureError("frame is not HEADERS/CONTINUATION")
    offset = 0
    padding = 0
    if frame.flags & FLAG_PADDED:
        if not frame.payload:
            raise CaptureError("PADDED HEADERS has no pad length")
        padding = frame.payload[0]
        offset = 1
    if frame.flags & FLAG_PRIORITY:
        offset += 5
    if offset > len(frame.payload) or padding > len(frame.payload) - offset:
        raise CaptureError("invalid HEADERS padding/priority")
    return frame.payload[offset : len(frame.payload) - padding]


class ConnectionCapture:
    def __init__(self, connection_id: str, certificate_sha256: str, alpn: str) -> None:
        self.connection_id = connection_id
        self.certificate_sha256 = certificate_sha256
        self.alpn = alpn
        self.lock = threading.RLock()
        self.preface = b""
        self.client_settings: list[tuple[int, int]] = []
        self.peer_table_sizes: list[int] = []
        self.settings_ack = False
        self.goaway_observed = False
        self.close_observed = False
        self.records: list[HeaderBlockRecord] = []
        self.errors: list[str] = []
        self._pending: PendingHeaderBlock | None = None
        self._decoder = Decoder(max_header_list_size=16 * 1024 * 1024)
        self._client_parser = H2FrameParser(True, self._set_preface, self._on_client_frame)
        self._server_parser = H2FrameParser(False, lambda _: None, self._on_server_frame)

    def feed_client(self, data: bytes) -> None:
        self._client_parser.feed(data)

    def feed_server(self, data: bytes) -> None:
        self._server_parser.feed(data)

    def mark_closed(self) -> None:
        with self.lock:
            self.close_observed = True

    def _set_preface(self, preface: bytes) -> None:
        with self.lock:
            self.preface = preface

    def _on_server_frame(self, frame: H2Frame) -> None:
        with self.lock:
            if frame.frame_type == FRAME_SETTINGS and not (frame.flags & FLAG_ACK):
                for setting_id, value in parse_settings(frame.payload):
                    if setting_id == SETTING_HEADER_TABLE_SIZE:
                        self.peer_table_sizes.append(value)
                        self._decoder.max_allowed_table_size = value
            elif frame.frame_type == FRAME_GOAWAY:
                self.goaway_observed = True

    def _on_client_frame(self, frame: H2Frame) -> None:
        with self.lock:
            if frame.frame_type == FRAME_SETTINGS:
                if frame.flags & FLAG_ACK:
                    self.settings_ack = True
                else:
                    self.client_settings.extend(parse_settings(frame.payload))
                return
            if frame.frame_type == FRAME_GOAWAY:
                self.goaway_observed = True
                return
            if frame.frame_type == FRAME_DATA and frame.flags & FLAG_END_STREAM:
                self._mark_stream_complete(frame.stream_id)
                return
            if frame.frame_type == FRAME_HEADERS:
                if self._pending is not None:
                    raise CaptureError("interleaved HEADERS while CONTINUATION is pending")
                fragment = header_fragment(frame)
                pending = PendingHeaderBlock(
                    stream_id=frame.stream_id,
                    started_at=time.monotonic(),
                    settings_version=len(self.peer_table_sizes),
                    end_stream=bool(frame.flags & FLAG_END_STREAM),
                )
                pending.fragments.append(fragment)
                pending.frames.append(captured_header_frame(frame, fragment))
                if frame.flags & FLAG_END_HEADERS:
                    self._finish_block(pending)
                else:
                    self._pending = pending
                return
            if frame.frame_type == FRAME_CONTINUATION:
                if self._pending is None or self._pending.stream_id != frame.stream_id:
                    raise CaptureError("unexpected CONTINUATION")
                fragment = header_fragment(frame)
                self._pending.fragments.append(fragment)
                self._pending.frames.append(captured_header_frame(frame, fragment))
                if frame.flags & FLAG_END_HEADERS:
                    pending = self._pending
                    self._pending = None
                    self._finish_block(pending)

    def _finish_block(self, pending: PendingHeaderBlock) -> None:
        block = b"".join(pending.fragments)
        decoded = self._decoder.decode(block, raw=True)
        fields = [
            DecodedField(bytes(item[0]), bytes(item[1]), not item.indexable)
            for item in decoded
        ]
        representations = analyze_hpack(block)
        field_representations = [r for r in representations if r.kind != "table_size_update"]
        if len(field_representations) != len(fields):
            raise CaptureError("HPACK representation/header count mismatch")
        self.records.append(
            HeaderBlockRecord(
                stream_id=pending.stream_id,
                started_at=pending.started_at,
                settings_version=pending.settings_version,
                frames=pending.frames,
                block=block,
                fields=fields,
                representations=representations,
                completed=pending.end_stream,
            )
        )

    def _mark_stream_complete(self, stream_id: int) -> None:
        for record in reversed(self.records):
            if record.stream_id == stream_id:
                record.completed = True
                return


def parse_settings(payload: bytes) -> list[tuple[int, int]]:
    if len(payload) % 6:
        raise CaptureError("invalid SETTINGS payload length")
    return [
        (int.from_bytes(payload[i : i + 2], "big"), int.from_bytes(payload[i + 2 : i + 6], "big"))
        for i in range(0, len(payload), 6)
    ]


def captured_header_frame(frame: H2Frame, fragment: bytes) -> CapturedHeaderFrame:
    return CapturedHeaderFrame(
        frame_type="HEADERS" if frame.frame_type == FRAME_HEADERS else "CONTINUATION",
        flags=frame.flags,
        stream_id=frame.stream_id,
        payload=frame.payload,
        fragment=fragment,
    )


class CertificateAuthority:
    def __init__(self, directory: Path, common_name: str) -> None:
        self.directory = directory
        self._lock = threading.Lock()
        self._contexts: dict[tuple[str, str], ssl.SSLContext] = {}
        self._key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        now = dt.datetime.now(dt.timezone.utc)
        subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, common_name)])
        self.cert = (
            x509.CertificateBuilder()
            .subject_name(subject)
            .issuer_name(subject)
            .public_key(self._key.public_key())
            .serial_number(x509.random_serial_number())
            .not_valid_before(now - dt.timedelta(minutes=5))
            .not_valid_after(now + dt.timedelta(days=2))
            .add_extension(x509.BasicConstraints(ca=True, path_length=0), critical=True)
            .add_extension(x509.SubjectKeyIdentifier.from_public_key(self._key.public_key()), critical=False)
            .add_extension(
                x509.AuthorityKeyIdentifier.from_issuer_public_key(self._key.public_key()),
                critical=False,
            )
            .add_extension(
                x509.KeyUsage(
                    digital_signature=True,
                    key_encipherment=False,
                    content_commitment=False,
                    data_encipherment=False,
                    key_agreement=False,
                    key_cert_sign=True,
                    crl_sign=True,
                    encipher_only=False,
                    decipher_only=False,
                ),
                critical=True,
            )
            .sign(self._key, hashes.SHA256())
        )
        self.ca_path = directory / "capture-ca.pem"
        self.ca_path.write_bytes(self.cert.public_bytes(serialization.Encoding.PEM))

    def server_context(self, hostname: str, alpn: str) -> ssl.SSLContext:
        cache_key = (hostname, alpn)
        with self._lock:
            cached = self._contexts.get(cache_key)
            if cached is not None:
                return cached
            context = self._create_server_context(hostname, alpn)
            self._contexts[cache_key] = context
            return context

    def _create_server_context(self, hostname: str, alpn: str) -> ssl.SSLContext:
        now = dt.datetime.now(dt.timezone.utc)
        leaf_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, hostname)])
        try:
            san_name: x509.GeneralName = x509.IPAddress(ipaddress.ip_address(hostname))
        except ValueError:
            san_name = x509.DNSName(hostname)
        cert = (
            x509.CertificateBuilder()
            .subject_name(subject)
            .issuer_name(self.cert.subject)
            .public_key(leaf_key.public_key())
            .serial_number(x509.random_serial_number())
            .not_valid_before(now - dt.timedelta(minutes=5))
            .not_valid_after(now + dt.timedelta(days=1))
            .add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True)
            .add_extension(x509.SubjectKeyIdentifier.from_public_key(leaf_key.public_key()), critical=False)
            .add_extension(
                x509.AuthorityKeyIdentifier.from_issuer_public_key(self._key.public_key()),
                critical=False,
            )
            .add_extension(x509.SubjectAlternativeName([san_name]), critical=False)
            .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH]), critical=False)
            .sign(self._key, hashes.SHA256())
        )
        tag = sha256_hex(f"{hostname}\0{alpn}".encode())[:16]
        cert_path = self.directory / f"leaf-{tag}.pem"
        key_path = self.directory / f"leaf-{tag}.key"
        cert_path.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
        key_path.write_bytes(
            leaf_key.private_bytes(
                serialization.Encoding.PEM,
                serialization.PrivateFormat.PKCS8,
                serialization.NoEncryption(),
            )
        )
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        context.set_alpn_protocols([alpn])
        context.load_cert_chain(cert_path, key_path)
        return context


class TransparentConnectProxy:
    def __init__(self, directory: Path, upstream_ca: Path | None = None) -> None:
        self.authority = CertificateAuthority(directory, "sub2api ephemeral wire capture")
        self.upstream_ca = upstream_ca
        self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen(64)
        self.listener.settimeout(0.25)
        self.port = self.listener.getsockname()[1]
        self._stop = threading.Event()
        self._accept_thread: threading.Thread | None = None
        self._threads: list[threading.Thread] = []
        self._sockets: set[socket.socket] = set()
        self._lock = threading.Lock()
        self.connections: list[ConnectionCapture] = []
        self.errors: list[str] = []

    def start(self) -> None:
        self._accept_thread = threading.Thread(target=self._accept_loop, name="capture-accept", daemon=True)
        self._accept_thread.start()

    def stop(self) -> None:
        self._stop.set()
        try:
            self.listener.close()
        except OSError:
            pass
        with self._lock:
            sockets = list(self._sockets)
        for sock in sockets:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                sock.close()
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

    def _handle(self, raw_client: socket.socket) -> None:
        raw_upstream: socket.socket | None = None
        client_tls: ssl.SSLSocket | None = None
        upstream_tls: ssl.SSLSocket | None = None
        capture: ConnectionCapture | None = None
        try:
            hostname, port = read_connect_request(raw_client)
            raw_upstream = socket.create_connection((hostname, port), timeout=20)
            upstream_context = ssl.create_default_context(cafile=str(self.upstream_ca) if self.upstream_ca else None)
            upstream_context.set_alpn_protocols(["h2", "http/1.1"])
            upstream_tls = upstream_context.wrap_socket(raw_upstream, server_hostname=hostname)
            alpn = upstream_tls.selected_alpn_protocol() or "http/1.1"
            peer_certificate = upstream_tls.getpeercert(binary_form=True) or b""
            raw_client.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            server_context = self.authority.server_context(hostname, alpn)
            client_tls = server_context.wrap_socket(raw_client, server_side=True)
            if client_tls.selected_alpn_protocol() != alpn:
                raise CaptureError("downstream/upstream ALPN mismatch")
            if alpn == "h2":
                capture = ConnectionCapture(
                    connection_id=f"connection-{id(client_tls)}-{time.monotonic_ns()}",
                    certificate_sha256=sha256_hex(peer_certificate),
                    alpn=alpn,
                )
                with self._lock:
                    self.connections.append(capture)
            with self._lock:
                self._sockets.update((client_tls, upstream_tls))

            server_thread = threading.Thread(
                target=self._relay,
                args=(upstream_tls, client_tls, capture.feed_server if capture else None, capture),
                daemon=True,
            )
            server_thread.start()
            self._relay(client_tls, upstream_tls, capture.feed_client if capture else None, capture)
            server_thread.join(timeout=3)
        except Exception as error:  # noqa: BLE001 - error text is deliberately not persisted
            with self._lock:
                self.errors.append(type(error).__name__)
        finally:
            if capture is not None:
                capture.mark_closed()
            for sock in (client_tls, upstream_tls, raw_client, raw_upstream):
                if sock is None:
                    continue
                with self._lock:
                    self._sockets.discard(sock)
                try:
                    sock.close()
                except OSError:
                    pass

    def _relay(
        self,
        source: ssl.SSLSocket,
        destination: ssl.SSLSocket,
        observer: Callable[[bytes], None] | None,
        capture: ConnectionCapture | None,
    ) -> None:
        try:
            while not self._stop.is_set():
                data = source.recv(64 * 1024)
                if not data:
                    return
                if observer is not None:
                    observer(data)
                destination.sendall(data)
        except (OSError, ssl.SSLError, CaptureError) as error:
            if capture is not None and not self._stop.is_set():
                with capture.lock:
                    capture.errors.append(type(error).__name__)
        finally:
            try:
                destination.shutdown(socket.SHUT_WR)
            except OSError:
                pass


def read_connect_request(client: socket.socket) -> tuple[str, int]:
    client.settimeout(20)
    data = bytearray()
    while b"\r\n\r\n" not in data:
        chunk = client.recv(4096)
        if not chunk:
            raise CaptureError("proxy client closed before CONNECT")
        data.extend(chunk)
        if len(data) > MAX_CONNECT_HEADER:
            raise CaptureError("CONNECT header too large")
    header, remainder = bytes(data).split(b"\r\n\r\n", 1)
    if remainder:
        raise CaptureError("unexpected bytes following CONNECT request")
    first_line = header.split(b"\r\n", 1)[0]
    parts = first_line.split()
    if len(parts) != 3 or parts[0].upper() != b"CONNECT":
        raise CaptureError("only CONNECT proxy requests are accepted")
    authority = parts[1].decode("ascii")
    if authority.startswith("["):
        close = authority.find("]")
        if close < 0:
            raise CaptureError("invalid IPv6 CONNECT authority")
        hostname = authority[1:close]
        port_text = authority[close + 2 :] if authority[close + 1 : close + 2] == ":" else "443"
    elif ":" in authority:
        hostname, port_text = authority.rsplit(":", 1)
    else:
        hostname, port_text = authority, "443"
    port = int(port_text)
    if not hostname or not (1 <= port <= 65535):
        raise CaptureError("invalid CONNECT target")
    return hostname, port


class GoEncoderBridge:
    def __init__(self, backend: Path, temp_dir: Path) -> None:
        suffix = ".exe" if os.name == "nt" else ""
        executable = temp_dir / f"grok-wire-encoder{suffix}"
        package = "./internal/pkg/grokhttp2/testdata/official-wire-capture/go-encoder"
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
            raise CaptureError("failed to build in-memory comparison helper")
        self.process = subprocess.Popen(
            [str(executable)],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            encoding="utf-8",
            bufsize=1,
        )

    def set_table_size(self, size: int) -> None:
        self._request({"op": "set_table_size", "size": size})

    def encode(self, fields: list[DecodedField]) -> tuple[bytes, dict[str, Any]]:
        result = self._request(
            {
                "op": "encode",
                "fields": [
                    {
                        "name_b64": base64.b64encode(item.name).decode("ascii"),
                        "value_b64": base64.b64encode(item.value).decode("ascii"),
                        "sensitive": item.sensitive,
                    }
                    for item in fields
                ],
            }
        )
        try:
            block = base64.b64decode(result.pop("block_b64"), validate=True)
        except Exception as error:  # noqa: BLE001
            raise CaptureError("invalid comparison helper response") from error
        return block, result

    def close(self) -> None:
        if self.process.poll() is None:
            try:
                self._request({"op": "close"})
            except CaptureError:
                pass
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)

    def _request(self, value: dict[str, Any]) -> dict[str, Any]:
        if self.process.stdin is None or self.process.stdout is None:
            raise CaptureError("comparison helper pipe unavailable")
        self.process.stdin.write(json_line(value) + "\n")
        self.process.stdin.flush()
        line = self.process.stdout.readline()
        if not line:
            raise CaptureError("comparison helper exited unexpectedly")
        response = json.loads(line)
        if not response.get("ok"):
            raise CaptureError("comparison helper rejected input")
        return response


class ACPClient:
    def __init__(self, command: list[str], environment: dict[str, str], cwd: Path) -> None:
        self.process = subprocess.Popen(
            command,
            cwd=cwd,
            env=environment,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            encoding="utf-8",
            errors="replace",
            bufsize=1,
        )
        self._responses: dict[int, queue.Queue[dict[str, Any]]] = {}
        self._responses_lock = threading.Lock()
        self._write_lock = threading.Lock()
        self._diagnostic_lock = threading.Lock()
        self._method_counts: collections.Counter[str] = collections.Counter()
        self._request_method_counts: collections.Counter[str] = collections.Counter()
        self._update_kind_counts: collections.Counter[str] = collections.Counter()
        self._unmatched_response_id_types: collections.Counter[str] = collections.Counter()
        self._reader = threading.Thread(target=self._read_loop, daemon=True)
        self._reader.start()

    @property
    def pid(self) -> int:
        return self.process.pid

    def request(self, request_id: int, method: str, params: dict[str, Any], timeout: float) -> dict[str, Any]:
        response_queue: queue.Queue[dict[str, Any]] = queue.Queue(maxsize=1)
        with self._responses_lock:
            self._responses[request_id] = response_queue
        self._write({"jsonrpc": "2.0", "id": request_id, "method": method, "params": params})
        try:
            response = response_queue.get(timeout=timeout)
        except queue.Empty as error:
            raise CaptureError(f"ACP {method} timed out ({self.diagnostic_summary()})") from error
        finally:
            with self._responses_lock:
                self._responses.pop(request_id, None)
        if "error" in response:
            code = response.get("error", {}).get("code", "unknown")
            raise CaptureError(f"ACP {method} failed with code {code}")
        return response.get("result", {})

    def diagnostic_summary(self) -> str:
        with self._diagnostic_lock:
            return json_line(
                {
                    "methods": dict(sorted(self._method_counts.items())),
                    "client_requests": dict(sorted(self._request_method_counts.items())),
                    "update_kinds": dict(sorted(self._update_kind_counts.items())),
                    "unmatched_response_id_types": dict(sorted(self._unmatched_response_id_types.items())),
                    "process_alive": self.process.poll() is None,
                }
            )

    def close(self) -> None:
        if self.process.stdin is not None:
            try:
                self.process.stdin.close()
            except OSError:
                pass
        try:
            self.process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            try:
                self.process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=10)
        self._reader.join(timeout=3)

    def _read_loop(self) -> None:
        if self.process.stdout is None:
            return
        for line in self.process.stdout:
            try:
                message = json.loads(line)
            except json.JSONDecodeError:
                continue
            request_id = message.get("id")
            method = message.get("method")
            if isinstance(method, str):
                with self._diagnostic_lock:
                    self._method_counts[method] += 1
                    if request_id is not None:
                        self._request_method_counts[method] += 1
                    update = message.get("params", {}).get("update", {})
                    if isinstance(update, dict) and isinstance(update.get("sessionUpdate"), str):
                        self._update_kind_counts[update["sessionUpdate"]] += 1
            normalized_id = request_id
            if isinstance(request_id, str) and request_id.isascii() and request_id.isdigit():
                normalized_id = int(request_id)
            if "method" not in message and isinstance(normalized_id, int):
                with self._responses_lock:
                    response_queue = self._responses.get(normalized_id)
                if response_queue is not None:
                    response_queue.put(message)
                else:
                    with self._diagnostic_lock:
                        self._unmatched_response_id_types[type(request_id).__name__] += 1
            elif "method" in message and request_id is not None:
                if method == "session/request_permission":
                    self._answer_permission_request(request_id, message.get("params", {}))
                else:
                    self._write(
                        {
                            "jsonrpc": "2.0",
                            "id": request_id,
                            "error": {"code": -32601, "message": "unsupported by capture client"},
                        }
                    )

    def _answer_permission_request(self, request_id: Any, params: Any) -> None:
        options = params.get("options", []) if isinstance(params, dict) else []
        selected: Any = None
        if isinstance(options, list):
            for option in options:
                if isinstance(option, dict) and option.get("kind") in ("allow_once", "allow_always"):
                    selected = option.get("optionId")
                    break
            if selected is None and options and isinstance(options[0], dict):
                selected = options[0].get("optionId")
        if selected is None:
            outcome: dict[str, Any] = {"outcome": "cancelled"}
        else:
            outcome = {"outcome": "selected", "optionId": selected}
        self._write({"jsonrpc": "2.0", "id": request_id, "result": {"outcome": outcome}})

    def _write(self, value: dict[str, Any]) -> None:
        if self.process.stdin is None:
            raise CaptureError("ACP stdin unavailable")
        with self._write_lock:
            self.process.stdin.write(json_line(value) + "\n")
            self.process.stdin.flush()


def auth_method_ids(initialize_result: dict[str, Any]) -> list[str]:
    result: list[str] = []
    methods = initialize_result.get("authMethods", [])
    if not isinstance(methods, list):
        return result
    for method in methods:
        if not isinstance(method, dict):
            continue
        identifier = method.get("id")
        if isinstance(identifier, str):
            result.append(identifier)
        elif isinstance(identifier, dict) and isinstance(identifier.get("id"), str):
            result.append(identifier["id"])
    return result


def run_two_prompts(
    grok_binary: Path,
    proxy: Any,
    workspace: Path,
    between_prompts: Callable[[int], None] | None = None,
) -> tuple[int, str, list[tuple[float, float]]]:
    environment = os.environ.copy()
    proxy_url = f"http://127.0.0.1:{proxy.port}"
    for key in ("HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"):
        environment[key] = proxy_url
    for key in ("NO_PROXY", "no_proxy"):
        environment[key] = ""
    authority = getattr(proxy, "authority", None)
    if authority is not None:
        environment["SSL_CERT_FILE"] = str(authority.ca_path)
    command = [
        str(grok_binary),
        "agent",
        "--always-approve",
        "--no-leader",
        "--model",
        "grok-4.5",
        "stdio",
    ]
    client = ACPClient(command, environment, workspace)
    intervals: list[tuple[float, float]] = []
    session_id = ""
    try:
        initialized = client.request(
            1,
            "initialize",
            {
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
                    "clientType": "sub2api-wire-capture",
                    "clientVersion": "1",
                },
            },
            timeout=90,
        )
        methods = auth_method_ids(initialized)
        if "cached_token" in methods:
            client.request(
                2,
                "authenticate",
                {"methodId": "cached_token", "_meta": {"headless": True}},
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
            raise CaptureError("ACP session/new returned no session id")
        prompts = (
            "Reply with exactly WIRE_ONE. Do not use tools.",
            "Reply with exactly WIRE_TWO. Do not use tools.",
        )
        for offset, prompt in enumerate(prompts, start=4):
            started_at = time.monotonic()
            client.request(
                offset,
                "session/prompt",
                {
                    "sessionId": session_id,
                    "prompt": [{"type": "text", "text": prompt}],
                },
                timeout=240,
            )
            intervals.append((started_at, time.monotonic()))
            if between_prompts is not None and offset < 5:
                between_prompts(offset - 3)
        return client.pid, session_id, intervals
    finally:
        client.close()


def compare_connection(connection: ConnectionCapture, backend: Path, temp_dir: Path) -> None:
    bridge = GoEncoderBridge(backend, temp_dir)
    try:
        applied_settings = 0
        with connection.lock:
            records = list(connection.records)
            table_sizes = list(connection.peer_table_sizes)
        for record in records:
            while applied_settings < record.settings_version:
                bridge.set_table_size(table_sizes[applied_settings])
                applied_settings += 1
            local_block, result = bridge.encode(record.fields)
            record.local_length = int(result["length"])
            record.local_sha256 = str(result["sha256"])
            record.byte_equal = local_block == record.block
    finally:
        bridge.close()


def select_target_records(
    connections: list[ConnectionCapture], intervals: list[tuple[float, float]]
) -> tuple[ConnectionCapture, list[HeaderBlockRecord]]:
    groups: list[list[tuple[ConnectionCapture, HeaderBlockRecord]]] = []
    for started_at, completed_at in intervals:
        candidates: list[tuple[ConnectionCapture, HeaderBlockRecord]] = []
        for connection in connections:
            with connection.lock:
                candidates.extend(
                    (connection, record)
                    for record in connection.records
                    if started_at <= record.started_at <= completed_at and record.is_inference_request()
                )
        if not candidates:
            raise CaptureError("no inference HEADERS block observed after prompt")
        groups.append(sorted(candidates, key=lambda item: item[1].started_at))

    adjacent = [
        (first, second)
        for first in groups[0]
        for second in groups[1]
        if first[0] is second[0] and second[1].stream_id == first[1].stream_id + 2
    ]
    if not adjacent:
        safe_streams = [[item[1].stream_id for item in group] for group in groups]
        raise CaptureError(f"no consecutive prompt stream pair among candidates {safe_streams}")
    first, second = max(adjacent, key=lambda pair: pair[0][1].started_at)
    connection = first[0]
    records = [first[1], second[1]]
    if any(record.stream_id == 0 or record.stream_id % 2 == 0 for record in records):
        raise CaptureError("invalid client request stream IDs")
    return connection, records


def representation_summary(record: HeaderBlockRecord) -> dict[str, Any]:
    fields_iter = iter(record.fields)
    branch_rows: list[dict[str, Any]] = []
    kinds: collections.Counter[str] = collections.Counter()
    dynamic_references = 0
    literal_nonempty_huffman = True
    sensitive_never_index = 0
    skip_without_index = 0
    skip_total = 0

    for representation in record.representations:
        kinds[representation.kind] += 1
        if representation.kind == "table_size_update":
            branch_rows.append({"kind": representation.kind, "size": representation.index})
            continue
        decoded = next(fields_iter)
        if representation.index > HPACK_STATIC_TABLE_LENGTH or representation.name_index > HPACK_STATIC_TABLE_LENGTH:
            dynamic_references += 1
        if representation.kind.startswith("literal_"):
            if representation.name_index == 0 and decoded.name and representation.name_huffman is not True:
                literal_nonempty_huffman = False
            if decoded.value and representation.value_huffman is not True:
                literal_nonempty_huffman = False
        if decoded.sensitive:
            if representation.kind == "literal_never_indexed":
                sensitive_never_index += 1
            else:
                literal_nonempty_huffman = False
        if decoded.name.lower() in SKIP_VALUE_INDEX:
            skip_total += 1
            if representation.kind in ("literal_without_indexing", "literal_never_indexed"):
                skip_without_index += 1
            elif representation.kind == "indexed" and representation.index <= HPACK_STATIC_TABLE_LENGTH:
                skip_without_index += 1
        branch_rows.append(
            {
                "kind": representation.kind,
                "index_class": (
                    "dynamic"
                    if representation.index > HPACK_STATIC_TABLE_LENGTH
                    or representation.name_index > HPACK_STATIC_TABLE_LENGTH
                    else "static_or_new"
                ),
                "new_name": representation.name_index == 0 and representation.kind != "indexed",
                "name_huffman": representation.name_huffman,
                "value_huffman": representation.value_huffman,
                "sensitive": decoded.sensitive,
                "skip_name": decoded.name.lower() in SKIP_VALUE_INDEX,
            }
        )
    fingerprint = sha256_hex(json_line(branch_rows).encode("ascii"))
    return {
        "representation_counts": dict(sorted(kinds.items())),
        "branch_fingerprint_sha256": fingerprint,
        "dynamic_reference_count": dynamic_references,
        "literal_nonempty_all_huffman": literal_nonempty_huffman,
        "sensitive_never_index_count": sensitive_never_index,
        "skip_field_count": skip_total,
        "skip_without_or_never_index_count": skip_without_index,
        "skip_policy_aligned": skip_total == skip_without_index,
    }


def request_report(record: HeaderBlockRecord) -> dict[str, Any]:
    result = {
        "stream_id": record.stream_id,
        "completed": record.completed,
        "decoded_header_count": len(record.fields),
        "frame_count": len(record.frames),
        "frames": [
            {
                "type": frame.frame_type,
                "flags": frame.flags,
                "payload_length": len(frame.payload),
                "fragment_length": len(frame.fragment),
                "payload_sha256": sha256_hex(frame.payload),
            }
            for frame in record.frames
        ],
        "official_header_block_length": len(record.block),
        "official_header_block_sha256": sha256_hex(record.block),
        "local_header_block_length": record.local_length,
        "local_header_block_sha256": record.local_sha256,
        "byte_equal": record.byte_equal,
    }
    result.update(representation_summary(record))
    return result


def extract_ascii_strings(binary: bytes, minimum: int = 8) -> list[bytes]:
    return [match.group(0) for match in re.finditer(rb"[ -~]{%d,}" % minimum, binary)]


def audit_binary(grok_binary: Path) -> dict[str, Any]:
    binary = grok_binary.read_bytes()
    strings = extract_ascii_strings(binary)
    joined = b"\n".join(strings)
    component_patterns = {
        "reqwest": rb"/reqwest-([0-9]+\.[0-9]+\.[0-9]+)/",
        "hyper": rb"/hyper-([0-9]+\.[0-9]+\.[0-9]+)/",
        "hyper-util": rb"/hyper-util-([0-9]+\.[0-9]+\.[0-9]+)/",
        "h2": rb"/h2-([0-9]+\.[0-9]+\.[0-9]+)/",
        "http": rb"/http-([0-9]+\.[0-9]+\.[0-9]+)/",
    }
    versions: dict[str, list[str]] = {}
    for component, pattern in component_patterns.items():
        versions[component] = sorted({value.decode("ascii") for value in re.findall(pattern, joined)})

    h2_anchor_pattern = re.compile(
        rb"/\.cargo/registry/src/index\.crates\.io-[^/]+/h2-0\.4\.15/src/"
        rb"([A-Za-z0-9_./-]+\.rs)(?::([0-9]+))?"
    )
    anchors = [(m.group(1).decode("ascii"), int(m.group(2) or 0)) for m in h2_anchor_pattern.finditer(joined)]
    unique_files = sorted({path for path, _ in anchors})
    line_anchor_count = sum(1 for _, line in anchors if line > 0)

    registry_roots = list((Path.home() / ".cargo" / "registry" / "src").glob("*"))
    local_h2 = next((root / "h2-0.4.15" for root in registry_roots if (root / "h2-0.4.15").is_dir()), None)
    anchors_valid: bool | None = None
    if local_h2 is not None:
        anchors_valid = True
        for relative, line in anchors:
            source = local_h2 / "src" / relative
            if not source.is_file():
                anchors_valid = False
                break
            if line and line > len(source.read_text(encoding="utf-8").splitlines()):
                anchors_valid = False
                break

    public_lock_url = (
        "https://raw.githubusercontent.com/xai-org/grok-build/"
        f"{PUBLIC_SYNC_COMMIT}/Cargo.lock"
    )
    public_lock = urllib.request.urlopen(public_lock_url, timeout=30).read()
    public_lock_hash = sha256_hex(public_lock)
    parsed_lock = tomllib.loads(public_lock.decode("utf-8"))
    h2_packages = [
        package
        for package in parsed_lock.get("package", [])
        if package.get("name") == "h2" and package.get("version") == "0.4.15"
    ]
    public_h2_registry_locked = bool(
        h2_packages
        and str(h2_packages[0].get("source", "")).startswith("registry+")
        and h2_packages[0].get("checksum") == H2_CRATE_SHA256
    )

    git_h2_markers = sum(
        1
        for value in strings
        if b"git+" in value.lower() and re.search(rb"(?:^|[/_-])h2(?:[/_.-]|$)", value.lower())
    )
    path_evidence = bool(anchors) and versions.get("h2") == ["0.4.15"]
    return {
        "binary_sha256": sha256_hex(binary),
        "expected_binary_sha256_match": sha256_hex(binary) == EXPECTED_BINARY_SHA256,
        "embedded_component_versions": versions,
        "h2_crates_io_registry_path_observed": path_evidence,
        "h2_source_file_anchor_count": len(anchors),
        "h2_unique_source_file_count": len(unique_files),
        "h2_line_anchor_count": line_anchor_count,
        "h2_anchors_valid_against_local_crates_io_source": anchors_valid,
        "embedded_git_h2_source_marker_count": git_h2_markers,
        "public_sync_commit": PUBLIC_SYNC_COMMIT,
        "public_cargo_lock_sha256": public_lock_hash,
        "public_cargo_lock_sha256_match": public_lock_hash == PUBLIC_LOCK_SHA256,
        "public_lock_uses_registry_h2_0_4_15": public_h2_registry_locked,
        "h2_crate_checksum": H2_CRATE_SHA256,
        "private_patch_absence_proven": False,
        "replacement_or_patch_evidence_found": not (
            path_evidence
            and git_h2_markers == 0
            and public_lock_hash == PUBLIC_LOCK_SHA256
            and public_h2_registry_locked
            and anchors_valid is not False
        ),
    }


def validate_capture(
    connection: ConnectionCapture,
    targets: list[HeaderBlockRecord],
) -> None:
    if connection.alpn != "h2" or connection.preface != CLIENT_PREFACE:
        raise CaptureError("capture did not negotiate a valid HTTP/2 connection")
    if not connection.client_settings or not connection.settings_ack:
        raise CaptureError("HTTP/2 SETTINGS lifecycle is incomplete")
    if not connection.close_observed and not connection.goaway_observed:
        raise CaptureError("HTTP/2 connection lifecycle is incomplete")
    if len(targets) != 2 or any(not item.completed for item in targets):
        raise CaptureError("two completed target requests are required")
    if any(not item.byte_equal for item in targets):
        raise CaptureError("official and local target HPACK bytes differ")
    with connection.lock:
        if any(not item.byte_equal for item in connection.records):
            raise CaptureError("an earlier header block on the target connection differs")


def build_report(
    grok_binary: Path,
    version: str,
    process_id: int,
    session_id: str,
    run_nonce: bytes,
    connection: ConnectionCapture,
    targets: list[HeaderBlockRecord],
    audit: dict[str, Any],
    config_hash_before: str,
    config_hash_after: str,
    parent_env_before: dict[str, str | None],
    parent_env_after: dict[str, str | None],
) -> dict[str, Any]:
    target_reports = [request_report(record) for record in targets]
    with connection.lock:
        compared_count = len(connection.records)
        all_equal = all(record.byte_equal for record in connection.records)
    scenario_patch = all_equal and all(item["byte_equal"] for item in target_reports)
    return {
        "schema_version": 1,
        "status": "OFFICIAL-WIRE-ALIGNED" if scenario_patch else "OFFICIAL-WIRE-MISMATCH",
        "captured_at_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
        "scope": {
            "official_process_count": 1,
            "official_session_count": 1,
            "official_connection_count": 1,
            "target_request_count": 2,
            "same_process": True,
            "same_session": True,
            "same_connection": True,
            "consecutive_odd_streams": targets[1].stream_id == targets[0].stream_id + 2,
        },
        "official_binary": {
            "version": version,
            "sha256": sha256_hex(grok_binary.read_bytes()),
            "process_hash": short_bound_hash(run_nonce, str(process_id)),
        },
        "bindings": {
            "connection_hash": short_bound_hash(run_nonce, connection.connection_id),
            "session_hash": short_bound_hash(run_nonce, session_id),
        },
        "transport": {
            "alpn": connection.alpn,
            "upstream_certificate_sha256": connection.certificate_sha256,
            "client_preface_exact": connection.preface == CLIENT_PREFACE,
            "client_settings": [
                {"id": setting_id, "value": value}
                for setting_id, value in connection.client_settings
            ],
            "peer_header_table_size_updates": list(connection.peer_table_sizes),
            "settings_ack": connection.settings_ack,
            "goaway_observed": connection.goaway_observed,
            "close_observed": connection.close_observed,
            "stream_ids": [record.stream_id for record in targets],
        },
        "requests": target_reports,
        "parity": {
            "all_header_blocks_on_target_connection_compared": compared_count,
            "all_header_blocks_on_target_connection_equal": all_equal,
            "target_header_blocks_equal": all(item["byte_equal"] for item in target_reports),
            "huffman_branch_aligned": all(item["literal_nonempty_all_huffman"] for item in target_reports),
            "dynamic_table_reuse_observed": target_reports[1]["dynamic_reference_count"] > 0,
            "sensitive_never_index_observed": any(
                item["sensitive_never_index_count"] > 0 for item in target_reports
            ),
            "skip_policy_aligned": all(item["skip_policy_aligned"] for item in target_reports),
        },
        "binary_audit": {
            **audit,
            "declared_scenario_wire_affecting_patch_observed": not scenario_patch,
            "verdict": (
                "NO_PATCH_EVIDENCE_AND_DECLARED_SCENARIO_WIRE_ALIGNED"
                if not audit["replacement_or_patch_evidence_found"] and scenario_patch
                else "REVIEW_REQUIRED"
            ),
        },
        "safety": {
            "raw_headers_persisted": False,
            "decoded_headers_persisted": False,
            "authorization_persisted": False,
            "request_or_response_body_persisted": False,
            "production_endpoint_persisted": False,
            "config_sha256_before": config_hash_before,
            "config_sha256_after": config_hash_after,
            "config_unchanged": config_hash_before == config_hash_after,
            "parent_proxy_environment_unchanged": parent_env_before == parent_env_after,
            "windows_root_store_modified": False,
            "temporary_ca_deleted_on_exit": True,
        },
        "limitations": {
            "private_patch_absence_is_not_mathematically_provable_from_stripped_binary": True,
            "wire_conclusion_is_limited_to_the_two_declared_requests_and_preceding_blocks_on_the_same_connection": True,
            "repository_report_cannot_reconstruct_credentials_or_raw_header_blocks": True,
        },
    }


def safe_write_report(path: Path, report: dict[str, Any]) -> None:
    serialized = json.dumps(report, ensure_ascii=True, indent=2, sort_keys=True) + "\n"
    forbidden = (
        "authorization",
        "bearer ",
        "access_token",
        "refresh_token",
        "id_token",
        "cli-chat-proxy",
        "api.x.ai",
    )
    lowered = serialized.lower()
    # The boolean safety key is allowed; no raw authorization header/value is.
    lowered_without_safety_key = lowered.replace('"authorization_persisted"', '"credential_persisted"')
    if any(value in lowered_without_safety_key for value in forbidden):
        raise CaptureError("derived report failed secret/endpoint denylist")
    path.write_text(serialized, encoding="ascii", newline="\n")


def file_sha256(path: Path) -> str:
    return sha256_hex(path.read_bytes())


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
    audit = audit_binary(grok_binary)
    run_nonce = os.urandom(32)

    with tempfile.TemporaryDirectory(prefix="sub2api-grok-wire-") as temp_name:
        temp_dir = Path(temp_name)
        workspace = temp_dir / "empty-workspace"
        workspace.mkdir()
        proxy = TransparentConnectProxy(temp_dir)
        proxy.start()
        try:
            process_id, session_id, intervals = run_two_prompts(grok_binary, proxy, workspace)
            time.sleep(1)
        finally:
            proxy.stop()
        connection, targets = select_target_records(proxy.connections, intervals)
        compare_connection(connection, backend, temp_dir)
        validate_capture(connection, targets)

        config_hash_after = file_sha256(config_path)
        parent_env_after = {key: os.environ.get(key) for key in parent_keys}
        report = build_report(
            grok_binary,
            version,
            process_id,
            session_id,
            run_nonce,
            connection,
            targets,
            audit,
            config_hash_before,
            config_hash_after,
            parent_env_before,
            parent_env_after,
        )
        if config_hash_before != config_hash_after:
            raise CaptureError("official config changed during capture")
        if parent_env_before != parent_env_after:
            raise CaptureError("parent proxy environment changed during capture")
        output = Path(args.output).resolve()
        safe_write_report(output, report)
        print(f"status={report['status']}")
        print(f"stream_ids={report['transport']['stream_ids']}")
        print(f"target_byte_equal={report['parity']['target_header_blocks_equal']}")
        print(f"all_connection_blocks_equal={report['parity']['all_header_blocks_on_target_connection_equal']}")
        print(f"binary_audit={report['binary_audit']['verdict']}")
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
        default=str(Path(__file__).resolve().parent / "official-wire-report.json"),
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
