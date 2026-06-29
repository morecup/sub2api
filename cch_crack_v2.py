#!/usr/bin/env python3
"""Comprehensive CCH cracker - implements all Bun hash algorithms."""
import json, struct, hashlib, re

# Load oracle data
with open(r"C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\captures\cch_oracle2_20260627-163332.json") as f:
    data = json.load(f)

rows = data["rows"]
# Only use rows where sent_has_00000 is true (CCH was actually computed)
test_rows = [r for r in rows if r.get("sent_has_00000", True)]
print(f"Total rows: {len(rows)}, testable: {len(test_rows)}")

# Parse base body (idx 0)
base = rows[0]
base_body = base["wire_body"].replace(f"cch={base['wire_cch']};", "cch=00000;")
base_cch = base["wire_cch"]  # fac75
print(f"Base CCH: {base_cch}, body len: {len(base_body)}")

# ============================================
# Hash algorithm implementations
# ============================================

def u64(x): return x & 0xFFFFFFFFFFFFFFFF
def u32(x): return x & 0xFFFFFFFF

def wyhash_mum(a, b):
    r = u64(a) * u64(b)
    return u64(r & 0xFFFFFFFFFFFFFFFF) ^ u64(r >> 64)

def wyhash_read8(data, i):
    return int.from_bytes(data[i:i+8], 'little')

def wyhash_read4(data, i):
    return int.from_bytes(data[i:i+4], 'little')

def wyhash_read_rest(data, i, remaining):
    if remaining == 0: return 0
    if remaining >= 8:
        return wyhash_read8(data, i)
    if remaining >= 4:
        v = wyhash_read4(data, i)
        v |= wyhash_read4(data, i + remaining - 4) << 32
        return v & 0xFFFFFFFFFFFFFFFF
    if remaining >= 2:
        v = int.from_bytes(data[i:i+2], 'little')
        v |= data[i + remaining - 1] << 16
        return v
    return data[i]

def wyhash_v4(data, seed=0):
    """wyhash v4 - as found in Bun runtime"""
    p0 = 0xa0761d6478bd642f
    p1 = 0xe7037ed1a0b428db
    p2 = 0xe7037ed1a0b428db  # same as p1
    p3 = 0xa0761d6478bd642f  # same as p0

    length = len(data)
    s = seed ^ p0
    i = 0

    # Process 32 bytes at a time
    while i + 32 <= length:
        a = wyhash_read8(data, i) ^ wyhash_read8(data, i + 8)
        b = wyhash_read8(data, i + 16) ^ wyhash_read8(data, i + 24)
        s = wyhash_mum(s ^ a, p1) ^ wyhash_mum(b, p2)  # Simplified
        i += 32

    # Process 16 bytes at a time
    while i + 16 <= length:
        a = wyhash_read8(data, i)
        b = wyhash_read8(data, i + 8)
        s = wyhash_mum(s ^ a, p1) ^ wyhash_mum(b, p2)
        i += 16

    # Process 8 bytes at a time
    while i + 8 <= length:
        v = wyhash_read8(data, i)
        s = wyhash_mum(s ^ v, p1)
        i += 8

    # Handle remaining
    remaining = length - i
    if remaining > 0:
        v = wyhash_read_rest(data, i, remaining)
        s = wyhash_mum(s ^ v, p0 ^ (remaining << 48))

    s = wyhash_mum(s ^ length, p1)
    return s & 0xFFFFFFFFFFFFFFFF

def wyhash_simple(data, seed=0):
    """Simpler wyhash variant - 8 bytes at a time"""
    p0 = 0xa0761d6478bd642f
    p1 = 0xe7037ed1a0b428db
    length = len(data)
    s = seed ^ p0
    i = 0
    while i + 8 <= length:
        v = wyhash_read8(data, i)
        s = wyhash_mum(s ^ v, p1)
        i += 8
    remaining = length - i
    if remaining > 0:
        v = 0
        for j in range(remaining):
            v |= data[i + j] << (j * 8)
        s = wyhash_mum(s ^ v, p0 ^ (remaining << 48))
    s = wyhash_mum(s ^ length, p1)
    return s & 0xFFFFFFFFFFFFFFFF

