package hxdna

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestFailureKindOf(t *testing.T) {
	base := errors.New("template 9000 not found")
	if got := FailureKindOf(Terminal(base)); got != FailureTerminal {
		t.Errorf("Terminal: got %q", got)
	}
	if got := FailureKindOf(fmt.Errorf("clone: %w", Retryable(base))); got != FailureRetryable {
		t.Errorf("wrapped Retryable: got %q", got)
	}
	if got := FailureKindOf(base); got != "" {
		t.Errorf("unclassified: got %q", got)
	}
	if Terminal(nil) != nil {
		t.Error("Terminal(nil) should be nil")
	}
	if !errors.Is(Unknown(base), base) {
		t.Error("FailureError should unwrap to the cause")
	}
}

func TestRouterSetsFailure(t *testing.T) {
	r := NewRouter(Manifest{WorkerType: "test"})
	r.Register("boom", func(HxCommand) (any, error) { return nil, Terminal(errors.New("nope")) })
	r.Register("plain", func(HxCommand) (any, error) { return nil, errors.New("plain") })

	if res := r.dispatch(HxCommand{CommandKey: "boom"}); res.Failure != FailureTerminal || res.Error != "nope" {
		t.Errorf("classified: %+v", res)
	}
	if res := r.dispatch(HxCommand{CommandKey: "plain"}); res.Failure != "" {
		t.Errorf("unclassified should leave Failure empty: %+v", res)
	}
	if res := r.dispatch(HxCommand{CommandKey: "missing"}); res.Failure != FailureTerminal {
		t.Errorf("unknown command should be terminal: %+v", res)
	}
}

func TestNewManifestSectionsOmittedWhenUnset(t *testing.T) {
	raw, _ := json.Marshal(Manifest{WorkerType: "test"})
	for _, k := range []string{"inventory_contract", "extensions", "failure"} {
		if strings.Contains(string(raw), k) {
			t.Errorf("%s serialized when unset: %s", k, raw)
		}
	}
}

func TestToJSONFallbackIsTerminal(t *testing.T) {
	res := HxResult{Success: true, Data: json.RawMessage(`{not json`)}
	var back HxResult
	if err := json.Unmarshal(res.ToJSON(), &back); err != nil {
		t.Fatal(err)
	}
	if back.Success || back.Failure != FailureTerminal {
		t.Errorf("fallback: %+v", back)
	}
}
