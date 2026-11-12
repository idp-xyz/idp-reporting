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
	classes     map[[2]string]string      // the data class of each connection: its adapter's word, read once (S08-A)
	keys        [][]byte                  // the cursor key ring, newest first (S04-H): the first signs, every one verifies
	writers     map[string]WorkbookWriter // by format; "" is the workbook
	issuer      Issuer                    // nil: issuing is unsupported
	now         func() time.Time
	quotas      Quotas      // resolved: every field in its bounds
	retention   Retention   // resolved: how long a result outlives its read, by class (ADR 0026)
	objects     ObjectStore // nil: artifact content stays inline in the row (S04-E)
	gate        queryGate   // the reads running per identity (S04-G)
}

func NewService(c Config) (*Service, error) {
	ring, e := cursorRing(c)
	if c.Repository == nil || e != nil || len(c.Connections) == 0 {
		return nil, ErrInvalid
	}
	quotas, e := c.Quotas.Resolved()
	if e != nil {
		return nil, e
	}
	retention, e := c.Retention.Resolved()
	if e != nil {
		return nil, e
	}
	s := &Service{quotas: quotas, retention: retention, objects: c.Objects, repo: c.Repository, connections: map[[2]string]Connection{}, classes: map[[2]string]string{}, keys: ring, writers: map[string]WorkbookWriter{}, issuer: c.Issuer, now: c.Now}
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
		// The data class is the adapter's word (S08-A, ADR 0012 decision 6): read once, here, and one of the closed set, or the
		// service is not made. Every result of the connection carries it in its evidence.
		class := DataClassOf(v.Adapter)
		if !ValidDataClass(class) {
			return nil, ErrInvalid
		}
		s.connections[k] = v
		s.classes[k] = class
	}
	return s, nil
}

// Cursor key ring bounds (S04-H).
const (
	minCursorKeyBytes = 32
	// MaxCursorKeys is the largest cursor key ring.
	MaxCursorKeys = 4
)

