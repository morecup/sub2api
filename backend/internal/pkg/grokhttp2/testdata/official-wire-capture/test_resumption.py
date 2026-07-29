from __future__ import annotations

import unittest

from capture_resumption import (
    TLSClientHello,
    TLSConnectionRecord,
    TLSHelloStreamParser,
    TLS_EXTENSION_PRE_SHARED_KEY,
    client_hello_report,
    compare_hello_shapes,
    parse_client_hello,
    parse_server_hello,
    select_resumption_pair,
)


def extension(extension_id: int, payload: bytes) -> bytes:
    return extension_id.to_bytes(2, "big") + len(payload).to_bytes(2, "big") + payload


def handshake(message_type: int, body: bytes) -> bytes:
    return bytes([message_type]) + len(body).to_bytes(3, "big") + body


def client_hello(include_psk: bool) -> bytes:
    extensions = [
        extension(0, b"\x00\x08\x00\x00\x05local"),
        extension(10, b"\x00\x02\x00\x1d"),
        extension(11, b"\x01\x00"),
        extension(13, b"\x00\x02\x04\x03"),
        extension(16, b"\x00\x03\x02h2"),
        extension(43, b"\x04\x03\x04\x03\x03"),
        extension(45, b"\x01\x01"),
        extension(51, b"\x00\x08\x00\x1d\x00\x04key!"),
    ]
    if include_psk:
        identity = b"ticket"
        identities = len(identity).to_bytes(2, "big") + identity + (123).to_bytes(4, "big")
        binders = b"\x04hash"
        psk = len(identities).to_bytes(2, "big") + identities + len(binders).to_bytes(2, "big") + binders
        extensions.append(extension(TLS_EXTENSION_PRE_SHARED_KEY, psk))
    extension_bytes = b"".join(extensions)
    body = (
        b"\x03\x03"
        + bytes(32)
        + b"\x20"
        + bytes(32)
        + b"\x00\x04\x13\x02\x13\x01"
        + b"\x01\x00"
        + len(extension_bytes).to_bytes(2, "big")
        + extension_bytes
    )
    return handshake(1, body)


def server_hello(include_psk: bool) -> bytes:
    extensions = [extension(43, b"\x03\x04")]
    if include_psk:
        extensions.append(extension(TLS_EXTENSION_PRE_SHARED_KEY, b"\x00\x00"))
    extension_bytes = b"".join(extensions)
    body = (
        b"\x03\x03"
        + bytes(32)
        + b"\x20"
        + bytes(32)
        + b"\x13\x02\x00"
        + len(extension_bytes).to_bytes(2, "big")
        + extension_bytes
    )
    return handshake(2, body)


class TLSHelloParserTests(unittest.TestCase):
    def test_fragmented_record_yields_resumed_client_hello(self) -> None:
        raw = client_hello(True)
        record = b"\x16\x03\x01" + len(raw).to_bytes(2, "big") + raw
        captured: list[bytes] = []
        parser = TLSHelloStreamParser(1, captured.append)
        for byte in record:
            parser.feed(bytes([byte]))
        self.assertEqual(captured, [raw])
        parsed = parse_client_hello(captured[0])
        report = client_hello_report(parsed, b"nonce")
        self.assertTrue(report["pre_shared_key_present"])
        self.assertTrue(report["pre_shared_key_last"])
        psk = next(item for item in report["extensions"] if item["id"] == TLS_EXTENSION_PRE_SHARED_KEY)
        self.assertEqual([item["identity_length"] for item in psk["identities"]], [6])
        self.assertEqual(psk["binder_lengths"], [4])

    def test_server_hello_reports_selected_identity(self) -> None:
        parsed = parse_server_hello(server_hello(True))
        selected = parsed.extension(TLS_EXTENSION_PRE_SHARED_KEY)
        self.assertIsNotNone(selected)
        self.assertEqual(selected.data, b"\x00\x00")

    def test_pair_selection_and_structural_comparison(self) -> None:
        fresh = TLSConnectionRecord(
            "official",
            "example.test",
            443,
            1.0,
            parse_client_hello(client_hello(False)),
            parse_server_hello(server_hello(False)),
        )
        resumed = TLSConnectionRecord(
            "official",
            "example.test",
            443,
            2.0,
            parse_client_hello(client_hello(True)),
            parse_server_hello(server_hello(True)),
        )
        self.assertEqual(select_resumption_pair([fresh, resumed], "official"), (fresh, resumed))
        report = client_hello_report(resumed.client_hello, b"nonce")
        parity = compare_hello_shapes(report, report)
        self.assertTrue(parity["all_required_equal"])


if __name__ == "__main__":
    unittest.main()
