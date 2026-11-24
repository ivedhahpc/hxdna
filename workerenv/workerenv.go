// Package workerenv holds the rules for a worker's environment variables — the key/value pairs an
// app (Provision) keeps for a worker and sends with every script it runs — in one place, for the app
// that accepts them and the worker that enforces them. Both must apply the same rules: an app that
// accepts a name the worker refuses makes every run fail; a preamble that differs by one byte makes
// the worker refuse every run that carries variables.
package workerenv

import (
	"regexp"
	"strings"
)

// MaxNameLen is the longest variable name.
const MaxNameLen = 128

var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// ValidName reports whether name can be a variable: letters, digits and _, not starting with a
// digit, up to MaxNameLen.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// RefusedName reports whether name is refused even though it's valid: names that change how bash,
// or every program a script starts, behaves rather than just what the script reads — through them
// the environment could run code the approved script doesn't contain (BASH_ENV is sourced by every
// bash script the script starts; LD_PRELOAD loads a library into every program). Also bash's
// read-only variables, which export can't set, and the Preamble's own __kv, which it unsets.
func RefusedName(name string) bool {
	switch name {
	case "ENV", "PATH", "IFS", "PS4", "CDPATH", "GLOBIGNORE", "PROMPT_COMMAND", "SHELLOPTS",
		"UID", "EUID", "PPID", "__kv":
		return true
	}
	return strings.HasPrefix(name, "BASH") || strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_")
}

// Preamble goes in front of every script body that receives variables: it reads NAME=value pairs,
// each ended by a NUL byte, from stdin and exports them, then runs the script. Values travel over
// stdin, never in the command line — any account on the machine can read a command line.
const Preamble = `while IFS= read -r -d '' __kv; do export -- "$__kv"; done; unset __kv; `
