package delivery_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"idp.local/reporting/delivery"
	r "idp.local/reporting/reporting"
)

// fake is a deliverer that answers a fixed result and counts its attempts.
type fake struct {
	result r.DeliveryResult
	calls  int
	last   r.Notification
}

func (f *fake) Deliver(_ context.Context, n r.Notification) r.DeliveryResult {
	f.calls++
	f.last = n
	return f.result
}

func notification() r.Notification {
	return r.Notification{Event: r.NotificationEvent, RunID: strings.Repeat("a", 32), ScheduleID: strings.Repeat("b", 32), Tenant: "t", Issuer: "https://idp.example", Subject: "s", Integration: "p", State: r.RunSucceeded, Attempt: 3}
}

// A composition needs at least one deliverer, each with a name of its own.
func TestACompositionIsRefusedWithoutNamedDeliverers(t *testing.T) {
	ok := &fake{result: r.DeliveryResult{Outcome: r.DeliveryOutcomeDelivered, Status: 204}}
	for name, list := range map[string][]delivery.Named{
		"none":          {},
		"no name":       {{Deliverer: ok}},
		"no deliverer":  {{Name: "webhook"}},
		"a name twice":  {{Name: "webhook", Deliverer: ok}, {Name: "webhook", Deliverer: ok}},
		"one of each":   {{Name: "webhook", Deliverer: ok}, {Name: "email", Deliverer: ok}, {Name: "", Deliverer: ok}},
		"nil deliverer": {{Name: "email", Deliverer: nil}},
	} {
		if _, e := delivery.NewMulti(nil, list...); !errors.Is(e, delivery.ErrConfig) {
			t.Errorf("%s: %v", name, e)
		}
	}
	m, e := delivery.NewMulti(nil, delivery.Named{Name: "webhook", Deliverer: ok}, delivery.Named{Name: "email", Deliverer: ok})
	if e != nil || !slices.Equal(m.Names(), []string{"webhook", "email"}) {
		t.Fatal(m, e)
	}
}

// Every deliverer gets every notification, whatever the others answered; each attempt is reported with its deliverer's word and the ids; the
// one result is the first deliverer's when all delivered, else the first that is tried again, else the first that is not.
func TestEveryDelivererGetsTheNotificationAndTheResultIsTheOneToRetry(t *testing.T) {
	result := func(outcome string, status int) r.DeliveryResult {
		return r.DeliveryResult{Outcome: outcome, Status: status}
	}
	delivered := func(status int) r.DeliveryResult { return result(r.DeliveryOutcomeDelivered, status) }
	for name, c := range map[string]struct {
		webhook, email r.DeliveryResult
		want           r.DeliveryResult
	}{
		"both delivered":                  {delivered(204), delivered(250), delivered(204)},
		"the mail could not be taken":     {delivered(204), result(r.DeliveryOutcomeThrottled, 451), result(r.DeliveryOutcomeThrottled, 451)},
		"the webhook was down":            {result(r.DeliveryOutcomeServerError, 503), delivered(250), result(r.DeliveryOutcomeServerError, 503)},
		"the webhook refused it":          {result(r.DeliveryOutcomeRejected, 400), delivered(250), result(r.DeliveryOutcomeRejected, 400)},
		"one refused, one to retry":       {result(r.DeliveryOutcomeRejected, 410), result(r.DeliveryOutcomeUnreachable, 0), result(r.DeliveryOutcomeUnreachable, 0)},
		"one to retry, one refused":       {result(r.DeliveryOutcomeUnreachable, 0), result(r.DeliveryOutcomeRejected, 550), result(r.DeliveryOutcomeUnreachable, 0)},
		"both to retry: the first":        {result(r.DeliveryOutcomeThrottled, 429), result(r.DeliveryOutcomeUnreachable, 0), result(r.DeliveryOutcomeThrottled, 429)},
		"both refused: the first":         {result(r.DeliveryOutcomeRedirected, 301), result(r.DeliveryOutcomeRejected, 535), result(r.DeliveryOutcomeRedirected, 301)},
		"the mail refused, the hook fine": {delivered(200), result(r.DeliveryOutcomeRejected, 550), result(r.DeliveryOutcomeRejected, 550)},
	} {
		hook, mail := &fake{result: c.webhook}, &fake{result: c.email}
		var reported []delivery.Attempt
		m, e := delivery.NewMulti(func(a delivery.Attempt) { reported = append(reported, a) }, delivery.Named{Name: "webhook", Deliverer: hook}, delivery.Named{Name: "email", Deliverer: mail})
		if e != nil {
			t.Fatal(e)
		}
		n := notification()
		got := m.Deliver(context.Background(), n)
		if got != c.want {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
		if hook.calls != 1 || mail.calls != 1 || hook.last != n || mail.last != n {
			t.Errorf("%s: webhook %d, email %d attempts", name, hook.calls, mail.calls)
		}
		want := []delivery.Attempt{
			{Deliverer: "webhook", RunID: n.RunID, ScheduleID: n.ScheduleID, Attempt: 3, Outcome: c.webhook.Outcome, Status: c.webhook.Status},
			{Deliverer: "email", RunID: n.RunID, ScheduleID: n.ScheduleID, Attempt: 3, Outcome: c.email.Outcome, Status: c.email.Status},
		}
		if !slices.Equal(reported, want) {
			t.Errorf("%s: reported %+v", name, reported)
		}
	}
	// One deliverer alone is passed through, with or without a report.
	hook := &fake{result: r.DeliveryResult{Outcome: r.DeliveryOutcomeRedirected, Status: 307}}
	m, _ := delivery.NewMulti(nil, delivery.Named{Name: "webhook", Deliverer: hook})
	if got := m.Deliver(context.Background(), notification()); got != hook.result || hook.calls != 1 {
		t.Fatalf("%+v", got)
	}
}
