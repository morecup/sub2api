use h2::client;
use http::header::HeaderValue;
use http::{Request, Version};
use serde::Serialize;
use sha2::{Digest, Sha256};
use std::env;
use std::error::Error;
use std::fs;
use std::io;
use std::path::PathBuf;
use tokio::io::{AsyncReadExt, AsyncWriteExt, DuplexStream};

type DynError = Box<dyn Error + Send + Sync>;

const CLIENT_PREFACE: &[u8] = b"PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n";
const HARNESS_CARGO_LOCK_SHA256: &str =
    "5f70259f7963478a0ecc247ae036ff78f600f3af7223e4b15106b5fdbf9cedca";
const PEER_SETTINGS: [Setting; 2] = [
    Setting {
        id: 1,
        name: "HEADER_TABLE_SIZE",
        value: 4096,
    },
    Setting {
        id: 5,
        name: "MAX_FRAME_SIZE",
        value: 16_384,
    },
];

#[derive(Clone, Copy, Serialize)]
struct Setting {
    id: u16,
    name: &'static str,
    value: u32,
}

#[derive(Serialize)]
struct Fixture {
    schema_version: u8,
    status: &'static str,
    official_wire_verified: bool,
    source: Source,
    synthetic_headers: Vec<SyntheticHeader>,
    stateful_case: CaptureCase,
    continuation_case: CaptureCase,
}

#[derive(Serialize)]
struct Source {
    official_binary: OfficialBinary,
    public_snapshot: PublicSnapshot,
    reference_crate: ReferenceCrate,
    harness: Harness,
}

#[derive(Serialize)]
struct OfficialBinary {
    product: &'static str,
    version: &'static str,
    internal_revision: &'static str,
    sha256: &'static str,
    reqwest_version: &'static str,
    hyper_version: &'static str,
    h2_version: &'static str,
    http_version: &'static str,
}

#[derive(Serialize)]
struct PublicSnapshot {
    sync_commit: &'static str,
    cargo_lock_sha256: &'static str,
    relationship_to_binary_revision: &'static str,
}

#[derive(Serialize)]
struct ReferenceCrate {
    name: &'static str,
    version: &'static str,
    checksum_sha256: &'static str,
}

#[derive(Serialize)]
struct Harness {
    generator: &'static str,
    command: &'static str,
    cargo_lock_sha256: &'static str,
}

#[derive(Serialize)]
struct SyntheticHeader {
    name: &'static str,
    value: String,
    sensitive: bool,
    behavior: &'static str,
}

#[derive(Serialize)]
struct CaptureCase {
    name: &'static str,
    connection_count: u8,
    client_preface_count: u8,
    request_count: usize,
    peer_settings: Vec<Setting>,
    streams: Vec<CapturedStream>,
}

#[derive(Serialize)]
struct CapturedStream {
    stream_id: u32,
    frames: Vec<CapturedFrame>,
    header_block_hex: String,
    header_block_sha256: String,
    decoded_headers: Vec<DecodedHeader>,
    decoded_header_count: usize,
}

#[derive(Serialize)]
struct CapturedFrame {
    #[serde(rename = "type")]
    frame_type: &'static str,
    flags: u8,
    stream_id: u32,
    frame_header_hex: String,
    payload_hex: String,
    fragment_hex: String,
}

#[derive(Clone, Serialize)]
struct DecodedHeader {
    name: String,
    value: String,
    sensitive: bool,
}

struct RawFrame {
    header: [u8; 9],
    frame_type: u8,
    flags: u8,
    stream_id: u32,
    payload: Vec<u8>,
}

struct RawCapturedStream {
    stream_id: u32,
    frames: Vec<CapturedFrame>,
    header_block: Vec<u8>,
}

