# Rust h2 reference fixture

This harness drives the crates.io `h2 0.4.15` client against a handwritten
in-memory raw HTTP/2 peer. The peer records the complete frame header and
payload before any HPACK decoder sees them.

The stateful stream `1/3` case includes dynamic-table reuse, consecutive
same-name `HeaderMap` values, and a sensitive value that reuses a dynamic name
index. The continuation case remains on an independent connection.

Generate a fixture without network access:

```text
cargo run --locked --offline -- <output-path>
```

Run the command twice with different output paths and compare the files
byte-for-byte before updating the checked-in fixture.
