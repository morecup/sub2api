#!/usr/bin/env python3
"""Systematic CCH algorithm cracker using oracle test data."""
import json, hashlib, struct, re, sys

# Load oracle data
with open(r"C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\captures\cch_oracle2_20260627-163332.json") as f:
    data = json.load(f)

rows = data["rows"]
print(f"Loaded {len(rows)} oracle rows")

# Parse into test pairs
pairs = []
for r in rows:
    wire = r["wire_body"]
    cch = r["wire_cch"]
    sent_body = wire.replace(f"cch={cch};", "cch=00000;").replace(f"cch={cch}\"", "cch=00000\"")
    if "00000" not in sent_body and r.get("sent_has_00000"):
        sent_body = re.sub(r'cch=[0-9a-f]{5};', 'cch=00000;', wire)
        sent_body = re.sub(r'cch=[0-9a-f]{5}"', 'cch=00000"', sent_body)
    pairs.append({
        "name": r["name"], "idx": r["idx"],
        "sent_body": sent_body, "wire_body": wire,
        "cch": cch, "sent_has_00000": r.get("sent_has_00000", True),
    })

print(f"Total pairs: {len(pairs)}")
print(f"Base CCH: {pairs[0]['cch']}")

def remove_key(json_str, key):
    try:
        obj = json.loads(json_str)
        if key in obj: del obj[key]
        return json.dumps(obj, separators=(',', ':'), ensure_ascii=True)
    except: return json_str

def blank_key(json_str, key):
    try:
        obj = json.loads(json_str)
        if key in obj: obj[key] = ""
        return json.dumps(obj, separators=(',', ':'), ensure_ascii=True)
    except: return json_str

def compute_hashes(data_bytes):
    results = []
    # SHA256
    h = hashlib.sha256(data_bytes).hexdigest()
    hi = int(h, 16)
    results.append(("sha256_first5", h[:5]))
    results.append(("sha256_last5", h[-5:]))
    results.append(("sha256_mod20", format(hi % (1 << 20), '05x')))
    results.append(("sha256_low32_mod20", format((hi & 0xFFFFFFFF) % (1 << 20), '05x')))
    results.append(("sha256_low20", format(hi & 0xFFFFF, '05x')))
    # MD5
    h = hashlib.md5(data_bytes).hexdigest()
    hi = int(h, 16)
    results.append(("md5_first5", h[:5]))
    results.append(("md5_mod20", format(hi % (1 << 20), '05x')))
    # SHA1
    h = hashlib.sha1(data_bytes).hexdigest()
    hi = int(h, 16)
    results.append(("sha1_first5", h[:5]))
    results.append(("sha1_mod20", format(hi % (1 << 20), '05x')))
    # CRC32
    crc = 0xFFFFFFFF
    for b in data_bytes:
        crc ^= b
        for _ in range(8):
            if crc & 1: crc = (crc >> 1) ^ 0xEDB88320
            else: crc >>= 1
    crc ^= 0xFFFFFFFF
    results.append(("crc32_mod20", format(crc % (1 << 20), '05x')))
    results.append(("crc32_low20", format(crc & 0xFFFFF, '05x')))
    # FNV-1a
    h = 0x811c9dc5
    for b in data_bytes:
        h ^= b; h = (h * 0x01000193) & 0xFFFFFFFF
    results.append(("fnv1a_mod20", format(h % (1 << 20), '05x')))
    results.append(("fnv1a_low20", format(h & 0xFFFFF, '05x')))
    # djb2
    h = 5381
    for b in data_bytes:
        h = ((h * 33) + b) & 0xFFFFFFFF
    results.append(("djb2_mod20", format(h % (1 << 20), '05x')))
    return results

# Test body variants
test_pairs = [p for p in pairs if p["sent_has_00000"]][:20]
print(f"\nTesting {len(test_pairs)} pairs with cch=00000 placeholder")

