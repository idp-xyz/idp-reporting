package reporting

import (
	"context"
	"errors"
	"time"
)

// Schedules is the service of scheduled reports (S16-A): the person's routes (create, list, read, pause, resume, delete, the runs) and the
// scheduler's Tick. It submits every run through the Service, as the run's owner, exactly as the owner's own POST /snapshot-jobs and
// POST /exports do: the same admission, the same quotas, the same source authority.
type Schedules struct {
	svc     *Service
	store   ScheduleStore
	limit   int
	resolve func(Identity) (Principal, bool)
	deliver Deliverer
	audit   AuditLog
	now     func() time.Time
}

// ScheduleOptions composes Schedules. Resolve answers the principal an identity is bound as now (the bindings in force: a run acts with the
// owner's current products and roles, never with what they were when the schedule was made); false for an identity no binding names any more.
// Limit is the schedules one identity may hold (0: DefaultSchedulesPerIdentity, at most MaxSchedulesPerIdentity). Deliver, when set, is told
// of every run that ends; Audit, when set, records each run's end as the owner's schedule.run event.
type ScheduleOptions struct {
	Store   ScheduleStore
	Resolve func(Identity) (Principal, bool)
	Limit   int
	Deliver Deliverer
	Audit   AuditLog
}

// NewSchedules refuses (ErrInvalid) a missing service, store or resolver and a limit out of bounds.
func NewSchedules(svc *Service, o ScheduleOptions) (*Schedules, error) {
	if svc == nil || o.Store == nil || o.Resolve == nil || o.Limit < 0 || o.Limit > MaxSchedulesPerIdentity {
		return nil, ErrInvalid
	}
	if o.Limit == 0 {
		o.Limit = DefaultSchedulesPerIdentity
	}
	return &Schedules{svc: svc, store: o.Store, limit: o.Limit, resolve: o.Resolve, deliver: o.Deliver, audit: o.Audit, now: svc.now}, nil
}

// Limit is the schedules one identity may hold.
func (s *Schedules) Limit() int { return s.limit }

// Delivers says whether runs that end are delivered to a webhook.
func (s *Schedules) Delivers() bool { return s.deliver != nil }

// ScheduleView is what the API shows of a schedule: the definition as the person wrote it, its state and its next occurrence.
type ScheduleView struct {
	ID          string     `json:"schedule_id"`
	Name        string     `json:"name"`
	Integration string     `json:"integration_id"`
	State       string     `json:"state"`
	Recurrence  Recurrence `json:"recurrence"`
	AsOf        string     `json:"as_of"`
	Format      string     `json:"export_format,omitempty"`
	Query       QuerySpec  `json:"query"`
	NextRunAt   string     `json:"next_run_at,omitempty"`
	CreatedAt   string     `json:"created_at"`
}