#[tokio::main(flavor = "current_thread")]
async fn main() -> Result<(), DynError> {
    let output_path = output_path()?;
    let stateful_requests = vec![stateful_request(false)?, stateful_request(true)?];
    let continuation_requests = vec![continuation_request()?];

    let fixture = Fixture {
        schema_version: 2,
        status: "SOURCE-ALIGNED / WIRE-UNVERIFIED",
        official_wire_verified: false,
        source: source_metadata(),
        synthetic_headers: synthetic_headers(),
        stateful_case: capture_case("stateful_stream_1_3", stateful_requests).await?,
        continuation_case: capture_case(
            "continuation_independent_connection",
            continuation_requests,
        )
        .await?,
    };

    let mut json = serde_json::to_string_pretty(&fixture)?;
    json.push('\n');
    fs::write(output_path, json)?;
    Ok(())
}

fn output_path() -> Result<PathBuf, DynError> {
    let mut args = env::args_os();
    let _program = args.next();
    let output = args.next().ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::InvalidInput,
            "usage: cargo run --locked --offline -- <output-path>",
        )
    })?;
    if args.next().is_some() {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "expected one output path").into());
    }
    Ok(PathBuf::from(output))
}

fn source_metadata() -> Source {
    Source {
        official_binary: OfficialBinary {
            product: "grok",
            version: "0.2.112",
            internal_revision: "9bbd559437",
            sha256: "2469bd182af212c7fcb84f2981999e4e8a6a7a2e4172bad3ae7f787a1f11407c",
            reqwest_version: "0.12.24",
            hyper_version: "1.8.1",
            h2_version: "0.4.15",
            http_version: "1.4.0",
        },
        public_snapshot: PublicSnapshot {
            sync_commit: "47348d13ec4508dcfe440e34c6d511bb02998fb2",
            cargo_lock_sha256: "852e088a2b4ac3586142592a6c6bbd3f78b8446a8fa8a24b5131baa44b31fd38",
            relationship_to_binary_revision:
                "independent_public_snapshot_not_binary_revision_equivalence",
        },
        reference_crate: ReferenceCrate {
            name: "h2",
            version: "0.4.15",
            checksum_sha256: "6cb093c84e8bd9b188d4c4a8cb6579fc016968d14c99882163cd3ff402a4f155",
        },
        harness: Harness {
            generator: "rust-h2-reference",
            command: "cargo run --locked --offline -- <output-path>",
            cargo_lock_sha256: HARNESS_CARGO_LOCK_SHA256,
        },
    }
}

fn synthetic_headers() -> Vec<SyntheticHeader> {
    vec![
        synthetic(
            "x-fixture-stable",
            "alpha-alpha-alpha",
            false,
            "dynamic_insert_then_indexed",
        ),
        synthetic(
            "x-fixture-repeat",
            "first-repeat-value",
            false,
            "multi_value_first_inserted_or_indexed",
        ),
        synthetic(
            "x-fixture-repeat",
            "second-repeat-value",
            false,
            "multi_value_nameless_without_index",
        ),
        synthetic(
            "content-type",
            "fixture-content-type-one",
            false,
            "dynamic_name_seed",
        ),
        synthetic(
            "content-type",
            "fixture-content-type-two",
            true,
            "sensitive_reuses_dynamic_name_index",
        ),
        synthetic(
            "x-fixture-sensitive",
            "synthetic-sensitive-value",
            true,
            "never_indexed",
        ),
        synthetic(":path", "/source-aligned", false, "skip_value_index"),
        synthetic("age", "7", false, "skip_value_index"),
        synthetic(
            "authorization",
            "Synthetic fixture credential",
            false,
            "skip_value_index",
        ),
        synthetic("content-length", "0", false, "skip_value_index"),
        synthetic("etag", "\"fixture-etag\"", false, "skip_value_index"),
        synthetic(
            "if-modified-since",
            "Thu, 01 Jan 1970 00:00:00 GMT",
            false,
            "skip_value_index",
        ),
        synthetic(
            "if-none-match",
            "\"fixture-etag\"",
            false,
            "skip_value_index",
        ),
        synthetic("location", "/fixture-location", false, "skip_value_index"),
        synthetic("cookie", "fixture=a", false, "skip_value_index"),
        synthetic("set-cookie", "fixture=a; Path=/", false, "skip_value_index"),
        synthetic("x-fixture-empty", "", false, "empty_literal"),
        synthetic(
            "x-fixture-oversized",
            &"q".repeat(3022),
            false,
            "three_quarter_no_index",
        ),
    ]
}

