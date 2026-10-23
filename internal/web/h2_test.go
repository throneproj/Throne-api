package web

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// serveTLS runs a gateway with TLS on a real listener.
func serveTLS(t *testing.T, edit func(s *Server)) string {
	t.Helper()
	s, err := New(Config{Throne: newScripted(), ThroneAddr: "127.0.0.1:9095", Listen: "127.0.0.1:0", StateDir: t.TempDir(),
		TLS: true})
	if err != nil {
		t.Fatal(err)
	}
	s.t = fastTiming
	if edit != nil {
		edit(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	return ln.Addr().String()
}

func TestHTTP2Settings(t *testing.T) {
	addr := serveTLS(t, nil)
	c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if p := c.ConnectionState().NegotiatedProtocol; p != "h2" {
		t.Fatalf("protocol %q", p)
	}
	io.WriteString(c, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
	c.Write([]byte{0, 0, 0, 4, 0, 0, 0, 0, 0})
	header := make([]byte, 9)
	if _, err := io.ReadFull(c, header); err != nil {
		t.Fatal(err)
	}
	if header[3] != 4 {
		t.Fatalf("first frame of type %d, want SETTINGS", header[3])
	}
	payload := make([]byte, int(header[0])<<16|int(header[1])<<8|int(header[2]))
	if _, err := io.ReadFull(c, payload); err != nil {
		t.Fatal(err)
	}
	streams := -1
	for p := payload; len(p) >= 6; p = p[6:] {
		if binary.BigEndian.Uint16(p) == 3 {
			streams = int(binary.BigEndian.Uint32(p[2:]))
		}
	}
	if streams != 32 {
		t.Errorf("SETTINGS_MAX_CONCURRENT_STREAMS %d", streams)
	}
}

// TestHTTP2Anonymous checks that streams of one connection count against the cap on
// requests without a session.
func TestHTTP2Anonymous(t *testing.T) {
	addr := serveTLS(t, func(s *Server) { s.t.bodyRead = time.Second })
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true, MaxConnsPerHost: 1}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	resp, err := client.Get("https://" + addr + "/auth/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("protocol %s", resp.Proto)
	}
	const n = 20
	var mu sync.Mutex
	codes := map[int]int{}
	var quick int
	var wg sync.WaitGroup
	var writers []*io.PipeWriter
	start := time.Now()
	for range n {
		pr, pw := io.Pipe()
		writers = append(writers, pw)
		wg.Go(func() {
			req, _ := http.NewRequest("POST", "https://"+addr+"/auth/login", pr)
			req.ContentLength = 100
			resp, err := client.Do(req)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				codes[0]++
				return
			}
			resp.Body.Close()
			codes[resp.StatusCode]++
			if resp.StatusCode == 429 && time.Since(start) < 700*time.Millisecond {
				quick++
			}
		})
	}
	for _, pw := range writers {
		go pw.Write([]byte(`{"pass`))
	}
	wg.Wait()
	for _, pw := range writers {
		pw.Close()
	}
	if codes[429] != n-8 || quick != n-8 {
		t.Errorf("outcomes %v, %d quick 429s", codes, quick)
	}
	if !strings.HasPrefix(resp.Proto, "HTTP/2") {
		t.Errorf("protocol %s", resp.Proto)
	}
}
