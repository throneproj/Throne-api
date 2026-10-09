#!/usr/bin/env python3
"""Reference implementation of the Throne API v1 frame layer. Writes v1.json next to itself.

Dependency-free on purpose: ChaCha20-Poly1305 is implemented here from RFC 8439 and checked
against the RFC's own test vectors before anything is written.
"""

import base64
import hashlib
import hmac
import json
import os
import struct

PROTOCOL = "throne-api/1"
VERSION = 0x01
C2S_LIMIT = 1 << 20
S2C_LIMIT = 64 << 20


def _rotl32(v, c):
    return ((v << c) & 0xFFFFFFFF) | (v >> (32 - c))


def _quarter(s, a, b, c, d):
    s[a] = (s[a] + s[b]) & 0xFFFFFFFF; s[d] = _rotl32(s[d] ^ s[a], 16)
    s[c] = (s[c] + s[d]) & 0xFFFFFFFF; s[b] = _rotl32(s[b] ^ s[c], 12)
    s[a] = (s[a] + s[b]) & 0xFFFFFFFF; s[d] = _rotl32(s[d] ^ s[a], 8)
    s[c] = (s[c] + s[d]) & 0xFFFFFFFF; s[b] = _rotl32(s[b] ^ s[c], 7)


def chacha20_block(key, counter, nonce):
    state = [0x61707865, 0x3320646E, 0x79622D32, 0x6B206574]
    state += list(struct.unpack("<8I", key)) + [counter] + list(struct.unpack("<3I", nonce))
    w = state[:]
    for _ in range(10):
        _quarter(w, 0, 4, 8, 12); _quarter(w, 1, 5, 9, 13)
        _quarter(w, 2, 6, 10, 14); _quarter(w, 3, 7, 11, 15)
        _quarter(w, 0, 5, 10, 15); _quarter(w, 1, 6, 11, 12)
        _quarter(w, 2, 7, 8, 13); _quarter(w, 3, 4, 9, 14)
    return struct.pack("<16I", *[(w[i] + state[i]) & 0xFFFFFFFF for i in range(16)])


