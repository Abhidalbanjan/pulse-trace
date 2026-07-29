package otlp

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// BuildServerTLS assembles the TLS config for the OTLP/gRPC receiver from its
// three file paths, or returns (nil, nil) when TLS is not configured.
//
// Why this matters for ingestion auth: the receiver authenticates each export
// with a per-tenant ingestion key carried in gRPC metadata. Over a plaintext
// connection that key crosses the wire in the clear and can be sniffed and
// replayed. Serving the receiver over TLS protects the credential in transit;
// supplying a client CA additionally turns on mTLS, so a client must present a
// certificate signed by that CA — a second, transport-level authentication
// factor on top of the ingestion key.
//
//   - certFile/keyFile empty  → TLS disabled (nil, nil); the caller keeps the
//     plaintext listener (fine for local/dev, or when a TLS-terminating LB or
//     ingress sits in front of :4317).
//   - one of them set, not both → error (a half-configured TLS setup must fail
//     loudly, not silently fall back to plaintext).
//   - clientCAFile set → require and verify a client certificate (mTLS).
func BuildServerTLS(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil // TLS not configured
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("OTLP TLS needs both OTLP_TLS_CERT_FILE and OTLP_TLS_KEY_FILE (got cert=%q key=%q)", certFile, keyFile)
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load OTLP TLS keypair: %w", err)
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	if clientCAFile != "" {
		caPEM, err := os.ReadFile(clientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read OTLP client CA %q: %w", clientCAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("OTLP client CA %q contained no valid certificates", clientCAFile)
		}
		cfg.ClientCAs = pool
		// mTLS: reject any client that doesn't present a cert signed by the CA.
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return cfg, nil
}
