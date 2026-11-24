package connection

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseFingerprint(t *testing.T) {
	want := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for _, in := range []string{
		strings.Repeat("ab", 32),
		want,
		strings.TrimSuffix(strings.Repeat("ab ", 32), " "), // a browser's form
		"sha256 Fingerprint=" + want,
		"Fingerprint (SHA-256):\n" + want + "\nFingerprint (SHA-1): " + strings.TrimSuffix(strings.Repeat("CD:", 20), ":"),
	} {
		if got, err := NormalizeFingerprint(in); err != nil || got != want {
			t.Errorf("%q → %q, %v", in, got, err)
		}
	}
	sha384 := strings.TrimSuffix(strings.Repeat("EF:", 48), ":")
	for _, in := range []string{
		"", "AB:CD", strings.Repeat("zz", 32),
		strings.TrimSuffix(strings.Repeat("CD:", 20), ":"), // SHA-1
		sha384, // SHA-384: never its first 32 pairs
		"sha384 Fingerprint=" + sha384,
		want + " " + strings.Replace(want, "AB", "CD", -1), // two different SHA-256s
	} {
		if _, err := ParseFingerprint(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestPort(t *testing.T) {
	if p, err := Port("", 8006); err != nil || p != 8006 {
		t.Errorf("default: %d %v", p, err)
	}
	for _, bad := range []string{"0", "65536", "x"} {
		if _, err := Port(bad, 1); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDecodeHidesContent(t *testing.T) {
	var v map[string]string
	if err := Decode(`{"token_secret":"s3cret"`, &v); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("err %v", err)
	}
}

func TestPinnedTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	get := func(pin []byte) error {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedTLS(pin)}}
		resp, err := c.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(sum[:]); err != nil {
		t.Fatalf("right pin: %v", err)
	}
	if err := get(make([]byte, 32)); err == nil {
		t.Fatal("wrong pin accepted")
	}
	// A missing or short pin refuses everything — never "no pin, accept all".
	for _, pin := range [][]byte{nil, sum[:16]} {
		if err := get(pin); err == nil {
			t.Fatalf("pin of %d bytes accepted", len(pin))
		}
	}
}
