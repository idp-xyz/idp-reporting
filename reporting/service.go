package reporting

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Service struct {
	repo        Repository
	connections map[[2]string]Connection
	key         []byte
	writers     map[string]WorkbookWriter // by format; "" is the workbook
	issuer      Issuer                    // nil: issuing is unsupported
	now         func() time.Time
}

func NewService(c Config) (*Service, error) {
	if c.Repository == nil || len(c.CursorKey) < 32 || len(c.Connections) == 0 {
		return nil, ErrInvalid
	}
	s := &Service{repo: c.Repository, connections: map[[2]string]Connection{}, key: slices.Clone(c.CursorKey), writers: map[string]WorkbookWriter{}, issuer: c.Issuer, now: c.Now}
	if s.now == nil {
		s.now = time.Now
	}
	if c.Writer != nil {
		s.writers[""] = c.Writer
	}
	for name, w := range c.Formats {
		if name == "" || !validFormat(name) || w == nil {
			return nil, ErrInvalid
		}
		s.writers[name] = w
	}
	for _, v := range c.Connections {
		k := [2]string{v.Tenant, v.Integration}
		if v.Tenant == "" || v.Integration == "" || v.SourceTenant == "" || v.Adapter == nil {
			return nil, ErrInvalid
		}
		if _, exists := s.connections[k]; exists {
			return nil, ErrInvalid
		}
		s.connections[k] = v
	}
	return s, nil
}
func (s *Service) source(p Principal, id string) (Connection, SourceContext, error) {
	if !p.Identity.Valid() || !slices.Contains(p.Integrations, id) {
		return Connection{}, SourceContext{}, ErrDenied
	}
	c, ok := s.connections[[2]string{p.Tenant, id}]
	if !ok {
		return c, SourceContext{}, ErrDenied
	}
	return c, SourceContext{Owner: Owner{Identity: p.Identity, Integration: id}, SourceTenant: c.SourceTenant}, nil
}
func (s *Service) sourceOwner(o Owner) (Connection, SourceContext, error) {
	c, ok := s.connections[[2]string{o.Tenant, o.Integration}]
	if !ok {
		return c, SourceContext{}, ErrDenied
	}
	return c, SourceContext{Owner: o, SourceTenant: c.SourceTenant}, nil
}
func (s *Service) Catalog(ctx context.Context, p Principal, integration string) ([]Dataset, error) {
	c, sc, e := s.source(p, integration)
	if e != nil {
		return nil, e
	}
	return c.Adapter.DescribeCatalog(ctx, sc)
}
func (s *Service) prepare(ctx context.Context, p Principal, q QuerySpec) (QuerySpec, Dataset, PreparedRead, error) {
	q = Normalize(q)
	c, sc, e := s.source(p, q.Integration)
	if e != nil {
		return q, Dataset{}, PreparedRead{}, e
	}
	cat, e := c.Adapter.DescribeCatalog(ctx, sc)
	if e != nil {
		return q, Dataset{}, PreparedRead{}, e
	}
	var d Dataset
	found := false
	for _, v := range cat {
		if v.Ref == q.Dataset {
			if found {
				return q, d, PreparedRead{}, ErrIntegrity
			}
			d = v
			found = true
		}
	}
	if !found {
		return q, d, PreparedRead{}, ErrUnsupported
	}
	if e = ValidateQuery(q, d); e != nil {
		return q, d, PreparedRead{}, e
	}
	prepared, e := c.Adapter.PrepareRead(ctx, sc, q, QueryHash(q))
	if e != nil {
		return q, d, prepared, e
	}
	if e = s.validPrepared(sc, q, prepared); e != nil {
		return q, d, prepared, e
	}
	if e = c.Adapter.AuthorizeCurrent(ctx, sc, prepared); e != nil {
		return q, d, prepared, e
	}
	return q, d, prepared, nil
}
func (s *Service) validPrepared(sc SourceContext, q QuerySpec, p PreparedRead) error {
	if p.Context != sc || p.Ref == "" || p.ScopeHandle == "" || p.Watermark == "" || p.QueryHash != QueryHash(q) || p.Dataset != q.Dataset || !slices.Equal(p.Companies, q.Companies) || p.Timezone != q.Timezone || !reflect.DeepEqual(p.CurrencyPolicy, q.CurrencyPolicy) {
		return ErrIntegrity
	}
	if !s.now().Before(p.ExpiresAt) {
		return ErrDenied
	}
	if p.ExpiresAt.After(s.now().Add(15 * time.Minute)) {
		return ErrIntegrity
	}
	return nil
}
func (s *Service) current(ctx context.Context, o Owner, q QuerySpec, p PreparedRead) error {
	c, sc, e := s.sourceOwner(o)
	if e != nil {
		return e
	}
	if e = s.validPrepared(sc, q, p); e != nil {
		return e
	}
	return c.Adapter.AuthorizeCurrent(ctx, sc, p)
}
func (s *Service) collect(ctx context.Context, o Owner, q QuerySpec, p PreparedRead, cols []Column) (Result, error) {
	var out Result
	if e := s.current(ctx, o, q, p); e != nil {
		return out, e
	}
	c, sc, _ := s.sourceOwner(o)
	out = Result{Columns: cols, Rows: [][]string{}, Completeness: "complete", Issues: []string{}, Evidence: Evidence{QueryHash: p.QueryHash, SourceRef: p.Ref, Watermark: p.Watermark, DataClass: "contract-simulator"}}
	cursor := ""
	seen := map[string]bool{}
	for n := 0; n <= MaxRows; n++ {
		if e := ctx.Err(); e != nil {
			return Result{}, e
		}
		page, e := c.Adapter.ReadPage(ctx, sc, p, cursor, SourcePageSize)
		if e != nil {
			return Result{}, e
		}
		if page.Ref != p.Ref || page.QueryHash != p.QueryHash || page.Watermark != p.Watermark || len(page.Rows) > SourcePageSize {
			return Result{}, ErrIntegrity
		}
		if e = validateRows(page.Rows, cols); e != nil {
			return Result{}, e
		}
		if e = validateTotals(page.Totals, q, cols); e != nil {
			return Result{}, e
		}
		if n == 0 {
			out.Totals = page.Totals
		} else if !reflect.DeepEqual(out.Totals, page.Totals) {
			return Result{}, ErrIntegrity
		}
		out.Rows = append(out.Rows, page.Rows...)
		if len(out.Rows) > MaxRows {
			return Result{}, ErrBudget
		}
		b, e := json.Marshal(out)
		if e != nil || len(b) > MaxResultBytes {
			return Result{}, ErrBudget
		}
		if page.NextCursor == "" {
			if e = s.current(ctx, o, q, p); e != nil {
				return Result{}, e
			}
			return out, nil
		}
		if len(page.Rows) == 0 || len(page.NextCursor) > 2048 || seen[page.NextCursor] {
			return Result{}, ErrIntegrity
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return Result{}, ErrBudget
}
func (s *Service) Query(ctx context.Context, p Principal, q QuerySpec) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, WorkTimeout)
	defer cancel()
	q, d, prepared, e := s.prepare(ctx, p, q)
	if e != nil {
		return Result{}, e
	}
	result, e := s.collect(ctx, prepared.Context.Owner, q, prepared, d.Columns)
	if e != nil {
		return Result{}, e
	}
	if len(result.Rows) > q.Page.Limit {
		result.Rows = result.Rows[:q.Page.Limit]
		result.Completeness = "preview"
	}
	return result, nil
}
func (s *Service) SubmitSnapshot(ctx context.Context, p Principal, q QuerySpec, key string) (JobView, error) {
	if !validKey(key) {
		return JobView{}, ErrInvalid
	}
	q, d, prepared, e := s.prepare(ctx, p, q)
	if e != nil {
		return JobView{}, e
	}
	j, e := s.repo.Admit(ctx, Job{ID: NewID(), Owner: prepared.Context.Owner, Kind: "snapshot", State: "queued", Key: key, RequestHash: Digest(q), Query: q, Prepared: prepared, Columns: d.Columns})
	if e != nil {
		return JobView{}, e
	}
	if e = s.current(ctx, j.Owner, j.Query, j.Prepared); e != nil {
		return JobView{}, e
	}
	return view(j), nil
}
func (s *Service) snapshot(ctx context.Context, p Principal, id string) (Snapshot, error) {
	if !p.Identity.Valid() || !validID(id) {
		return Snapshot{}, ErrNotFound
	}
	v, e := s.repo.Snapshot(ctx, p.Identity, id)
	if e != nil {
		return v, e
	}
	if _, _, e = s.source(p, v.Owner.Integration); e != nil {
		return Snapshot{}, e
	}
	if !s.now().Before(v.ExpiresAt) || v.SHA256 != Digest(v.Result) {
		return Snapshot{}, ErrIntegrity
	}
	if e = s.current(ctx, v.Owner, v.Query, v.Prepared); e != nil {
		return Snapshot{}, e
	}
	return v, nil
}
func (s *Service) Rows(ctx context.Context, p Principal, id string, limit int, cursor string) (RowsPage, error) {
	if limit < 1 || limit > 100 {
		return RowsPage{}, ErrInvalid
	}
	snap, e := s.snapshot(ctx, p, id)
	if e != nil {
		return RowsPage{}, e
	}
	start := 0
	if cursor != "" {
		start, e = s.decodeCursor(cursor, snap)
		if e != nil {
			return RowsPage{}, e
		}
	}
	if start > len(snap.Result.Rows) {
		return RowsPage{}, ErrInvalid
	}
	end := min(start+limit, len(snap.Result.Rows))
	out := RowsPage{SnapshotID: id, TotalRows: len(snap.Result.Rows), Result: snap.Result, ExpiresAt: snap.ExpiresAt}
	out.Rows = snap.Result.Rows[start:end]
	if start > 0 || end < len(snap.Result.Rows) {
		out.Completeness = "page"
	}
	if end < len(snap.Result.Rows) {
		out.NextCursor = s.encodeCursor(snap, end)
		out.Completeness = "page"
	}
	return out, nil
}

