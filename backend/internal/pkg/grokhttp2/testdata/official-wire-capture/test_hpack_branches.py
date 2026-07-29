from __future__ import annotations

import socket
import ssl
from pathlib import Path
import tempfile
import time
import unittest

from h2.config import H2Configuration
from h2.connection import H2Connection
from h2.events import StreamEnded

import capture
import capture_hpack_branches as branches


def receive_until(client: socket.socket, marker: bytes) -> bytes:
    response = bytearray()
    while marker not in response:
        chunk = client.recv(4096)
        if not chunk:
            break
        response.extend(chunk)
    return bytes(response)


class LocalOnlyProxyTests(unittest.TestCase):
    def test_rejects_nonlocal_connect_without_resolving_it(self) -> None:
        proxy = branches.LocalOnlyConnectProxy(target_port=443)
        proxy.start()
        try:
            client = socket.create_connection(("127.0.0.1", proxy.port), timeout=5)
            with client:
                client.sendall(
                    b"CONNECT production.invalid:443 HTTP/1.1\r\n"
                    b"Host: production.invalid:443\r\n\r\n"
                )
                response = receive_until(client, b"\r\n\r\n")
            self.assertTrue(response.startswith(b"HTTP/1.1 403"))
            self.assertEqual(proxy.blocked_count, 1)
            self.assertEqual(proxy.accepted_count, 0)
        finally:
            proxy.stop()


class LocalH2EvidenceServerTests(unittest.TestCase):
    def test_long_path_produces_captured_continuation(self) -> None:
        synthetic_key = "xai-synthetic-hpack-unit"
        with tempfile.TemporaryDirectory(prefix="hpack-branches-test-") as name:
            server = branches.LocalH2CaptureServer(Path(name), synthetic_key)
            server.start()
            try:
                raw = socket.create_connection(("127.0.0.1", server.port), timeout=10)
                context = ssl.create_default_context(cafile=str(server.authority.ca_path))
                context.set_alpn_protocols(["h2"])
                with context.wrap_socket(raw, server_hostname="localhost") as tls:
                    protocol = H2Connection(
                        config=H2Configuration(client_side=True, header_encoding=None)
                    )
                    protocol.initiate_connection()
                    tls.sendall(protocol.data_to_send())
                    protocol.receive_data(tls.recv(64 * 1024))
                    pending = protocol.data_to_send()
                    if pending:
                        tls.sendall(pending)

                    for _ in range(2):
                        stream_id = protocol.get_next_available_stream_id()
                        protocol.send_headers(
                            stream_id,
                            [
                                (b":method", b"POST"),
                                (b":scheme", b"https"),
                                (b":authority", b"localhost"),
                                (
                                    b":path",
                                    b"/"
                                    + b"~" * branches.LONG_PATH_CHARS
                                    + b"/v1/responses",
                                ),
                                (
                                    b"authorization",
                                    b"Bearer " + synthetic_key.encode("ascii"),
                                ),
                                (b"content-length", b"0"),
                            ],
                            end_stream=True,
                        )
                        tls.sendall(protocol.data_to_send())
                        ended = False
                        deadline = time.monotonic() + 10
                        while not ended and time.monotonic() < deadline:
                            for event in protocol.receive_data(tls.recv(64 * 1024)):
                                if (
                                    isinstance(event, StreamEnded)
                                    and event.stream_id == stream_id
                                ):
                                    ended = True
                            pending = protocol.data_to_send()
                            if pending:
                                tls.sendall(pending)
                        self.assertTrue(ended)
                self.assertTrue(server.target_seen.wait(timeout=5))
            finally:
                server.stop()

            connection, targets = branches.select_targets(server.connections)
            self.assertEqual(connection.alpn, "h2")
            self.assertEqual(len(targets), 2)
            self.assertEqual([target.stream_id for target in targets], [1, 3])
            for target in targets:
                self.assertGreaterEqual(len(target.frames), 2)
                self.assertEqual(target.frames[0].frame_type, "HEADERS")
                self.assertTrue(
                    all(frame.frame_type == "CONTINUATION" for frame in target.frames[1:])
                )
            self.assertEqual(server.auth_matches, [True, True])


class TargetAnalysisTests(unittest.TestCase):
    def test_bearer_auth_builder_is_not_a_sensitive_marking_api(self) -> None:
        counts = {
            "set_sensitive_call_count": 0,
            "header_sensitive_call_count": 0,
            "bearer_auth_call_count": 1,
        }
        self.assertFalse(branches.source_uses_sensitive_marking(counts))

    def test_maps_auth_field_to_its_wire_representation(self) -> None:
        record = capture.HeaderBlockRecord(
            stream_id=1,
            started_at=0,
            settings_version=0,
            frames=[
                capture.CapturedHeaderFrame("HEADERS", 0, 1, b"", b"a" * 16384),
                capture.CapturedHeaderFrame("CONTINUATION", 4, 1, b"", b"b"),
            ],
            block=b"x" * 16385,
            fields=[
                capture.DecodedField(b":path", b"/v1/responses", False),
                capture.DecodedField(b"authorization", b"synthetic", False),
            ],
            representations=[
                capture.Representation("literal_without_indexing"),
                capture.Representation("literal_without_indexing", value_huffman=True),
            ],
            completed=True,
            local_length=16385,
            local_sha256="0" * 64,
            byte_equal=True,
        )
        report = branches.analyze_target(record)
        self.assertEqual(report["auth_header_representation"], "literal_without_indexing")
        self.assertEqual(report["continuation_count"], 1)
        self.assertTrue(report["frame_split_equal"])


if __name__ == "__main__":
    unittest.main()
