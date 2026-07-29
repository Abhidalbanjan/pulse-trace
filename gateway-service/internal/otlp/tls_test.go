package otlp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSelfSigned generates an ephemeral self-signed cert + key into dir and
// returns their paths, so the TLS loader can be exercised without checked-in
// key material.
func writeSelfSigned(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pulsetrace-otlp-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certOut, _ := os.Create(certPath)
	_ = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	certOut.Close()

	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyOut, _ := os.Create(keyPath)
	_ = pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyOut.Close()

	return certPath, keyPath
}

func TestBuildServerTLS_DisabledWhenUnset(t *testing.T) {
	cfg, err := BuildServerTLS("", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil config (TLS disabled) when no cert/key set")
	}
}

func TestBuildServerTLS_HalfConfiguredIsAnError(t *testing.T) {
	// A half-configured TLS setup must fail loudly, never silently fall back to
	// plaintext — that would be a security downgrade a deployer wouldn't notice.
	if _, err := BuildServerTLS("cert.pem", "", ""); err == nil {
		t.Error("cert without key should error")
	}
	if _, err := BuildServerTLS("", "key.pem", ""); err == nil {
		t.Error("key without cert should error")
	}
}

func TestBuildServerTLS_LoadsKeypair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir)

	cfg, err := BuildServerTLS(certPath, keyPath, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected a TLS config")
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected 1 certificate, got %d", len(cfg.Certificates))
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS1.2 (%x)", cfg.MinVersion, tls.VersionTLS12)
	}
	// Without a client CA, client certs are not required.
	if cfg.ClientAuth != tls.NoClientCert {
		t.Errorf("ClientAuth = %v, want NoClientCert without a client CA", cfg.ClientAuth)
	}
}

func TestBuildServerTLS_MTLSWhenClientCASet(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir)
	// Reuse the self-signed cert as the client CA — it's a valid CA cert.
	cfg, err := BuildServerTLS(certPath, keyPath, certPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAndVerifyClientCert (mTLS)", cfg.ClientAuth)
	}
	if cfg.ClientCAs == nil {
		t.Error("expected ClientCAs pool to be set for mTLS")
	}
}

func TestBuildServerTLS_BadPathsError(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir)

	if _, err := BuildServerTLS("/nonexistent/cert.pem", "/nonexistent/key.pem", ""); err == nil {
		t.Error("expected an error for missing keypair files")
	}
	// A client-CA file that isn't valid PEM must be rejected, not ignored.
	badCA := filepath.Join(dir, "bad-ca.pem")
	_ = os.WriteFile(badCA, []byte("not a certificate"), 0o600)
	if _, err := BuildServerTLS(certPath, keyPath, badCA); err == nil {
		t.Error("expected an error for a client CA file with no valid certs")
	}
}
