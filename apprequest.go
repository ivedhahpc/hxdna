package hxdna

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// App Requests — the contract between the console (HelixOps) and an app (Provision, Network) for work
// the console hands over. Design: docs/architecture/app-handoff.md in the docs repo.
//
// The console owns tickets; an app never sees one. A request carries a request ID, a capability, its
// inputs and plain facts. The app answers at once (accepted or rejected), may send updates, and ends
// with exactly one result of four fixed kinds. The four kinds never grow: a new situation is a new
// capability (app side) or a new outcome → playbook mapping (console side).

// App capability kinds.
const (
	// AppCapabilityRead looks only — it changes nothing.
	AppCapabilityRead = "read"
	// AppCapabilityWrite changes something; the console gets approval before it sends one.
	AppCapabilityWrite = "write"
)

// AppResultKind is how a request ended — one of four, fixed.
type AppResultKind string

const (
	// AppResultResolved: the app did it; Evidence says what it found or changed.
	AppResultResolved AppResultKind = "resolved"
	// AppResultDiagnosed: the app found the cause and changed nothing; Outcome is what it recommends.
	// The console sets it as the log's outcome and its resolver decides what runs next.
	AppResultDiagnosed AppResultKind = "diagnosed"
	// AppResultNeedsHuman: the app tried, or can't decide; Note says why. A person takes it.
	AppResultNeedsHuman AppResultKind = "needs_human"
	// AppResultRejected: not this app's, bad inputs, or not installed for the org. A person takes it.
	AppResultRejected AppResultKind = "rejected"
)

// AppResultKinds is every kind, in order.
var AppResultKinds = []AppResultKind{AppResultResolved, AppResultDiagnosed, AppResultNeedsHuman, AppResultRejected}

func (k AppResultKind) valid() bool {
	for _, v := range AppResultKinds {
		if k == v {
			return true
		}
	}
	return false
}

var (
	appKeyRe     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	appOutcomeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// AppCapability is one thing an app can be asked to do, as it lists it in its manifest. The console
// offers it in the playbook editor and maps its outcomes to playbooks.
type AppCapability struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	// Kind is AppCapabilityRead or AppCapabilityWrite.
	Kind string `json:"kind"`
	// Inputs declares the inputs it takes (same idiom as a worker command's InputSchema).
	Inputs InputSchema `json:"inputs,omitempty"`
	// ResultKinds are the kinds it may answer with. Rejected is always allowed, listed or not.
	ResultKinds []AppResultKind `json:"result_kinds"`
	// Outcomes are what a diagnosed result may recommend — required when it can answer diagnosed.
	Outcomes []string `json:"outcomes,omitempty"`
	// MaxHours is how long it may take before the console counts the request overdue.
	MaxHours int `json:"max_hours"`
}

// Validate checks a capability as an app declares it.
func (c AppCapability) Validate() error {
	if !appKeyRe.MatchString(c.Key) {
		return fmt.Errorf("capability key %q: lowercase letters, digits and _, starting with a letter", c.Key)
	}
	if c.Kind != AppCapabilityRead && c.Kind != AppCapabilityWrite {
		return fmt.Errorf("capability %s: kind must be read or write", c.Key)
	}
	if len(c.ResultKinds) == 0 {
		return fmt.Errorf("capability %s: declare at least one result kind", c.Key)
	}
	seen := map[AppResultKind]bool{}
	for _, k := range c.ResultKinds {
		if !k.valid() {
			return fmt.Errorf("capability %s: unknown result kind %q", c.Key, k)
		}
		if seen[k] {
			return fmt.Errorf("capability %s: result kind %q listed twice", c.Key, k)
		}
		seen[k] = true
	}
	// A read capability changes nothing, so it can't report that it resolved anything.
	if c.Kind == AppCapabilityRead && seen[AppResultResolved] {
		return fmt.Errorf("capability %s: a read capability changes nothing — it can't answer resolved (use diagnosed)", c.Key)
	}
	if seen[AppResultDiagnosed] && len(c.Outcomes) == 0 {
		return fmt.Errorf("capability %s: it can answer diagnosed, so list the outcomes it may recommend", c.Key)
	}
	outcomes := map[string]bool{}
	for _, o := range c.Outcomes {
		if !appOutcomeRe.MatchString(o) {
			return fmt.Errorf("capability %s: outcome %q: lowercase letters, digits and _", c.Key, o)
		}
		if outcomes[o] {
			return fmt.Errorf("capability %s: outcome %q listed twice", c.Key, o)
		}
		outcomes[o] = true
	}
	if c.MaxHours < 1 || c.MaxHours > 24*30 {
		return fmt.Errorf("capability %s: max_hours must be 1–720", c.Key)
	}
	return nil
}

// AppRequestContext is plain facts the app may show or use — what was seen, and since when. It
// never names a ticket.
type AppRequestContext struct {
	// Symptom is the console's word for what's wrong (its triage outcome), e.g. needs_rebuild.
	Symptom string `json:"symptom,omitempty"`
	// Detail is one line of what was seen.
	Detail string `json:"detail,omitempty"`
	// Since is when the problem was first seen ("how long has this node been out").
	Since *time.Time `json:"since,omitempty"`
}

// AppRequest is one piece of work the console sends an app (POST {app}/requests).
type AppRequest struct {
	RequestID      string            `json:"request_id"`
	OrganizationID string            `json:"organization_id"`
	Capability     string            `json:"capability"`
	Inputs         map[string]string `json:"inputs,omitempty"`
	Context        AppRequestContext `json:"context"`
	// ReplyBy is when the request counts as overdue without an update or result.
	ReplyBy time.Time `json:"reply_by"`
}