// RunView is what the API shows of a run: its occurrence, its state, the jobs it submitted (read through GET /jobs/{id}), their results once
// they succeeded (GET /snapshots/{id}/rows, GET /artifacts/{id}/content) and its delivery.
type RunView struct {
	ID          string `json:"run_id"`
	ScheduleID  string `json:"schedule_id"`
	DueAt       string `json:"due_at"`
	State       string `json:"state"`
	AsOf        string `json:"as_of"`
	SnapshotJob string `json:"snapshot_job_id,omitempty"`
	ExportJob   string `json:"export_job_id,omitempty"`
	SnapshotID  string `json:"snapshot_id,omitempty"`
	ArtifactID  string `json:"artifact_id,omitempty"`
	Format      string `json:"export_format,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
	Delivery    string `json:"delivery"`
	FinishedAt  string `json:"finished_at,omitempty"`
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func scheduleView(v Schedule) ScheduleView {
	return ScheduleView{ID: v.ID, Name: v.Name, Integration: v.Owner.Integration, State: v.State, Recurrence: v.Recurrence, AsOf: v.AsOf, Format: v.Format, Query: v.Query, NextRunAt: stamp(v.NextRunAt), CreatedAt: stamp(v.CreatedAt)}
}

func runView(v ScheduleRun) RunView {
	return RunView{ID: v.ID, ScheduleID: v.ScheduleID, DueAt: stamp(v.DueAt), State: v.State, AsOf: v.Query.AsOf, SnapshotJob: v.SnapshotJob, ExportJob: v.ExportJob, SnapshotID: v.SnapshotID, ArtifactID: v.ArtifactID, Format: v.Format, ErrorCode: v.ErrorCode, Delivery: v.Delivery, FinishedAt: stamp(v.FinishedAt)}
}

// validated is the person's request checked as the service checks a snapshot's before it asks the source: the caller holds the product, the
// dataset is in the published catalog and the query fits it. It reads no row and prepares no read: each run does, as its owner, when it runs.
func (s *Schedules) validated(ctx context.Context, p Principal, q QuerySpec) (QuerySpec, Owner, error) {
	q = Normalize(q)
	c, sc, e := s.svc.source(p, q.Integration)
	if e != nil {
		return q, Owner{}, e
	}
	catalog, e := c.Adapter.DescribeCatalog(ctx, sc)
	if e != nil {
		return q, Owner{}, e
	}
	var d *Dataset
	for n := range catalog {
		if catalog[n].Ref == q.Dataset {
			if d != nil {
				return q, Owner{}, ErrIntegrity
			}
			d = &catalog[n]
		}
	}
	if d == nil {
		return q, Owner{}, ErrUnsupported
	}
	if e = ValidateQuery(q, *d); e != nil {
		return q, Owner{}, e
	}
	return q, sc.Owner, nil
}

// exportFormat is the service's format for a schedule's: "" for none, ErrUnsupported for one the service does not write.
func (s *Schedules) exportFormat(f string) error {
	switch f {
	case "":
		return nil
	case "xlsx":
		if s.svc.writers[""] == nil {
			return ErrUnsupported
		}
		return nil
	}
	if !validFormat(f) || s.svc.writers[f] == nil {
		return ErrUnsupported
	}
	return nil
}

// Create makes a schedule of the caller's, active, under its Idempotency-Key. The as_of rule defaults to run_date.
func (s *Schedules) Create(ctx context.Context, p Principal, in ScheduleInput, key string) (ScheduleView, error) {
	if !validKey(key) || !ValidScheduleName(in.Name) {
		return ScheduleView{}, ErrInvalid
	}
	if in.AsOf == "" {
		in.AsOf = AsOfRunDate
	}
	if in.AsOf != AsOfRunDate && in.AsOf != AsOfPreviousDay && in.AsOf != AsOfFixed {
		return ScheduleView{}, ErrInvalid
	}
	if e := in.Recurrence.Validate(); e != nil {
		return ScheduleView{}, e
	}
	if e := s.exportFormat(in.Format); e != nil {
		return ScheduleView{}, e
	}
	q, owner, e := s.validated(ctx, p, in.Query)
	if e != nil {
		return ScheduleView{}, e
	}
	in.Query = q
	v := Schedule{ID: NewID(), Owner: owner, Key: key, RequestHash: Digest(in), Name: in.Name, Query: q, Format: in.Format, AsOf: in.AsOf, Recurrence: in.Recurrence}
	if e = ValidateSchedule(v); e != nil {
		return ScheduleView{}, e
	}
	v, e = s.store.CreateSchedule(ctx, v, s.limit)
	if e != nil {
		return ScheduleView{}, e
	}
	return scheduleView(v), nil
}

// own reads one of the caller's schedules under a product the caller holds now.
func (s *Schedules) own(ctx context.Context, p Principal, id string) (Schedule, error) {
	if !p.Identity.Valid() || !validID(id) {
		return Schedule{}, ErrNotFound
	}
	v, e := s.store.Schedule(ctx, p.Identity, id)
	if e != nil {
		return Schedule{}, e
	}
	if _, _, e = s.svc.source(p, v.Owner.Integration); e != nil {
		return Schedule{}, e
	}
	return v, nil
}

// Get reads one of the caller's schedules.
func (s *Schedules) Get(ctx context.Context, p Principal, id string) (ScheduleView, error) {
	v, e := s.own(ctx, p, id)
	if e != nil {
		return ScheduleView{}, e
	}
	return scheduleView(v), nil
}

// List is one page of the caller's schedules, newest first, under the products the caller holds now.
func (s *Schedules) List(ctx context.Context, p Principal, l ScheduleListing) ([]ScheduleView, error) {
	if !p.Identity.Valid() || !l.Valid(MaxScheduleListing) {
		return nil, ErrInvalid
	}
	all, e := s.store.Schedules(ctx, p.Identity, l)
	if e != nil {
		return nil, e
	}
	held := s.svc.held(p)
	out := []ScheduleView{}
	for _, v := range all {
		for _, h := range held {
			if h == v.Owner.Integration {
				out = append(out, scheduleView(v))
				break
			}
		}
	}
	return out, nil
}

// SetState pauses, resumes or deletes one of the caller's schedules. A resumption checks the query against the catalog again, as a creation
// does: a schedule whose dataset was withdrawn is not resumed.
func (s *Schedules) SetState(ctx context.Context, p Principal, id, state string) (ScheduleView, error) {
	if state != ScheduleActive && state != SchedulePaused && state != ScheduleDeleted {
		return ScheduleView{}, ErrInvalid
	}
	v, e := s.own(ctx, p, id)
	if e != nil {
		return ScheduleView{}, e
	}
	if state == ScheduleActive && v.State != ScheduleActive {
		if _, _, e = s.validated(ctx, p, v.Query); e != nil {
			return ScheduleView{}, e
		}
	}
	if v, e = s.store.SetScheduleState(ctx, p.Identity, id, state); e != nil {
		return ScheduleView{}, e
	}
	return scheduleView(v), nil
}

// Runs is one page of a schedule's runs, newest occurrence first.
func (s *Schedules) Runs(ctx context.Context, p Principal, id string, l ScheduleListing) ([]RunView, error) {
	if !l.Valid(MaxRunListing) {
		return nil, ErrInvalid
	}
	if _, e := s.own(ctx, p, id); e != nil {
		return nil, e
	}
	runs, e := s.store.ScheduleRuns(ctx, p.Identity, id, l)
	if e != nil {
		return nil, e
	}
	out := make([]RunView, 0, len(runs))
	for _, v := range runs {
		out = append(out, runView(v))
	}
	return out, nil
}

// Notification is what a delivery says of a run that ended: whose it is, which occurrence, how it ended and the ids of its results. It carries
// no value of the report: no row, no query, no name. The receiver reads the results, if it is to, with the owner's own access, through the
// routes whose source authority decides every read.
type Notification struct {
	Event       string `json:"event"`
	RunID       string `json:"run_id"`
	ScheduleID  string `json:"schedule_id"`
	Tenant      string `json:"tenant"`
	Issuer      string `json:"issuer"`
	Subject     string `json:"subject"`
	Integration string `json:"integration_id"`
	DueAt       string `json:"due_at"`
	FinishedAt  string `json:"finished_at"`
	State       string `json:"state"`
	SnapshotID  string `json:"snapshot_id,omitempty"`
	ArtifactID  string `json:"artifact_id,omitempty"`
	Format      string `json:"export_format,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
	Attempt     int    `json:"attempt"`
	SentAt      string `json:"sent_at"`
}

