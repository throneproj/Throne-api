// Package wire implements version 1 of the Throne API wire protocol (spec/protocol.md):
// the handshake, the key derivation and the encrypted frames, in both roles.
package wire

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// Version is the protocol version, the first byte of the ClientHello.
	Version = 0x01
	// KeySize is the size of the shared key.
	KeySize = 32
	// RandomSize is the size of the random value in each hello.
	RandomSize = 16
	// MinLength is the smallest frame length: the tag of an empty message.
	MinLength = chacha20poly1305.Overhead
	// MaxClientLength is the largest length of a frame the client sends.
	MaxClientLength = 1 << 20
	// MaxServerLength is the largest length of a frame the server sends.
	MaxServerLength = 64 << 20

	headerSize = 4
	labelC2S   = "throne-api/1 c2s"
	labelS2C   = "throne-api/1 s2c"
)

var (
	// ErrVersion means the client speaks a protocol version the server does not.
	ErrVersion = errors.New("wire: unsupported protocol version")
	// ErrLength means a received frame announced a length out of range.
	ErrLength = errors.New("wire: frame length out of range")
	// ErrAuth means a received frame failed authentication.
	ErrAuth = errors.New("wire: frame failed authentication")
	// ErrTooLarge means a message does not fit in one frame.
	ErrTooLarge = errors.New("wire: message too large for a frame")
	// ErrCounter means a direction has used every counter value.
	ErrCounter = errors.New("wire: frame counter exhausted")
)

// DeriveKeys returns the keys of both directions for the shared key and the random
// values S (the ServerHello) and C (the ClientHello without its version byte).
func DeriveKeys(key *[KeySize]byte, s, c []byte) (c2s, s2c [KeySize]byte) {
	return derive(key, labelC2S, s, c), derive(key, labelS2C, s, c)
}

func derive(key *[KeySize]byte, label string, s, c []byte) (out [KeySize]byte) {
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(label))
	mac.Write(s)
	mac.Write(c)
	mac.Sum(out[:0])
	return out
}

// half is the state of one direction: its cipher, its counter and its length limit.
type half struct {
	aead    cipher.AEAD
	counter uint64
	spent   bool
	limit   int
}

func newHalf(key [KeySize]byte, limit int) (*half, error) {
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	return &half{aead: aead, limit: limit}, nil
}

func (h *half) nonce() []byte {
	n := make([]byte, chacha20poly1305.NonceSize)
	binary.BigEndian.PutUint64(n[4:], h.counter)
	return n
}

// advance moves to the next counter value; the direction is spent rather than let it wrap.
func (h *half) advance() {
	h.counter++
	h.spent = h.counter == 0
}

// seal returns the frame that carries p.
func (h *half) seal(p []byte) ([]byte, error) {
	if h.spent {
		return nil, ErrCounter
	}
	n := len(p) + chacha20poly1305.Overhead
	if n > h.limit {
		return nil, ErrTooLarge
	}
	var hdr [headerSize]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(n))
	frame := make([]byte, headerSize, headerSize+n)
	copy(frame, hdr[:])
	frame = h.aead.Seal(frame, h.nonce(), p, hdr[:])
	h.advance()
	return frame, nil
}

// read reads the next frame from r and returns its message. It checks the length
// before it reads the rest of the frame.
func (h *half) read(r io.Reader) ([]byte, error) {
	if h.spent {
		return nil, ErrCounter
	}
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n < MinLength || int64(n) > int64(h.limit) {
		return nil, ErrLength
	}
	// The buffer grows with the data that arrives rather than trusting an unauthenticated length.
	sealed, err := io.ReadAll(io.LimitReader(r, int64(n)))
	if err != nil {
		return nil, err
	}
	if len(sealed) < int(n) {
		return nil, io.ErrUnexpectedEOF
	}
	p, err := h.aead.Open(sealed[:0], h.nonce(), sealed, hdr[:])
	if err != nil {
		return nil, ErrAuth
	}
	h.advance()
	return p, nil
}

// Conn carries messages over a connection after the handshake. It is not safe for
// concurrent use.
type Conn struct {
	nc   net.Conn
	send *half
	recv *half
}

// Client runs the client's side of the handshake on nc. On error the caller closes nc.
func Client(nc net.Conn, key *[KeySize]byte) (*Conn, error) {
	return clientHandshake(nc, key, rand.Reader)
}

// Server runs the server's side of the handshake on nc. On error, ErrVersion included,
// the caller closes nc without sending anything.
func Server(nc net.Conn, key *[KeySize]byte) (*Conn, error) {
	return serverHandshake(nc, key, rand.Reader)
}

func clientHandshake(nc net.Conn, key *[KeySize]byte, rnd io.Reader) (*Conn, error) {
	hello := make([]byte, 1+RandomSize)
	hello[0] = Version
	if _, err := io.ReadFull(rnd, hello[1:]); err != nil {
		return nil, err
	}
	s := make([]byte, RandomSize)
	if err := swapHellos(nc, hello, s); err != nil {
		return nil, err
	}
	c2s, s2c := DeriveKeys(key, s, hello[1:])
	return newConn(nc, c2s, MaxClientLength, s2c, MaxServerLength)
}

func serverHandshake(nc net.Conn, key *[KeySize]byte, rnd io.Reader) (*Conn, error) {
	s := make([]byte, RandomSize)
	if _, err := io.ReadFull(rnd, s); err != nil {
		return nil, err
	}
	hello := make([]byte, 1+RandomSize)
	if err := swapHellos(nc, s, hello); err != nil {
		return nil, err
	}
	if hello[0] != Version {
		return nil, ErrVersion
	}
	c2s, s2c := DeriveKeys(key, s, hello[1:])
	return newConn(nc, s2c, MaxServerLength, c2s, MaxClientLength)
}

func newConn(nc net.Conn, sendKey [KeySize]byte, sendLimit int, recvKey [KeySize]byte, recvLimit int) (*Conn, error) {
	send, err := newHalf(sendKey, sendLimit)
	if err != nil {
		return nil, err
	}
	recv, err := newHalf(recvKey, recvLimit)
	if err != nil {
		return nil, err
	}
	return &Conn{nc: nc, send: send, recv: recv}, nil
}

// swapHellos writes mine while it reads the peer's hello, since neither side waits for
// the other.
func swapHellos(nc net.Conn, mine, peer []byte) error {
	written := make(chan error, 1)
	go func() {
		_, err := nc.Write(mine)
		written <- err
	}()
	if _, err := io.ReadFull(nc, peer); err != nil {
		return err
	}
	return <-written
}

// WriteMessage sends p in one frame.
func (c *Conn) WriteMessage(p []byte) error {
	frame, err := c.send.seal(p)
	if err != nil {
		return err
	}
	_, err = c.nc.Write(frame)
	return err
}

// ReadMessage receives the next frame and returns its message.
func (c *Conn) ReadMessage() ([]byte, error) {
	return c.recv.read(c.nc)
}
