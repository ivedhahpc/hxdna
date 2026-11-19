package sealed

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pass = "correct horse battery"

// A light scrypt cost for tests only — the real one (18) takes about a second per try, by design.
func init() { workFactor = 10 }

func TestEncryptDecrypt(t *testing.T) {
	in := map[string]string{"JWT_SECRET": "abc=def", "DB_PASSWORD": "p w"}
	data, err := Encrypt(in, pass)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "abc=def") {
		t.Fatal("a secret is readable in the sealed file")
	}
	out, err := Decrypt(data, pass)
	if err != nil || out["JWT_SECRET"] != "abc=def" || out["DB_PASSWORD"] != "p w" || len(out) != 2 {
		t.Fatalf("round trip: %v, %v", out, err)
	}
	if _, err := Decrypt(data, "wrong passphrase!!"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if _, err := Encrypt(in, "short"); err == nil {
		t.Fatal("accepted a short passphrase")
	}
	if _, err := Encrypt(map[string]string{"A": "two\nlines"}, pass); err == nil {
		t.Fatal("accepted a two-line value")
	}
}

func TestParseEnv(t *testing.T) {
	got, err := ParseEnv([]byte("# c\n\nexport A=1\nB = \"two words\"\nC='x=y'\nD=\n"))
	if err != nil || got["A"] != "1" || got["B"] != "two words" || got["C"] != "x=y" || got["D"] != "" || len(got) != 4 {
		t.Fatalf("got %v, %v", got, err)
	}
	got, _ = ParseEnv([]byte("K=abc # rotated in June\nQ=\"x # y\"\nH=a#b\nT=t1\t# tab\n"))
	if got["K"] != "abc" || got["Q"] != "x # y" || got["H"] != "a#b" || got["T"] != "t1" {
		t.Fatalf("inline comments: %v", got)
	}
	if _, err := ParseEnv([]byte("not a line\n")); err == nil {
		t.Fatal("accepted a malformed line")
	}
}