// NotificationEvent is the one event a delivery carries.
const NotificationEvent = "schedule.run.finished"

// The outcomes of one delivery attempt: the fixed words of the log and the metrics. Retry says which are tried again.
const (
	DeliveryOutcomeDelivered   = "delivered"    // a 2xx answer
	DeliveryOutcomeRejected    = "rejected"     // a 4xx answer other than 408 and 429: the receiver refused it, it is not tried again
	DeliveryOutcomeRedirected  = "redirected"   // a 3xx answer: redirects are never followed, it is not tried again
	DeliveryOutcomeThrottled   = "throttled"    // 408 or 429
	DeliveryOutcomeServerError = "server_error" // a 5xx answer
	DeliveryOutcomeUnreachable = "unreachable"  // no answer: the connection, TLS or the timeout failed
)

// DeliveryResult is one attempt's outcome and the HTTP status of the answer (0 without one).
type DeliveryResult struct {
	Outcome string
	Status  int
}

// Retry says whether the outcome is tried again (within MaxDeliveryAttempts).
func (d DeliveryResult) Retry() bool {
	return d.Outcome == DeliveryOutcomeThrottled || d.Outcome == DeliveryOutcomeServerError || d.Outcome == DeliveryOutcomeUnreachable
}

// Deliverer delivers a notification (delivery/webhook is the one of S16-A). It is the deployment's: no route configures one.
type Deliverer interface {
	Deliver(ctx context.Context, n Notification) DeliveryResult
}

// DeliveryRecord is one attempt as the scheduler reports it, for the log: ids, the attempt's number, its outcome word and status, and the
// delivery's state after it.
type DeliveryRecord struct {
	RunID, ScheduleID string
	Attempt           int
	Outcome           string
	Status            int
	Delivery          string
}