// cursorRing resolves Config.CursorKey (the ring of one) or Config.CursorKeys into a private copy. Exactly one of the two fields must be
// set; 1 to 4 keys, each at least 32 bytes, pairwise distinct. The error is ErrInvalid with a fixed text: it never carries key material.
func cursorRing(c Config) ([][]byte, error) {
	if (len(c.CursorKey) > 0) == (len(c.CursorKeys) > 0) {
		return nil, ErrInvalid
	}
	src := c.CursorKeys
	if len(c.CursorKey) > 0 {
		src = [][]byte{c.CursorKey}
	}
	if len(src) > MaxCursorKeys {
		return nil, ErrInvalid
	}
	ring := make([][]byte, 0, len(src))
	for _, k := range src {
		if len(k) < minCursorKeyBytes {
			return nil, ErrInvalid
		}
		for _, o := range ring {
			if hmac.Equal(o, k) {
				return nil, ErrInvalid
			}
		}
		ring = append(ring, slices.Clone(k))
	}
	return ring, nil
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

// DataClass is the data class of one of the principal's products (S08-A, ADR 0012 decision 6): the word of the connection's adapter as
// NewService read it, which every result of the connection carries in its evidence. The connection is resolved as Catalog resolves it,
// with the same refusal for a product the principal does not hold; the adapter is not asked again.
func (s *Service) DataClass(p Principal, integration string) (string, error) {
	c, _, e := s.source(p, integration)
	if e != nil {
		return "", e
	}
	return s.classes[[2]string{c.Tenant, c.Integration}], nil
}

// ListCompanies is the company directory of ADR 0015 (S19-B) for one of the principal's products: the connection is resolved as Catalog
// resolves it (the same refusal for a product the principal does not hold); an adapter without the capability answers supported false
// and an empty list; otherwise the adapter's answer is held to ValidateCompanyList (ErrIntegrity: nothing of it is passed on) and
// returned as the directory answers it. Nothing is stored or logged: the list is the source's at this instant, and the source
// authorizes every read as before.
func (s *Service) ListCompanies(ctx context.Context, p Principal, integration string) (CompanyDirectoryAnswer, error) {
	c, sc, e := s.source(p, integration)
	if e != nil {
		return CompanyDirectoryAnswer{}, e
	}
	directory, ok := c.Adapter.(CompanyDirectory)
	if !ok {
		return CompanyDirectoryAnswer{Companies: []Company{}}, nil
	}
	list, e := directory.ListCompanies(ctx, sc)
	if e != nil {
		return CompanyDirectoryAnswer{}, e
	}
	if e = ValidateCompanyList(list); e != nil {
		return CompanyDirectoryAnswer{}, e
	}
	companies := make([]Company, len(list.Companies))
	copy(companies, list.Companies)
	return CompanyDirectoryAnswer{Supported: true, Partial: list.Partial, Companies: companies}, nil
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

// validPrepared holds a prepared read to its query and its context while the read itself lasts: the pages can still be read and a
// product can still check a row against its own frozen rows. The read is the query's and the context's and its lifetimes are well formed
// (ErrIntegrity), it has not expired (ErrDenied), and it expires at most 15 minutes from now (ErrIntegrity).
func (s *Service) validPrepared(sc SourceContext, q QuerySpec, p PreparedRead) error {
	if !boundRead(sc, q, p) {
		return ErrIntegrity
	}
	if !s.now().Before(p.ExpiresAt) {
		return ErrDenied
	}
	if p.ExpiresAt.After(s.now().Add(MaxReadLifetime)) {
		return ErrIntegrity
	}
	return nil
}

// validScope is validPrepared for what needs the source's authorization and not its pages (ADR 0026): the read's scope has not expired.
// Without a declared scope the scope is the read, and the two are the same check.
func (s *Service) validScope(sc SourceContext, q QuerySpec, p PreparedRead) error {
	if !boundRead(sc, q, p) {
		return ErrIntegrity
	}
	if !s.now().Before(p.ScopeEnd()) {
		return ErrDenied
	}
	if p.ExpiresAt.After(s.now().Add(MaxReadLifetime)) {
		return ErrIntegrity
	}
	return nil
}

// boundRead: the read is the query's, in the context it is used in, and the scope it declares is not before its own expiry.
func boundRead(sc SourceContext, q QuerySpec, p PreparedRead) bool {
	return p.Context == sc && p.Ref != "" && p.ScopeHandle != "" && p.Watermark != "" && p.QueryHash == QueryHash(q) && p.Dataset == q.Dataset && slices.Equal(p.Companies, q.Companies) && p.Timezone == q.Timezone && reflect.DeepEqual(p.CurrencyPolicy, q.CurrencyPolicy) && p.ValidLifetimes()
}

// current asks the source whether the owner's current rights still cover the read, while the read itself lasts.
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

// currentScope asks the same of the source while the read's scope lasts (ADR 0026 decision 4): what every disclosure of a kept result
// checks, after the read's pages are gone. A refusal is a refusal and a source that cannot be asked is an error: nothing falls back to the
// owner alone.
func (s *Service) currentScope(ctx context.Context, o Owner, q QuerySpec, p PreparedRead) error {
	c, sc, e := s.sourceOwner(o)
	if e != nil {
		return e
	}
	if e = s.validScope(sc, q, p); e != nil {
		return e
	}
	return c.Adapter.AuthorizeCurrent(ctx, sc, p)
}

// authorizeJob asks the source for a job as its kind and state need (ADR 0026 decision 6): a snapshot or an issue job that is not finished
// needs the read itself; an export, and any finished job, whose result may be kept beyond the read, needs the read's scope.
func (s *Service) authorizeJob(ctx context.Context, j Job) error {
	if j.Kind == "export" || Terminal(j.State) {
		return s.currentScope(ctx, j.Owner, j.Query, j.Prepared)
	}
	return s.current(ctx, j.Owner, j.Query, j.Prepared)
}
func (s *Service) collect(ctx context.Context, o Owner, q QuerySpec, p PreparedRead, cols []Column) (Result, error) {
	var out Result
	if e := s.current(ctx, o, q, p); e != nil {
		return out, e
	}
	c, sc, _ := s.sourceOwner(o)
	// The evidence says where the data comes from in the word of the connection's adapter (S08-A): never a constant of the platform.
	out = Result{Columns: cols, Rows: [][]string{}, Completeness: "complete", Issues: []string{}, Evidence: Evidence{QueryHash: p.QueryHash, SourceRef: p.Ref, Watermark: p.Watermark, DataClass: s.classes[[2]string{o.Tenant, o.Integration}]}}
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
	// A read beyond the identity's queries in flight is refused before anything runs (S04-G); the slot is given back on every way out.
	release, e := s.admitRead(p)
	if e != nil {
		return Result{}, e
	}
	defer release()
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

// SubmitSnapshot admits a person's freeze of a query: a working snapshot, which lives as long as its read (ADR 0026 decision 3).
func (s *Service) SubmitSnapshot(ctx context.Context, p Principal, q QuerySpec, key string) (JobView, error) {
	return s.submitSnapshot(ctx, p, q, key, 0)
}

// SubmitKeptSnapshot admits a freeze whose snapshot is itself a result someone asked to have later, a scheduled run's (ADR 0020): it is
// kept as an export file is (Retention.Results), within the read's scope. With a source that vouches for no scope it is SubmitSnapshot.
// It is the scheduler's; no route of the API reaches it.
func (s *Service) SubmitKeptSnapshot(ctx context.Context, p Principal, q QuerySpec, key string) (JobView, error) {
	return s.submitSnapshot(ctx, p, q, key, s.retention.Results)
}

func (s *Service) submitSnapshot(ctx context.Context, p Principal, q QuerySpec, key string, class time.Duration) (JobView, error) {
	if !validKey(key) {
		return JobView{}, ErrInvalid
	}
	q, d, prepared, e := s.prepare(ctx, p, q)
	if e != nil {
		return JobView{}, e
	}
	j, e := s.repo.Admit(ctx, Job{ID: NewID(), Owner: prepared.Context.Owner, Kind: "snapshot", State: "queued", Key: key, RequestHash: Digest(q), Query: q, Prepared: prepared, Columns: d.Columns, KeepUntil: kept(prepared, class)}, s.quotas)
	if e != nil {
		return JobView{}, e
	}
	if j.Reduced || (j.Prepared.vouched() && s.expired(j)) {
		// The key names a finished job whose result is over: a replay of it is denied, the same before the sweep has cleared its payload
		// (S04-B) and after. Where the read's scope goes on after the read, the result's own time decides (expired): the scope would
		// still answer for a working snapshot that has ended. Without a declared scope the authorization below denies it, as it always did.
		return JobView{}, ErrDenied
	}
	// A replay answers the job the key already names: its own read, asked as that job's state needs (a finished job whose snapshot is kept
	// is asked by its scope, ADR 0026; a queued or running one needs the read itself, and is denied once that is over, as it always was).
	if e = s.authorizeJob(ctx, j); e != nil {
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
	// A stored snapshot keeps the data class it was frozen with (S08-A); a word outside the closed set is a store that does not agree
	// with itself, and nothing of the snapshot is served, exported or issued.
	if !s.now().Before(v.ExpiresAt) || v.SHA256 != Digest(v.Result) || !ValidDataClass(v.Result.Evidence.DataClass) {
		return Snapshot{}, ErrIntegrity
	}
	// The source is asked by the read's scope (ADR 0026 decision 4): for a working snapshot, which ends with its read, that is the check
	// it always was; a kept snapshot is authorized after its read's pages are gone, for as long as it is kept.
	if e = s.currentScope(ctx, v.Owner, v.Query, v.Prepared); e != nil {
		return Snapshot{}, e
	}
	return v, nil
}
func (s *Service) Rows(ctx context.Context, p Principal, id string, limit int, cursor string) (RowsPage, error) {
	if limit < 1 || limit > 100 {
		return RowsPage{}, ErrInvalid
	}
	release, e := s.admitRead(p)
	if e != nil {
		return RowsPage{}, e
	}
	defer release()
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
	return s.SubmitExportWith(ctx, p, id, ExportOptions{Format: format}, key)
}

// ExportOptions is what an export may carry beyond its snapshot (S23-A, ADR 0024): its format ("" and "xlsx" mean the workbook) and, when
// the caller has resolved a layout, the presentation the file is rendered through, naming the layout version it came from.
type ExportOptions struct {
	Format       string
	Presentation *Presentation
}

// SubmitExportWith admits an export of the caller's current, unexpired snapshot with the given options. A presentation must be whole
// (ErrInvalid), name its layout version, be for a format whose writer can present (ErrUnsupported otherwise), and fit the snapshot's
// columns and dimensions (ErrUnsupported: it is refused whole, never applied in part); the job freezes it, and its request hash binds the
// layout version, so that a replay of the key with another version, or with none, is ErrConflict. The caller (the HTTP layer, which owns
// the layout store) has checked that the layout is bound to the snapshot's dataset and readable by the principal. Admission reads no row.
func (s *Service) SubmitExportWith(ctx context.Context, p Principal, id string, o ExportOptions, key string) (JobView, error) {
	format := o.Format
	if format == "xlsx" {
		format = ""
	}
	w := s.writers[format]
	if w == nil {
		return JobView{}, ErrUnsupported
	}
	if !validKey(key) {
		return JobView{}, ErrInvalid
	}
	var presentation *Presentation
	if o.Presentation != nil {
		if e := o.Presentation.Validate(); e != nil {
			return JobView{}, e
		}
		if o.Presentation.LayoutID == "" {
			return JobView{}, fmt.Errorf("a presentation of an export must name its layout version: %w", ErrInvalid)
		}
		if _, ok := w.(PresentedWriter); !ok {
			return JobView{}, fmt.Errorf("the %q writer cannot render a presentation: %w", o.Format, ErrUnsupported)
		}
		frozen := o.Presentation.Clone()
		presentation = &frozen
	}
	snap, e := s.snapshot(ctx, p, id)
	if e != nil {
		return JobView{}, e
	}
	if presentation != nil {
		if e = presentation.Fits(snap.Result.Columns, snap.Query.Dimensions); e != nil {
			return JobView{}, e
		}
	}
	layoutID, layoutVersion := layoutOf(presentation)
	// An export file is a kept result (ADR 0026 decision 3): it lives the results' time after the read it came from, within the read's
	// scope; with a source that vouches for no scope it lives as long as its read, as it always did.
	keep := kept(snap.Prepared, s.retention.Results)
	if !keep.IsZero() && !s.now().Before(keep) {
		// The file would be over before it is made: its snapshot was kept under a longer results' time than the one in force now.
		// Refused here, rather than rendered and stored and then refused at its publication.
		return JobView{}, ErrDenied
	}
	j, e := s.repo.Admit(ctx, Job{ID: NewID(), Owner: snap.Owner, Kind: "export", State: "queued", Key: key, RequestHash: ExportRequestHashWithLayout(id, format, layoutID, layoutVersion), Query: snap.Query, Prepared: snap.Prepared, SnapshotID: id, Format: format, Presentation: presentation, KeepUntil: keep}, s.quotas)
	if e != nil {
		return JobView{}, e
	}
	return view(j), nil
}
func view(j Job) JobView {
	v := JobView{ID: j.ID, Kind: j.Kind, State: j.State, SnapshotID: j.SnapshotID, Format: j.Format, ArtifactID: j.ArtifactID, ErrorCode: j.ErrorCode}
	if j.Kind == "issue" {
		// The number is the source product's and is known once the job succeeded: jobView reads it
		// from the record. An issue job itself carries none.
		v.Issue = issueSummary(j)
	}
	if j.Presentation != nil {
		// Which layout version an export prints (S23-A): the identity, never the presentation itself.
		v.Layout = &LayoutRef{ID: j.Presentation.LayoutID, Version: j.Presentation.LayoutVersion}
	}
	return v
}

// issueSummary is what an issue job says about itself: its template and the row it freezes, or, for a schema 2.0
// template (S03-K), the group it freezes in place of a row.
func issueSummary(j Job) *IssueSummary {
	s := &IssueSummary{TemplateID: j.TemplateID, TemplateVersion: j.TemplateVersion, TemplateSHA256: j.TemplateSHA256}
	if j.Group != nil {
		s.Group = j.Group.View()
		return s
	}
	row := j.RowIndex
	s.RowIndex = &row
	return s
}

// expired says whether a finished job's results are over: retention cleared its frozen read, or its time has passed by the service's
// clock. For a job that succeeded that time is its result's expiry (the read's own, or the later time the job keeps its result until,
// ADR 0026); a job that made nothing is over when its read is. A queued or running job is never expired in this sense (its read decides
// its fate when a worker meets it).
func (s *Service) expired(j Job) bool {
	if !Terminal(j.State) {
		return false
	}
	end := j.Prepared.ExpiresAt
	if j.State == "succeeded" {
		end = j.ResultEnd()
	}
	return j.Reduced || !s.now().Before(end)
}

// expiredView is the view of a finished job past its expiry: Expired is true for a job that succeeded, whose snapshot and artifact are
// no longer served (and, after the grace period, no longer stored). The ids stay: they are the audit of what the job produced.
func (s *Service) expiredView(ctx context.Context, p Principal, j Job) (JobView, error) {
	out := view(j)
	if j.State != "succeeded" {
		return out, nil
	}
	out.Expired = true
	if j.Kind == "issue" {
		rec, e := s.issueView(ctx, p, j)
		if e != nil {
			return JobView{}, e
		}
		out.Issue.Number, out.Issue.Cancellation = rec.Number, rec.Cancellation
	}
	return out, nil
}

// jobView is how an issue job is shown wherever it is shown: the admission and its replay, the
// poll and the answer to a cancel. A job that succeeded carries the product's number, read from
// its record through the cross-checks issueView makes (the record must be the job's, the stamp
// the number's), and the cancellation of its document when the product has made one (S24-A); any
// other state carries neither. The record's view is returned for callers that check more of it.
// For another kind of job it is view.
func (s *Service) jobView(ctx context.Context, p Principal, j Job) (JobView, IssueView, error) {
	v := view(j)
	if j.Kind != "issue" || j.State != "succeeded" {
		return v, IssueView{}, nil
	}
	rec, e := s.issueView(ctx, p, j)
	if e != nil {
		return JobView{}, IssueView{}, e
	}
	v.Issue.Number, v.Issue.Cancellation = rec.Number, rec.Cancellation
	return v, rec, nil
}

// CanIssue reports whether the service renders issue jobs. Whether an integration can issue
// depends on its adapter, which must implement DocumentAuthority: SubmitIssue says so.
func (s *Service) CanIssue() bool { return s.issuer != nil }

// Quotas is the per-identity admission limits in force (S04-D), every field resolved.
func (s *Service) Quotas() Quotas { return s.quotas }

// Retention is how long a result is kept after its read, by class, as in force (ADR 0026), every field resolved.
func (s *Service) Retention() Retention { return s.retention }

// IssueInput is what the caller freezes when it asks for an official issue: the published
// version's canonical configuration as the template store holds it.
//
// A 1.x template is issued for one row of the snapshot (RowIndex). A schema 2.0 template is issued for one
// row group (S03-K): the caller has resolved the group in the snapshot (NewIssueGroup) and RowIndex is 0.
type IssueInput struct {
	TemplateID      string
	TemplateVersion int
	Template        []byte
	SnapshotID      string
	RowIndex        int
	Group           *IssueGroup
	// Space is "team" for a template of the tenant's shared space (S12-C), "" for the caller's own.
	Space string
}

// SubmitIssue admits an issue job for the caller's current, unexpired snapshot. The caller
// (the HTTP layer, which owns the template store) has checked that the version is the
// published one and that the principal may use the template; the job freezes the
// configuration so that what was checked is what gets issued. The job has no number: the
// source product gives it when the worker asks it to prepare the document, so the snapshot's
// integration must have an adapter that implements DocumentAuthority (else ErrUnsupported), and, for a
// group, one that declares contract version 2 (GroupIssueSupported: else ErrUnsupported, whatever else is wrong).
// A group must be the snapshot's own, whole (VerifyGroup: else ErrInvalid). Admission reads no source data.
func (s *Service) SubmitIssue(ctx context.Context, p Principal, in IssueInput, key string) (JobView, error) {
	if s.issuer == nil || s.writers["pdf"] == nil {
		return JobView{}, ErrUnsupported
	}
	if !validKey(key) || !validID(in.TemplateID) || in.TemplateVersion < 1 || len(in.Template) < 2 || len(in.Template) > MaxTemplateBytes || !json.Valid(in.Template) || in.RowIndex < 0 || (in.Group != nil && in.RowIndex != 0) {
		return JobView{}, ErrInvalid
	}
	snap, e := s.snapshot(ctx, p, in.SnapshotID)
	if e != nil {
		return JobView{}, e
	}
	// The product checks the row against its own frozen rows (ADR 0003), so an issue needs the read itself and not only its scope
	// (ADR 0026 decision 6): a kept snapshot whose read has expired is shown and exported, and not issued from. A snapshot that ends
	// with its read was refused above when the read was over, as it always was.
	if snap.ExpiresAt.After(snap.Prepared.ExpiresAt) && !s.now().Before(snap.Prepared.ExpiresAt) {
		return JobView{}, ErrDenied
	}
	c, _, e := s.source(p, snap.Owner.Integration)
	if e != nil {
		return JobView{}, e
	}
	if _, ok := c.Adapter.(DocumentAuthority); !ok {
		return JobView{}, ErrUnsupported
	}
	sum := bytesDigest(in.Template)
	// An issued document's file is kept the issued documents' time after its read, within the read's scope (ADR 0026 decision 3).
	job := Job{ID: NewID(), Owner: snap.Owner, Kind: "issue", State: "queued", Key: key, Query: snap.Query, Prepared: snap.Prepared, SnapshotID: in.SnapshotID, Format: "pdf",
		TemplateID: in.TemplateID, TemplateVersion: in.TemplateVersion, TemplateSHA256: sum, Template: string(in.Template), KeepUntil: kept(snap.Prepared, s.retention.Issued)}
	if in.Space == "team" {
		job.TemplateSpace = "team"
	} else if in.Space != "" {
		return JobView{}, ErrInvalid
	}
	if in.Group != nil {
		// The product must speak the contract that carries a group: a version-1 adapter is never sent one.
		if !GroupIssueSupported(c.Adapter) {
			return JobView{}, ErrUnsupported
		}
		group := in.Group.Clone()
		if e = VerifyGroup(snap, group); e != nil {
			return JobView{}, ErrInvalid
		}
		job.Group, job.RequestHash = &group, IssueGroupRequestHash(in.TemplateID, in.TemplateVersion, sum, in.SnapshotID, group)
	} else {
		if in.RowIndex >= len(snap.Result.Rows) {
			return JobView{}, ErrInvalid
		}
		job.RowIndex, job.RequestHash = in.RowIndex, IssueRequestHash(in.TemplateID, in.TemplateVersion, sum, in.SnapshotID, in.RowIndex)
	}
	j, e := s.repo.Admit(ctx, job, s.quotas)
	if e != nil {
		return JobView{}, e
	}
	// A replay of the key of a job that has succeeded shows it as the poll does, with its number.
	v, _, e := s.jobView(ctx, p, j)
	return v, e
}

// Jobs lists the caller's jobs of every kind under the integrations the caller holds, newest first, at most limit of them (S07-C).
// An entry is what the job says of itself (its kind, state, ids, code, and, for an issue, its template and, once it succeeded, its
// number), with expired set as the poll sets it; the source is not asked and no result is authorized here: a result is authorized
// when it is read (GET /jobs/{id}, the snapshot's rows, the artifact's content). An issue record that cannot be read as published is
// an error for the whole page, as in Issues.
//
// S07-E: the page is narrowed by f's Kind and State and starts after f's Before (the last job of the previous page); f's Integrations
// are the service's to fill (the held ones) and must be empty.
func (s *Service) Jobs(ctx context.Context, p Principal, f JobListing) ([]JobView, error) {
	if !p.Identity.Valid() {
		return nil, ErrNotFound
	}
	if f.Integrations != nil {
		return nil, ErrInvalid
	}
	f.Integrations = []string{"-"}
	if !f.Valid() {
		return nil, ErrInvalid
	}
	f.Integrations = s.held(p)
	out := []JobView{}
	if len(f.Integrations) == 0 {
		return out, nil
	}
	jobs, e := s.repo.Jobs(ctx, p.Identity, f)
	if e != nil {
		return nil, e
	}
	for _, j := range jobs {
		if _, _, e := s.source(p, j.Owner.Integration); e != nil {
			return nil, ErrIntegrity
		}
		var v JobView
		if s.expired(j) {
			v, e = s.expiredView(ctx, p, j)
		} else {
			v, _, e = s.jobView(ctx, p, j)
		}
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}

// Served is the principal's products that the service has a connection for (ADR 0016): what GET /session lists.
func (s *Service) Served(p Principal) []string { return s.held(p) }

// held is the integrations of the principal that the service connects: the ones its listings are filtered by, in the repository.
func (s *Service) held(p Principal) []string {
	integrations := []string{}
	for _, id := range p.Integrations {
		if _, _, e := s.source(p, id); e == nil {
			integrations = append(integrations, id)
		}
	}
	return integrations
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
	integrations := s.held(p)
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
	v, _, e := s.issueRecord(ctx, p, j)
	return v, e
}

// issueRecord is the view of an issue job and, for a job that succeeded, the record the view was made from, read once and checked
// against the job. A cancellation the repository read with the record (S24-A) is held to the record as a repository holds it before
// keeping it (ValidateCancellation): one that is not the record's, or not of the contract's form, is INTEGRITY_FAILURE for the view,
// like a record that is not the job's. The record is the zero value for a job that has not succeeded.
func (s *Service) issueRecord(ctx context.Context, p Principal, j Job) (IssueView, Issue, error) {
	v := IssueView{ID: j.ID, Integration: j.Owner.Integration, State: j.State, ErrorCode: j.ErrorCode, SnapshotID: j.SnapshotID, IssueSummary: *issueSummary(j)}
	if j.State != "succeeded" {
		return v, Issue{}, nil
	}
	rec, e := s.repo.Issue(ctx, p.Identity, j.ID)
	if errors.Is(e, ErrNotFound) {
		return IssueView{}, Issue{}, fmt.Errorf("the record of a succeeded issue job is missing: %w", ErrIntegrity)
	}
	if e != nil {
		return IssueView{}, Issue{}, e
	}
	if rec.Owner != j.Owner || rec.ArtifactID != j.ArtifactID || rec.TemplateID != j.TemplateID || rec.TemplateVersion != j.TemplateVersion || rec.TemplateSHA256 != j.TemplateSHA256 || rec.SnapshotID != j.SnapshotID || rec.RowIndex != j.RowIndex || !rec.Group.Equal(j.Group) || rec.Stamp != IssueStamp(rec.Number, rec.IssuedAt, rec.ID) {
		return IssueView{}, Issue{}, ErrIntegrity
	}
	v.Number = rec.Number
	v.ArtifactID, v.SnapshotSHA256, v.SHA256, v.Pages, v.Stamp, v.IssuedAt = rec.ArtifactID, rec.SnapshotSHA256, rec.SHA256, rec.Pages, rec.Stamp, rec.IssuedAt.UTC().Format(time.RFC3339)
	v.ReservationRef, v.ConfirmationRef, v.ConfirmedAt = rec.ReservationRef, rec.ConfirmationRef, rec.ConfirmedAt.UTC().Format(time.RFC3339)
	v.Expired = s.expired(j)
	if c := rec.Cancellation; c != nil {
		if ValidateCancellation(rec, *c) != nil {
			return IssueView{}, Issue{}, fmt.Errorf("the cancellation read with an issue record is not that record's: %w", ErrIntegrity)
		}
		v.Cancellation = &IssueCancellationView{Ref: c.Ref, CancelledAt: c.CancelledAt.UTC().Format(time.RFC3339), Reason: c.Reason}
	}
	return v, rec, nil
}

// Sweep is the retention of S04-B: it deletes the expired artifacts, then snapshots, once the grace period has passed by the store's
// clock, and reduces the payload of the finished jobs whose results are gone. See SweepOptions and Repository.Sweep. It returns counts only.
func (s *Service) Sweep(ctx context.Context, o SweepOptions) (SweepResult, error) {
	o, e := o.Resolved()
	if e != nil {
		return SweepResult{}, e
	}
	return s.repo.Sweep(ctx, o)
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
	if s.expired(j) {
		// A finished job whose result is over (its read has expired and it keeps nothing longer, or what it kept has expired too), or
		// whose frozen read was cleared (S04-B), no longer discloses anything: there is no result to authorize, so it is not asked of
		// the source, and the view says what is left: the job's own state, its ids, its code, and, for an issue, the number its
		// permanent record holds.
		return s.expiredView(ctx, p, j)
	}
	if e = s.authorizeJob(ctx, j); e != nil {
		return s.endedMeanwhile(ctx, p, j, e)
	}
	out := view(j)
	if j.State == "succeeded" {
		if j.Kind == "snapshot" {
			_, e = s.snapshot(ctx, p, j.SnapshotID)
		} else {
			var a Artifact
			// The row, not the file: a job poll does not read an object (S04-E), so it neither costs the store a read of up to
			// MaxArtifactBytes nor depends on the store being reachable; the bytes are checked when they are downloaded.
			a, e = s.artifactRow(ctx, p, j.ArtifactID)
			if e == nil && j.Kind == "issue" {
				// The artifact must claim the record and the record must name the artifact; the
				// number the job view shows is the record's.
				var v IssueView
				if out, v, e = s.jobView(ctx, p, j); e == nil && (a.Issue != j.ID || v.ArtifactID != a.ID || v.SHA256 != a.SHA256) {
					e = ErrIntegrity
				}
			}
		}
		if e != nil {
			return s.endedMeanwhile(ctx, p, j, e)
		}
	}
	return out, nil
}

// endedMeanwhile answers a poll whose checks failed with e. A poll looks at the clock more than once: first to see whether the job's
// result is over, then, after the source has been asked, when it reads the result's row. When the result's time lies between two of
// those looks, the first says the result lasts and a later one refuses it as a result that has ended (a working snapshot at its
// read's expiry, and a kept file at its own time: ErrIntegrity, as an expired result always was; a result kept until its read's scope
// ends: ErrDenied). So the clock is looked at once more: when the result is over by now, the job is what a poll a moment later
// finds, a finished job whose result is over, and it is answered as that, whatever the check that failed had said (it may have been
// the source's own refusal, or its being unreachable, an instant before the end; a result that is over discloses nothing, and
// needs no answer from the source). While the result lasts, the error stands. This is asked only of a read whose scope goes on
// after it (ADR 0026): under a source that vouches for nothing, a poll at that instant is answered as it always was.
func (s *Service) endedMeanwhile(ctx context.Context, p Principal, j Job, e error) (JobView, error) {
	if j.Prepared.vouched() && s.expired(j) {
		return s.expiredView(ctx, p, j)
	}
	return JobView{}, e
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
	// Cleanup remains possible after source revocation; never return output refs. An issue job
	// that had succeeded already (the cancel changed nothing) still shows its number, as in every
	// other answer: the number is the record's, never a reference to the output.
	v, _, e := s.jobView(ctx, p, j)
	if e != nil {
		return JobView{}, e
	}
	v.SnapshotID = ""
	v.ArtifactID = ""
	return v, nil
}

// Download returns the artifact with its file, after everything the row says has been checked: the source product's current authority over
// the snapshot, the owner, the expiry and, for an issued PDF, the record it was issued under (artifactRow). When the file is an object (S04-E)
// it is read only then, with the row's digest and length, and every byte served has been compared with the row again here, whatever the
// object store's implementation did: a different length or digest, or a missing object, is ErrIntegrity; a store that cannot be reached is
// an *ObjectStoreError (ErrUnavailable), and nothing else of the store's reaches the caller.
//
// The file of an issue the source product has cancelled is no longer handed out (S24-A, ADR 0025 decision 4): ErrIssueCancelled, said as
// soon as the record says so, before the source product is asked to authorize anything and whatever has expired since. The platform does
// not alter or recall the file; it stops being the place the document comes from, and the register keeps saying what was cancelled.
func (s *Service) Download(ctx context.Context, p Principal, id string) (Artifact, error) {
	a, e := s.artifact(ctx, p, id, true)
	if e != nil || a.Object == "" {
		return a, e
	}
	if s.objects == nil {
		// The row promises an object and this service has no store to read it from: the deployment lost its configuration.
		return Artifact{}, &ObjectStoreError{}
	}
	b, e := s.objects.Get(ctx, a.Object, a.SHA256, a.Size)
	switch {
	case errors.Is(e, ErrIntegrity):
		return Artifact{}, fmt.Errorf("the object of the artifact does not match its row: %w", ErrIntegrity)
	case e != nil:
		var o *ObjectStoreError
		if errors.As(e, &o) {
			return Artifact{}, &ObjectStoreError{Status: o.Status}
		}
		return Artifact{}, &ObjectStoreError{}
	case len(b) != a.Size || bytesDigest(b) != a.SHA256:
		return Artifact{}, fmt.Errorf("the object of the artifact does not match its row: %w", ErrIntegrity)
	}
	a.Content = b
	return a, nil
}

// artifactRow is the artifact as its row describes it, authorized and cross-checked but with no object read: Download without the file. An
// inline artifact is checked against its content here, as it always was; an object's bytes are checked when Download reads them. It says
// nothing of a cancellation: a job's poll shows a cancelled issue with its cancellation, and only the download of its file is refused.
func (s *Service) artifactRow(ctx context.Context, p Principal, id string) (Artifact, error) {
	return s.artifact(ctx, p, id, false)
}

// artifact is artifactRow, and with serve the row of a file about to be handed out (Download): the file of a cancelled issue is then
// refused with ErrIssueCancelled. The record of an issued PDF is read before the snapshot (and, for a file that outlives its read, ADR
// 0026, before its scope is authorized), so that this refusal is given without asking the source product anything, for as long as the
// file is kept; every other answer about the record is given where it always was, after the snapshot's or the scope's and the row's,
// so that nothing else answers differently than before cancellations existed.
func (s *Service) artifact(ctx context.Context, p Principal, id string, serve bool) (Artifact, error) {
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
	// An issued PDF is served only as the record it was issued under describes it.
	describes := func(rec Issue) bool {
		return rec.Owner == a.Owner && rec.ArtifactID == a.ID && rec.SHA256 == a.SHA256 && rec.SnapshotID == a.SnapshotID
	}
	var rec Issue
	var recErr error
	if a.Issue != "" {
		rec, recErr = s.repo.Issue(ctx, p.Identity, a.Issue)
		if serve && recErr == nil && rec.Cancellation != nil && describes(rec) {
			return Artifact{}, ErrIssueCancelled
		}
	}
	if a.Query != nil || a.Prepared != nil {
		// A file that outlives its read stands without its snapshot (ADR 0026 decisions 4 and 5): it carries the query and the read it was
		// made from, its own expiry is later than the read's and not later than the read's scope (what a job may keep, validKeep), and the
		// source is asked by that scope now. Its snapshot may be gone; it is not read.
		if a.Query == nil || a.Prepared == nil || a.Prepared.Context.Owner != a.Owner || !s.now().Before(a.ExpiresAt) || !a.ExpiresAt.After(a.Prepared.ExpiresAt) || a.ExpiresAt.After(a.Prepared.ScopeEnd()) || !ValidArtifactBody(a) {
			return Artifact{}, ErrIntegrity
		}
		if e = s.currentScope(ctx, a.Owner, *a.Query, *a.Prepared); e != nil {
			return Artifact{}, e
		}
	} else {
		// A file that lives as long as its read is served through its snapshot and never later than it, as every file was.
		snap, e := s.snapshot(ctx, p, a.SnapshotID)
		if e != nil {
			return Artifact{}, e
		}
		if snap.Owner != a.Owner || !s.now().Before(a.ExpiresAt) || a.ExpiresAt.After(snap.ExpiresAt) || !ValidArtifactBody(a) {
			return Artifact{}, ErrIntegrity
		}
	}
	if a.Issue != "" {
		if errors.Is(recErr, ErrNotFound) {
			return Artifact{}, fmt.Errorf("the record an artifact names is missing: %w", ErrIntegrity)
		}
		if recErr != nil {
			return Artifact{}, recErr
		}
		if !describes(rec) {
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

// leaseCheckTimeout bounds the read with which RunOne confirms a lost lease, and failTimeout the
// writes that record or resolve a failure. With them a worker has recorded its failure within its
// lease: WorkTimeout + leaseCheckTimeout + failTimeout < LeaseDuration (TestFailureIsRecordedWithinTheLease).
const (
	leaseCheckTimeout = time.Second
	failTimeout       = 2 * time.Second
)

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

// RunOne claims one job and executes it. A job that fails is failed with the code of its error; a
// panic anywhere in its execution, the adapters' calls included, is an error with the code
// EXECUTION_FAILED (the panic's value is not kept: it may carry data), not the end of the worker.
//
// An issue job (S03-H) that fails other than by losing its lease is voided at the source product
// when this attempt called PrepareDocument, or when the claim is of a later generation than the
// first (an earlier attempt may have reserved or confirmed): the product is told to void whatever
// it holds for the job id, with the code as the reason. The order is what keeps a late void from
// voiding a number that another worker confirmed and published: the failure is recorded first,
// fenced by the claim (Fail: running, this generation, lease not run out), as ISSUE_UNRECONCILED,
// which is true until the void is done; only then is the void delivered, in a context of its own;
// when it succeeded the failure is resolved to the job's own code (ResolveFailure, fenced on the
// same generation and on that code). If the failure is not recorded (the lease is lost, the store
// cannot be reached) nothing is voided and the lease decides: the next claim, of a later
// generation, voids. A void that cannot be delivered leaves the job ISSUE_UNRECONCILED, and the
// product can find the reservation by the job id; so does a crash between the steps. A first
// attempt that fails before it reached the product calls nothing there. Losing the lease never
// voids: the worker that holds the job reuses the same reservation, which the product answers
// again by the job id. An answer of ErrLease counts as the loss of the lease only when the store
// confirms it (holdsJob says otherwise, or cannot say): while the store still has the claim
// running with a lease that has not run out, the answer is INTEGRITY_FAILURE and the job is
// failed (and voided) like any other.
func (s *Service) RunOne(ctx context.Context) (bool, error) {
	claim, e := s.repo.Claim(ctx, LeaseDuration)
	if e != nil || claim == nil {
		return false, e
	}
	ctx, cancel := context.WithTimeout(ctx, WorkTimeout)
	defer cancel()
	var attempt issueAttempt
	e = s.executeRecovered(ctx, *claim, &attempt)
	if e == nil {
		return true, nil
	}
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
		return true, s.failAndVoid(ctx, *claim, e, code)
	}
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), failTimeout)
	defer stop()
	_ = s.repo.Fail(cleanup, *claim, code)
	return true, e
}

// executeRecovered is execute, with a panic turned into an error that carries no value.
func (s *Service) executeRecovered(ctx context.Context, claim Claim, attempt *issueAttempt) (e error) {
	defer func() {
		if recover() != nil {
			e = errExecutionPanicked
		}
	}()
	return s.execute(ctx, claim, attempt)
}

// errExecutionPanicked is what a panic during a job's execution becomes: an error of no known
// kind, so EXECUTION_FAILED.
var errExecutionPanicked = errors.New("the job's execution panicked")

// failAndVoid fails an issue job whose attempt may have left a reservation or a confirmation at
// the source product, and voids it there, in the order RunOne describes. It returns the error
// RunOne returns: e, or ErrUnreconciled with the causes when the job stays so.
func (s *Service) failAndVoid(ctx context.Context, claim Claim, e error, code string) error {
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), failTimeout)
	defer stop()
	// 1. The failure is recorded first, as unreconciled, fenced by the claim. If it does not take,
	// the job is not this claim's any more (or the store cannot be reached): the lease decides.
	if s.repo.Fail(cleanup, claim, ErrUnreconciled.Error()) != nil {
		return e
	}
	// 2. Then the void. The job is failed, so no other worker can claim it: whatever the product
	// holds for it stays this job's, however late the void arrives.
	if voidErr := s.voidIssue(ctx, claim.Job, code); voidErr != nil {
		return fmt.Errorf("%w: the source product could not void the job: %w; the job's own failure: %w", ErrUnreconciled, voidErr, e)
	}
	// 3. The void is done: the job's own code replaces the unreconciled one.
	if code == ErrUnreconciled.Error() {
		return e
	}
	resolve, stopResolve := context.WithTimeout(context.WithoutCancel(ctx), failTimeout)
	defer stopResolve()
	if resolveErr := s.repo.ResolveFailure(resolve, claim, code); resolveErr != nil {
		return fmt.Errorf("%w: the source product voided the job but the platform could not record it: %w; the job's own failure: %w", ErrUnreconciled, resolveErr, e)
	}
	return e
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
// was never asked and holds nothing. The platform has recorded the failure before it asks (see
// failAndVoid), and a panic in the adapter is a void that failed.
func (s *Service) voidIssue(ctx context.Context, j Job, reason string) (err error) {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), VoidTimeout)
	defer stop()
	c, sc, e := s.sourceOwner(j.Owner)
	if e != nil {
		return e
	}
	authority, ok := c.Adapter.(DocumentAuthority)
	if !ok {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = errExecutionPanicked
		}
	}()
	return authority.VoidIssue(ctx, sc, j.ID, reason)
}

// validReservation: what the worker requires of the product's answer to PrepareDocument: its
// reference, this job's id, a valid number, an issue time that is a whole second between 1970 and
// 9999 and an expiry within those years and after it. The times are judged by value.
func validReservation(j Job, res IssueReservation) bool {
	return ValidProductRef(res.Ref) && res.JobID == j.ID && ValidIssueNumber(res.Number) && ValidProductTime(res.IssuedAt) && inProductYears(res.ExpiresAt) && res.ExpiresAt.After(res.IssuedAt)
}

// validReceipt: what the worker requires of the product's answer to ConfirmIssue: its reference and a
// confirmation time that is a whole second between the issue time and 9999.
func validReceipt(res IssueReservation, receipt IssueReceipt) bool {
	return ValidProductRef(receipt.Ref) && ValidProductTime(receipt.ConfirmedAt) && !receipt.ConfirmedAt.Before(res.IssuedAt)
}

func (s *Service) execute(ctx context.Context, claim Claim, attempt *issueAttempt) error {
	j := claim.Job
	var pub Publication
	// A snapshot job and an issue job need the read itself (the pages; the product's own frozen rows), an export its snapshot and the
	// read's scope (ADR 0026 decision 6): authorizeJob asks accordingly, here and before the publication.
	if e := s.authorizeJob(ctx, j); e != nil {
		return e
	}
	// An export that no worker reached until its file's own time had passed is denied here, before anything is rendered or put: the
	// store would refuse its publication (Job.PublishDeadline). Only a file kept beyond its read has such a time before its scope
	// ends; every other job's deadline is the one authorizeJob has just held it to.
	if j.Kind == "export" && !j.KeepUntil.IsZero() && !s.now().Before(j.KeepUntil) {
		return ErrDenied
	}
	switch j.Kind {
	case "snapshot":
		result, e := s.collect(ctx, j.Owner, j.Query, j.Prepared, j.Columns)
		if e != nil {
			return e
		}
		pub.Snapshot = &Snapshot{ID: NewID(), Owner: j.Owner, Query: j.Query, Prepared: j.Prepared, Result: result, ExpiresAt: j.ResultEnd(), SHA256: Digest(result)}
	case "export":
		w := s.writers[j.Format]
		if w == nil {
			return ErrUnsupported
		}
		p := Principal{Identity: j.Owner.Identity, Integrations: []string{j.Owner.Integration}}
		snap, e := s.snapshot(ctx, p, j.SnapshotID)
		if e != nil {
			// Only an export whose scope goes on after its read can meet its snapshot's end here (the authorization above passed by the
			// scope); with a source that vouches for no scope the error is what it always was.
			if j.Prepared.vouched() && s.snapshotEnded(ctx, j, e) {
				return ErrDenied
			}
			return e
		}
		if !sameBasis(j, snap) {
			return ErrIntegrity
		}
		var b []byte
		if j.Presentation != nil {
			// The frozen presentation (S23-A): the writer renders through it or the job is refused; without one the writer is called
			// exactly as it always was.
			pw, ok := w.(PresentedWriter)
			if !ok {
				return ErrUnsupported
			}
			b, e = pw.WritePresented(ctx, snap, *j.Presentation)
		} else {
			b, e = w.Write(ctx, snap)
		}
		if e != nil {
			return e
		}
		if len(b) == 0 || len(b) > MaxArtifactBytes {
			return ErrBudget
		}
		layoutID, layoutVersion := layoutOf(j.Presentation)
		if pub.Artifact, e = s.keep(ctx, withBasis(Artifact{ID: NewID(), Owner: j.Owner, SnapshotID: snap.ID, Format: j.Format, ExpiresAt: fileEnd(j, snap), LayoutID: layoutID, LayoutVersion: layoutVersion}, j), b); e != nil {
			return e
		}
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
		// A group (S03-K) is asked only of a product that declares the contract that carries one: a job
		// whose adapter has since lost that is refused before anything is read or asked, however it was admitted.
		if j.Group != nil && !GroupIssueSupported(c.Adapter) {
			return ErrUnsupported
		}
		p := Principal{Identity: j.Owner.Identity, Integrations: []string{j.Owner.Integration}}
		snap, e := s.snapshot(ctx, p, j.SnapshotID)
		if e != nil {
			return e
		}
		if !sameBasis(j, snap) {
			return ErrIntegrity
		}
		// The frozen row or group must still be the verified snapshot's: the snapshot is frozen, so a mismatch is
		// not a refusal of the caller's request but a store that does not agree with itself.
		var intent IssueIntent
		if j.Group != nil {
			if VerifyGroup(snap, *j.Group) != nil {
				return ErrIntegrity
			}
			intent = IssueIntent{JobID: j.ID, Prepared: snap.Prepared, TemplateID: j.TemplateID, TemplateVersion: j.TemplateVersion, TemplateSHA256: j.TemplateSHA256}
			group := j.Group.Clone()
			intent.Group = &group
		} else {
			if j.RowIndex >= len(snap.Result.Rows) {
				return ErrIntegrity
			}
			intent = IssueIntent{JobID: j.ID, Prepared: snap.Prepared, RowIndex: j.RowIndex, RowSHA256: Digest(snap.Result.Rows[j.RowIndex]), TemplateID: j.TemplateID, TemplateVersion: j.TemplateVersion, TemplateSHA256: j.TemplateSHA256}
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
		res, e := authority.PrepareDocument(ctx, sc, intent)
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
		result, e := s.issuer.Issue(ctx, IssueRequest{Job: j, Snapshot: snap, Stamp: stamp, Number: res.Number, At: issuedAt})
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
		// With an object store the file is put there before the product is told it exists (S04-E): the product never confirms a file the
		// platform failed to keep, and a PUT that fails is a failure of the job like any other, voided at the product (S03-H). The row that
		// names the object is published after the confirmation; an object whose row never comes is an orphan the sweep reconciles.
		id := NewID()
		if pub.Artifact, e = s.keep(ctx, withBasis(Artifact{ID: id, Owner: j.Owner, SnapshotID: snap.ID, Format: "pdf", ExpiresAt: fileEnd(j, snap), Issue: j.ID}, j), b); e != nil {
			return e
		}
		receipt, e := authority.ConfirmIssue(ctx, sc, IssueConfirmation{JobID: j.ID, ReservationRef: res.Ref, SHA256: sum, Pages: result.Pages})
		if e != nil {
			return fromProduct(e)
		}
		if !validReceipt(res, receipt) {
			return ErrIntegrity
		}
		pub.Issue = &Issue{ID: j.ID, Owner: j.Owner, Number: res.Number, TemplateID: j.TemplateID, TemplateVersion: j.TemplateVersion, TemplateSHA256: j.TemplateSHA256, SnapshotID: snap.ID, SnapshotSHA256: snap.SHA256, RowIndex: j.RowIndex, Group: j.Group.cloned(), ArtifactID: id, SHA256: sum, Pages: result.Pages, Stamp: stamp, IssuedAt: issuedAt,
			ReservationRef: res.Ref, ConfirmationRef: receipt.Ref, ConfirmedAt: receipt.ConfirmedAt.UTC()}
	default:
		return ErrIntegrity
	}
	if e := s.authorizeJob(ctx, j); e != nil {
		return e
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	return s.repo.Publish(ctx, claim, pub)
}

// snapshotEnded says whether an export's snapshot could not be read (e) because it has ended, rather than because the store disagrees
// with itself: a working snapshot ends with its read, and an export whose scope goes on may meet it after that (ADR 0026), stored but
// expired, or already swept. Such a job is denied, as an export whose read had expired always was.
func (s *Service) snapshotEnded(ctx context.Context, j Job, e error) bool {
	if errors.Is(e, ErrNotFound) {
		return !s.now().Before(j.Prepared.ExpiresAt)
	}
	if !errors.Is(e, ErrIntegrity) {
		return false
	}
	v, e := s.repo.Snapshot(ctx, j.Owner.Identity, j.SnapshotID)
	return e == nil && !s.now().Before(v.ExpiresAt)
}

// withBasis gives an artifact its own basis when its job keeps it beyond the read (ADR 0026 decision 5): the job's frozen query and read,
// by which the file is authorized after its snapshot's rows are gone. An artifact that lives as long as its read gets none, and is
// encoded as it always was.
func withBasis(a Artifact, j Job) Artifact {
	if j.KeepUntil.IsZero() {
		return a
	}
	q, p := j.Query, j.Prepared
	a.Query, a.Prepared = &q, &p
	return a
}

// keep makes the artifact for the file b: inline with the content when no object store is configured, else the file is put in the object
// store, under the key of the artifact's id, and the artifact carries the key and the length and no content (S04-E). The store's error is
// not relayed: a job whose file could not be put fails with EXECUTION_FAILED and its text names nothing of the store.
func (s *Service) keep(ctx context.Context, a Artifact, b []byte) (*Artifact, error) {
	a.SHA256 = bytesDigest(b)
	if s.objects == nil {
		a.Content = b
		return &a, nil
	}
	a.Object, a.Size = ObjectKey(a.ID), len(b)
	if s.objects.Put(ctx, a.Object, b, a.SHA256) != nil {
		return nil, errObjectStored
	}
	return &a, nil
}
func (s *Service) encodeCursor(snap Snapshot, start int) string {
	payload := snap.ID + ":" + Digest(snap.Owner) + ":" + snap.Prepared.QueryHash + ":" + strconv.Itoa(start)
	h := hmac.New(sha256.New, s.keys[0])
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
	// Every key of the ring is evaluated, with no early exit on the first match, so that the time does not say which key signed.
	matched := 0
	for _, k := range s.keys {
		h := hmac.New(sha256.New, k)
		h.Write(b)
		if hmac.Equal(sig, h.Sum(nil)) {
			matched = 1
		}
	}
	if matched != 1 {
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
