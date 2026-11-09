package reporting_test

import (
	"errors"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
)

// A recurrence is one of three forms with exactly its fields, a wall-clock time and a known IANA zone; anything else is refused.
func TestRecurrenceIsBounded(t *testing.T) {
	good := []r.Recurrence{
		{Frequency: "daily", At: "00:00", Timezone: "UTC"},
		{Frequency: "daily", At: "23:59", Timezone: "Asia/Shanghai"},
		{Frequency: "weekly", At: "08:30", Weekday: 1, Timezone: "Europe/Berlin"},
		{Frequency: "weekly", At: "08:30", Weekday: 7, Timezone: "America/Argentina/Buenos_Aires"},
		{Frequency: "monthly", At: "06:00", Day: 1, Timezone: "America/New_York"},
		{Frequency: "monthly", At: "06:00", Day: 28, Timezone: "Etc/GMT+5"},
	}
	for _, c := range good {
		if e := c.Validate(); e != nil {
			t.Errorf("%+v refused: %v", c, e)
		}
	}
	bad := []r.Recurrence{
		{Frequency: "hourly", At: "08:00", Timezone: "UTC"},
		{Frequency: "", At: "08:00", Timezone: "UTC"},
		{Frequency: "daily", At: "8:00", Timezone: "UTC"},
		{Frequency: "daily", At: "24:00", Timezone: "UTC"},
		{Frequency: "daily", At: "08:60", Timezone: "UTC"},
		{Frequency: "daily", At: "08:00:00", Timezone: "UTC"},
		{Frequency: "daily", At: "08:00", Timezone: ""},
		{Frequency: "daily", At: "08:00", Timezone: "Local"},
		{Frequency: "daily", At: "08:00", Timezone: "utc"},
		{Frequency: "daily", At: "08:00", Timezone: "Mars/Olympus_Mons"},
		{Frequency: "daily", At: "08:00", Timezone: "../../etc/passwd"},
		{Frequency: "daily", At: "08:00", Timezone: "Asia/Shanghai "},
		{Frequency: "daily", At: "08:00", Weekday: 1, Timezone: "UTC"},
		{Frequency: "daily", At: "08:00", Day: 1, Timezone: "UTC"},
		{Frequency: "weekly", At: "08:00", Timezone: "UTC"},
		{Frequency: "weekly", At: "08:00", Weekday: 8, Timezone: "UTC"},
		{Frequency: "weekly", At: "08:00", Weekday: 1, Day: 1, Timezone: "UTC"},
		{Frequency: "monthly", At: "08:00", Timezone: "UTC"},
		{Frequency: "monthly", At: "08:00", Day: 29, Timezone: "UTC"},
		{Frequency: "monthly", At: "08:00", Day: 31, Timezone: "UTC"},
		{Frequency: "monthly", At: "08:00", Day: 1, Weekday: 1, Timezone: "UTC"},
	}
	for _, c := range bad {
		if e := c.Validate(); !errors.Is(e, r.ErrInvalid) {
			t.Errorf("%+v accepted: %v", c, e)
		}
		if _, e := c.Next(time.Now()); e == nil {
			t.Errorf("%+v has a next run", c)
		}
	}
}

