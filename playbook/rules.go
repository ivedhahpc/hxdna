package playbook

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var stepNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidateStepName: a step's name is how later steps reference its result ({{name.field}}), so it
// is a lowercase identifier.
func ValidateStepName(name string) error {
	if !stepNameRe.MatchString(name) {
		return validationErr("step_name must start with a lowercase letter and contain only lowercase letters, digits, and underscores")
	}
	return nil
}

// OnlyParams rejects any param key not in allowed.
func OnlyParams(params map[string]string, allowed ...string) error {
	for k := range params {
		known := false
		for _, a := range allowed {
			if k == a {
				known = true
				break
			}
		}
		if !known {
			return validationErr(fmt.Sprintf("%q is not a setting of this step", k))
		}
	}
	return nil
}

// ValidateActionParams rejects an action param its contract entry doesn't declare (the device
// itself is never a param).
func ValidateActionParams(params map[string]string, inputSchema map[string]string, key string) error {
	for k := range params {
		if _, ok := inputSchema[k]; !ok {
			return validationErr(fmt.Sprintf("%q is not a param of %s", k, key))
		}
	}
	return nil
}

// reasonMaxLen bounds a run's reason — a line or two, not a document.
const reasonMaxLen = 500

// CheckReason checks, before anything runs, that the run has what the playbook needs from a
// reason, and returns the trimmed reason to keep on the run (nil when none was given). A person
// must give one when the playbook asks for it. {{reason}} with no reason reads fallback (HelixOps:
// the ticket's triage outcome — automation has no person to ask); without that either, the run is
// refused here rather than failing at the step after earlier ones already acted.
func CheckReason(p *Playbook, steps []*Step, reason string, byPerson bool, fallback string) (*string, error) {
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > reasonMaxLen {
		return nil, validationErr(fmt.Sprintf("the reason is too long — keep it under %d characters", reasonMaxLen))
	}
	if reason != "" {
		return &reason, nil
	}
	if p.AskReason && byPerson {
		return nil, validationErr(fmt.Sprintf("%s asks for a reason — say why you're running it", p.Name))
	}
	if UsesReason(steps) && fallback == "" {
		return nil, validationErr(fmt.Sprintf("%s uses {{reason}}, but this run has none — switch on \"Ask for a reason when run\" on the playbook", p.Name))
	}
	return nil, nil
}

// changedSince reports whether the playbook or any of its steps was created or edited after t. A
// deleted step leaves no trace, but deleting one can't make the checked actions less safe.
func changedSince(p *Playbook, steps []*Step, t time.Time) bool {
	if p.UpdatedAt.After(t) {
		return true
	}
	for _, st := range steps {
		if st.UpdatedAt.After(t) || st.CreatedAt.After(t) {
			return true
		}
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
