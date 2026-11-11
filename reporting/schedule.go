package reporting

import (
	"context"
	"regexp"
	"strconv"
	"time"

	// The zone database is in the binary, so that a server without the system's zoneinfo judges and computes a zone as every other does
	// (S16-A): a recurrence names an IANA zone, and its next run is a function of that name alone.
	_ "time/tzdata"
)

// Scheduled reports (S16-A, docs/S16A_SCOPE.md, docs/ADR_0020_SCHEDULED_REPORTS.md). A schedule is one identity's: a query of the published
// catalog under one of its products, frozen as the person wrote it, a bounded recurrence in an IANA zone, and optionally an export format.
// Each occurrence is a run: the scheduler freezes the query's snapshot as its owner, through the same admission as a person's
// POST /snapshot-jobs, then, when the schedule names a format, exports it as POST /exports does. The source product's authority decides every
// read, as for any job: the run's principal is the owner's binding in force at the run, never a copy kept with the schedule.

// The frequencies of a recurrence.
const (
	FrequencyDaily   = "daily"
	FrequencyWeekly  = "weekly"
	FrequencyMonthly = "monthly"
)

// Recurrence is when a schedule runs: every day, one day of the week (1 Monday .. 7 Sunday, ISO 8601) or one day of the month (1..28, so that
// every month has it) at a wall-clock time HH:MM in an IANA zone. There is no free-form expression: the server can enumerate every value.
//
// A wall-clock time that a zone skips on a day (the spring change) runs at the instant Go's time.Date gives it on that day, and one that a
// zone repeats (the autumn change) runs once, at the instant time.Date gives it: one run per date in every case.
type Recurrence struct {
	Frequency string `json:"frequency"`
	At        string `json:"at"`
	Weekday   int    `json:"weekday,omitempty"`
	Day       int    `json:"day,omitempty"`
	Timezone  string `json:"timezone"`
}

var (
	wallClockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	// zonePattern is the spelling of an IANA zone name: "UTC" or Area/Location[/Sub], letters, digits and _ + -, at most 64 characters.
	zonePattern = regexp.MustCompile(`^(UTC|[A-Z][A-Za-z]+(/[A-Za-z0-9_+-]+){1,2})$`)
)

// location is the recurrence's zone, or ErrInvalid: a name of the right spelling that the zone database knows ("Local" never is).
func (c Recurrence) location() (*time.Location, error) {
	if len(c.Timezone) > 64 || !zonePattern.MatchString(c.Timezone) {
		return nil, ErrInvalid
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, ErrInvalid
	}
	return loc, nil
}