fn synthetic(
    name: &'static str,
    value: &str,
    sensitive: bool,
    behavior: &'static str,
) -> SyntheticHeader {
    SyntheticHeader {
        name,
        value: value.to_owned(),
        sensitive,
        behavior,
    }
}

fn stateful_request(second_request: bool) -> Result<(Request<()>, Vec<DecodedHeader>), DynError> {
    let mut request = Request::builder()
        .method("POST")
        .uri("https://fixture.invalid/source-aligned")
        .version(Version::HTTP_2)
        .body(())?;
    let headers = request.headers_mut();
    headers.insert(
        "x-fixture-stable",
        HeaderValue::from_static("alpha-alpha-alpha"),
    );
    headers.append(
        "x-fixture-repeat",
        HeaderValue::from_static("first-repeat-value"),
    );
    headers.append(
        "x-fixture-repeat",
        HeaderValue::from_static("second-repeat-value"),
    );
    let mut content_type = if second_request {
        HeaderValue::from_static("fixture-content-type-two")
    } else {
        HeaderValue::from_static("fixture-content-type-one")
    };
    content_type.set_sensitive(second_request);
    headers.insert("content-type", content_type);
    let mut sensitive = HeaderValue::from_static("synthetic-sensitive-value");
    sensitive.set_sensitive(true);
    headers.insert("x-fixture-sensitive", sensitive);
    headers.insert("age", HeaderValue::from_static("7"));
    headers.insert(
        "authorization",
        HeaderValue::from_static("Synthetic fixture credential"),
    );
    headers.insert("content-length", HeaderValue::from_static("0"));
    headers.insert("etag", HeaderValue::from_static("\"fixture-etag\""));
    headers.insert(
        "if-modified-since",
        HeaderValue::from_static("Thu, 01 Jan 1970 00:00:00 GMT"),
    );
    headers.insert(
        "if-none-match",
        HeaderValue::from_static("\"fixture-etag\""),
    );
    headers.insert("location", HeaderValue::from_static("/fixture-location"));
    headers.insert("cookie", HeaderValue::from_static("fixture=a"));
    headers.insert("set-cookie", HeaderValue::from_static("fixture=a; Path=/"));
    headers.insert("x-fixture-empty", HeaderValue::from_static(""));
    headers.insert(
        "x-fixture-oversized",
        HeaderValue::from_bytes("q".repeat(3022).as_bytes())?,
    );
    let decoded = decoded_request_headers(&request)?;
    Ok((request, decoded))
}

fn continuation_request() -> Result<(Request<()>, Vec<DecodedHeader>), DynError> {
    let mut request = Request::builder()
        .method("GET")
        .uri("https://fixture.invalid/continuation")
        .version(Version::HTTP_2)
        .body(())?;
    request.headers_mut().insert(
        "x-fixture-continuation",
        HeaderValue::from_bytes("~".repeat(10_200).as_bytes())?,
    );
    let decoded = decoded_request_headers(&request)?;
    Ok((request, decoded))
}

fn decoded_request_headers(request: &Request<()>) -> Result<Vec<DecodedHeader>, DynError> {
    let uri = request.uri();
    let path = uri
        .path_and_query()
        .map(|value| value.as_str())
        .filter(|value| !value.is_empty())
        .unwrap_or("/");
    let mut fields = vec![
        decoded(":method", request.method().as_str(), false),
        decoded(":scheme", uri.scheme_str().unwrap_or("http"), false),
        decoded(
            ":authority",
            uri.authority().map(|value| value.as_str()).unwrap_or(""),
            false,
        ),
        decoded(":path", path, false),
    ];
    for (name, value) in request.headers() {
        fields.push(decoded(
            name.as_str(),
            value.to_str()?,
            value.is_sensitive(),
        ));
    }
    Ok(fields)
}

fn decoded(name: &str, value: &str, sensitive: bool) -> DecodedHeader {
    DecodedHeader {
        name: name.to_owned(),
        value: value.to_owned(),
        sensitive,
    }
}