// Next is the first occurrence strictly after the time it is given, in UTC, in the recurrence's zone, across the zone's changes.
func TestRecurrenceNext(t *testing.T) {
	at := func(s string) time.Time {
		v, e := time.Parse(time.RFC3339, s)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	cases := []struct {
		c           r.Recurrence
		after, want string
	}{
		// Shanghai is UTC+8: 08:00 there is 00:00 UTC.
		{r.Recurrence{Frequency: "daily", At: "08:00", Timezone: "Asia/Shanghai"}, "2026-11-09T00:00:00Z", "2026-11-10T00:00:00Z"},
		{r.Recurrence{Frequency: "daily", At: "08:00", Timezone: "Asia/Shanghai"}, "2026-11-08T23:59:59Z", "2026-11-09T00:00:00Z"},
		{r.Recurrence{Frequency: "daily", At: "00:30", Timezone: "Asia/Shanghai"}, "2026-11-09T17:00:00Z", "2026-11-10T16:30:00Z"},
		// 2026-11-09 is a Monday.
		{r.Recurrence{Frequency: "weekly", At: "09:00", Weekday: 1, Timezone: "UTC"}, "2026-11-09T08:00:00Z", "2026-11-09T09:00:00Z"},
		{r.Recurrence{Frequency: "weekly", At: "09:00", Weekday: 1, Timezone: "UTC"}, "2026-11-09T09:00:00Z", "2026-11-16T09:00:00Z"},
		{r.Recurrence{Frequency: "weekly", At: "09:00", Weekday: 7, Timezone: "UTC"}, "2026-11-09T09:00:00Z", "2026-11-15T09:00:00Z"},
		{r.Recurrence{Frequency: "monthly", At: "06:00", Day: 28, Timezone: "UTC"}, "2026-01-28T06:00:00Z", "2026-02-28T06:00:00Z"},
		{r.Recurrence{Frequency: "monthly", At: "06:00", Day: 1, Timezone: "UTC"}, "2026-12-15T00:00:00Z", "2027-01-01T06:00:00Z"},
		// New York: EDT (UTC-4) until 2026-11-01, EST (UTC-5) after; the wall-clock time stays 07:00.
		{r.Recurrence{Frequency: "daily", At: "07:00", Timezone: "America/New_York"}, "2026-10-31T12:00:00Z", "2026-11-01T12:00:00Z"},
		{r.Recurrence{Frequency: "daily", At: "07:00", Timezone: "America/New_York"}, "2026-10-30T12:00:00Z", "2026-10-31T11:00:00Z"},
	}
	for _, c := range cases {
		got, e := c.c.Next(at(c.after))
		if e != nil || !got.Equal(at(c.want)) || got.Location() != time.UTC {
			t.Errorf("%+v after %s: %v %v, want %s", c.c, c.after, got, e, c.want)
		}
	}
	// A wall-clock time that the spring change skips, and one that the autumn change repeats, still run once a day, on every day.
	for _, c := range []r.Recurrence{{Frequency: "daily", At: "02:30", Timezone: "America/New_York"}, {Frequency: "daily", At: "01:30", Timezone: "America/New_York"}} {
		from := at("2026-03-05T00:00:00Z")
		seen := map[string]bool{}
		for n := 0; n < 300; n++ {
			next, e := c.Next(from)
			if e != nil || !next.After(from) {
				t.Fatalf("%+v after %v: %v %v", c, from, next, e)
			}
			day := next.In(mustZone(t, "America/New_York")).Format("2006-01-02")
			if seen[day] {
				t.Fatalf("%+v runs twice on %s", c, day)
			}
			seen[day] = true
			from = next
		}
		if len(seen) != 300 {
			t.Fatalf("%+v: %d days", c, len(seen))
		}
	}
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	l, e := time.LoadLocation(name)
	if e != nil {
		t.Fatal(e)
	}
	return l
}

// A run's query carries the as_of of its schedule's rule: the occurrence's date in the recurrence's zone, the day before, or the query's own.
func TestStartRunFreezesTheAsOf(t *testing.T) {
	p := contractsim.DemoPrincipal()
	base := r.Schedule{ID: r.NewID(), Owner: contractsim.DemoOwner(p, contractsim.FinanceIntegration), Query: contractsim.FinancialQuery(), Recurrence: r.Recurrence{Frequency: "daily", At: "07:00", Timezone: "Asia/Shanghai"}}
	// 23:00 UTC on 2026-11-09 is 07:00 on 2026-11-10 in Shanghai.
	due := time.Date(2026, 11, 9, 23, 0, 0, 0, time.UTC)
	for rule, want := range map[string]string{r.AsOfRunDate: "2026-11-10", r.AsOfPreviousDay: "2026-11-09", r.AsOfFixed: "2026-10-26"} {
		s := base
		s.AsOf = rule
		run, e := r.StartRun(s, due)
		if e != nil || run.Query.AsOf != want || run.State != r.RunDue || run.Delivery != r.DeliveryNone || !run.DueAt.Equal(due) || run.ScheduleID != s.ID || run.Owner != s.Owner {
			t.Fatalf("%s: %+v %v", rule, run, e)
		}
	}
	if s := base; s.Query.AsOf != "2026-10-26" {
		t.Fatal("the schedule's query was changed")
	}
}

// A schedule's name is text a person wrote: not blank, no control character, bounded.
func TestScheduleNames(t *testing.T) {
	for _, n := range []string{"应收日报", "Weekly AR — company demo", "a"} {
		if !r.ValidScheduleName(n) {
			t.Errorf("%q refused", n)
		}
	}
	long := make([]byte, r.MaxScheduleNameBytes+1)
	for i := range long {
		long[i] = 'a'
	}
	for _, n := range []string{"", "   ", "a\nb", "a\x00", "a\x7f", string(long), "\xff"} {
		if r.ValidScheduleName(n) {
			t.Errorf("%q accepted", n)
		}
	}
}