def xxhash64(data, seed=0):
    """xxHash64 implementation"""
    PRIME1 = 0x9E3779B185EBCA87
    PRIME2 = 0xC2B2AE3D27D4EB4F
    PRIME3 = 0x165667B19E3779F9
    PRIME4 = 0x85EBCA77C2B2AE63
    PRIME5 = 0x27D4EB2F165667C5

    length = len(data)
    h = seed + PRIME5 + length
    i = 0

    while i + 8 <= length:
        k = int.from_bytes(data[i:i+8], 'little')
        k = u64(k * PRIME2)
        k = u64((k << 31) | (k >> 33))
        k = u64(k * PRIME1)
        h = u64(h ^ k)
        h = u64(((h << 27) | (h >> 37)) * PRIME1 + PRIME4)
        i += 8

    while i + 4 <= length:
        k = int.from_bytes(data[i:i+4], 'little')
        h = u64(h + u64(k * PRIME1))
        h = u64(((h << 23) | (h >> 41)) * PRIME2 + PRIME3)
        i += 4

    while i < length:
        h = u64(h + u64(data[i] * PRIME5))
        h = u64(((h << 11) | (h >> 53)) * PRIME1)
        i += 1

    h = u64(h ^ (h >> 33))
    h = u64(h * PRIME2)
    h = u64(h ^ (h >> 29))
    h = u64(h * PRIME3)
    h = u64(h ^ (h >> 32))
    return h

def fnv1a_64(data):
    h = 0xcbf29ce484222325
    for b in data:
        h ^= b
        h = u64(h * 0x100000001b3)
    return h

def murmur3_32(data, seed=0):
    h = seed
    c1 = 0xcc9e2d51
    c2 = 0x1b873593
    length = len(data)
    i = 0
    while i + 4 <= length:
        k = int.from_bytes(data[i:i+4], 'little')
        k = u32(k * c1)
        k = u32((k << 15) | (k >> 17))
        k = u32(k * c2)
        h = u32(h ^ k)
        h = u32((h << 13) | (h >> 19))
        h = u32(h * 5 + 0xe6546b64)
        i += 4
    k = 0
    remaining = length - i
    if remaining >= 3: k ^= data[i+2] << 16
    if remaining >= 2: k ^= data[i+1] << 8
    if remaining >= 1:
        k ^= data[i]
        k = u32(k * c1)
        k = u32((k << 15) | (k >> 17))
        k = u32(k * c2)
        h = u32(h ^ k)
    h = u32(h ^ length)
    h = u32(h ^ (h >> 16))
    h = u32(h * 0x85ebca6b)
    h = u32(h ^ (h >> 13))
    h = u32(h * 0xc2b2ae35)
    h = u32(h ^ (h >> 16))
    return h

def crc32_hash(data):
    crc = 0xFFFFFFFF
    for b in data:
        crc ^= b
        for _ in range(8):
            if crc & 1: crc = (crc >> 1) ^ 0xEDB88320
            else: crc >>= 1
    return crc ^ 0xFFFFFFFF

# ============================================
# Truncation methods
# ============================================
def truncate_methods(h, bits=64):
    """Generate all possible 5-hex-char truncations from a hash value"""
    results = {}
    if bits == 64:
        results["mod20"] = format(h % (1 << 20), '05x')
        results["low20"] = format(h & 0xFFFFF, '05x')
        results["low32_mod20"] = format((h & 0xFFFFFFFF) % (1 << 20), '05x')
        results["low32_low20"] = format(h & 0xFFFFFFFF & 0xFFFFF, '05x')
        results["high20"] = format((h >> 44) & 0xFFFFF, '05x')
        results["mid20"] = format((h >> 22) & 0xFFFFF, '05x')
        results["hex16_first5"] = format(h & 0xFFFFFFFF, '08x')[:5]
        results["hex16_last5"] = format(h & 0xFFFFFFFF, '08x')[-5:]
        results["hex64_first5"] = format(h, '016x')[:5]
        results["hex64_last5"] = format(h, '016x')[-5:]
        results["mod20_32"] = format(u32(h) % (1 << 20), '05x')
        # XOR high and low 32 bits, then mod20
        xor32 = u32((h >> 32) ^ (h & 0xFFFFFFFF))
        results["xor32_mod20"] = format(xor32 % (1 << 20), '05x')
        results["xor32_low20"] = format(xor32 & 0xFFFFF, '05x')
    elif bits == 32:
        results["mod20"] = format(h % (1 << 20), '05x')
        results["low20"] = format(h & 0xFFFFF, '05x')
        results["hex_first5"] = format(h, '08x')[:5]
        results["hex_last5"] = format(h, '08x')[-5:]
    return results

