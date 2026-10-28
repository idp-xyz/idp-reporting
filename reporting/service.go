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
		v.Issue = &IssueSummary{Number: j.Number, TemplateID: j.TemplateID, TemplateVersion: j.TemplateVersion, TemplateSHA256: j.TemplateSHA256, RowIndex: j.RowIndex}
	}
	return v
}

// CanIssue reports whether the service renders issue jobs.
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
// configuration so that what was checked is what gets issued. The repository assigns the
// number.
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

// Issues lists the caller's issue jobs, newest first, with the record of each that succeeded.
// The records are read as the job views are: the integration must still be the caller's.
func (s *Service) Issues(ctx context.Context, p Principal, limit int) ([]IssueView, error) {
	if !p.Identity.Valid() {
		return nil, ErrNotFound
	}
	if limit < 1 || limit > MaxIssueListing {
		return nil, ErrInvalid
	}
	jobs, e := s.repo.Issues(ctx, p.Identity, limit)
	if e != nil {
		return nil, e
	}
	out := []IssueView{}
	for _, j := range jobs {
		if _, _, e := s.source(p, j.Owner.Integration); e != nil {
			continue
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
	v := IssueView{ID: j.ID, State: j.State, ErrorCode: j.ErrorCode, SnapshotID: j.SnapshotID, IssueSummary: IssueSummary{Number: j.Number, TemplateID: j.TemplateID, TemplateVersion: j.TemplateVersion, TemplateSHA256: j.TemplateSHA256, RowIndex: j.RowIndex}}
	if j.State != "succeeded" {
		return v, nil
	}
	rec, e := s.repo.Issue(ctx, p.Identity, j.ID)
	if e != nil {
		return IssueView{}, e
	}
	if rec.Owner != j.Owner || rec.Number != j.Number || rec.ArtifactID != j.ArtifactID || rec.TemplateID != j.TemplateID || rec.TemplateVersion != j.TemplateVersion || rec.TemplateSHA256 != j.TemplateSHA256 || rec.SnapshotID != j.SnapshotID || rec.RowIndex != j.RowIndex || rec.Stamp != IssueStamp(rec.Number, rec.IssuedAt, rec.ID) {
		return IssueView{}, ErrIntegrity
	}
	v.ArtifactID, v.SnapshotSHA256, v.SHA256, v.Pages, v.Stamp, v.IssuedAt = rec.ArtifactID, rec.SnapshotSHA256, rec.SHA256, rec.Pages, rec.Stamp, rec.IssuedAt.UTC().Format(time.RFC3339)
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
	if j.State == "succeeded" {
		if j.Kind == "snapshot" {
			_, e = s.snapshot(ctx, p, j.SnapshotID)
		} else {
			_, e = s.Download(ctx, p, j.ArtifactID)
		}
		if e != nil {
			return JobView{}, e
		}
	}
	return view(j), nil
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
	return a, nil
}
func bytesDigest(b []byte) string { v := sha256.Sum256(b); return fmt.Sprintf("%x", v[:]) }
func (s *Service) RunOne(ctx context.Context) (bool, error) {
	claim, e := s.repo.Claim(ctx, LeaseDuration)
	if e != nil || claim == nil {
		return false, e
	}
	ctx, cancel := context.WithTimeout(ctx, WorkTimeout)
	defer cancel()
	e = s.execute(ctx, *claim)
	if e != nil {
		code := "EXECUTION_FAILED"
		for _, known := range []error{ErrDenied, ErrUnavailable, ErrInvalid, ErrIntegrity, ErrBudget, ErrLease, ErrUnsupported, ErrNotFound, ErrUnpublished} {
			if errors.Is(e, known) {
				code = known.Error()
				break
			}
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		_ = s.repo.Fail(cleanup, *claim, code)
	}
	return true, e
}
func (s *Service) execute(ctx context.Context, claim Claim) error {
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
		if s.issuer == nil || j.Number < 1 {
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
		at := s.now().UTC().Truncate(time.Second)
		stamp := IssueStamp(j.Number, at, j.ID)
		result, e := s.issuer.Issue(ctx, IssueRequest{Job: j, Snapshot: snap, Stamp: stamp})
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
		id := NewID()
		pub.Artifact = &Artifact{ID: id, Owner: j.Owner, SnapshotID: snap.ID, Format: "pdf", ExpiresAt: snap.ExpiresAt, Content: b, SHA256: bytesDigest(b), Issue: j.ID}
		pub.Issue = &Issue{ID: j.ID, Owner: j.Owner, Number: j.Number, TemplateID: j.TemplateID, TemplateVersion: j.TemplateVersion, TemplateSHA256: j.TemplateSHA256, SnapshotID: snap.ID, SnapshotSHA256: snap.SHA256, RowIndex: j.RowIndex, ArtifactID: id, SHA256: pub.Artifact.SHA256, Pages: result.Pages, Stamp: stamp, IssuedAt: at}
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
