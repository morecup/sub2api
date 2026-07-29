import queue
import socket
import ssl
import tempfile
import threading
import unittest
from pathlib import Path

import capture


def frame(frame_type: int, flags: int, stream_id: int, payload: bytes) -> bytes:
    return (
        len(payload).to_bytes(3, "big")
        + bytes((frame_type, flags))
        + stream_id.to_bytes(4, "big")
        + payload
    )


def receive_exact(sock: socket.socket, length: int) -> bytes:
    result = bytearray()
    while len(result) < length:
        chunk = sock.recv(length - len(result))
        if not chunk:
            raise EOFError("socket closed")
        result.extend(chunk)
    return bytes(result)


def receive_until(sock: socket.socket, marker: bytes) -> bytes:
    result = bytearray()
    while marker not in result:
        chunk = sock.recv(4096)
        if not chunk:
            raise EOFError("socket closed")
        result.extend(chunk)
    return bytes(result)


class FrameParserTests(unittest.TestCase):
    def test_fragmented_input_preserves_frames(self) -> None:
        prefaces: list[bytes] = []
        frames: list[capture.H2Frame] = []
        parser = capture.H2FrameParser(True, prefaces.append, frames.append)
        wire = capture.CLIENT_PREFACE + frame(capture.FRAME_HEADERS, 5, 1, b"\x82")
        for current in wire:
            parser.feed(bytes((current,)))
        self.assertEqual(prefaces, [capture.CLIENT_PREFACE])
        self.assertEqual(len(frames), 1)
        self.assertEqual(frames[0].header + frames[0].payload, wire[len(capture.CLIENT_PREFACE) :])

    def test_hpack_analyzer_reports_wire_decisions_without_decoding_values(self) -> None:
        # Indexed :method GET, then a never-indexed literal using indexed name 23.
        decisions = capture.analyze_hpack(b"\x82\x1f\x08\x01x")
        self.assertEqual([item.kind for item in decisions], ["indexed", "literal_never_indexed"])
        self.assertEqual(decisions[1].name_index, 23)
        self.assertFalse(decisions[1].value_huffman)


class TransparentProxyTests(unittest.TestCase):
    def test_tls_proxy_forwards_decrypted_h2_bytes_unchanged(self) -> None:
        with tempfile.TemporaryDirectory(prefix="wire-proxy-test-") as name:
            root = Path(name)
            upstream_dir = root / "upstream"
            proxy_dir = root / "proxy"
            upstream_dir.mkdir()
            proxy_dir.mkdir()
            upstream_authority = capture.CertificateAuthority(upstream_dir, "test upstream CA")
            upstream_context = upstream_authority.server_context("localhost", "h2")
            listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            listener.bind(("127.0.0.1", 0))
            listener.listen(1)
            upstream_port = listener.getsockname()[1]
            observed: queue.Queue[bytes] = queue.Queue(maxsize=1)
            server_reply = frame(capture.FRAME_SETTINGS, 0, 0, b"")
            request_wire = (
                capture.CLIENT_PREFACE
                + frame(capture.FRAME_SETTINGS, 0, 0, b"")
                + frame(capture.FRAME_HEADERS, 5, 1, b"\x82")
            )

            def serve() -> None:
                raw, _ = listener.accept()
                with upstream_context.wrap_socket(raw, server_side=True) as tls:
                    observed.put(receive_exact(tls, len(request_wire)))
                    tls.sendall(server_reply)

            server = threading.Thread(target=serve, daemon=True)
            server.start()
            proxy = capture.TransparentConnectProxy(proxy_dir, upstream_authority.ca_path)
            proxy.start()
            try:
                raw_client = socket.create_connection(("127.0.0.1", proxy.port), timeout=10)
                raw_client.sendall(
                    f"CONNECT localhost:{upstream_port} HTTP/1.1\r\nHost: localhost:{upstream_port}\r\n\r\n".encode()
                )
                response = receive_until(raw_client, b"\r\n\r\n")
                self.assertTrue(response.startswith(b"HTTP/1.1 200"))
                client_context = ssl.create_default_context(cafile=str(proxy.authority.ca_path))
                client_context.set_alpn_protocols(["h2"])
                with client_context.wrap_socket(raw_client, server_hostname="localhost") as tls:
                    tls.sendall(request_wire)
                    self.assertEqual(receive_exact(tls, len(server_reply)), server_reply)
                self.assertEqual(observed.get(timeout=5), request_wire)
            finally:
                proxy.stop()
                listener.close()
                server.join(timeout=5)
            self.assertTrue(proxy.connections)
            connection = proxy.connections[0]
            self.assertEqual(connection.preface, capture.CLIENT_PREFACE)
            self.assertEqual(connection.records[0].block, b"\x82")


class ReportSafetyTests(unittest.TestCase):
    def test_safe_report_writer_rejects_credential_material(self) -> None:
        with tempfile.TemporaryDirectory(prefix="wire-report-test-") as name:
            output = Path(name) / "report.json"
            capture.safe_write_report(output, {"status": "safe", "authorization_persisted": False})
            self.assertTrue(output.is_file())
            with self.assertRaises(capture.CaptureError):
                capture.safe_write_report(output, {"value": "Bearer synthetic-secret"})


class TargetSelectionTests(unittest.TestCase):
    def test_selects_adjacent_pair_across_prompt_retry_intervals(self) -> None:
        connection = capture.ConnectionCapture("synthetic", "0" * 64, "h2")
        fields = [
            capture.DecodedField(b":method", b"POST", False),
            capture.DecodedField(b":path", b"/v1/responses", False),
            capture.DecodedField(b"authorization", b"synthetic", False),
            capture.DecodedField(b"x-grok-client-version", b"test", False),
        ]

        def record(stream_id: int, started_at: float) -> capture.HeaderBlockRecord:
            return capture.HeaderBlockRecord(
                stream_id=stream_id,
                started_at=started_at,
                settings_version=0,
                frames=[],
                block=b"",
                fields=fields,
                representations=[],
                completed=True,
            )

        # Stream 1 is a failed/retried first-turn request; 3/5 are the adjacent
        # pair that actually bridges the two prompt intervals.
        connection.records = [record(1, 1.0), record(3, 2.0), record(5, 11.0)]
        selected_connection, selected = capture.select_target_records(
            [connection], [(0.5, 3.0), (10.0, 12.0)]
        )
        self.assertIs(selected_connection, connection)
        self.assertEqual([item.stream_id for item in selected], [3, 5])


if __name__ == "__main__":
    unittest.main()
