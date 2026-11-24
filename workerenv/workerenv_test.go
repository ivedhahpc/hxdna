package workerenv

import (
	"strings"
	"testing"
)

func TestNames(t *testing.T) {
	for _, n := range []string{"TOKEN", "_x", "ELK_TOKEN_2", strings.Repeat("A", MaxNameLen)} {
		if !ValidName(n) || RefusedName(n) {
			t.Errorf("%q should be allowed", n)
		}
	}
	for _, n := range []string{"", "1A", "A-B", "A B", strings.Repeat("A", MaxNameLen+1)} {
		if ValidName(n) {
			t.Errorf("%q is valid", n)
		}
	}
	for _, n := range []string{"PATH", "IFS", "BASH_ENV", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "__kv", "PROMPT_COMMAND"} {
		if !RefusedName(n) {
			t.Errorf("%q not refused", n)
		}
	}
}

// The preamble is part of every signed script: changing it changes what machines run. This pins it.
func TestPreambleUnchanged(t *testing.T) {
	if Preamble != `while IFS= read -r -d '' __kv; do export -- "$__kv"; done; unset __kv; ` {
		t.Fatal("Preamble changed — apps and workers must change together")
	}
}
