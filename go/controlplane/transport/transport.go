// Package transport provides a factory for an *http.Transport that works in
// enterprise networks with corporate proxies and private CA certificates.
package transport

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
)

// Default returns an *http.Transport that:
//   - Respects HTTP_PROXY, HTTPS_PROXY, and NO_PROXY env vars automatically
//     via [http.ProxyFromEnvironment] (standard Go behaviour).
//   - Loads a custom CA bundle from the PURSER_CA_BUNDLE env var when set,
//     appending the certificates to the system root pool so that internal
//     services with private-CA-signed certificates are trusted.
//
// Returns an error only when PURSER_CA_BUNDLE is set but cannot be read or
// contains no valid PEM certificates.
func Default() (*http.Transport, error) {
	tlsCfg := &tls.Config{}

	if caPath := os.Getenv("PURSER_CA_BUNDLE"); caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("reading CA bundle %s: %w", caPath, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			// SystemCertPool returns an error on some platforms (e.g. Windows
			// without cgo). Fall back to an empty pool so the CA bundle still
			// takes effect.
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no valid PEM certificates found in CA bundle %s", caPath)
		}
		tlsCfg.RootCAs = pool
	}

	return &http.Transport{
		// ProxyFromEnvironment honours HTTP_PROXY, HTTPS_PROXY, and NO_PROXY
		// (or their lower-case equivalents) — the standard Go approach.
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: tlsCfg,
	}, nil
}