def chacha20_xor(key, counter, nonce, data):
    out = bytearray()
    for i in range(0, len(data), 64):
        block = chacha20_block(key, counter + i // 64, nonce)
        out += bytes(x ^ y for x, y in zip(data[i:i + 64], block))
    return bytes(out)


def poly1305(key, msg):
    r = int.from_bytes(key[:16], "little") & 0x0FFFFFFC0FFFFFFC0FFFFFFC0FFFFFFF
    s = int.from_bytes(key[16:], "little")
    p = (1 << 130) - 5
    acc = 0
    for i in range(0, len(msg), 16):
        acc = ((acc + int.from_bytes(msg[i:i + 16] + b"\x01", "little")) * r) % p
    return ((acc + s) % (1 << 128)).to_bytes(16, "little")


def _pad16(b):
    return b"\x00" * (-len(b) % 16)


def _tag(key, nonce, aad, ct):
    otk = chacha20_block(key, 0, nonce)[:32]
    return poly1305(otk, aad + _pad16(aad) + ct + _pad16(ct) + struct.pack("<QQ", len(aad), len(ct)))


def aead_seal(key, nonce, aad, plaintext):
    ct = chacha20_xor(key, 1, nonce, plaintext)
    return ct + _tag(key, nonce, aad, ct)


def aead_open(key, nonce, aad, sealed):
    if len(sealed) < 16:
        return None
    ct, tag = sealed[:-16], sealed[-16:]
    if not hmac.compare_digest(_tag(key, nonce, aad, ct), tag):
        return None
    return chacha20_xor(key, 1, nonce, ct)


def self_test():
    # RFC 8439 2.3.2
    block = chacha20_block(bytes(range(32)), 1, bytes.fromhex("000000090000004a00000000"))
    assert block.hex().startswith("10f1e7e4d13b5915500fdd1fa32071c4"), "chacha20 block"
    # RFC 8439 2.5.2
    tag = poly1305(bytes.fromhex("85d6be7857556d337f4452fe42d506a80103808afb0db2fd4abff6af4149f51b"),
                   b"Cryptographic Forum Research Group")
    assert tag.hex() == "a8061dc1305136c6c22b8baf0c0127a9", "poly1305"
    # RFC 8439 2.8.2
    pt = (b"Ladies and Gentlemen of the class of '99: If I could offer you only one tip "
          b"for the future, sunscreen would be it.")
    sealed = aead_seal(bytes(range(0x80, 0xA0)), bytes.fromhex("070000004041424344454647"),
                       bytes.fromhex("50515253c0c1c2c3c4c5c6c7"), pt)
    assert sealed[:16].hex() == "d31a8d34648e60db7b86afbc53ef7ec2", "aead ciphertext"
    assert sealed[-16:].hex() == "1ae10b594f09e26a7e902ecbd0600691", "aead tag"


def key_text(key):
    return base64.urlsafe_b64encode(key).rstrip(b"=").decode()


def derive(key, s, c):
    msg = s + c
    k_c2s = hmac.new(key, (PROTOCOL + " c2s").encode() + msg, hashlib.sha256).digest()
    k_s2c = hmac.new(key, (PROTOCOL + " s2c").encode() + msg, hashlib.sha256).digest()
    return k_c2s, k_s2c


def nonce(counter):
    return b"\x00\x00\x00\x00" + struct.pack(">Q", counter)


def seal_frame(key, counter, plaintext):
    length = struct.pack(">I", len(plaintext) + 16)
    return length + aead_seal(key, nonce(counter), length, plaintext)


def open_frame(key, counter, wire, limit):
    if len(wire) < 4:
        return None
    (length,) = struct.unpack(">I", wire[:4])
    if length < 16 or length > limit or len(wire) != 4 + length:
        return None
    return aead_open(key, nonce(counter), wire[:4], wire[4:])


def build_case(name, key, s, c, messages):
    k_c2s, k_s2c = derive(key, s, c)
    keys = {"c2s": k_c2s, "s2c": k_s2c}
    limits = {"c2s": C2S_LIMIT, "s2c": S2C_LIMIT}
    counters = {"c2s": 0, "s2c": 0}
    frames = []
    for direction, text in messages:
        wire = seal_frame(keys[direction], counters[direction], text.encode())
        assert open_frame(keys[direction], counters[direction], wire, limits[direction]) == text.encode()
        frames.append({"dir": direction, "counter": counters[direction], "plaintext": text, "wire": wire.hex()})
        counters[direction] += 1
    return {
        "name": name,
        "key": key_text(key),
        "server_hello": s.hex(),
        "client_hello": bytes([VERSION]).hex() + c.hex(),
        "k_c2s": k_c2s.hex(),
        "k_s2c": k_s2c.hex(),
        "frames": frames,
    }, keys


def invalid_frames(case, keys):
    frames = case["frames"]
    c2s0 = bytes.fromhex(frames[0]["wire"])
    s2c0 = bytes.fromhex(frames[1]["wire"])
    c2s1 = bytes.fromhex(frames[2]["wire"])
    other_key = hashlib.sha256(b"not the key").digest()
    other_c2s, _ = derive(other_key, bytes.fromhex(case["server_hello"]), bytes.fromhex(case["client_hello"])[1:])

    def flip(wire, index):
        b = bytearray(wire)
        b[index] ^= 0x01
        return bytes(b)

    longer = struct.pack(">I", len(c2s0) - 4 + 1) + c2s0[4:] + b"\x00"
    cases = [
        ("tag bit flipped", "c2s", 0, flip(c2s0, len(c2s0) - 1)),
        ("ciphertext bit flipped", "c2s", 0, flip(c2s0, 4)),
        ("length header and size changed", "c2s", 0, longer),
        ("frame from a later counter", "c2s", 0, c2s1),
        ("server frame sent back to the server", "c2s", 0, s2c0),
        ("client frame sent back to the client", "s2c", 0, c2s0),
        ("sealed with another key", "c2s", 0, seal_frame(other_c2s, 0, frames[0]["plaintext"].encode())),
        ("length below 16", "c2s", 0, struct.pack(">I", 15) + b"\x00" * 15),
        ("length above the client limit", "c2s", 0, struct.pack(">I", C2S_LIMIT + 1)),
    ]
    limits = {"c2s": C2S_LIMIT, "s2c": S2C_LIMIT}
    out = []
    for name, direction, counter, wire in cases:
        assert open_frame(keys[direction], counter, wire, limits[direction]) is None, name
        out.append({"name": name, "dir": direction, "counter": counter, "wire": wire.hex()})
    return out


def main():
    self_test()

    first, first_keys = build_case(
        "two exchanges",
        bytes(range(32)),
        bytes(range(0xA0, 0xB0)),
        bytes(range(0xB0, 0xC0)),
        [
            ("c2s", '{"method":"GET","path":"/v1/status"}'),
            ("s2c", '{"status":200,"body":{"state":"idle"}}'),
            ("c2s", '{"method":"PUT","path":"/v1/connection","body":{"profile":12}}'),
            ("s2c", '{"status":409,"error":{"code":"busy","message":"Another profile is starting..."}}'),
        ],
    )
    first["invalid"] = invalid_frames(first, first_keys)

    long_body = json.dumps({"profiles": [{"id": i, "name": "Server %d — ü" % i} for i in range(12)]},
                           ensure_ascii=False, separators=(",", ":"))
    second, _ = build_case(
        "multi-block and empty frames",
        hashlib.sha256(b"throne-api vector key 2").digest(),
        hashlib.sha256(b"server hello 2").digest()[:16],
        hashlib.sha256(b"client hello 2").digest()[:16],
        [
            ("c2s", '{"method":"GET","path":"/v1/profiles","query":{"group":"3","name":"DE|NL"}}'),
            ("s2c", '{"status":200,"body":' + long_body + "}"),
            ("c2s", ""),
            ("s2c", ""),
        ],
    )

    doc = {
        "protocol": PROTOCOL,
        "description": "Frames are listed in send order. Every frame must seal to `wire` and open back to "
                       "`plaintext`. Every `invalid` entry must be rejected by a receiver that expects "
                       "direction `dir` and counter `counter` under that case's keys. Empty plaintexts "
                       "exercise the frame layer only; they are not valid messages.",
        "cases": [first, second],
    }
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "v1.json")
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(doc, f, ensure_ascii=False, indent=2)
        f.write("\n")
    print("wrote", path)


if __name__ == "__main__":
    main()
