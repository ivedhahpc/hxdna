package playbook

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// placeholder matches {{name}} or {{step_name.dot.path}} anywhere in a param value or step text —
// text is prose, so a reference can sit anywhere in it.
var placeholder = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)(?:\.([^}\s]+))?\s*\}\}`)

// reasonPlaceholder matches {{reason}} as placeholder reads it.
var reasonPlaceholder = regexp.MustCompile(`\{\{\s*reason\s*\}\}`)

// Expander fills a step's {{placeholders}}. Bare names the engine knows: resource_id, run_by and
// reason. Any other bare name goes to Extra (the app's own — HelixOps: ticket_id, triage_outcome…).
// {{step.field.path}} reads an earlier step's result.
type Expander struct {
	Run *Run
	// Results are the earlier steps' results by step name.
	Results map[string]map[string]any
	// ForPeople is for text only people read (a chat message): a {{reason}} with nothing to read
	// becomes "no reason given" instead of failing the run.
	ForPeople bool
	// Extra resolves the app's bare names: ok false means it doesn't know name.
	Extra func(name string, forPeople bool) (value string, ok bool, err error)
}

// Text replaces every placeholder in text, erroring on the first one that doesn't resolve rather
// than sending a blank or broken value to a live system.
func (x Expander) Text(text string) (string, error) {
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	out := placeholder.ReplaceAllStringFunc(text, func(m string) string {
		sub := placeholder.FindStringSubmatch(m)
		name, path := sub[1], sub[2]
		if path == "" {
			switch name {
			case "resource_id":
				return x.Run.ResourceID
			case "run_by":
				return x.Run.RunByName
			case "reason":
				// What the person typed; else the app's fallback (HelixOps: the triage outcome).
				// CheckReason refuses a run that has neither before it starts.
				if x.Run.Reason != nil && *x.Run.Reason != "" {
					return *x.Run.Reason
				}
				if x.Run.ReasonFallback != "" {
					return x.Run.ReasonFallback
				}
				if x.ForPeople {
					return "no reason given"
				}
				fail(fmt.Errorf("{{reason}} needs a reason — switch on \"Ask for a reason when run\" on the playbook"))
				return m
			}
			if x.Extra != nil {
				v, ok, err := x.Extra(name, x.ForPeople)
				if err != nil {
					fail(err)
					return m
				}
				if ok {
					return v
				}
			}
			fail(fmt.Errorf("unknown placeholder %s", m))
			return m
		}
		data, ok := x.Results[name]
		if !ok {
			fail(fmt.Errorf("%s refers to step %q, which has not run", m, name))
			return m
		}
		v, found := lookupDotPath(data, path)
		if !found {
			fail(fmt.Errorf("%s: field %q not found in step %q's result", m, path, name))
			return m
		}
		return stringifyValue(v)
	})
	return out, firstErr
}

// Params expands every param value. A value that expands to "" is left out.
func (x Expander) Params(params map[string]string) (map[string]string, error) {
	if len(params) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(params))
	for k, v := range params {
		expanded, err := x.Text(v)
		if err != nil {
			return nil, fmt.Errorf("params.%s: %w", k, err)
		}
		if expanded != "" {
			out[k] = expanded
		}
	}
	return out, nil
}

// RenameStepRefs rewrites step's references to another step's fields — {{oldName.field}} — to
// newName, in its text and params, and reports whether anything changed. A bare {{name}} is a
// built-in, never a step, so only references with a field change. The app saves the changed steps
// in the same transaction as the rename, so a playbook never points at a name that's gone.
func RenameStepRefs(step *Step, oldName, newName string) bool {
	rewrite := func(text string) string {
		return placeholder.ReplaceAllStringFunc(text, func(m string) string {
			sub := placeholder.FindStringSubmatch(m)
			if sub[1] != oldName || sub[2] == "" {
				return m
			}
			return "{{" + newName + "." + sub[2] + "}}"
		})
	}
	changed := false
	if step.Content != nil {
		if text := rewrite(*step.Content); text != *step.Content {
			step.Content, changed = &text, true
		}
	}
	for k, v := range step.Params {
		if text := rewrite(v); text != v {
			step.Params[k], changed = text, true
		}
	}
	return changed
}

// UsesReason reports whether any step's text or params carry {{reason}}.
func UsesReason(steps []*Step) bool {
	for _, st := range steps {
		if st.Content != nil && reasonPlaceholder.MatchString(*st.Content) {
			return true
		}
		for _, v := range st.Params {
			if reasonPlaceholder.MatchString(v) {
				return true
			}
		}
	}
	return false
}

// lookupDotPath walks a.b.c through nested objects.
func lookupDotPath(data map[string]any, path string) (any, bool) {
	var cur any = data
	for _, part := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// stringifyValue renders a result value for a param or text: numbers without scientific notation,
// objects and arrays as JSON rather than dropped.
func stringifyValue(v any) string {
	switch tv := v.(type) {
	case string:
		return tv
	case float64:
		return strconv.FormatFloat(tv, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(tv)
	case nil:
		return ""
	default:
		b, err := json.Marshal(tv)
		if err != nil {
			return fmt.Sprint(tv)
		}
		return string(b)
	}
}
