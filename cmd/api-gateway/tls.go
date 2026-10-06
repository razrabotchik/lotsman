package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// This fixture serves TLS as well as plain HTTP because lotsman refuses a
// plaintext token endpoint: `authProfiles[].tokenURL` must be https, with no
// exception for loopback (internal/config.requireHTTPS). Without a TLS
// listener the client-credentials path could not be exercised by hand at all.
//
// The certificate is written where the operator can point SSL_CERT_FILE at it,
// and *reused* across restarts. Regenerating it per run was the first version,
// and it was wrong for the only thing this is for: a fixture gets restarted
// constantly, and every restart invalidated the trust a client had already
// been given -- the mint then failed with a certificate error that says
// nothing about what changed.
//
// Reuse means the private key lives in that directory too, at 0600 and under a
// name that says what it is. That is a real trade and it is made knowingly: a
// development key for a loopback listener, in a directory the operator named,
// against a fixture that must not be exposed to a network at all. Nothing is
// installed into a system trust store.

// certificate is a generated identity for the TLS listener.
type certificate struct {
	tls  tls.Certificate
	pem  []byte
	path string
}

// newCertificate loads the certificate in dir, or issues one when there is
// none and when the one there has expired or is about to.
//
// Ed25519 rather than RSA: it is a line shorter and generates instantly.
func newCertificate(dir string) (*certificate, error) {
	if existing, err := loadCertificate(dir); err == nil {
		return existing, nil
	}
	return issueCertificate(dir)
}

// loadCertificate reuses the pair in dir when it is still good for an hour.
func loadCertificate(dir string) (*certificate, error) {
	certPath, keyPath := certificatePaths(dir)
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	// An hour of margin: a certificate that expires mid-session fails in a way
	// nobody connects to the clock.
	if time.Now().Add(time.Hour).After(parsed.NotAfter) {
		return nil, fmt.Errorf("certificate in %s expires at %s", dir, parsed.NotAfter.Format(time.RFC3339))
	}
	return &certificate{tls: pair, pem: certPEM, path: certPath}, nil
}

func certificatePaths(dir string) (certPath, keyPath string) {
	return filepath.Join(dir, "api-gateway-ca.pem"),
		filepath.Join(dir, "api-gateway-key.pem")
}

func issueCertificate(dir string) (*certificate, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "lotsman development fixture"},
		NotBefore:    time.Now().Add(-time.Hour),
		// Long enough that a day of work does not trip over it, short enough
		// that a forgotten development key stops working on its own.
		NotAfter:              time.Now().Add(30 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// Self-signed and its own authority, so one file is enough for a client
		// to verify the chain.
		IsCA:        true,
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, public, private)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load key pair: %w", err)
	}

	certPath, keyPath := certificatePaths(dir)
	// 0o644: the point of this file is that another process reads it, and a
	// certificate is public by construction.
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil { //nolint:gosec // G306: a certificate is public by construction.
		return nil, fmt.Errorf("write %s: %w", certPath, err)
	}
	// 0o600 for the key, and only because reuse is what makes restarting this
	// fixture survivable for a client that already trusts it.
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", keyPath, err)
	}
	return &certificate{tls: pair, pem: certPEM, path: certPath}, nil
}