// TickResult is what one Tick did: the runs it started, the runs it advanced, and every delivery attempt.
type TickResult struct {
	Started    int
	Advanced   int
	Lost       int // runs whose lease was lost before their advance was recorded
	Deliveries []DeliveryRecord
}

// The scheduler's batch per Tick.
const (
	tickStartBatch = 32
	tickClaimBatch = 16
	// advanceTimeout bounds one run's advance: a submission (bounded by WorkTimeout) and a delivery, within RunLease.
	advanceTimeout = 90 * time.Second
)

// Tick is one pass of the scheduler: it starts the runs that are due, then advances the runs whose time to be looked at has come. Several
// servers may tick at once: the store's locks and leases give each run to one of them. An error of the store ends the pass; a run's own
// failure is recorded on the run.
func (s *Schedules) Tick(ctx context.Context) (TickResult, error) {
	var res TickResult
	n, e := s.store.StartDueRuns(ctx, tickStartBatch)
	res.Started = n
	if e != nil {
		return res, e
	}
	// One claim at a time, so that each run's lease starts when its advance does.
	for n := 0; n < tickClaimBatch; n++ {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		claims, e := s.store.ClaimRuns(ctx, RunLease, 1)
		if e != nil {
			return res, e
		}
		if len(claims) == 0 {
			break
		}
		c := claims[0]
		run, recheck, delivered, ok := s.advance(ctx, c)
		if !ok {
			// The pass was stopped inside the advance: nothing is recorded, the lease runs out and the run is claimed again.
			continue
		}
		res.Deliveries = append(res.Deliveries, delivered...)
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		e = s.store.UpdateRun(uctx, c, run, recheck)
		cancel()
		switch {
		case errors.Is(e, ErrLease):
			res.Lost++
		case e != nil:
			return res, e
		default:
			res.Advanced++
		}
	}
	return res, nil
}

// advance moves a claimed run on by what can be done now, and says when to look at it again. ok is false when ctx ended inside it.
func (s *Schedules) advance(parent context.Context, c RunClaim) (run ScheduleRun, recheck time.Duration, delivered []DeliveryRecord, ok bool) {
	ctx, cancel := context.WithTimeout(parent, advanceTimeout)
	defer cancel()
	run, recheck = c.Run, RunRecheck
	now := s.now()
	end := func(state, code string) {
		run.State, run.ErrorCode, run.FinishedAt = state, code, now
	}
	if RunOpen(run.State) {
		if !now.Before(run.CreatedAt.Add(RunMaxDuration)) {
			end(RunFailed, RunTimeoutCode)
		} else if done := s.step(ctx, c, &run, &recheck, end); !done {
			return run, 0, nil, false
		}
		if parent.Err() != nil {
			return run, 0, nil, false
		}
		if !RunOpen(run.State) {
			if s.deliver != nil {
				run.Delivery = DeliveryPending
			}
			s.recordEnd(ctx, run)
		}
	}
	if run.Delivery == DeliveryPending && !RunOpen(run.State) {
		n := Notification{Event: NotificationEvent, RunID: run.ID, ScheduleID: run.ScheduleID, Tenant: run.Owner.Tenant, Issuer: run.Owner.Issuer, Subject: run.Owner.Subject,
			Integration: run.Owner.Integration, DueAt: stamp(run.DueAt), FinishedAt: stamp(run.FinishedAt), State: run.State, SnapshotID: run.SnapshotID, ArtifactID: run.ArtifactID,
			Format: run.Format, ErrorCode: run.ErrorCode, Attempt: run.Attempts + 1, SentAt: stamp(now)}
		result := s.deliver.Deliver(ctx, n)
		if parent.Err() != nil {
			return run, 0, nil, false
		}
		run.Attempts++
		switch {
		case result.Outcome == DeliveryOutcomeDelivered:
			run.Delivery = DeliveryDelivered
		case result.Retry() && run.Attempts < MaxDeliveryAttempts:
			recheck = min(DeliveryBackoff<<(run.Attempts-1), DeliveryMaxBackoff)
		default:
			run.Delivery = DeliveryFailed
		}
		delivered = append(delivered, DeliveryRecord{RunID: run.ID, ScheduleID: run.ScheduleID, Attempt: run.Attempts, Outcome: result.Outcome, Status: result.Status, Delivery: run.Delivery})
	}
	return run, recheck, delivered, true
}

