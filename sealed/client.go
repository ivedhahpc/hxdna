package sealed

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// SendUnlock hands passphrase to the running app's unlock socket and returns its answer: nil once
// the app is unlocked, else why not (a wrong passphrase, too many tries…).
func SendUnlock(socketPath, passphrase string) error {
	if socketPath == "" {
		socketPath = DefaultSocket
	}
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("can't reach the app's unlock socket (%s) — is it running, and is it sealed and still locked? %w", socketPath, err)
	}
	defer conn.Close()
	// Decrypting takes a moment (scrypt); allow it.
	_ = conn.SetDeadline(time.Now().Add(time.Minute))
	if _, err := fmt.Fprintf(conn, "%s\n", passphrase); err != nil {
		return err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && reply == "" {
		return fmt.Errorf("no answer from the app: %w", err)
	}
	reply = strings.TrimSpace(reply)
	if reply == "ok" {
		return nil
	}
	return errors.New(strings.TrimPrefix(reply, "error: "))
}

// ReadPassphrase asks for a passphrase on the terminal without echoing it. confirm asks twice and
// refuses a mismatch (for setting a new one). Reads /dev/tty, so it works under `docker exec -it`
// even when stdin is redirected; refuses without a terminal rather than read a passphrase from a pipe.
func ReadPassphrase(prompt string, confirm bool) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", errors.New("no terminal to ask on — run it with -it (docker exec -it …)")
	}
	defer tty.Close()
	read := func(p string) (string, error) {
		fmt.Fprint(tty, p)
		b, err := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		return string(b), err
	}
	first, err := read(prompt)
	if err != nil {
		return "", err
	}
	if first == "" {
		return "", errors.New("no passphrase entered")
	}
	if confirm {
		second, err := read("Again: ")
		if err != nil {
			return "", err
		}
		if second != first {
			return "", errors.New("the two passphrases don't match")
		}
	}
	return first, nil
}