// Validate refuses (ErrInvalid) a recurrence that is not one of the three forms with exactly the fields of its form.
func (c Recurrence) Validate() error {
	if !wallClockPattern.MatchString(c.At) {
		return ErrInvalid
	}
	if _, err := c.location(); err != nil {
		return err
	}
	switch c.Frequency {
	case FrequencyDaily:
		if c.Weekday != 0 || c.Day != 0 {
			return ErrInvalid
		}
	case FrequencyWeekly:
		if c.Weekday < 1 || c.Weekday > 7 || c.Day != 0 {
			return ErrInvalid
		}
	case FrequencyMonthly:
		if c.Day < 1 || c.Day > 28 || c.Weekday != 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Next is the first occurrence strictly after after, in UTC. A valid recurrence always has one within 62 days.
func (c Recurrence) Next(after time.Time) (time.Time, error) {
	if err := c.Validate(); err != nil {
		return time.Time{}, err
	}
	loc, _ := c.location()
	hour, _ := strconv.Atoi(c.At[:2])
	minute, _ := strconv.Atoi(c.At[3:])
	y, m, d := after.In(loc).Date()
	for i := 0; i <= 62; i++ {
		// The calendar is walked in UTC, where every day has 24 hours; only the occurrence itself is placed in the zone.
		day := time.Date(y, m, d+i, 12, 0, 0, 0, time.UTC)
		switch c.Frequency {
		case FrequencyWeekly:
			if isoWeekday(day.Weekday()) != c.Weekday {
				continue
			}
		case FrequencyMonthly:
			if day.Day() != c.Day {
				continue
			}
		}
		at := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc)
		if at.After(after) {
			return at.UTC(), nil
		}
	}
	return time.Time{}, ErrIntegrity
}

func isoWeekday(w time.Weekday) int {
	if w == time.Sunday {
		return 7
	}
	return int(w)
}

// The as-of rule of a schedule: what as_of the run's query carries.
const (
	AsOfRunDate     = "run_date"     // the occurrence's date in the recurrence's zone (the default)
	AsOfPreviousDay = "previous_day" // the day before it: a report of the day that ended
	AsOfFixed       = "fixed"        // the query's own as_of, unchanged
)

// The states of a schedule. A deleted schedule is a tombstone (the application role never deletes a row, S04-A): it is not listed, not
// found, never runs, and no longer counts against the limit.
const (
	ScheduleActive  = "active"
	SchedulePaused  = "paused"
	ScheduleDeleted = "deleted"
)

// The bounds of schedules.
const (
	DefaultSchedulesPerIdentity = 20
	MaxSchedulesPerIdentity     = 200
	MaxScheduleListing          = 100
	MaxScheduleNameBytes        = 120
	// QuotaSchedules is the quota of schedules one identity may hold (active and paused): the word of the 429 answer.
	QuotaSchedules = "schedules"
)

// Schedule is one identity's scheduled report. Format is "" for a run that freezes the snapshot alone, else the export format ("xlsx" or a
// configured one) of its artifact. NextRunAt is the occurrence the scheduler starts next, zero unless the schedule is active. Key and
// RequestHash are the creation's idempotency, as a job's.
type Schedule struct {
	ID          string
	Owner       Owner
	Key         string
	RequestHash string
	Name        string
	Query       QuerySpec
	Format      string `json:",omitempty"`
	AsOf        string
	Recurrence  Recurrence
	State       string    `json:"-"`
	NextRunAt   time.Time `json:"-"`
	CreatedAt   time.Time `json:"-"`
	UpdatedAt   time.Time `json:"-"`
}

// ScheduleInput is what a person asks for in POST /schedules.
type ScheduleInput struct {
	Name       string     `json:"name"`
	Query      QuerySpec  `json:"query"`
	Format     string     `json:"export_format,omitempty"`
	AsOf       string     `json:"as_of,omitempty"`
	Recurrence Recurrence `json:"recurrence"`
}

// ValidScheduleName: 1..120 bytes of UTF-8 text without control characters, not blank, as the person wrote it (plainText).
func ValidScheduleName(n string) bool { return plainText(n, MaxScheduleNameBytes) }

// ValidateSchedule is what a store holds every schedule to before it keeps one: its own fields and its definition, never the source's view of
// the query (the service checks that against the catalog when the schedule is made, and the source product at every run).
func ValidateSchedule(s Schedule) error {
	if !validID(s.ID) || !s.Owner.Identity.Valid() || s.Owner.Integration == "" || s.Owner.Integration != s.Query.Integration || !validKey(s.Key) || len(s.RequestHash) != 64 ||
		!ValidScheduleName(s.Name) || (s.Format != "" && s.Format != "xlsx" && !validFormat(s.Format)) ||
		(s.AsOf != AsOfRunDate && s.AsOf != AsOfPreviousDay && s.AsOf != AsOfFixed) || s.Recurrence.Validate() != nil {
		return ErrInvalid
	}
	if _, e := time.Parse("2006-01-02", s.Query.AsOf); e != nil {
		return ErrInvalid
	}
	return nil
}

// The states of a run. A run is open while it is due, freezing its snapshot or exporting it; it ends succeeded, failed, refused (the owner is
// no longer bound, lacks the role, or the source product refused the read) or cancelled (the schedule was paused or deleted before the run
// started, or its owner cancelled its job).
const (
	RunDue       = "due"
	RunSnapshot  = "snapshot"
	RunExport    = "export"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunRefused   = "refused"
	RunCancelled = "cancelled"
)

// The states of a run's delivery: none (no deliverer is configured, or the run is not over), pending, delivered, or failed (refused by the
// receiver, or its attempts used up).
const (
	DeliveryNone      = "none"
	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

// RunOpen says whether a run's state is one the scheduler still advances.
func RunOpen(state string) bool { return state == RunDue || state == RunSnapshot || state == RunExport }

// ValidRunState and ValidDelivery say whether a word is one of the states.
func ValidRunState(s string) bool {
	return RunOpen(s) || s == RunSucceeded || s == RunFailed || s == RunRefused || s == RunCancelled
}
func ValidDelivery(s string) bool {
	return s == DeliveryNone || s == DeliveryPending || s == DeliveryDelivered || s == DeliveryFailed
}

// ScheduleRun is one occurrence of a schedule. Query and Format are frozen when the run starts (the schedule's, with the run's as_of), so
// that a run does not depend on what is done to its schedule afterwards. The job ids are the jobs it submitted; the snapshot and artifact
// ids are theirs once they succeeded. Generation fences the scheduler that holds the run, as a job's fences its worker.
type ScheduleRun struct {
	ID          string
	ScheduleID  string
	Owner       Owner
	DueAt       time.Time
	State       string `json:"-"`
	Query       QuerySpec
	Format      string    `json:",omitempty"`
	SnapshotJob string    `json:"-"`
	ExportJob   string    `json:"-"`
	SnapshotID  string    `json:"-"`
	ArtifactID  string    `json:"-"`
	ErrorCode   string    `json:"-"`
	Delivery    string    `json:"-"`
	Attempts    int       `json:"-"`
	Generation  int64     `json:"-"`
	CreatedAt   time.Time `json:"-"`
	FinishedAt  time.Time `json:"-"`
}

// RunClaim is a run the scheduler holds for a lease, with the state its schedule had when it was claimed.
type RunClaim struct {
	Run           ScheduleRun
	ScheduleState string
}

// StartRun makes the run of the schedule's occurrence due: the schedule's query with the as_of of its rule, in the recurrence's zone.
func StartRun(s Schedule, due time.Time) (ScheduleRun, error) {
	loc, err := s.Recurrence.location()
	if err != nil {
		return ScheduleRun{}, err
	}
	q := Normalize(s.Query)
	switch s.AsOf {
	case AsOfRunDate:
		q.AsOf = due.In(loc).Format("2006-01-02")
	case AsOfPreviousDay:
		y, m, d := due.In(loc).Date()
		q.AsOf = time.Date(y, m, d-1, 12, 0, 0, 0, time.UTC).Format("2006-01-02")
	}
	return ScheduleRun{ID: NewID(), ScheduleID: s.ID, Owner: s.Owner, DueAt: due.UTC(), State: RunDue, Query: q, Format: s.Format, Delivery: DeliveryNone}, nil
}

// The runs' bounds and pace.
const (
	MaxRunListing = 100
	// RunLease is how long a scheduler holds a run it claimed: longer than what one advance can take (a submission bounded by WorkTimeout and a
	// delivery bounded by its timeout).
	RunLease = 2 * time.Minute
	// RunRecheck is when an open run whose job is not over is looked at again.
	RunRecheck = 10 * time.Second
	// RunRetry is when a run whose start the source product could not answer (AUTHORITY_UNAVAILABLE) is tried again, until RunMaxWait after
	// the run was started.
	RunRetry   = time.Minute
	RunMaxWait = time.Hour
	// RunMaxDuration bounds a run: one whose jobs have not ended within it after the run was started (its CreatedAt, which is the occurrence
	// or, for a run caught up after a downtime, later) fails RUN_TIMEOUT (its jobs are left as they are, the owner's own).
	RunMaxDuration = 6 * time.Hour
	// MaxDeliveryAttempts bounds the attempts of a delivery; the waits between them double from DeliveryBackoff, at most DeliveryMaxBackoff.
	MaxDeliveryAttempts = 6
	DeliveryBackoff     = 30 * time.Second
	DeliveryMaxBackoff  = 15 * time.Minute
)

// RunTimeoutCode is the code of a run that did not end within RunMaxDuration; JobCancelledCode the code of a run whose job its owner cancelled.
const (
	RunTimeoutCode   = "RUN_TIMEOUT"
	JobCancelledCode = "JOB_CANCELLED"
)

// ScheduleListing is one page of an identity's schedules, newest first, or of one schedule's runs: at most Limit, strictly older than Before
// (the last of the previous page) when it is set.
type ScheduleListing struct {
	Limit  int
	Before string
}

// Valid says whether the listing can be run against a bound.
func (l ScheduleListing) Valid(most int) bool {
	return l.Limit >= 1 && l.Limit <= most && (l.Before == "" || validID(l.Before))
}

// ScheduleStore keeps schedules and their runs (S16-A), under the same isolation as the jobs: every call of a person is one identity's and
// sees only its rows; the scheduler's calls (StartDueRuns, ClaimRuns, UpdateRun) work across identities, as the workers' claims do.
//
// CreateSchedule stores the schedule, active, its first occurrence after the store's clock as NextRunAt; or answers the schedule its key
// already names (a replay, never refused by the limit; another request under the key is ErrConflict). Otherwise, in the same atomic step as the
// insert, the identity's schedules that are not deleted are counted against limit: at it, a *QuotaError naming QuotaSchedules, and nothing is
// stored.
//
// SetScheduleState pauses (NextRunAt cleared), resumes (NextRunAt the first occurrence after the store's clock: what was missed while paused
// is not run) or deletes a schedule of the identity; the same state again changes nothing; a deleted schedule is ErrNotFound.
//
// StartDueRuns starts, for at most limit active schedules whose NextRunAt is not after the store's clock and that have no open run, one run
// each (StartRun of that occurrence) and moves NextRunAt to the first occurrence after the store's clock: after a downtime the missed
// occurrences are caught up once, by the oldest, never one run each. Each schedule is locked while it is started, so that two schedulers never
// start the same occurrence, and a schedule another scheduler holds is passed over.
//
// ClaimRuns claims at most limit runs that are open or whose delivery is pending, whose time to be looked at has come and whose lease has run
// out by the store's clock, each for the lease (its generation one higher). UpdateRun records what the scheduler did with a claimed run (its
// state, ids, code, delivery and attempts; the rest is the run's own), releases the lease and sets when the run is to be looked at again;
// it is ErrLease, changing nothing, when the claim no longer holds the run (another generation, or the lease ran out).
type ScheduleStore interface {
	CreateSchedule(ctx context.Context, s Schedule, limit int) (Schedule, error)
	Schedule(ctx context.Context, i Identity, id string) (Schedule, error)
	Schedules(ctx context.Context, i Identity, l ScheduleListing) ([]Schedule, error)
	SetScheduleState(ctx context.Context, i Identity, id, state string) (Schedule, error)
	ScheduleRuns(ctx context.Context, i Identity, scheduleID string, l ScheduleListing) ([]ScheduleRun, error)
	StartDueRuns(ctx context.Context, limit int) (int, error)
	ClaimRuns(ctx context.Context, lease time.Duration, limit int) ([]RunClaim, error)
	UpdateRun(ctx context.Context, c RunClaim, run ScheduleRun, recheck time.Duration) error
}

// ValidRunUpdate is what a store holds an update to: the claimed run's identity unchanged, known states, a code and ids of their shape.
func ValidRunUpdate(c RunClaim, run ScheduleRun) error {
	if run.ID != c.Run.ID || run.ScheduleID != c.Run.ScheduleID || run.Owner != c.Run.Owner || !run.DueAt.Equal(c.Run.DueAt) || run.Generation != c.Run.Generation ||
		!ValidRunState(run.State) || !ValidDelivery(run.Delivery) || run.Attempts < 0 || run.Attempts > MaxDeliveryAttempts || len(run.ErrorCode) > 64 {
		return ErrInvalid
	}
	for _, id := range []string{run.SnapshotJob, run.ExportJob, run.SnapshotID, run.ArtifactID} {
		if id != "" && !validID(id) {
			return ErrInvalid
		}
	}
	return nil
}
