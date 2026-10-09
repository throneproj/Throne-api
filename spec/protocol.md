# Throne API wire protocol, version 1

The Throne API carries JSON requests over TCP. Every message is encrypted and
authenticated with a 32-byte key that Throne and the client share. Without the key nobody
can read the traffic or send requests, whether Throne listens on loopback or on a LAN.

This document is normative. [api.md](api.md) lists the requests.
[vectors/v1.json](vectors/v1.json) holds test vectors that every implementation must pass.

## Building blocks

- HMAC-SHA256 (RFC 2104 with SHA-256)
- ChaCha20-Poly1305 as specified in RFC 8439, section 2.8: 32-byte key, 12-byte nonce,
  16-byte tag
- The operating system's cryptographically secure random number generator

## The key

Throne generates the key: 32 random bytes. Its text form is base64url without padding
(RFC 4648, section 5), which is 43 characters. Throne shows and copies the key in this
form, and clients read it from the `THRONE_KEY` environment variable or a key file.

A client rejects a key that does not decode to exactly 32 bytes. Surrounding whitespace,
such as the newline at the end of a key file, is ignored.

## Connection

The client opens a TCP connection to Throne. The default port is 9095.

### Handshake

Both sides send a hello as soon as the connection is open. Neither waits for the other.

```
server → client   ServerHello = S             16 bytes
client → server   ClientHello = 0x01 || C     17 bytes
```

S and C are fresh random values for every connection. `0x01` is the protocol version. A
server that does not support the version closes the connection.

Both sides then derive two 32-byte keys:

```
k_c2s = HMAC-SHA256(K, "throne-api/1 c2s" || S || C)
k_s2c = HMAC-SHA256(K, "throne-api/1 s2c" || S || C)
```

K is the 32-byte key. The labels are ASCII, 16 bytes each, with no terminator. k_c2s
protects the frames the client sends, k_s2c the frames the server sends.

### Frames

After the hellos, each side sends frames:

```
frame  = length || sealed
length = size of sealed, 4 bytes, unsigned big-endian
sealed = ChaCha20-Poly1305 encryption of the message (ciphertext, then the 16-byte tag),
         with key   = the sender's direction key,
              nonce = 0x00000000 || counter (8 bytes, unsigned big-endian),
              aad   = length (the same 4 bytes)
```

Each direction keeps its own counter. The first frame in each direction uses counter 0,
and every frame after it adds 1. The receiver opens each frame with the counter value it
expects next, so a frame that is replayed, reordered, dropped or sent back to its sender
fails authentication.

`length` is at least 16, which is an empty message. The upper limits are:

| Direction | Largest `length` |
|---|---|
| client → server | 1 MiB (1 048 576) |
| server → client | 64 MiB (67 108 864) |

A receiver checks `length` before it reads the rest of the frame.

Any failure ends the connection: an unsupported version, a `length` out of range, a frame
that fails authentication, or a timeout. The receiver closes the connection without sending
anything.

## Messages

The plaintext of every frame is one JSON object (RFC 8259) in UTF-8, without a byte order
mark.

### Request (client → server)

```json
{"method": "GET", "path": "/v1/profiles", "query": {"group": "3"}}
```

| Field | Type | |
|---|---|---|
| `method` | string | `GET`, `POST`, `PUT` or `DELETE` |
| `path` | string | Starts with `/`. No query string and no fragment |
| `query` | object | Optional. Every value is a string |
| `body` | any | Optional. A missing body and `null` mean the same |

### Response (server → client)

```json
{"status": 200, "body": {"state": "idle"}}
{"status": 409, "error": {"code": "busy", "message": "Another profile is starting..."}}
```

`status` follows HTTP status semantics. A 2xx response carries `body`, which may be `null`.
A 4xx or 5xx response carries `error`: `code` is stable and meant for programs, and
`message` is meant for people and may change or be translated.

A frame that authenticates but does not hold a valid request object gets status 400 with
code `bad_request`.

### Exchanges

Requests and responses alternate. The client sends a request and waits for its response,
and then it may send the next request on the same connection. The server never sends a
frame except as the response to a request.

Either side may close the connection at any time. The server closes it:

- when the ClientHello has not arrived 10 seconds after the connection opened;
- when no complete request has arrived 60 seconds after the hellos or after the last
  response.

The time the server spends on a request does not count toward these limits. Some requests
wait for a result before they respond (see `timeout` in [api.md](api.md)), so clients set
their own read timeout above the `timeout` they ask for.

## Security

The protocol provides the following:

- Only a holder of the key can produce frames that the other side accepts. So only a
  holder of the key can send requests, and only Throne can answer them.
- The messages are encrypted. Someone watching the network sees frame sizes and timing,
  not contents.
- Frames cannot be replayed. S and C are fresh for every connection, so a frame from one
  connection fails on every other. Within a connection the counters stop replayed,
  reordered, dropped and reflected frames.
- A response is bound to its connection and its position in it, so it cannot be swapped
  for another response.

The protocol does not provide the following:

- Forward secrecy. Anyone who learns the key can decrypt traffic they recorded earlier.
  Generating a new key in Throne ends this for future connections.
- Hiding that a connection exists, when it is used or how large its messages are.
- Limits on what a key holder can do. The key grants every request in api.md.

Implementations must:

- take S and C from the operating system's random number generator;
- verify tags only with the AEAD's own decryption function, never with a comparison that
  is not constant-time;
- never use or reveal any plaintext from a frame that failed authentication;
- keep both counters as unsigned 64-bit integers, and close the connection rather than let
  a counter wrap.

## Test vectors

[vectors/v1.json](vectors/v1.json) contains, for each case: the key in text form, both
hellos, both derived keys, and a list of frames with their plaintext and the exact bytes
on the wire. An implementation must produce the same `wire` bytes from the plaintext and
recover the plaintext from them. Each case also lists invalid frames that a receiver
expecting that direction and counter must reject.

[vectors/gen_vectors.py](vectors/gen_vectors.py) regenerates the file. It is a
dependency-free reference implementation that first checks itself against the
ChaCha20-Poly1305 test vectors in RFC 8439.