// Formats lists the configured export formats, the workbook first as "xlsx".
func (s *Service) Formats() []string {
	out := []string{} // never nil: clients expect a list
	for name := range s.writers {
		if name != "" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	if s.writers[""] != nil {
		out = append([]string{"xlsx"}, out...)
	}
	return out
}

// SubmitExport exports a snapshot as the workbook.
func (s *Service) SubmitExport(ctx context.Context, p Principal, id, key string) (JobView, error) {
	return s.SubmitExportFormat(ctx, p, id, "", key)
}

// SubmitExportFormat exports a snapshot in a configured format; "" and "xlsx" mean the workbook.
func (s *Service) SubmitExportFormat(ctx context.Context, p Principal, id, format, key string) (JobView, error) {
	if format == "xlsx" {
		format = ""
	}
	if s.writers[format] == nil {
		return JobView{}, ErrUnsupported
	}
	if !validKey(key) {
		return JobView{}, ErrInvalid
	}
	snap, e := s.snapshot(ctx, p, id)
	if e != nil {
		return JobView{}, e
	}
	j, e := s.repo.Admit(ctx, Job{ID: NewID(), Owner: snap.Owner, Kind: "export", State: "queued", Key: key, RequestHash: ExportRequestHash(id, format), Query: snap.Query, Prepared: snap.Prepared, SnapshotID: id, Format: format})
	if e != nil {
		return JobView{}, e
	}
	return view(j), nil
}
func view(j Job) JobView {
	v := JobView{ID: j.ID, Kind: j.Kind, State: j.State, SnapshotID: j.SnapshotID, Format: j.Format, ArtifactID: j.ArtifactID, ErrorCode: j.ErrorCode}
	if j.Kind == "issue" {
		// The number is the source product's and is known once the job succeeded: Job reads it
		// from the record. An issue job itself carries none.
		v.Issue = &IssueSummary{TemplateID: j.TemplateID, TemplateVersion: j.TemplateVersion, TemplateSHA256: j.TemplateSHA256, RowIndex: j.RowIndex}
	}
	return v
}

// CanIssue reports whether the service renders issue jobs. Whether an integration can issue
// depends on its adapter, which must implement DocumentAuthority: SubmitIssue says so.
func (s *Service) CanIssue() bool { return s.issuer != nil }

// IssueInput is what the caller freezes when it asks for an official issue: the published
// version's canonical configuration as the template store holds it.
type IssueInput struct {
	TemplateID      string
	TemplateVersion int
	Template        []byte
	SnapshotID      string
	RowIndex        int
}

// SubmitIssue admits an issue job for the caller's current, unexpired snapshot. The caller
// (the HTTP layer, which owns the template store) has checked that the version is the
// published one and that the principal may use the template; the job freezes the
// configuration so that what was checked is what gets issued. The job has no number: the
// source product gives it when the worker asks it to prepare the document, so the snapshot's
// integration must have an adapter that implements DocumentAuthority (else ErrUnsupported).
// Admission reads no source data.
func (s *Service) SubmitIssue(ctx context.Context, p Principal, in IssueInput, key string) (JobView, error) {
	if s.issuer == nil || s.writers["pdf"] == nil {
		return JobView{}, ErrUnsupported
	}
	if !validKey(key) || !validID(in.TemplateID) || in.TemplateVersion < 1 || len(in.Template) < 2 || len(in.Template) > MaxTemplateBytes || !json.Valid(in.Template) || in.RowIndex < 0 {
		return JobView{}, ErrInvalid
	}
	snap, e := s.snapshot(ctx, p, in.SnapshotID)
	if e != nil {
		return JobView{}, e
	}
	c, _, e := s.source(p, snap.Owner.Integration)
	if e != nil {
		return JobView{}, e
	}
	if _, ok := c.Adapter.(DocumentAuthority); !ok {
		return JobView{}, ErrUnsupported
	}
	if in.RowIndex >= len(snap.Result.Rows) {
		return JobView{}, ErrInvalid
	}
	sum := bytesDigest(in.Template)
	j, e := s.repo.Admit(ctx, Job{ID: NewID(), Owner: snap.Owner, Kind: "issue", State: "queued", Key: key, RequestHash: IssueRequestHash(in.TemplateID, in.TemplateVersion, sum, in.SnapshotID, in.RowIndex), Query: snap.Query, Prepared: snap.Prepared, SnapshotID: in.SnapshotID, Format: "pdf",
		TemplateID: in.TemplateID, TemplateVersion: in.TemplateVersion, TemplateSHA256: sum, Template: string(in.Template), RowIndex: in.RowIndex})
	if e != nil {
		return JobView{}, e
	}
	return view(j), nil
}

// Issues lists the caller's issue jobs under the integrations the caller holds, newest first,
// with the record of each that succeeded. The listing fails closed: one record that cannot
// be read as published (tampered, missing) is an error for the whole page.
func (s *Service) Issues(ctx context.Context, p Principal, limit int) ([]IssueView, error) {
	if !p.Identity.Valid() {
		return nil, ErrNotFound
	}
	if limit < 1 || limit > MaxIssueListing {
		return nil, ErrInvalid
	}
	integrations := []string{}
	for _, id := range p.Integrations {
		if _, _, e := s.source(p, id); e == nil {
			integrations = append(integrations, id)
		}
	}
	out := []IssueView{}
	if len(integrations) == 0 {
		return out, nil
	}
	jobs, e := s.repo.Issues(ctx, p.Identity, integrations, limit)
	if e != nil {
		return nil, e
	}
	for _, j := range jobs {
		if _, _, e := s.source(p, j.Owner.Integration); e != nil {
			return nil, ErrIntegrity
		}
		v, e := s.issueView(ctx, p, j)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}

// Issue reads one issue job with its record.
func (s *Service) Issue(ctx context.Context, p Principal, id string) (IssueView, error) {
	if !p.Identity.Valid() || !validID(id) {
		return IssueView{}, ErrNotFound
	}
	j, e := s.repo.Job(ctx, p.Identity, id)
	if e != nil {
		return IssueView{}, e
	}
	if j.Kind != "issue" {
		return IssueView{}, ErrNotFound
	}
	if _, _, e = s.source(p, j.Owner.Integration); e != nil {
		return IssueView{}, e
	}
	return s.issueView(ctx, p, j)
}
func (s *Service) issueView(ctx context.Context, p Principal, j Job) (IssueView, error) {
	v := IssueView{ID: j.ID, State: j.State, ErrorCode: j.ErrorCode, SnapshotID: j.SnapshotID, IssueSummary: IssueSummary{TemplateID: j.TemplateID, TemplateVersion: j.TemplateVersion, TemplateSHA256: j.TemplateSHA256, RowIndex: j.RowIndex}}
	if j.State != "succeeded" {
		return v, nil
	}
	rec, e := s.repo.Issue(ctx, p.Identity, j.ID)
	if errors.Is(e, ErrNotFound) {
		return IssueView{}, fmt.Errorf("the record of a succeeded issue job is missing: %w", ErrIntegrity)
	}
	if e != nil {
		return IssueView{}, e
	}
	if rec.Owner != j.Owner || rec.ArtifactID != j.ArtifactID || rec.TemplateID != j.TemplateID || rec.TemplateVersion != j.TemplateVersion || rec.TemplateSHA256 != j.TemplateSHA256 || rec.SnapshotID != j.SnapshotID || rec.RowIndex != j.RowIndex || rec.Stamp != IssueStamp(rec.Number, rec.IssuedAt, rec.ID) {
		return IssueView{}, ErrIntegrity
	}
	v.Number = rec.Number
	v.ArtifactID, v.SnapshotSHA256, v.SHA256, v.Pages, v.Stamp, v.IssuedAt = rec.ArtifactID, rec.SnapshotSHA256, rec.SHA256, rec.Pages, rec.Stamp, rec.IssuedAt.UTC().Format(time.RFC3339)
	v.ReservationRef, v.ConfirmationRef, v.ConfirmedAt = rec.ReservationRef, rec.ConfirmationRef, rec.ConfirmedAt.UTC().Format(time.RFC3339)
	return v, nil
}
func (s *Service) Job(ctx context.Context, p Principal, id string) (JobView, error) {
	if !p.Identity.Valid() || !validID(id) {
		return JobView{}, ErrNotFound
	}
	j, e := s.repo.Job(ctx, p.Identity, id)
	if e != nil {
		return JobView{}, e
	}
	if _, _, e = s.source(p, j.Owner.Integration); e != nil {
		return JobView{}, e
	}
	if e = s.current(ctx, j.Owner, j.Query, j.Prepared); e != nil {
		return JobView{}, e
	}
	out := view(j)
	if j.State == "succeeded" {
		if j.Kind == "snapshot" {
			_, e = s.snapshot(ctx, p, j.SnapshotID)
		} else {
			var a Artifact
			a, e = s.Download(ctx, p, j.ArtifactID)
			if e == nil && j.Kind == "issue" {
				// The artifact must claim the record and the record must name the artifact; the
				// number the job view shows is the record's.
				var v IssueView
				if v, e = s.issueView(ctx, p, j); e == nil && (a.Issue != j.ID || v.ArtifactID != a.ID || v.SHA256 != a.SHA256) {
					e = ErrIntegrity
				}
				if e == nil {
					out.Issue.Number = v.Number
				}
			}
		}
		if e != nil {
			return JobView{}, e
		}
	}
	return out, nil
}
func (s *Service) Cancel(ctx context.Context, p Principal, id string) (JobView, error) {
	if !p.Identity.Valid() || !validID(id) {
		return JobView{}, ErrNotFound
	}
	j, e := s.repo.Job(ctx, p.Identity, id)
	if e != nil {
		return JobView{}, e
	}
	if _, _, e = s.source(p, j.Owner.Integration); e != nil {
		return JobView{}, e
	}
	j, e = s.repo.Cancel(ctx, p.Identity, id)
	if e != nil {
		return JobView{}, e
	}
	// Cleanup remains possible after source revocation; never return output refs.
	v := view(j)
	v.SnapshotID = ""
	v.ArtifactID = ""
	return v, nil
}
func (s *Service) Download(ctx context.Context, p Principal, id string) (Artifact, error) {
	if !p.Identity.Valid() || !validID(id) {
		return Artifact{}, ErrNotFound
	}
	a, e := s.repo.Artifact(ctx, p.Identity, id)
	if e != nil {
		return Artifact{}, e
	}
	if _, _, e = s.source(p, a.Owner.Integration); e != nil {
		return Artifact{}, e
	}
	snap, e := s.snapshot(ctx, p, a.SnapshotID)
	if e != nil {
		return Artifact{}, e
	}
	if snap.Owner != a.Owner || !s.now().Before(a.ExpiresAt) || a.ExpiresAt.After(snap.ExpiresAt) || len(a.Content) == 0 || len(a.Content) > MaxArtifactBytes || a.SHA256 != bytesDigest(a.Content) {
		return Artifact{}, ErrIntegrity
	}
	if a.Issue != "" {
		// An issued PDF is served only as the record it was issued under describes it.
		rec, e := s.repo.Issue(ctx, p.Identity, a.Issue)
		if errors.Is(e, ErrNotFound) {
			return Artifact{}, fmt.Errorf("the record an artifact names is missing: %w", ErrIntegrity)
		}
		if e != nil {
			return Artifact{}, e
		}
		if rec.Owner != a.Owner || rec.ArtifactID != a.ID || rec.SHA256 != a.SHA256 || rec.SnapshotID != a.SnapshotID {
			return Artifact{}, ErrIntegrity
		}
	}
	return a, nil
}
func bytesDigest(b []byte) string { v := sha256.Sum256(b); return fmt.Sprintf("%x", v[:]) }

// errorCode is the code a job that failed with e carries.
func errorCode(e error) string {
	for _, known := range []error{ErrDenied, ErrUnavailable, ErrInvalid, ErrIntegrity, ErrBudget, ErrLease, ErrUnsupported, ErrNotFound, ErrUnpublished, ErrUnreconciled} {
		if errors.Is(e, known) {
			return known.Error()
		}
	}
	return "EXECUTION_FAILED"
}

// issueAttempt records what one execution of an issue job did at the source product: once
// PrepareDocument was called the product may hold a reservation for the job, whatever the call
// returned (an answer can be lost on its way back).
type issueAttempt struct{ prepared bool }

// leaseCheckTimeout bounds the read with which RunOne confirms a lost lease; with it a worker is
// still done within its lease (WorkTimeout + leaseCheckTimeout + VoidTimeout + 2 s < LeaseDuration).
const leaseCheckTimeout = time.Second

// fromProduct: the platform's lease error is its own, and a source product cannot speak it. An
// adapter that answers with it (a product that mistook it for a retry hint, or a wrapper that
// passes a repository's error on) has given an answer the platform cannot trust: INTEGRITY_FAILURE,
// so that the worker does not take the answer for the loss of its lease and keeps the void.
func fromProduct(e error) error {
	if errors.Is(e, ErrLease) {
		return fmt.Errorf("%w: the source product answered with the platform's lease error: %v", ErrIntegrity, e)
	}
	return e
}

// RunOne claims one job and executes it. A job that fails is failed with the code of its error.
// An issue job (S03-H) that fails other than by losing its lease is first voided at the source
// product when this attempt called PrepareDocument, or when the claim is of a later generation
// than the first (an earlier attempt may have reserved or confirmed): the product is told to void
// whatever it holds for the job id, with the code as the reason. A void that cannot be delivered
// fails the job with ISSUE_UNRECONCILED instead of its own code, and the product can find the
// reservation by the job id. A first attempt that fails before it reached the product calls
// nothing there. Losing the lease never voids: the worker that holds the job reuses the same
// reservation, which the product answers again by the job id. An answer of ErrLease counts as the
// loss of the lease only when the store confirms it (holdsJob says otherwise, or cannot say): while
// the store still has the claim running with a lease that has not run out, the answer is
// INTEGRITY_FAILURE and the job is voided and failed like any other.
func (s *Service) RunOne(ctx context.Context) (bool, error) {
	claim, e := s.repo.Claim(ctx, LeaseDuration)
	if e != nil || claim == nil {
		return false, e
	}
	ctx, cancel := context.WithTimeout(ctx, WorkTimeout)
	defer cancel()
	var attempt issueAttempt
	e = s.execute(ctx, *claim, &attempt)
	if e != nil {
		// ErrLease is believed only when the store confirms it: an answer of lost lease while the
		// store still has this claim running with a lease that has not run out is not a lost lease,
		// and the job fails (and voids) like any other.
		lost := errors.Is(e, ErrLease)
		if lost && s.holdsJob(ctx, claim.Job) {
			lost = false
			e = fmt.Errorf("%w: answered %w although the store still holds the job for this claim", ErrIntegrity, e)
		}
		code := errorCode(e)
		if j := claim.Job; j.Kind == "issue" && !lost && (attempt.prepared || j.Generation > 1) {
			holds, voidErr := s.voidIssue(ctx, j, code)
			if !holds {
				return true, e
			}
			if voidErr != nil {
				code = ErrUnreconciled.Error()
				e = fmt.Errorf("%w: the source product could not void the job: %w; the job's own failure: %w", ErrUnreconciled, voidErr, e)
			}
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		_ = s.repo.Fail(cleanup, *claim, code)
	}
	return true, e
}

// holdsJob: the store says that this claim still holds the job, running at the claim's generation
// with a lease that has not run out by the service's clock. It reads in a context of its own
// bounded by leaseCheckTimeout (the job's may have run out); a store that cannot be read, or
// that does not say so, does not confirm it.
func (s *Service) holdsJob(ctx context.Context, j Job) bool {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), leaseCheckTimeout)
	defer stop()
	now, e := s.repo.Job(ctx, j.Owner.Identity, j.ID)
	return e == nil && now.State == "running" && now.Generation == j.Generation && now.LeaseUntil.After(s.now())
}

// voidIssue voids at the source product whatever it holds for a failed issue job, in a context of
// its own (the job's may have run out) bounded by VoidTimeout. An adapter without the capability
// was never asked and holds nothing. holds is false, and nothing is voided, when the store does
// not say that this attempt still holds the job: the publication may have committed although it
// reported an error, or another worker may have claimed the job and reuse its reservation; the
// job is then not failed either (a store that cannot be read leaves the job to its lease).
func (s *Service) voidIssue(ctx context.Context, j Job, reason string) (holds bool, err error) {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), VoidTimeout)
	defer stop()
	if now, e := s.repo.Job(ctx, j.Owner.Identity, j.ID); e != nil || now.State != "running" || now.Generation != j.Generation {
		return false, nil
	}
	c, sc, e := s.sourceOwner(j.Owner)
	if e != nil {
		return true, e
	}
	authority, ok := c.Adapter.(DocumentAuthority)
	if !ok {
		return true, nil
	}
	return true, authority.VoidIssue(ctx, sc, j.ID, reason)
}

