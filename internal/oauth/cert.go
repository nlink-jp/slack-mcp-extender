package oauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

// generateLoopbackCert creates an ephemeral self-signed TLS certificate for
// the local OAuth callback server. Slack rejects http:// redirect URIs at
// app-registration time, so the loopback listener must speak TLS; the cert
// lives only in memory and is regenerated on every login. The browser shows
// a one-time "not secure" warning — clicking through is expected; a
// loopback listener has no realistic MITM threat.
//
// SANs cover 127.0.0.1/::1 and "localhost" (the redirect URI uses the DNS
// name, which provider allow-lists accept more readily than IP literals).
func generateLoopbackCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "slack-mcp-extender local OAuth callback"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		DNSNames:     []string{"localhost"},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create cert: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  key,
		Leaf:        &template,
	}, nil
}
