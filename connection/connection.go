// Package connection holds what an app and a platform worker share about the connection the app
// sends with every command (Provision: Platform → Connection): the param's name, how it's read, and
// TLS certificate pinning by SHA-256 fingerprint. One copy, so the app that validates a fingerprint
// and the worker that checks it can't disagree.
package connection

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Param is the command param that carries the connection: a JSON object of strings.
const Param = "connection"

// Decode reads the connection param into v. Its error never quotes the param — it may hold a secret.
func Decode(raw string, v any) error {
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return errors.New("the connection sent with the command isn't valid JSON")
	}
	return nil
}

// Port reads a port; "" gives def.
func Port(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, errors.New("the connection's port isn't a port number")
	}
	return n, nil
}

// ErrFingerprint is a value that isn't a SHA-256 fingerprint.
var ErrFingerprint = errors.New("must be a SHA-256 fingerprint: 32 pairs of hex digits, e.g. AB:CD:…")

// hexRunRe finds a run of hex pairs separated by colons or spaces — a fingerprint as Proxmox, openssl
// or a browser shows it.
var hexRunRe = regexp.MustCompile(`(?i)[0-9a-f]{2}(?:[: ][0-9a-f]{2})+`)

var unseparate = strings.NewReplacer(":", "", " ", "", "\t", "")

// ParseFingerprint reads a SHA-256 certificate fingerprint: 64 hex digits, separated by colons or
// spaces or not at all, any case — or text containing one (e.g. "sha256 Fingerprint=AB:CD:…"). Only
// a run of exactly 32 pairs counts: a SHA-1 (20) or SHA-384 (48) fingerprint is refused, never cut
// down to something that looks right.
func ParseFingerprint(fp string) ([]byte, error) {
	fp = strings.TrimSpace(fp)
	if b, err := hex.DecodeString(unseparate.Replace(fp)); err == nil {
		if len(b) != sha256.Size {
			return nil, ErrFingerprint
		}
		return b, nil
	}
	var found []byte
	for _, run := range hexRunRe.FindAllString(fp, -1) {
		b, err := hex.DecodeString(unseparate.Replace(run))
		if err != nil || len(b) != sha256.Size {
			continue
		}
		if found != nil {
			return nil, ErrFingerprint // two SHA-256 fingerprints: which one is meant?
		}
		found = b
	}
	if found == nil {
		return nil, ErrFingerprint
	}
	return found, nil
}

// FormatFingerprint writes a fingerprint as AB:CD:… (Proxmox's and govmomi's form).
func FormatFingerprint(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = strings.ToUpper(hex.EncodeToString([]byte{x}))
	}
	return strings.Join(parts, ":")
}

// NormalizeFingerprint is ParseFingerprint then FormatFingerprint — how an app stores one.
func NormalizeFingerprint(fp string) (string, error) {
	b, err := ParseFingerprint(fp)
	if err != nil {
		return "", err
	}
	return FormatFingerprint(b), nil
}

// PinnedTLS is a TLS config that accepts exactly the certificate whose SHA-256 fingerprint is pin,
// self-signed or not. Verification is replaced, not skipped, and VerifyConnection runs on every
// handshake, resumed ones too.
func PinnedTLS(pin []byte) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // checked by VerifyConnection below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the server presented no certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], pin) != 1 {
				return errors.New("the server's certificate doesn't match the pinned TLS fingerprint")
			}
			return nil
		},
	}
}
