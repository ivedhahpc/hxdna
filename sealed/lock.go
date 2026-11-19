package sealed

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultSocket is where the unlock listener waits, inside the app's container.
const DefaultSocket = "/run/sealed/unlock.sock"

// Lock is an app's locked/unlocked state. The app starts its HTTP server behind Middleware, runs
// Listen, and builds everything that needs a secret only after Wait returns the secrets.
type Lock struct {
	secretsPath string
	socketPath  string
	// FirstDelay is the wait after the first wrong passphrase; each further one doubles it, up to
	// MaxDelay. A right passphrase resets it. Tests shorten them.
	FirstDelay time.Duration
	MaxDelay   time.Duration
	// OnUnlock, when set, is told once the secrets are open (e.g. to log it — never the secrets).
	OnUnlock func()

	mu       sync.Mutex
	secrets  map[string]string
	done     chan struct{}
	failures int
	nextTry  time.Time
}

// NewLock is the lock for the sealed file at secretsPath, unlocked over socketPath (DefaultSocket
// when empty).
func NewLock(secretsPath, socketPath string) *Lock {
	if socketPath == "" {
		socketPath = DefaultSocket
	}
	return &Lock{
		secretsPath: secretsPath, socketPath: socketPath,
		FirstDelay: 2 * time.Second, MaxDelay: 5 * time.Minute,
		done: make(chan struct{}),
	}
}

// Unlocked reports whether the secrets are open.
func (l *Lock) Unlocked() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// Wait blocks until the lock is unlocked (or ctx ends) and returns the secrets. Called once, by the
// part of the app that builds everything secret.
func (l *Lock) Wait(ctx context.Context) (map[string]string, error) {
	select {
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		out := make(map[string]string, len(l.secrets))
		for k, v := range l.secrets {
			out[k] = v
		}
		return out, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Apply puts secrets into the process environment, so an app's existing config loader (reading
// os.Getenv) picks them up after unlock. Process memory only: Setenv never reaches
// /proc/<pid>/environ, Docker's container config or any file.
func Apply(secrets map[string]string) error {
	for k, v := range secrets {
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	return nil
}

// try opens the secrets with passphrase. Wrong tries are slowed down: each makes the next wait
// longer, and a try before then is refused without even attempting it.
func (l *Lock) try(passphrase string) error {
	if l.Unlocked() {
		return nil
	}
	l.mu.Lock()
	if wait := time.Until(l.nextTry); wait > 0 {
		l.mu.Unlock()
		return fmt.Errorf("too many wrong tries — wait %s and try again", wait.Round(time.Second))
	}
	l.mu.Unlock()

	secrets, err := DecryptFile(l.secretsPath, passphrase)

	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		if errors.Is(err, ErrWrongPassphrase) {
			delay := l.FirstDelay << l.failures
			if delay > l.MaxDelay || delay <= 0 {
				delay = l.MaxDelay
			}
			l.failures++
			l.nextTry = time.Now().Add(delay)
		}
		return err
	}
	if l.Unlocked() {
		return nil
	}
	l.secrets, l.failures = secrets, 0
	close(l.done)
	if l.OnUnlock != nil {
		go l.OnUnlock()
	}
	return nil
}

// Listen serves the unlock socket until the lock is unlocked or ctx ends. The socket is created
// mode 0600 in a 0700 directory — only the container's own user can reach it (`docker exec`), never
// the network. One line in (the passphrase), one line out: "ok" or "error: <why>".
func (l *Lock) Listen(ctx context.Context) error {
	// The socket must sit in a folder only this user can enter: then nobody else can reach it, even
	// in the moment before its own mode is set.
	dir := dirOf(l.socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(dir); err != nil {
		return err
	} else if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("the unlock socket's folder %s must be private (mode 0700, not %v) — give the socket a folder of its own", dir, st.Mode().Perm())
	}
	// A socket left by a previous run is removed — only a socket, never a file that's in the way.
	if st, err := os.Lstat(l.socketPath); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("%s exists and isn't a socket — refusing to remove it", l.socketPath)
		}
		_ = os.Remove(l.socketPath)
	}
	ln, err := net.Listen("unix", l.socketPath)
	if err != nil {
		return err
	}
	defer os.Remove(l.socketPath)
	if err := os.Chmod(l.socketPath, 0o600); err != nil {
		ln.Close()
		return err
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-l.done:
		}
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if l.Unlocked() || ctx.Err() != nil {
				return nil
			}
			return err
		}
		l.serve(conn)
	}
}

// serve answers one unlock attempt. One at a time (Listen doesn't spawn), so tries can't race the
// delay.
func (l *Lock) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	// At most 4 KB: a passphrase, not whatever a client cares to send.
	line, err := bufio.NewReader(io.LimitReader(conn, 4096)).ReadString('\n')
	if err != nil && line == "" {
		return
	}
	passphrase := strings.TrimRight(line, "\r\n")
	if err := l.try(passphrase); err != nil {
		fmt.Fprintf(conn, "error: %s\n", oneLine(err))
		return
	}
	fmt.Fprintln(conn, "ok")
}

// Middleware answers every request with 503 {"code":"Locked"} until unlocked, except the paths in
// allow (exact match, e.g. "/lock-status", "/health") — the frontend reads one to show its locked page.
// Put it inside the app's CORS handling (CORS outermost): a browser on another origin can only read
// the 503 — and show "locked" rather than a network error — when it carries the CORS headers.
func (l *Lock) Middleware(next http.Handler, allow ...string) http.Handler {
	ok := map[string]bool{}
	for _, p := range allow {
		ok[p] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.Unlocked() || ok[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code": "Locked", "message": "This server is locked — an administrator needs to unlock it.",
		})
	})
}

// StatusHandler answers {"locked": true|false} — what a frontend polls to show or leave its locked
// page. Safe while locked: it says nothing else.
func (l *Lock) StatusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"locked": !l.Unlocked()})
	})
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}

func oneLine(err error) string {
	return strings.ReplaceAll(err.Error(), "\n", " ")
}
