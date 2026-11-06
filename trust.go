package hxdna

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
)

// tlsConfig trusts the system's certificate authorities plus caPEM — the private CA a control
// plane reached by IP signs its HTTPS and NATS certificates with. Nil for an empty caPEM: the
// system's authorities alone, as for a public certificate.
func tlsConfig(caPEM string) (*tls.Config, error) {
	if caPEM == "" {
		return nil, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, fmt.Errorf("the control plane's CA holds no usable certificate")
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}
