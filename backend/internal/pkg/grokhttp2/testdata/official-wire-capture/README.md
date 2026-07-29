# Official wire capture

This directory contains an opt-in evidence harness for the installed official
Grok CLI. It is not part of production request handling.

Security boundary:

- The proxy terminates downstream and upstream TLS, then forwards decrypted
  HTTP/2 bytes unchanged. It does not decode and re-encode frames in flight.
- Raw `HEADERS` / `CONTINUATION`, decoded headers, credentials, request bodies,
  response bodies, session IDs, and production endpoint names stay in memory.
- The Go helper receives decoded fields through an anonymous pipe, calls the
  repository's `NewGrokClientEncoder`, and returns comparison bytes through the
  same pipe. Those bytes are never written to disk.
- The JSON report contains only hashes, lengths, flags, stream IDs, SETTINGS,
  HPACK branch summaries, equality results, and binary provenance observations.
- Trust is scoped to the one child process through `SSL_CERT_FILE`. The harness
  never installs a Windows root certificate and never edits Grok config.
- The resumption harness passes TLS through unchanged and persists only
  structural summaries. The HPACK branch harness uses an ephemeral `GROK_HOME`,
  a synthetic API key, and a CONNECT proxy that refuses every non-local target.

Run the local byte-forwarding and safety tests first:

```powershell
python -m unittest -v test_capture.py test_resumption.py test_hpack_branches.py
```

Run each official capture explicitly:

```powershell
python capture.py --run-official
python capture_resumption.py --run-official
python capture_hpack_branches.py --run-official
```

The live command sends exactly two fixed synthetic prompts through one
`grok agent stdio` process. It verifies the same session, same HTTP/2
connection, consecutive odd stream IDs, complete SETTINGS lifecycle, and
byte-for-byte equality for every client header block preceding and including
the two target requests on that connection.

The binary audit deliberately does not claim that a stripped executable can
prove the absolute absence of unused private changes. It checks the installed
binary hash, embedded crates.io source paths and versions, h2 source/line
anchors, the public snapshot lockfile, and replacement markers. Exact live
wire equality narrows the conclusion to the declared captured scenario.

The resumption report proves one official and one local TLS 1.3 PSK recovery
shape. The HPACK branch report proves official `HEADERS + CONTINUATION` parity
on streams 1/3. Its sensitive classification is deliberately narrower:
observed OAuth and API-key auth builders do not mark Authorization sensitive,
so never-index is not applicable to those builders rather than live-exercised.