async fn capture_case(
    name: &'static str,
    requests: Vec<(Request<()>, Vec<DecodedHeader>)>,
) -> Result<CaptureCase, DynError> {
    let request_count = requests.len();
    let (client_io, peer_io) = tokio::io::duplex(1 << 20);
    let peer_task = tokio::spawn(raw_peer(peer_io, request_count));
    let (mut sender, connection) = client::handshake(client_io).await?;
    let connection_task = tokio::spawn(async move {
        let _ = connection.await;
    });

    let mut decoded_headers = Vec::with_capacity(request_count);
    for (request, decoded) in requests {
        sender = sender.ready().await?;
        let (response, _send_stream) = sender.send_request(request, true)?;
        let response = response.await?;
        if response.status() != 200 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "raw peer response was not 200",
            )
            .into());
        }
        decoded_headers.push(decoded);
    }
    drop(sender);

    let raw_streams = peer_task.await??;
    connection_task.abort();
    if raw_streams.len() != decoded_headers.len() {
        return Err(
            io::Error::new(io::ErrorKind::InvalidData, "capture/header count mismatch").into(),
        );
    }

    let streams = raw_streams
        .into_iter()
        .zip(decoded_headers)
        .map(|(raw, decoded_headers)| CapturedStream {
            stream_id: raw.stream_id,
            frames: raw.frames,
            header_block_hex: hex::encode(&raw.header_block),
            header_block_sha256: sha256_hex(&raw.header_block),
            decoded_header_count: decoded_headers.len(),
            decoded_headers,
        })
        .collect();

    Ok(CaptureCase {
        name,
        connection_count: 1,
        client_preface_count: 1,
        request_count,
        peer_settings: PEER_SETTINGS.to_vec(),
        streams,
    })
}

async fn raw_peer(
    mut io: DuplexStream,
    expected_requests: usize,
) -> Result<Vec<RawCapturedStream>, DynError> {
    let mut preface = [0u8; 24];
    io.read_exact(&mut preface).await?;
    if preface != CLIENT_PREFACE {
        return Err(
            io::Error::new(io::ErrorKind::InvalidData, "invalid HTTP/2 client preface").into(),
        );
    }

    let client_settings = read_frame(&mut io).await?;
    if client_settings.frame_type != 0x4
        || client_settings.flags & 0x1 != 0
        || client_settings.stream_id != 0
    {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "first client frame was not SETTINGS",
        )
        .into());
    }
    write_peer_settings(&mut io).await?;
    write_frame(&mut io, 0x4, 0x1, 0, &[]).await?;

    let mut completed = Vec::with_capacity(expected_requests);
    let mut current: Option<RawCapturedStream> = None;
    while completed.len() < expected_requests {
        let frame = read_frame(&mut io).await?;
        match frame.frame_type {
            0x1 => {
                if frame.stream_id == 0 || current.is_some() {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidData,
                        "invalid HEADERS sequence",
                    )
                    .into());
                }
                if frame.flags & 0x1 == 0 {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidData,
                        "fixture request did not end on HEADERS",
                    )
                    .into());
                }
                let fragment = header_fragment(&frame)?;
                let end_headers = frame.flags & 0x4 != 0;
                let stream_id = frame.stream_id;
                let captured_frame = captured_frame(&frame, fragment);
                let stream = RawCapturedStream {
                    stream_id,
                    frames: vec![captured_frame],
                    header_block: fragment.to_vec(),
                };
                if end_headers {
                    write_response(&mut io, stream_id).await?;
                    completed.push(stream);
                } else {
                    current = Some(stream);
                }
            }
            0x9 => {
                let stream = current.as_mut().ok_or_else(|| {
                    io::Error::new(io::ErrorKind::InvalidData, "CONTINUATION without HEADERS")
                })?;
                if frame.stream_id != stream.stream_id {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidData,
                        "CONTINUATION stream mismatch",
                    )
                    .into());
                }
                let fragment = header_fragment(&frame)?;
                stream.header_block.extend_from_slice(fragment);
                stream.frames.push(captured_frame(&frame, fragment));
                if frame.flags & 0x4 != 0 {
                    let stream = current.take().expect("current stream");
                    write_response(&mut io, stream.stream_id).await?;
                    completed.push(stream);
                }
            }
            0x4 | 0x6 | 0x8 => {}
            other => {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    format!("unexpected client frame type 0x{other:02x}"),
                )
                .into())
            }
        }
    }
    Ok(completed)
}

