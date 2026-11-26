// Package tlspin builds TLS configurations that trust a server either through
// the system certificate store or through a pinned public key, which is how
// a self-hosted server with a self-signed certificate is trusted safely.
package tlspin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Fingerprint returns the SHA-256 of a certificate's public key (SPKI) as
// lower-case hex. Pinning the key survives certificate renewal with the same
// key.
func Fingerprint(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(h[:])
}

// Normalize accepts a fingerprint with colons, spaces or upper case.
func Normalize(pin string) string {
	pin = strings.ToLower(pin)
	return strings.NewReplacer(":", "", " ", "", "-", "").Replace(pin)
}

// Config returns a client TLS config. With an empty pin the system roots are
// used; with a pin, the leaf key must match it exactly.
func Config(pin string) *tls.Config {
	pin = Normalize(pin)
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if pin == "" {
		return cfg
	}
	// Chain validation is replaced by an exact key match, which is stricter
	// than trusting any CA for this one server.
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("tlspin: no server certificate")
		}
		leaf := cs.PeerCertificates[0]
		if Fingerprint(leaf) != pin {
			return errors.New("tlspin: server key does not match the pinned fingerprint")
		}
		now := time.Now()
		if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
			return errors.New("tlspin: server certificate is not valid now")
		}
		return nil
	}
	return cfg
}

// HTTPClient returns an HTTP client using Config(pin).
func HTTPClient(pin string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = Config(pin)
	return &http.Client{Transport: tr, Timeout: 60 * time.Second}
}

// GenerateSelfSigned writes a P-256 certificate and key valid for hosts.
func GenerateSelfSigned(certPath, keyPath string, hosts []string, validFor time.Duration) (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hosts[0], Organization: []string{"Remote Support Manager (self-hosted)"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return "", err
	}
	cert, _ := x509.ParseCertificate(der)
	return Fingerprint(cert), nil
}

// FileFingerprint reads a PEM certificate and returns its pin.
func FileFingerprint(certPath string) (string, error) {
	b, err := os.ReadFile(certPath)
	if err != nil {
		return "", err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return "", errors.New("tlspin: no PEM certificate")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return "", err
	}
	return Fingerprint(c), nil
}
