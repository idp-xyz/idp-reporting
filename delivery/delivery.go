// Package delivery composes the deliverers of a scheduled run's notification (S16-B, docs/S16B_SCOPE.md, the amendment of
// docs/ADR_0020_SCHEDULED_REPORTS.md): Multi gives one notification to each deliverer the deployment configured (delivery/webhook,
// delivery/email) and answers the scheduler one result, so that the scheduler's one retry policy covers them all. Each deliverer stays what
// it is; a deliverer that is not configured is simply absent. Nothing of a notification is kept here.
package delivery

import (
	"context"
	"errors"

	r "idp.local/reporting/reporting"
)

// Named is one deliverer under the fixed word the log calls it by ("webhook", "email").
type Named struct {
	Name      string
	Deliverer r.Deliverer
}

// Attempt is one deliverer's attempt as Multi reports it: the deliverer's word, the run's and the schedule's platform ids, the attempt's
// number and the outcome word with the status the deliverer gave (an HTTP status, an SMTP reply code, 0 without one). Nothing of the owner
// and nothing of what was sent.
type Attempt struct {
	Deliverer         string
	RunID, ScheduleID string
	Attempt           int
	Outcome           string
	Status            int
}

// ErrConfig is the refusal of a composition: no deliverer, one without a name or without a deliverer, or a name used twice.
var ErrConfig = errors.New("delivery: at least one deliverer, each with a name of its own")

// Multi is the fan-out: one notification to every deliverer, one result back.
type Multi struct {
	deliverers []Named
	report     func(Attempt)
}

// NewMulti composes the deliverers in the order given. report, when set, is told of each deliverer's attempt as it ends (the server logs it);
// it is called on the scheduler's goroutine, inside Deliver.
func NewMulti(report func(Attempt), deliverers ...Named) (*Multi, error) {
	if len(deliverers) == 0 {
		return nil, ErrConfig
	}
	seen := map[string]bool{}
	for _, d := range deliverers {
		if d.Name == "" || d.Deliverer == nil || seen[d.Name] {
			return nil, ErrConfig
		}
		seen[d.Name] = true
	}
	return &Multi{deliverers: append([]Named(nil), deliverers...), report: report}, nil
}

// Names lists the deliverers' words, in order.
func (m *Multi) Names() []string {
	out := make([]string, 0, len(m.deliverers))
	for _, d := range m.deliverers {
		out = append(out, d.Name)
	}
	return out
}

// Deliver implements reporting.Deliverer: one attempt of each deliverer, in order, every one of them whatever the others answered. The result
// is the first deliverer's when all delivered; otherwise the first outcome that is tried again (DeliveryResult.Retry), so that the scheduler's
// retry reaches the deliverer that could not take the notification now; otherwise the first that is not. A retry repeats every deliverer: a
// delivery is at least once by the run's id (ADR 0020 decision 4), to each of them.
func (m *Multi) Deliver(ctx context.Context, n r.Notification) r.DeliveryResult {
	var first, retried, failed *r.DeliveryResult
	for _, d := range m.deliverers {
		res := d.Deliverer.Deliver(ctx, n)
		if m.report != nil {
			m.report(Attempt{Deliverer: d.Name, RunID: n.RunID, ScheduleID: n.ScheduleID, Attempt: n.Attempt, Outcome: res.Outcome, Status: res.Status})
		}
		if first == nil {
			first = &res
		}
		switch {
		case res.Outcome == r.DeliveryOutcomeDelivered:
		case res.Retry():
			if retried == nil {
				retried = &res
			}
		default:
			if failed == nil {
				failed = &res
			}
		}
	}
	switch {
	case retried != nil:
		return *retried
	case failed != nil:
		return *failed
	}
	return *first
}

var _ r.Deliverer = (*Multi)(nil)
