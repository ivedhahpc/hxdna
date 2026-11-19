// Package sealed keeps an app's secrets encrypted on disk and starts the app locked until a person
// unlocks it. The secrets live in an age file (scrypt passphrase), the passphrase never touches the
// server's disk, and the app decrypts into memory only after `-cmd unlock` hands it the passphrase
// over a Unix socket inside the container.
//
// Its own module inside the hxdna repo (like hxdna/playbook), so workers — which import hxdna —
// never pull it in. Design: docs/architecture/sealed-secrets.md in the hive docs repo.
package sealed

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"filippo.io/age"
)

// ErrWrongPassphrase means the passphrase doesn't open the file.
var ErrWrongPassphrase = errors.New("wrong passphrase")

// MinPassphrase is the shortest passphrase Encrypt accepts.
const MinPassphrase = 12

// workFactor is scrypt's cost (log2 N): age's own default, about a second to try one passphrase.
// Tests lower it; nothing else should.
var workFactor = 18

// keyRe: a variable name as a shell and .env file know it.
var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// inlineComment: whitespace then # — where a comment starts on an unquoted .env value.
var inlineComment = regexp.MustCompile(`\s#`)

// Exists reports whether a sealed secrets file is at path — the app is then sealed and starts locked.
// Without one it keeps reading plain .env (local development).
func Exists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// Encrypt seals secrets with passphrase: an age file (scrypt) whose content is a .env-style body,
// KEY=value per line, sorted. Standard age format — `age -d` opens it anywhere, for disaster recovery.
func Encrypt(secrets map[string]string, passphrase string) ([]byte, error) {
	if len(passphrase) < MinPassphrase {
		return nil, fmt.Errorf("the passphrase must be at least %d characters", MinPassphrase)
	}
	body, err := FormatEnv(secrets)
	if err != nil {
		return nil, err
	}
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	r.SetWorkFactor(workFactor)
	var out bytes.Buffer
	w, err := age.Encrypt(&out, r)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(body); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Decrypt opens a sealed file's bytes with passphrase. ErrWrongPassphrase when it doesn't open it.
func Decrypt(data []byte, passphrase string) (map[string]string, error) {
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(bytes.NewReader(data), id)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, ErrWrongPassphrase
		}
		return nil, fmt.Errorf("can't open the secrets file: %w", err)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("can't read the secrets file: %w", err)
	}
	return ParseEnv(body)
}

// DecryptFile is Decrypt for the file at path.
func DecryptFile(path, passphrase string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decrypt(data, passphrase)
}

// EncryptFile seals secrets into path: written to a temporary file beside it and renamed over it, so a
// failure part-way never leaves a half-written file. Mode 0600.
func EncryptFile(path string, secrets map[string]string, passphrase string) error {
	data, err := Encrypt(secrets, passphrase)
	if err != nil {
		return err
	}
	return writeAtomic(path, data, 0o600)
}

// ParseEnv reads a .env-style body: KEY=value per line; blank lines and # comments skipped; an
// optional leading "export "; a value in matching single or double quotes is unquoted (no escapes
// inside — a value is taken as written). On an unquoted value, " #" starts a comment, as the apps'
// own .env loader reads it — so a sealed secret never carries a note written after it. A malformed
// line is an error, never skipped silently.
func ParseEnv(body []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !keyRe.MatchString(k) {
			return nil, fmt.Errorf("line %d isn't KEY=value", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		} else if loc := inlineComment.FindStringIndex(v); loc != nil {
			v = strings.TrimSpace(v[:loc[0]])
		}
		out[k] = v
	}
	return out, sc.Err()
}

// FormatEnv writes secrets as a .env-style body, one KEY=value per line, sorted. A value with a
// newline can't be written as one line and is refused.
func FormatEnv(secrets map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(secrets))
	for k, v := range secrets {
		if !keyRe.MatchString(k) {
			return nil, fmt.Errorf("%q isn't a valid variable name", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("%s: a value must be one line", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, secrets[k])
	}
	return b.Bytes(), nil
}

// InitFromEnv moves the named keys out of the plain .env at envPath into a new sealed file at
// secretsPath: the sealed file is written first, then .env is rewritten without those lines (every
// other line — comments, settings — kept as it was). Refuses if secretsPath already exists, so a
// second run can't overwrite secrets. Returns the keys moved; a named key not in .env is skipped.
// Nothing keeps a plain copy: no backup of the old .env is made.
func InitFromEnv(envPath, secretsPath string, keys []string, passphrase string) ([]string, error) {
	if Exists(secretsPath) {
		return nil, fmt.Errorf("%s already exists — edit it instead", secretsPath)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return nil, err
	}
	env, err := ParseEnv(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", envPath, err)
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	secrets := map[string]string{}
	var moved []string
	for _, k := range keys {
		if v, ok := env[k]; ok {
			secrets[k] = v
			moved = append(moved, k)
		}
	}
	if len(moved) == 0 {
		return nil, fmt.Errorf("none of the secrets are in %s", envPath)
	}
	if err := EncryptFile(secretsPath, secrets, passphrase); err != nil {
		return nil, err
	}
	// Check the file opens before taking anything out of .env.
	if _, err := DecryptFile(secretsPath, passphrase); err != nil {
		_ = os.Remove(secretsPath)
		return nil, fmt.Errorf("the sealed file didn't open again, so .env was left as it was: %w", err)
	}
	var kept bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimPrefix(strings.TrimSpace(line), "export ")
		if k, _, ok := strings.Cut(t, "="); ok && want[strings.TrimSpace(k)] {
			continue
		}
		kept.WriteString(line + "\n")
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	st, err := os.Stat(envPath)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(envPath, kept.Bytes(), st.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("sealed %s, but couldn't rewrite %s — remove the secret lines from it by hand: %w", secretsPath, envPath, err)
	}
	sort.Strings(moved)
	return moved, nil
}

// ChangePassphrase re-seals the file at path with a new passphrase.
func ChangePassphrase(path, oldPassphrase, newPassphrase string) error {
	secrets, err := DecryptFile(path, oldPassphrase)
	if err != nil {
		return err
	}
	return EncryptFile(path, secrets, newPassphrase)
}

// EditFile opens the sealed file at path, writes it as plain text to a temporary file in RAM where
// the system has one (/dev/shm; else the temp dir), lets edit change it, then seals the result back
// and wipes the plain copy. A result that doesn't parse is refused and the sealed file is left as it
// was.
func EditFile(path, passphrase string, edit func(plainPath string) error) error {
	secrets, err := DecryptFile(path, passphrase)
	if err != nil {
		return err
	}
	body, err := FormatEnv(secrets)
	if err != nil {
		return err
	}
	dir := os.TempDir()
	if st, err := os.Stat("/dev/shm"); err == nil && st.IsDir() {
		dir = "/dev/shm"
	}
	f, err := os.CreateTemp(dir, "secrets-*.env")
	if err != nil {
		return err
	}
	plain := f.Name()
	defer wipe(plain)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := edit(plain); err != nil {
		return fmt.Errorf("editor: %w — nothing changed", err)
	}
	edited, err := os.ReadFile(plain)
	if err != nil {
		return err
	}
	next, err := ParseEnv(edited)
	if err != nil {
		return fmt.Errorf("%w — nothing changed", err)
	}
	return EncryptFile(path, next, passphrase)
}

// wipe overwrites a file with zeros before removing it.
func wipe(path string) {
	if st, err := os.Stat(path); err == nil {
		_ = os.WriteFile(path, make([]byte, st.Size()), 0o600)
	}
	_ = os.Remove(path)
}

// writeAtomic writes data to a temporary file beside path and renames it over path.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