// Validate checks a request's shape (the app also checks the org has it installed and the capability
// is offered).
func (r AppRequest) Validate() error {
	switch {
	case strings.TrimSpace(r.RequestID) == "":
		return errors.New("request_id is required")
	case strings.TrimSpace(r.OrganizationID) == "":
		return errors.New("organization_id is required")
	case !appKeyRe.MatchString(r.Capability):
		return fmt.Errorf("capability %q is not a capability key", r.Capability)
	case r.ReplyBy.IsZero():
		return errors.New("reply_by is required")
	}
	return nil
}

// AppRequestUpdate is a progress note from the app (POST /console/requests/{id}/updates). The console
// posts it on the ticket.
type AppRequestUpdate struct {
	Text string `json:"text"`
}

// MaxAppUpdateLen bounds an update's text.
const MaxAppUpdateLen = 4000

// Validate checks an update.
func (u AppRequestUpdate) Validate() error {
	t := strings.TrimSpace(u.Text)
	if t == "" {
		return errors.New("update text is empty")
	}
	if len(t) > MaxAppUpdateLen {
		return fmt.Errorf("update text is at most %d characters", MaxAppUpdateLen)
	}
	return nil
}

// AppRequestResult is how a request ended (POST /console/requests/{id}/result) — exactly one per
// request.
type AppRequestResult struct {
	Kind AppResultKind `json:"kind"`
	// Outcome is what a diagnosed result recommends — one of the capability's Outcomes. Empty for
	// every other kind.
	Outcome string `json:"outcome,omitempty"`
	// Evidence is what the app found or changed, for people and later playbook steps.
	Evidence json.RawMessage `json:"evidence,omitempty"`
	// Note is one line in the app's words — required for needs_human and rejected.
	Note string `json:"note,omitempty"`
}

// MaxAppEvidenceLen bounds a result's evidence.
const MaxAppEvidenceLen = 64 << 10

// Validate checks a result against the capability it answers. capability nil checks the shape only.
func (r AppRequestResult) Validate(capability *AppCapability) error {
	if !r.Kind.valid() {
		return fmt.Errorf("unknown result kind %q", r.Kind)
	}
	if r.Kind == AppResultDiagnosed {
		if r.Outcome == "" {
			return errors.New("a diagnosed result needs the outcome it recommends")
		}
	} else if r.Outcome != "" {
		return fmt.Errorf("only a diagnosed result carries an outcome, not %s", r.Kind)
	}
	if (r.Kind == AppResultNeedsHuman || r.Kind == AppResultRejected) && strings.TrimSpace(r.Note) == "" {
		return fmt.Errorf("a %s result needs a note saying why", r.Kind)
	}
	if len(r.Evidence) > MaxAppEvidenceLen {
		return fmt.Errorf("evidence is at most %d KB", MaxAppEvidenceLen>>10)
	}
	if len(r.Evidence) > 0 && !json.Valid(r.Evidence) {
		return errors.New("evidence must be JSON")
	}
	if capability == nil {
		return nil
	}
	allowed := r.Kind == AppResultRejected // always allowed
	for _, k := range capability.ResultKinds {
		if k == r.Kind {
			allowed = true
		}
	}
	if !allowed {
		return fmt.Errorf("capability %s doesn't answer %s", capability.Key, r.Kind)
	}
	if r.Kind == AppResultDiagnosed {
		for _, o := range capability.Outcomes {
			if o == r.Outcome {
				return nil
			}
		}
		return fmt.Errorf("capability %s doesn't recommend outcome %q", capability.Key, r.Outcome)
	}
	return nil
}

// ─── Signing console → app requests ──────────────────────────────────────────────────────────────

// Headers on every console → app call. The signature covers the timestamp and the exact body, so a
// captured call can't be replayed later or altered.
const (
	ConsoleSignatureHeader = "X-Console-Signature"
	ConsoleTimestampHeader = "X-Console-Timestamp"
)

// MaxConsoleSignatureSkew is how far a request's timestamp may be from the app's clock.
const MaxConsoleSignatureSkew = 5 * time.Minute

// SignConsoleRequest returns the two header values for body, signed with the app's signing key at ts:
// "sha256=<hex HMAC-SHA256 of '<unix ts>.<body>'>" and the unix timestamp.
func SignConsoleRequest(key, body []byte, ts time.Time) (signature, timestamp string) {
	timestamp = strconv.FormatInt(ts.Unix(), 10)
	return "sha256=" + consoleMAC(key, timestamp, body), timestamp
}

// VerifyConsoleRequest checks a console → app call: the signature matches body under key, and the
// timestamp is within MaxConsoleSignatureSkew of now.
func VerifyConsoleRequest(key, body []byte, signature, timestamp string, now time.Time) error {
	if len(key) == 0 {
		return errors.New("no signing key configured")
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return errors.New("missing or bad timestamp")
	}
	if d := now.Sub(time.Unix(sec, 0)); d > MaxConsoleSignatureSkew || d < -MaxConsoleSignatureSkew {
		return errors.New("timestamp too far from now — a replay, or a clock out of sync")
	}
	got, ok := strings.CutPrefix(strings.TrimSpace(signature), "sha256=")
	if !ok {
		return errors.New("missing or bad signature")
	}
	want := consoleMAC(key, strconv.FormatInt(sec, 10), body)
	if !hmac.Equal([]byte(got), []byte(want)) {
		return errors.New("signature doesn't match")
	}
	return nil
}

func consoleMAC(key []byte, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(timestamp))
	m.Write([]byte("."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}
