package hxdna

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestTLSConfig(t *testing.T) {
	if cfg, err := tlsConfig(""); cfg != nil || err != nil {
		t.Errorf("no CA: want nil config and no error, got %v, %v", cfg, err)
	}
	if _, err := tlsConfig("-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n"); err == nil {
		t.Error("a key instead of a certificate: want an error")
	}
	cfg, err := tlsConfig(testCAPEM(t))
	if err != nil || cfg == nil || cfg.RootCAs == nil {
		t.Fatalf("valid CA: want a config with roots, got %v, %v", cfg, err)
	}
}

// A CA must never switch on TLS for a nats:// URL: that would cut a worker off from a broker
// that has no TLS yet.
func TestNATSOptionsTLSOnlyForTLSURL(t *testing.T) {
	ca := testCAPEM(t)
	cases := []struct {
		url, ca string
		want    int
	}{
		{"nats://10.0.0.1:4222", ca, 0},
		{"tls://10.0.0.1:4222", ca, 1},
		{"tls://10.0.0.1:4222", "", 0},
	}
	for _, c := range cases {
		s := &State{NatsURL: c.url, CA: c.ca}
		if got := len(s.NATSOptions()); got != c.want {
			t.Errorf("%s with CA=%v: want %d options, got %d", c.url, c.ca != "", c.want, got)
		}
	}
}

func TestStateKeepsCA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ca := testCAPEM(t)
	s := &State{WorkerID: "w", OrgID: "o", NatsURL: "tls://10.0.0.1:4222", ControlURL: "https://10.0.0.1:8443", SubjectPrefix: "hx", CA: ca}
	if err := SaveState(".hxdna-test", s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(".hxdna-test")
	if err != nil {
		t.Fatal(err)
	}
	if got.CA != ca {
		t.Error("CA lost across save and load")
	}

	// Re-enrolled with a control plane without a CA: the old ca.pem goes with it.
	s.CA = ""
	if err := SaveState(".hxdna-test", s); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".hxdna-test", "ca.pem")); !os.IsNotExist(err) {
		t.Errorf("old ca.pem still there after saving without a CA (stat err %v)", err)
	}
	if got, err := LoadState(".hxdna-test"); err != nil || got.CA != "" {
		t.Errorf("want no CA after saving without one, got %q, %v", got.CA, err)
	}
}
