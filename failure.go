package hxdna

import "errors"

// FailureKind classifies why a command failed, so the control plane knows what to do next
// without reading the error text. Common to every contract.
type FailureKind string

const (
	// FailureRetryable: try again — a lock held, a task still running, the platform briefly
	// unreachable. Nothing was changed, or repeating is safe.
	FailureRetryable FailureKind = "retryable"
	// FailureTerminal: repeating won't help — a missing image, a name taken, an invalid value.
	// Nothing was changed.
	FailureTerminal FailureKind = "terminal"
	// FailureUnknown: the command may or may not have taken effect (e.g. a write that timed
	// out). A person checks before anything else is tried.
	FailureUnknown FailureKind = "unknown"
)

// FailureError is an error with its FailureKind. A handler returns one (Retryable,
// Terminal, Unknown) and the router puts the kind on HxResult.Failure.
type FailureError struct {
	Kind FailureKind
	Err  error
}

func (e *FailureError) Error() string { return e.Err.Error() }
func (e *FailureError) Unwrap() error { return e.Err }

// Retryable marks err as safe to try again.
func Retryable(err error) error { return classify(FailureRetryable, err) }

// Terminal marks err as one repeating won't fix.
func Terminal(err error) error { return classify(FailureTerminal, err) }

// Unknown marks err as one after which the outcome isn't known.
func Unknown(err error) error { return classify(FailureUnknown, err) }

func classify(kind FailureKind, err error) error {
	if err == nil {
		return nil
	}
	return &FailureError{Kind: kind, Err: err}
}

// FailureKindOf returns err's kind, or "" when the handler didn't classify it (older
// workers, or an error nobody wrapped) — the control plane then falls back to its own rules.
func FailureKindOf(err error) FailureKind {
	var fe *FailureError
	if errors.As(err, &fe) {
		return fe.Kind
	}
	return ""
}