# ============================================
# Body preprocessing variants
# ============================================
def get_body_variants(body_str):
    """Generate all possible body preprocessing variants"""
    body_bytes = body_str.encode('utf-8')
    variants = {"raw": body_bytes}

    # Without cch=00000;
    no_cch = body_str.replace(" cch=00000;", "").encode('utf-8')
    variants["no_cch_space"] = no_cch
    no_cch2 = body_str.replace("cch=00000;", "").encode('utf-8')
    variants["no_cch"] = no_cch2

    # With cch replaced to empty
    variants["cch_empty"] = body_str.replace("00000", "").encode('utf-8')

    # Replace cch value with 5 zeros in different ways
    variants["cch_00000_nospace"] = body_str.replace(" cch=00000;", "cch=00000;").encode('utf-8')

    # Model value blanked (keep key)
    try:
        obj = json.loads(body_str)
        if "model" in obj:
            obj["model"] = ""
            variants["model_blank"] = json.dumps(obj, separators=(',', ':'), ensure_ascii=True).encode('utf-8')
        if "max_tokens" in obj:
            obj2 = json.loads(body_str)
            del obj2["max_tokens"]
            variants["no_max"] = json.dumps(obj2, separators=(',', ':'), ensure_ascii=True).encode('utf-8')
        if "model" in obj and "max_tokens" in obj:
            obj3 = json.loads(body_str)
            obj3["model"] = ""
            del obj3["max_tokens"]
            variants["model_blank_no_max"] = json.dumps(obj3, separators=(',', ':'), ensure_ascii=True).encode('utf-8')
    except:
        pass

    return variants

# ============================================
# HTTP header variants
# ============================================
def get_http_request_variants(body_str, url_path="/v1/messages?beta=true&idx=0"):
    """Generate full HTTP request variants"""
    body_bytes = body_str.encode('utf-8')
    headers_str = f"POST {url_path} HTTP/1.1\r\nContent-Type: application/json\r\nanthropic-version: 2023-06-01\r\nContent-Length: {len(body_bytes)}\r\n\r\n"
    variants = {
        "http_full": headers_str.encode('utf-8') + body_bytes,
    }
    # Minimal headers
    minimal = f"Content-Type: application/json\r\nanthropic-version: 2023-06-01\r\n"
    variants["headers_body"] = minimal.encode('utf-8') + body_bytes
    # Just content-type
    variants["ct_body"] = b"application/json" + body_bytes
    return variants

# ============================================
# Main test loop
# ============================================
print("\n=== Testing hash algorithms ===")

# Use first 10 testable rows
test_set = test_rows[:10]
all_matches = {}

for row in test_set:
    wire = row["wire_body"]
    cch = row["wire_cch"]
    sent_body = wire.replace(f"cch={cch};", "cch=00000;")

    body_variants = get_body_variants(sent_body)
    http_variants = get_http_request_variants(sent_body, f"/v1/messages?beta=true&idx={row['idx']}")
    all_variants = {**body_variants, **http_variants}

    for vname, vdata in all_variants.items():
        # Test all hash algorithms
        hashes = {}

        # wyhash variants
        for seed in [0, 1, 0xabc, 0x12345, 0x59cf53e54c78, 0xffffffff]:
            h = wyhash_v4(vdata, seed)
            for tname, tval in truncate_methods(h).items():
                hashes[f"wyhash_v4_s{seed:x}_{tname}"] = tval
            h = wyhash_simple(vdata, seed)
            for tname, tval in truncate_methods(h).items():
                hashes[f"wyhash_simple_s{seed:x}_{tname}"] = tval

        # xxHash64
        for seed in [0, 1, 0xabc]:
            h = xxhash64(vdata, seed)
            for tname, tval in truncate_methods(h).items():
                hashes[f"xxhash64_s{seed:x}_{tname}"] = tval

        # FNV1a-64
        h = fnv1a_64(vdata)
        for tname, tval in truncate_methods(h).items():
            hashes[f"fnv1a64_{tname}"] = tval

        # murmur3-32
        for seed in [0, 1, 0xabc]:
            h = murmur3_32(vdata, seed)
            for tname, tval in truncate_methods(h, 32).items():
                hashes[f"murmur3_32_s{seed:x}_{tname}"] = tval

        # CRC32
        h = crc32_hash(vdata)
        for tname, tval in truncate_methods(h, 32).items():
            hashes[f"crc32_{tname}"] = tval

        # SHA256
        h = hashlib.sha256(vdata).digest()
        h64 = int.from_bytes(h[:8], 'big')
        h32 = int.from_bytes(h[:4], 'big')
        for tname, tval in truncate_methods(h64).items():
            hashes[f"sha256_{tname}"] = tval

        # MD5
        h = hashlib.md5(vdata).digest()
        h64 = int.from_bytes(h[:8], 'big')
        for tname, tval in truncate_methods(h64).items():
            hashes[f"md5_{tname}"] = tval

        # Check matches
        for hname, hval in hashes.items():
            if hval == cch:
                key = f"{vname}/{hname}"
                if key not in all_matches:
                    all_matches[key] = []
                all_matches[key].append(row["idx"])

# Report results
print(f"\nMatches found:")
if all_matches:
    for key, indices in sorted(all_matches.items(), key=lambda x: -len(x[1])):
        print(f"  {key}: matched {len(indices)} rows: {indices[:10]}")
else:
    print("  NO MATCHES FOUND")