async fn read_frame(io: &mut DuplexStream) -> Result<RawFrame, DynError> {
    let mut header = [0u8; 9];
    io.read_exact(&mut header).await?;
    let length =
        (usize::from(header[0]) << 16) | (usize::from(header[1]) << 8) | usize::from(header[2]);
    let mut payload = vec![0u8; length];
    io.read_exact(&mut payload).await?;
    let stream_id = u32::from_be_bytes(header[5..9].try_into().expect("four-byte stream ID"));
    if stream_id & 0x8000_0000 != 0 {
        return Err(io::Error::new(io::ErrorKind::InvalidData, "reserved stream bit set").into());
    }
    Ok(RawFrame {
        header,
        frame_type: header[3],
        flags: header[4],
        stream_id,
        payload,
    })
}

async fn write_peer_settings(io: &mut DuplexStream) -> Result<(), DynError> {
    let mut payload = Vec::with_capacity(PEER_SETTINGS.len() * 6);
    for setting in PEER_SETTINGS {
        payload.extend_from_slice(&setting.id.to_be_bytes());
        payload.extend_from_slice(&setting.value.to_be_bytes());
    }
    write_frame(io, 0x4, 0, 0, &payload).await
}

async fn write_response(io: &mut DuplexStream, stream_id: u32) -> Result<(), DynError> {
    write_frame(io, 0x1, 0x5, stream_id, &[0x88]).await
}

async fn write_frame(
    io: &mut DuplexStream,
    frame_type: u8,
    flags: u8,
    stream_id: u32,
    payload: &[u8],
) -> Result<(), DynError> {
    if payload.len() > 0x00ff_ffff {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "frame payload too large").into());
    }
    let length = payload.len() as u32;
    let mut header = [0u8; 9];
    header[0] = (length >> 16) as u8;
    header[1] = (length >> 8) as u8;
    header[2] = length as u8;
    header[3] = frame_type;
    header[4] = flags;
    header[5..9].copy_from_slice(&(stream_id & 0x7fff_ffff).to_be_bytes());
    io.write_all(&header).await?;
    io.write_all(payload).await?;
    io.flush().await?;
    Ok(())
}

fn header_fragment(frame: &RawFrame) -> Result<&[u8], DynError> {
    match frame.frame_type {
        0x9 => Ok(&frame.payload),
        0x1 => {
            let mut offset = 0usize;
            let mut padding = 0usize;
            if frame.flags & 0x8 != 0 {
                padding = usize::from(*frame.payload.first().ok_or_else(|| {
                    io::Error::new(
                        io::ErrorKind::InvalidData,
                        "PADDED HEADERS missing pad length",
                    )
                })?);
                offset += 1;
            }
            if frame.flags & 0x20 != 0 {
                offset += 5;
            }
            if offset > frame.payload.len() || padding > frame.payload.len() - offset {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "invalid HEADERS padding/priority",
                )
                .into());
            }
            Ok(&frame.payload[offset..frame.payload.len() - padding])
        }
        _ => Err(io::Error::new(io::ErrorKind::InvalidData, "not a header block frame").into()),
    }
}

fn captured_frame(frame: &RawFrame, fragment: &[u8]) -> CapturedFrame {
    CapturedFrame {
        frame_type: if frame.frame_type == 0x1 {
            "HEADERS"
        } else {
            "CONTINUATION"
        },
        flags: frame.flags,
        stream_id: frame.stream_id,
        frame_header_hex: hex::encode(frame.header),
        payload_hex: hex::encode(&frame.payload),
        fragment_hex: hex::encode(fragment),
    }
}

fn sha256_hex(value: &[u8]) -> String {
    hex::encode(Sha256::digest(value))
}
