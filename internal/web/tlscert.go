package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const certValidity = 10 * 365 * 24 * time.Hour

func certDir(stateDir string) string { return filepath.Join(stateDir, "tls") }

// loadCertificate loads the given certificate, or the self-signed one in the state
// directory, which it creates when there is none or it is unusable or expired.
func loadCertificate(stateDir, certFile, keyFile string) (cert tls.Certificate, created bool, err error) {
	if certFile != "" {
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return cert, false, &ConfigError{"TLS certificate: " + err.Error()}
		}
		return cert, false, nil
	}
	dir := certDir(stateDir)
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if cert, err = tls.LoadX509KeyPair(certFile, keyFile); err == nil {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil && time.Now().Before(leaf.NotAfter) {
			return cert, false, nil
		}
	}
	certPEM, keyPEM, err := selfSigned(time.Now())
	if err != nil {
		return cert, false, fmt.Errorf("create a TLS certificate: %w", err)
	}
	if err := makeDir(dir); err != nil {
		return cert, false, fmt.Errorf("create a TLS certificate: %w", err)
	}
	if err := writeFileAtomic(keyFile, ".key-", keyPEM); err != nil {
		return cert, false, fmt.Errorf("save the TLS key: %w", err)
	}
	if err := writeFileAtomic(certFile, ".cert-", certPEM); err != nil {
		return cert, false, fmt.Errorf("save the TLS certificate: %w", err)
	}
	cert, err = tls.X509KeyPair(certPEM, keyPEM)
	return cert, true, err
}

// selfSigned creates an ECDSA P-256 certificate for "Throne web UI", valid for 10 years,
// for localhost, the loopback addresses, the host name and the addresses of the network
// interfaces.
func selfSigned(now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Throne web UI"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	if host, err := os.Hostname(); err == nil {
		if host = strings.ToLower(strings.TrimSuffix(host, ".")); validDNSName(host) && host != "localhost" {
			tmpl.DNSNames = append(tmpl.DNSNames, host)
		}
	}
	for _, ip := range interfaceAddrs(false) {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.IP(ip.AsSlice()))
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), nil
}

func validDNSName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// fingerprint is the SHA-256 of a DER certificate in uppercase hex pairs, separated by
// colons.
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}
