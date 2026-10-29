package pdf

import (
	"errors"
	"fmt"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// tooWideError marks a character wider than the cell or box it would have to be placed in. It is
// an unsupported combination like every layout refusal, and the reason of its detail is DOES_NOT_FIT
// where other measurer refusals (a character the fonts lack) are NOT_COVERED.
type tooWideError struct{}

func (tooWideError) Error() string        { return r.ErrUnsupported.Error() }
func (tooWideError) Is(target error) bool { return target == r.ErrUnsupported }

// refuse is a layout refusal with its reason and place: an unsupported combination whose message
// is format (without values) and whose detail is the place with the reason (ADR 0004, decision 8).
func refuse(reason string, d doctemplates.Detail, format string, args ...any) error {
	d.Reason = reason
	return doctemplates.Refuse(fmt.Errorf(format+": %w", append(args, r.ErrUnsupported)...), d)
}

// placed gives a refusal that has no detail yet the place it was found at: what the measurer refuses
// is a character the fonts cannot print (NOT_COVERED) or one wider than its place (DOES_NOT_FIT). Any
// other error, and a refusal that already has a detail, stays as it is.
func placed(err error, d doctemplates.Detail) error {
	if err == nil || !errors.Is(err, r.ErrUnsupported) {
		return err
	}
	if _, ok := doctemplates.DetailOf(err); ok {
		return err
	}
	d.Reason = doctemplates.ReasonNotCovered
	var wide tooWideError
	if errors.As(err, &wide) {
		d.Reason = doctemplates.ReasonDoesNotFit
	}
	return doctemplates.Refuse(err, d)
}

func ptr(n int) *int { return &n }