func TestInitFromEnv(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	sec := filepath.Join(dir, "secrets.age")
	if err := os.WriteFile(env, []byte("# settings\nPORT=8080\nJWT_SECRET=s3cret\nENCRYPTION_KEY=k3y\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	moved, err := InitFromEnv(env, sec, []string{"JWT_SECRET", "ENCRYPTION_KEY", "NOT_THERE"}, pass)
	if err != nil || strings.Join(moved, ",") != "ENCRYPTION_KEY,JWT_SECRET" {
		t.Fatalf("moved %v, %v", moved, err)
	}
	left, _ := os.ReadFile(env)
	if string(left) != "# settings\nPORT=8080\n" {
		t.Fatalf(".env left as %q", left)
	}
	if st, _ := os.Stat(env); st.Mode().Perm() != 0o640 {
		t.Fatalf(".env mode changed to %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(sec); st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file mode %v, want 0600", st.Mode().Perm())
	}
	got, err := DecryptFile(sec, pass)
	if err != nil || got["JWT_SECRET"] != "s3cret" || got["ENCRYPTION_KEY"] != "k3y" {
		t.Fatalf("sealed %v, %v", got, err)
	}
	if _, err := InitFromEnv(env, sec, []string{"PORT"}, pass); err == nil {
		t.Fatal("a second init overwrote the sealed file")
	}
}

func TestEditAndChangePassphrase(t *testing.T) {
	sec := filepath.Join(t.TempDir(), "secrets.age")
	if err := EncryptFile(sec, map[string]string{"A": "1"}, pass); err != nil {
		t.Fatal(err)
	}
	var seen string
	err := EditFile(sec, pass, func(p string) error {
		seen = p
		return os.WriteFile(p, []byte("A=2\nB=3\n"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatal("the plain copy was left behind")
	}
	if got, _ := DecryptFile(sec, pass); got["A"] != "2" || got["B"] != "3" {
		t.Fatalf("after edit: %v", got)
	}
	// A result that doesn't parse changes nothing.
	if err := EditFile(sec, pass, func(p string) error { return os.WriteFile(p, []byte("broken line\n"), 0o600) }); err == nil {
		t.Fatal("accepted a broken edit")
	}
	if got, _ := DecryptFile(sec, pass); got["A"] != "2" {
		t.Fatalf("a broken edit changed the file: %v", got)
	}
	if err := ChangePassphrase(sec, pass, "a new long passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptFile(sec, pass); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatal("the old passphrase still opens it")
	}
	if got, err := DecryptFile(sec, "a new long passphrase"); err != nil || got["B"] != "3" {
		t.Fatalf("new passphrase: %v, %v", got, err)
	}
}

// The whole lock: locked middleware, a wrong try slowed down, the right passphrase over the socket,
// Wait returning the secrets, then everything passes.
func TestLock(t *testing.T) {
	dir, err := os.MkdirTemp("", "sl") // short: a Unix socket path has a length limit
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sec := filepath.Join(dir, "secrets.age")
	if err := EncryptFile(sec, map[string]string{"JWT_SECRET": "s"}, pass); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "run", "u.sock")
	l := NewLock(sec, sock)
	l.FirstDelay, l.MaxDelay = 200*time.Millisecond, time.Second

	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), "/lock-status")
	if rec := httptest.NewRecorder(); func() bool { h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/x", nil)); return rec.Code != 503 }() {
		t.Fatal("not 503 while locked")
	}
	if rec := httptest.NewRecorder(); func() bool { h.ServeHTTP(rec, httptest.NewRequest("GET", "/lock-status", nil)); return rec.Code != 204 }() {
		t.Fatal("an allowed path was blocked")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listened := make(chan error, 1)
	go func() { listened <- l.Listen(ctx) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st, err := os.Stat(sock); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v (mode %v)", err, st.Mode().Perm())
	}

	if err := SendUnlock(sock, "not the passphrase"); err == nil || !strings.Contains(err.Error(), "wrong passphrase") {
		t.Fatalf("wrong passphrase answered %v", err)
	}
	if err := SendUnlock(sock, pass); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("a try inside the delay answered %v, want refused", err)
	}
	time.Sleep(250 * time.Millisecond)
	if err := SendUnlock(sock, pass); err != nil {
		t.Fatalf("right passphrase: %v", err)
	}
	got, err := l.Wait(ctx)
	if err != nil || got["JWT_SECRET"] != "s" {
		t.Fatalf("Wait: %v, %v", got, err)
	}
	if !l.Unlocked() {
		t.Fatal("not unlocked")
	}
	if err := <-listened; err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("the socket was left behind after unlocking")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/x", nil))
	if rec.Code != 204 {
		t.Fatalf("after unlock got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	l.StatusHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/lock-status", nil))
	if strings.TrimSpace(rec.Body.String()) != `{"locked":false}` {
		t.Fatalf("status %s", rec.Body.String())
	}
}

func TestApply(t *testing.T) {
	if err := Apply(map[string]string{"SEALED_TEST_X": "1"}); err != nil || os.Getenv("SEALED_TEST_X") != "1" {
		t.Fatal("Apply didn't set the variable")
	}
	os.Unsetenv("SEALED_TEST_X")
}

// The socket's folder must be private, and a file in the socket's place is never removed.
func TestListenRefusesUnsafePlaces(t *testing.T) {
	dir, err := os.MkdirTemp("", "sl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	open := filepath.Join(dir, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(open, 0o755)
	if err := NewLock("x", filepath.Join(open, "u.sock")).Listen(context.Background()); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("a shared folder answered %v", err)
	}
	priv := filepath.Join(dir, "priv")
	if err := os.Mkdir(priv, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(priv, "u.sock")
	os.WriteFile(file, []byte("keep me"), 0o600)
	if err := NewLock("x", file).Listen(context.Background()); err == nil || !strings.Contains(err.Error(), "isn't a socket") {
		t.Fatalf("a file in the way answered %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "keep me" {
		t.Fatal("the file in the way was removed")
	}
}