# ============================================
# Phase 2: If no match, try including salt
# ============================================
if not all_matches:
    print("\n=== Phase 2: Salt combinations ===")
    salt = b"59cf53e54c78"

    for row in test_set[:5]:
        wire = row["wire_body"]
        cch = row["wire_cch"]
        sent_body = wire.replace(f"cch={cch};", "cch=00000;")
        body_bytes = sent_body.encode('utf-8')

        combos = {
            "salt+body": salt + body_bytes,
            "body+salt": body_bytes + salt,
            "salt+body+salt": salt + body_bytes + salt,
        }

        found = False
        for cname, cdata in combos.items():
            for seed in [0, 1]:
                h = wyhash_simple(cdata, seed)
                for tname, tval in truncate_methods(h).items():
                    if tval == cch:
                        print(f"  MATCH: {cname}/wyhash_s{seed}/{tname} for idx={row['idx']}")
                        found = True
                h = wyhash_v4(cdata, seed)
                for tname, tval in truncate_methods(h).items():
                    if tval == cch:
                        print(f"  MATCH: {cname}/wyhash_v4_s{seed}/{tname} for idx={row['idx']}")
                        found = True

        if not found and row["idx"] == 0:
            print(f"  No salt match for base (cch={cch})")

# ============================================
# Phase 3: Analyze model value independence
# ============================================
print("\n=== Phase 3: Model independence analysis ===")
# idx 0: model=claude-sonnet-4-6, max_tokens=1 â†?fac75
# idx 2: model=xxx, max_tokens=999999 â†?fac75 (same!)
# These have DIFFERENT body bytes but same CCH
# So CCH = hash(normalized_body) where model_value and max_tokens_value are stripped

r0 = rows[0]  # base
r2 = rows[2]  # model_and_max_changed

body0 = r0["wire_body"].replace(f"cch={r0['wire_cch']};", "cch=00000;")
body2 = r2["wire_body"].replace(f"cch={r2['wire_cch']};", "cch=00000;")

# Show exact differences
min_len = min(len(body0), len(body2))
diffs = []
for i in range(min_len):
    if body0[i] != body2[i]:
        diffs.append(i)
if len(body0) != len(body2):
    diffs.extend(range(min_len, max(len(body0), len(body2))))

print(f"Body0 len: {len(body0)}, Body2 len: {len(body2)}")
print(f"Differences at {len(diffs)} positions")
if diffs:
    s = max(0, diffs[0] - 20)
    e = min(max(len(body0), len(body2)), diffs[-1] + 20)
    print(f"Body0: ...{body0[s:e]}...")
    print(f"Body2: ...{body2[s:e]}...")

# The difference is: model value ("claude-sonnet-4-6" vs "xxx") and max_tokens value (1 vs 999999)
# If we zero these out, the bodies should be identical
# Let's try: replace model value with empty string in the raw body
def zero_model_and_max(body_str):
    """Replace model value and max_tokens value with empty/zero in raw body"""
    # Replace "model":"..." with "model":""
    body_str = re.sub(r'"model":"[^"]*"', '"model":""', body_str)
    # Replace "max_tokens":NUMBER with "max_tokens":0
    body_str = re.sub(r'"max_tokens":\d+', '"max_tokens":0', body_str)
    return body_str

norm0 = zero_model_and_max(body0)
norm2 = zero_model_and_max(body2)
print(f"\nNormalized body0: {norm0[:80]}...")
print(f"Normalized body2: {norm2[:80]}...")
print(f"Normalized equal: {norm0 == norm2}")

if norm0 == norm2:
    # Now hash the normalized body
    nb = norm0.encode('utf-8')
    print(f"\nHashing normalized body (len={len(nb)}):")
    for seed in [0, 1, 0xabc]:
        h = wyhash_simple(nb, seed)
        print(f"  wyhash_simple(s={seed}): {h} -> mod20={format(h%(1<<20),'05x')} low20={format(h&0xFFFFF,'05x')}")
        h = wyhash_v4(nb, seed)
        print(f"  wyhash_v4(s={seed}): {h} -> mod20={format(h%(1<<20),'05x')} low20={format(h&0xFFFFF,'05x')}")

    h = fnv1a_64(nb)
    print(f"  fnv1a_64: {h} -> mod20={format(h%(1<<20),'05x')} low20={format(h&0xFFFFF,'05x')}")

    h = xxhash64(nb, 0)
    print(f"  xxhash64(s=0): {h} -> mod20={format(h%(1<<20),'05x')} low20={format(h&0xFFFFF,'05x')}")

    h = murmur3_32(nb, 0)
    print(f"  murmur3_32(s=0): {h} -> mod20={format(h%(1<<20),'05x')} low20={format(h&0xFFFFF,'05x')}")

    h = hashlib.sha256(nb).hexdigest()
    print(f"  sha256: {h[:16]}... -> first5={h[:5]}")

    print(f"\n  Expected CCH: {base_cch}")