// step does the one thing an open run waits for: its start (the snapshot's submission), the end of its snapshot job (and the export's
// submission), or the end of its export job. done is false when ctx ended before an answer.
func (s *Schedules) step(ctx context.Context, c RunClaim, run *ScheduleRun, recheck *time.Duration, end func(state, code string)) (done bool) {
	// refusal ends the run by an error of a submission: the owner's or the source's refusal, a quota, or a code; an outage of the source
	// product before the run started is tried again, within RunMaxWait of the occurrence.
	refusal := func(e error, starting bool) bool {
		switch {
		case ctx.Err() != nil:
			return false
		case errors.Is(e, ErrDenied) || errors.Is(e, ErrRoleDenied):
			end(RunRefused, codeOfRefusal(e))
		case errors.Is(e, ErrUnavailable) && starting && s.now().Before(run.CreatedAt.Add(RunMaxWait)):
			*recheck = RunRetry
		case errors.Is(e, ErrQuota):
			end(RunFailed, "QUOTA_EXCEEDED")
		default:
			end(RunFailed, errorCode(e))
		}
		return true
	}
	principal := func() (Principal, bool) {
		p, bound := s.resolve(run.Owner.Identity)
		switch {
		case !bound || p.Identity != run.Owner.Identity:
			end(RunRefused, ErrDenied.Error())
			return Principal{}, false
		case !p.Has(RoleViewer):
			// The role a person needs to submit the same jobs (POST /snapshot-jobs, POST /exports: a viewer's, S12-A).
			end(RunRefused, ErrRoleDenied.Error())
			return Principal{}, false
		}
		return p, true
	}
	switch run.State {
	case RunDue:
		if c.ScheduleState != ScheduleActive {
			end(RunCancelled, "SCHEDULE_"+map[string]string{SchedulePaused: "PAUSED", ScheduleDeleted: "DELETED"}[c.ScheduleState])
			return true
		}
		p, ok := principal()
		if !ok {
			return true
		}
		v, e := s.svc.SubmitSnapshot(ctx, p, run.Query, "schedule:"+run.ID)
		if e != nil {
			return refusal(e, true)
		}
		run.SnapshotJob, run.State = v.ID, RunSnapshot
		return true
	case RunSnapshot, RunExport:
		id := run.SnapshotJob
		if run.State == RunExport {
			id = run.ExportJob
		}
		j, e := s.svc.repo.Job(ctx, run.Owner.Identity, id)
		if e != nil {
			return refusal(e, false)
		}
		switch j.State {
		case "queued", "running":
			return true
		case "failed":
			end(RunFailed, j.ErrorCode)
			return true
		case "cancelled":
			end(RunCancelled, JobCancelledCode)
			return true
		}
		// The job succeeded: its result is read through the service as the owner, so that the source product's authority over it is asked
		// now, as it is when the owner polls the job.
		p, ok := principal()
		if !ok {
			return true
		}
		v, e := s.svc.Job(ctx, p, id)
		if e != nil {
			return refusal(e, false)
		}
		if run.State == RunExport {
			run.ArtifactID = v.ArtifactID
			end(RunSucceeded, "")
			return true
		}
		run.SnapshotID = v.SnapshotID
		if run.Format == "" {
			end(RunSucceeded, "")
			return true
		}
		x, e := s.svc.SubmitExportFormat(ctx, p, v.SnapshotID, run.Format, "schedule:"+run.ID)
		if e != nil {
			return refusal(e, false)
		}
		run.ExportJob, run.State = x.ID, RunExport
		return true
	}
	return true
}

// codeOfRefusal is the code of a refused run: the role's or the scope's.
func codeOfRefusal(e error) string {
	if errors.Is(e, ErrRoleDenied) {
		return ErrRoleDenied.Error()
	}
	return ErrDenied.Error()
}

// recordEnd records the run's end as its owner's schedule.run event, when the scheduler has an audit; a failure to record does not hold the
// run (a schedule's run is not one of the actions that must not run unrecorded: it has run).
func (s *Schedules) recordEnd(ctx context.Context, run ScheduleRun) {
	if s.audit == nil {
		return
	}
	outcome := map[string]string{RunSucceeded: "succeeded", RunFailed: "failed", RunRefused: "refused", RunCancelled: "refused"}[run.State]
	code := run.ErrorCode
	if len(code) > 64 {
		code = code[:64]
	}
	_ = s.audit.Record(context.WithoutCancel(ctx), AuditEvent{ID: NewID(), Identity: run.Owner.Identity, Action: "schedule.run", ObjectKind: "schedule", ObjectID: run.ScheduleID, Outcome: outcome, Code: code})
}