best_algo = {}
for p in test_pairs:
    sent = p["sent_body"].encode('utf-8')
    wire = p["wire_body"].encode('utf-8')

    variants = {
        "sent_raw": sent,
        "wire_raw": wire,
        "sent_no_model_val": blank_key(p["sent_body"], "model").encode('utf-8'),
        "sent_no_model_key": remove_key(p["sent_body"], "model").encode('utf-8'),
        "sent_no_max": remove_key(p["sent_body"], "max_tokens").encode('utf-8'),
        "sent_no_model_max": remove_key(remove_key(p["sent_body"], "model"), "max_tokens").encode('utf-8'),
    }

    # Body without cch=00000; portion
    no_cch = p["sent_body"].replace(" cch=00000;", "").encode('utf-8')
    no_cch2 = p["sent_body"].replace("cch=00000;", "").encode('utf-8')
    variants["sent_no_cch"] = no_cch
    variants["sent_no_cch2"] = no_cch2

    for vname, vdata in variants.items():
        hashes = compute_hashes(vdata)
        for hname, hval in hashes:
            key = f"{vname}/{hname}"
            if hval == p["cch"]:
                best_algo[key] = best_algo.get(key, 0) + 1

print(f"\nHash matches (out of {len(test_pairs)}):")
if best_algo:
    for algo, count in sorted(best_algo.items(), key=lambda x: -x[1])[:20]:
        print(f"  {algo}: {count}")
else:
    print("  No matches found in basic hashes")

# Phase 2: wyhash simulation
print("\n=== wyhash simulation ===")
def wyhash_mum(a, b):
    r = a * b
    return (r & 0xFFFFFFFFFFFFFFFF) ^ (r >> 64)

def wyhash_v4(data, seed=0):
    p0 = 0xa0761d6478bd642f
    p1 = 0xe7037ed1a0b428db
    length = len(data)
    s = seed ^ p0
    i = 0
    while i + 8 <= length:
        v = int.from_bytes(data[i:i+8], 'little')
        s = wyhash_mum(s ^ v, p1)
        i += 8
    remaining = length - i
    if remaining > 0:
        v = 0
        for j in range(remaining):
            v |= data[i+j] << (j * 8)
        s = wyhash_mum(s ^ v, p0 ^ (remaining << 48))
    s = wyhash_mum(s ^ length, p1)
    return s & 0xFFFFFFFFFFFFFFFF

wyhash_results = {}
for p in test_pairs:
    sent = p["sent_body"].encode('utf-8')
    for seed in [0, 0xabc, 0x12345, 0x59cf53e54c78]:
        h = wyhash_v4(sent, seed)
        candidates = {
            f"wy_s{seed:x}_mod20": format(h % (1 << 20), '05x'),
            f"wy_s{seed:x}_low20": format(h & 0xFFFFF, '05x'),
            f"wy_s{seed:x}_low32_mod20": format((h & 0xFFFFFFFF) % (1 << 20), '05x'),
            f"wy_s{seed:x}_low32_hex5": format(h & 0xFFFFFFFF, '08x')[:5],
            f"wy_s{seed:x}_high20": format((h >> 44) & 0xFFFFF, '05x'),
        }
        for cn, cv in candidates.items():
            if cv == p["cch"]:
                wyhash_results[cn] = wyhash_results.get(cn, 0) + 1

if wyhash_results:
    for algo, count in sorted(wyhash_results.items(), key=lambda x: -x[1]):
        print(f"  {algo}: {count}/{len(test_pairs)}")
else:
    print("  No wyhash matches")

# Phase 3: Detailed pair analysis
print("\n=== Key pair comparisons ===")
p0, p2, p3, p6 = pairs[0], pairs[2], pairs[3], pairs[6]
print(f"base (cch={p0['cch']}): len={len(p0['sent_body'])}")
print(f"model+max changed (cch={p2['cch']}): len={len(p2['sent_body'])}")
print(f"  â†?Same CCH! Model value and max_tokens value don't matter")
print(f"no_model (cch={p3['cch']}): len={len(p3['sent_body'])}")
print(f"  â†?Different CCH! Model KEY presence matters")
print(f"stream=false (cch={p6['cch']}): len={len(p6['sent_body'])}")
print(f"  â†?Different CCH! stream value matters")

# Show exact diffs
for i in range(min(len(p0['sent_body']), len(p2['sent_body']))):
    if p0['sent_body'][i] != p2['sent_body'][i]:
        s = max(0, i-15)
        e = min(len(p0['sent_body']), len(p2['sent_body']), i+25)
        print(f"\nbase vs model_changed diff at pos {i}:")
        print(f"  base:  ...{repr(p0['sent_body'][s:e])}...")
        print(f"  model: ...{repr(p2['sent_body'][s:e])}...")
        break
