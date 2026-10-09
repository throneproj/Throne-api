package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"testing"
)

type hexBytes []byte

func (h *hexBytes) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := hex.DecodeString(s)
	*h = v
	return err
}

type vectorFrame struct {
	Name      string   `json:"name"`
	Dir       string   `json:"dir"`
	Counter   uint64   `json:"counter"`
	Plaintext string   `json:"plaintext"`
	Wire      hexBytes `json:"wire"`
}

type vectorCase struct {
	Name        string        `json:"name"`
	Key         string        `json:"key"`
	ServerHello hexBytes      `json:"server_hello"`
	ClientHello hexBytes      `json:"client_hello"`
	KC2S        hexBytes      `json:"k_c2s"`
	KS2C        hexBytes      `json:"k_s2c"`
	Frames      []vectorFrame `json:"frames"`
	Invalid     []vectorFrame `json:"invalid"`
}

func loadVectors(t *testing.T) []vectorCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "spec", "vectors", "v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Protocol string       `json:"protocol"`
		Cases    []vectorCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Protocol != "throne-api/1" || len(doc.Cases) == 0 {
		t.Fatalf("unexpected vector file: protocol %q, %d cases", doc.Protocol, len(doc.Cases))
	}
	return doc.Cases
}

func caseKey(t *testing.T, c vectorCase) *[KeySize]byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(c.Key)
	if err != nil || len(b) != KeySize {
		t.Fatalf("case %q: key does not decode to %d bytes", c.Name, KeySize)
	}
	return (*[KeySize]byte)(b)
}

func directions(t *testing.T, c vectorCase) (keys map[string][KeySize]byte, limits map[string]int) {
	t.Helper()
	if len(c.ServerHello) != RandomSize || len(c.ClientHello) != 1+RandomSize || c.ClientHello[0] != Version {
		t.Fatalf("case %q: malformed hellos", c.Name)
	}
	c2s, s2c := DeriveKeys(caseKey(t, c), c.ServerHello, c.ClientHello[1:])
	return map[string][KeySize]byte{"c2s": c2s, "s2c": s2c},
		map[string]int{"c2s": MaxClientLength, "s2c": MaxServerLength}
}

func newTestHalf(t *testing.T, key [KeySize]byte, limit int, counter uint64) *half {
	t.Helper()
	h, err := newHalf(key, limit)
	if err != nil {
		t.Fatal(err)
	}
	h.counter = counter
	return h
}

func TestVectorKeys(t *testing.T) {
	for _, c := range loadVectors(t) {
		keys, _ := directions(t, c)
		c2s, s2c := keys["c2s"], keys["s2c"]
		if !bytes.Equal(c2s[:], c.KC2S) {
			t.Errorf("case %q: k_c2s differs from the vector", c.Name)
		}
		if !bytes.Equal(s2c[:], c.KS2C) {
			t.Errorf("case %q: k_s2c differs from the vector", c.Name)
		}
	}
}

func TestVectorFrames(t *testing.T) {
	for _, c := range loadVectors(t) {
		keys, limits := directions(t, c)
		for i, f := range c.Frames {
			got, err := newTestHalf(t, keys[f.Dir], limits[f.Dir], f.Counter).seal([]byte(f.Plaintext))
			if err != nil {
				t.Fatalf("case %q frame %d: seal: %v", c.Name, i, err)
			}
			if !bytes.Equal(got, f.Wire) {
				t.Errorf("case %q frame %d: sealed\n%x\nwant\n%x", c.Name, i, got, []byte(f.Wire))
			}

			h := newTestHalf(t, keys[f.Dir], limits[f.Dir], f.Counter)
			r := bytes.NewReader(f.Wire)
			p, err := h.read(r)
			if err != nil {
				t.Fatalf("case %q frame %d: open: %v", c.Name, i, err)
			}
			if string(p) != f.Plaintext {
				t.Errorf("case %q frame %d: opened %q, want %q", c.Name, i, p, f.Plaintext)
			}
			if r.Len() != 0 {
				t.Errorf("case %q frame %d: %d bytes left unread", c.Name, i, r.Len())
			}
			if h.counter != f.Counter+1 {
				t.Errorf("case %q frame %d: counter %d after open, want %d", c.Name, i, h.counter, f.Counter+1)
			}
		}
	}
}