// validReservation: what the worker requires of the product's answer to PrepareDocument.
func validReservation(j Job, res IssueReservation) bool {
	return validRef(res.Ref) && res.JobID == j.ID && ValidIssueNumber(res.Number) && wholeSecond(res.IssuedAt) && res.ExpiresAt.After(res.IssuedAt)
}

// validReceipt: what the worker requires of the product's answer to ConfirmIssue.
func validReceipt(res IssueReservation, receipt IssueReceipt) bool {
	return validRef(receipt.Ref) && wholeSecond(receipt.ConfirmedAt) && !receipt.ConfirmedAt.Before(res.IssuedAt)
}

func (s *Service) execute(ctx context.Context, claim Claim, attempt *issueAttempt) error {
	j := claim.Job
	var pub Publication
	if e := s.current(ctx, j.Owner, j.Query, j.Prepared); e != nil {
		return e
	}
	switch j.Kind {
	case "snapshot":
		result, e := s.collect(ctx, j.Owner, j.Query, j.Prepared, j.Columns)
		if e != nil {
			return e
		}
		pub.Snapshot = &Snapshot{ID: NewID(), Owner: j.Owner, Query: j.Query, Prepared: j.Prepared, Result: result, ExpiresAt: j.Prepared.ExpiresAt, SHA256: Digest(result)}
	case "export":
		w := s.writers[j.Format]
		if w == nil {
			return ErrUnsupported
		}
		p := Principal{Identity: j.Owner.Identity, Integrations: []string{j.Owner.Integration}}
		snap, e := s.snapshot(ctx, p, j.SnapshotID)
		if e != nil {
			return e
		}
		b, e := w.Write(ctx, snap)
		if e != nil {
			return e
		}
		if len(b) == 0 || len(b) > MaxArtifactBytes {
			return ErrBudget
		}
		pub.Artifact = &Artifact{ID: NewID(), Owner: j.Owner, SnapshotID: snap.ID, Format: j.Format, ExpiresAt: snap.ExpiresAt, Content: b, SHA256: bytesDigest(b)}
	case "issue":
		// Under the source product's authority (S03-H): the issuer checks what it can without
		// the product, the product authorizes the issue and gives the number and the issue time,
		// the platform renders from what is frozen and what the product reserved, the product
		// confirms the file, and only then are the artifact and the record published. Every call
		// to the product is idempotent by the job id, so a job claimed again reuses its
		// reservation and, rendering the same bytes, its confirmation.
		c, sc, e := s.sourceOwner(j.Owner)
		if e != nil {
			return e
		}
		authority, ok := c.Adapter.(DocumentAuthority)
		if s.issuer == nil || !ok {
			return ErrUnsupported
		}
		p := Principal{Identity: j.Owner.Identity, Integrations: []string{j.Owner.Integration}}
		snap, e := s.snapshot(ctx, p, j.SnapshotID)
		if e != nil {
			return e
		}
		if j.RowIndex >= len(snap.Result.Rows) {
			return ErrIntegrity
		}
		// The product is asked only after everything that can be checked without it has passed
		// (the template is still the published version, the row and the table bind, the
		// background resolves, the layout accepts the values): a product must account for every
		// number it voids, so none of these may cost it a reservation. Issue repeats the checks
		// that can change meanwhile.
		if e := s.issuer.Check(ctx, j, snap); e != nil {
			return e
		}
		attempt.prepared = true
		res, e := authority.PrepareDocument(ctx, sc, IssueIntent{JobID: j.ID, Prepared: snap.Prepared, RowIndex: j.RowIndex, RowSHA256: Digest(snap.Result.Rows[j.RowIndex]), TemplateID: j.TemplateID, TemplateVersion: j.TemplateVersion, TemplateSHA256: j.TemplateSHA256})
		if e != nil {
			return fromProduct(e)
		}
		// The times are judged by value; what the product returns may carry any location (a time
		// read back from its database), while the renderer takes only time.UTC.
		if !validReservation(j, res) {
			return ErrIntegrity
		}
		issuedAt := res.IssuedAt.UTC()
		stamp := IssueStamp(res.Number, issuedAt, j.ID)
		result, e := s.issuer.Issue(ctx, IssueRequest{Job: j, Snapshot: snap, Stamp: stamp, At: issuedAt})
		if e != nil {
			return e
		}
		b := result.PDF
		if len(b) == 0 || len(b) > MaxArtifactBytes {
			return ErrBudget
		}
		if result.Pages < 1 || result.Pages > MaxIssuePages || !bytes.HasPrefix(b, []byte("%PDF-")) {
			return ErrIntegrity
		}
		sum := bytesDigest(b)
		receipt, e := authority.ConfirmIssue(ctx, sc, IssueConfirmation{JobID: j.ID, ReservationRef: res.Ref, SHA256: sum, Pages: result.Pages})
		if e != nil {
			return fromProduct(e)
		}
		if !validReceipt(res, receipt) {
			return ErrIntegrity
		}
		id := NewID()
		pub.Artifact = &Artifact{ID: id, Owner: j.Owner, SnapshotID: snap.ID, Format: "pdf", ExpiresAt: snap.ExpiresAt, Content: b, SHA256: sum, Issue: j.ID}
		pub.Issue = &Issue{ID: j.ID, Owner: j.Owner, Number: res.Number, TemplateID: j.TemplateID, TemplateVersion: j.TemplateVersion, TemplateSHA256: j.TemplateSHA256, SnapshotID: snap.ID, SnapshotSHA256: snap.SHA256, RowIndex: j.RowIndex, ArtifactID: id, SHA256: sum, Pages: result.Pages, Stamp: stamp, IssuedAt: issuedAt,
			ReservationRef: res.Ref, ConfirmationRef: receipt.Ref, ConfirmedAt: receipt.ConfirmedAt.UTC()}
	default:
		return ErrIntegrity
	}
	if e := s.current(ctx, j.Owner, j.Query, j.Prepared); e != nil {
		return e
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	return s.repo.Publish(ctx, claim, pub)
}
func (s *Service) encodeCursor(snap Snapshot, start int) string {
	payload := snap.ID + ":" + Digest(snap.Owner) + ":" + snap.Prepared.QueryHash + ":" + strconv.Itoa(start)
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Service) decodeCursor(token string, snap Snapshot) (int, error) {
	if len(token) > 2048 {
		return 0, ErrInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return 0, ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return 0, ErrInvalid
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return 0, ErrInvalid
	}
	h := hmac.New(sha256.New, s.key)
	h.Write(b)
	if !hmac.Equal(sig, h.Sum(nil)) {
		return 0, ErrInvalid
	}
	fields := strings.Split(string(b), ":")
	if len(fields) != 4 || fields[0] != snap.ID || fields[1] != Digest(snap.Owner) || fields[2] != snap.Prepared.QueryHash {
		return 0, ErrInvalid
	}
	return offset(fields[3])
}

// AuthorizedSnapshot supplies a verified frozen result to trusted in-process renderers.
// Callers must recheck before releasing long-running rendered output. Never expose this object over HTTP.
func (s *Service) AuthorizedSnapshot(ctx context.Context, p Principal, id string) (Snapshot, error) {
	return s.snapshot(ctx, p, id)
}
