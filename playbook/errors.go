package playbook

import (
	"errors"
	"fmt"
)

// ValidationError is a problem with what was asked — a step that can't be saved, a run that can't
// start. Its message is written for the person who asked; apps answer it as a 400.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func validationErr(msg string) error { return &ValidationError{msg: msg} }

// IsValidation reports whether err is (or wraps) a ValidationError.
func IsValidation(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// ErrNotFound: the playbook or run isn't there (or belongs to another organization).
var ErrNotFound = errors.New("not found")

// ErrRunNotReady: Execute on a run that isn't waiting to be run (its checks failed or are still
// running, or it already ran).
var ErrRunNotReady = errors.New("playbook run is not ready to run")

// ErrChecksStale: a ready run's checks no longer stand — they passed more than ChecksValidFor ago,
// or the playbook (or a step) changed since. Run would act on checks the device may no longer pass.
var ErrChecksStale = errors.New("the checks are out of date — check the device again before running")

// ErrChecksFailed: RunNow's checks stopped the run before any action. Nothing was changed.
var ErrChecksFailed = errors.New("playbook checks did not pass")

// ErrResourceBusy: another run is acting on the resource, or acted on it after this run's checks
// started. Nothing acted; try again later — a new run checks the resource as it is then. Never
// queued: a run held back would act on checks the other run may have made untrue.
var ErrResourceBusy = errors.New("already processing")

// resourceBusy says which run is in the way.
func resourceBusy(holder *Run) error {
	if holder.Status == RunRunning {
		return fmt.Errorf("%w — %s is running on %s. Try again when it finishes", ErrResourceBusy, holder.PlaybookName, holder.ResourceID)
	}
	return fmt.Errorf("%w — %s ran on %s after these checks started. Check again", ErrResourceBusy, holder.PlaybookName, holder.ResourceID)
}