func TestVectorInvalid(t *testing.T) {
	n := 0
	for _, c := range loadVectors(t) {
		keys, limits := directions(t, c)
		for _, f := range c.Invalid {
			n++
			want := ErrAuth
			if l := binary.BigEndian.Uint32(f.Wire); l < MinLength || int64(l) > int64(limits[f.Dir]) {
				want = ErrLength
			}
			h := newTestHalf(t, keys[f.Dir], limits[f.Dir], f.Counter)
			p, err := h.read(bytes.NewReader(f.Wire))
			if !errors.Is(err, want) {
				t.Errorf("case %q, %q: err = %v, want %v", c.Name, f.Name, err, want)
			}
			if p != nil {
				t.Errorf("case %q, %q: returned plaintext of a rejected frame", c.Name, f.Name)
			}
			if h.counter != f.Counter {
				t.Errorf("case %q, %q: counter moved on a rejected frame", c.Name, f.Name)
			}
		}
	}
	if n == 0 {
		t.Fatal("no invalid vectors")
	}
}

// pipeWrite writes b in the background, since net.Pipe blocks a write until it is read.
func pipeWrite(w io.Writer, b []byte) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := w.Write(b)
		done <- err
	}()
	return done
}

func readExactly(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestVectorReplay runs each case through the real handshake and Conn in both roles,
// with the case's random values, and compares every byte on the wire.
func TestVectorReplay(t *testing.T) {
	for _, c := range loadVectors(t) {
		for _, role := range []string{"client", "server"} {
			t.Run(c.Name+"/"+role, func(t *testing.T) {
				local, peer := net.Pipe()
				defer local.Close()
				defer peer.Close()
				key := caseKey(t, c)

				type result struct {
					conn *Conn
					err  error
				}
				done := make(chan result, 1)
				mine, theirs := c.ClientHello, c.ServerHello
				if role == "server" {
					mine, theirs = c.ServerHello, c.ClientHello
				}
				go func() {
					var r result
					if role == "client" {
						r.conn, r.err = clientHandshake(local, key, bytes.NewReader(c.ClientHello[1:]))
					} else {
						r.conn, r.err = serverHandshake(local, key, bytes.NewReader(c.ServerHello))
					}
					done <- r
				}()
				sent := pipeWrite(peer, theirs)
				if got := readExactly(t, peer, len(mine)); !bytes.Equal(got, mine) {
					t.Fatalf("hello = %x, want %x", got, []byte(mine))
				}
				if err := <-sent; err != nil {
					t.Fatal(err)
				}
				r := <-done
				if r.err != nil {
					t.Fatal(r.err)
				}

				outgoing := "c2s"
				if role == "server" {
					outgoing = "s2c"
				}
				for i, f := range c.Frames {
					if f.Dir == outgoing {
						wrote := make(chan error, 1)
						go func() { wrote <- r.conn.WriteMessage([]byte(f.Plaintext)) }()
						if got := readExactly(t, peer, len(f.Wire)); !bytes.Equal(got, f.Wire) {
							t.Errorf("frame %d on the wire:\n%x\nwant\n%x", i, got, []byte(f.Wire))
						}
						if err := <-wrote; err != nil {
							t.Fatal(err)
						}
						continue
					}
					wrote := pipeWrite(peer, f.Wire)
					p, err := r.conn.ReadMessage()
					if err != nil {
						t.Fatalf("frame %d: %v", i, err)
					}
					if string(p) != f.Plaintext {
						t.Errorf("frame %d: got %q, want %q", i, p, f.Plaintext)
					}
					if err := <-wrote; err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestServerRejectsVersion(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	var key [KeySize]byte
	done := make(chan error, 1)
	go func() {
		_, err := Server(local, &key)
		done <- err
	}()
	hello := make([]byte, 1+RandomSize)
	hello[0] = Version + 1
	sent := pipeWrite(peer, hello)
	readExactly(t, peer, RandomSize)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrVersion) {
		t.Fatalf("err = %v, want ErrVersion", err)
	}
}

func TestCounterExhausted(t *testing.T) {
	var key [KeySize]byte
	s := newTestHalf(t, key, MaxClientLength, math.MaxUint64)
	frame, err := s.seal([]byte("last"))
	if err != nil {
		t.Fatalf("seal with the largest counter: %v", err)
	}
	if _, err := s.seal([]byte("wrapped")); !errors.Is(err, ErrCounter) {
		t.Fatalf("seal after the largest counter: err = %v, want ErrCounter", err)
	}
	r := newTestHalf(t, key, MaxClientLength, math.MaxUint64)
	if p, err := r.read(bytes.NewReader(frame)); err != nil || string(p) != "last" {
		t.Fatalf("open with the largest counter: %q, %v", p, err)
	}
	if _, err := r.read(bytes.NewReader(frame)); !errors.Is(err, ErrCounter) {
		t.Fatalf("open after the largest counter: err = %v, want ErrCounter", err)
	}
}

func TestSealLimit(t *testing.T) {
	var key [KeySize]byte
	h := newTestHalf(t, key, MaxClientLength, 0)
	frame, err := h.seal(make([]byte, MaxClientLength-MinLength))
	if err != nil {
		t.Fatalf("largest message: %v", err)
	}
	if n := binary.BigEndian.Uint32(frame); n != MaxClientLength {
		t.Fatalf("length = %d, want %d", n, MaxClientLength)
	}
	if _, err := h.seal(make([]byte, MaxClientLength-MinLength+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("one byte over: err = %v, want ErrTooLarge", err)
	}
}

func header(n uint32) []byte {
	return binary.BigEndian.AppendUint32(nil, n)
}

func TestReadLengths(t *testing.T) {
	var key [KeySize]byte
	tests := []struct {
		name  string
		limit int
		wire  []byte
		want  error
	}{
		{"closed between frames", MaxServerLength, nil, io.EOF},
		{"partial header", MaxServerLength, []byte{0, 0}, io.ErrUnexpectedEOF},
		{"partial body", MaxServerLength, append(header(MinLength), 1, 2, 3), io.ErrUnexpectedEOF},
		{"below the minimum", MaxServerLength, append(header(MinLength-1), make([]byte, MinLength-1)...), ErrLength},
		{"server frame above the client limit", MaxServerLength, header(MaxClientLength + 1), io.ErrUnexpectedEOF},
		{"server frame above the server limit", MaxServerLength, header(MaxServerLength + 1), ErrLength},
		{"client frame above the client limit", MaxClientLength, header(MaxClientLength + 1), ErrLength},
		{"largest length", MaxServerLength, header(math.MaxUint32), ErrLength},
	}
	for _, tt := range tests {
		_, err := newTestHalf(t, key, tt.limit, 0).read(bytes.NewReader(tt.wire))
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
	}
}

func TestTCPRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var key, other [KeySize]byte
	for i := range key {
		key[i] = byte(i)
		other[i] = byte(255 - i)
	}

	serverErr := make(chan error, 1)
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			func() {
				defer nc.Close()
				c, err := Server(nc, &key)
				if err != nil {
					serverErr <- err
					return
				}
				for {
					p, err := c.ReadMessage()
					if err != nil {
						serverErr <- err
						return
					}
					if err := c.WriteMessage(append([]byte("echo "), p...)); err != nil {
						serverErr <- err
						return
					}
				}
			}()
		}
	}()

	dial := func(k *[KeySize]byte) (*Conn, net.Conn) {
		nc, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		c, err := Client(nc, k)
		if err != nil {
			nc.Close()
			t.Fatal(err)
		}
		return c, nc
	}

	c, nc := dial(&key)
	for _, m := range []string{"one", "", "three"} {
		if err := c.WriteMessage([]byte(m)); err != nil {
			t.Fatal(err)
		}
		p, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if string(p) != "echo "+m {
			t.Fatalf("got %q, want %q", p, "echo "+m)
		}
	}
	nc.Close()
	if err := <-serverErr; !errors.Is(err, io.EOF) {
		t.Fatalf("server after the client closed: %v, want EOF", err)
	}

	c, nc = dial(&other)
	defer nc.Close()
	if err := c.WriteMessage([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; !errors.Is(err, ErrAuth) {
		t.Fatalf("server with another key: %v, want ErrAuth", err)
	}
	if _, err := c.ReadMessage(); err == nil {
		t.Fatal("client read a response although the server rejected its key")
	}
}
