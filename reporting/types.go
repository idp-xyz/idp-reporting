// Package reporting owns domain-neutral report execution and private assets.
package reporting

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

var (
	ErrInvalid = errors.New("INVALID_REQUEST")
	ErrDenied  = errors.New("AUTH_SCOPE_DENIED")
	// ErrRoleDenied is a refusal of an action the principal's roles do not allow (S12-A, ADR 0018): 403, distinct from a scope refusal.
	ErrRoleDenied = errors.New("AUTH_ROLE_DENIED")
	// ErrAuditUnavailable is a refusal of a publication or an issue whose audit event could not be written (S12-B, ADR 0018 Q4): 503.
	ErrAuditUnavailable = errors.New("AUDIT_UNAVAILABLE")
	ErrNotFound         = errors.New("NOT_FOUND")
	ErrConflict         = errors.New("IDEMPOTENCY_CONFLICT")
	ErrUnsupported      = errors.New("UNSUPPORTED_COMBINATION")
	ErrIntegrity        = errors.New("INTEGRITY_FAILURE")
	ErrBudget           = errors.New("BUDGET_EXCEEDED")
	ErrLease            = errors.New("LEASE_LOST")
	ErrUnavailable      = errors.New("AUTHORITY_UNAVAILABLE")
	// ErrUnpublished: an issue names a template version that is not the published one.
	ErrUnpublished = errors.New("TEMPLATE_NOT_PUBLISHED")
	// ErrUnreconciled: an issue job failed after the source product may have reserved or confirmed
	// a number for it, and the platform has not voided that at the product (S03-H): the failure is
	// recorded as this before the void is asked for, and stays so when the void fails, or the worker
	// dies, or the job's own code cannot be recorded afterwards. The product can find the
	// reservation by the job id.
	ErrUnreconciled = errors.New("ISSUE_UNRECONCILED")
	// ErrIssueCancelled: the file of an issue the source product has cancelled is no longer handed out (S24-A, ADR 0025 decision 4): 409.
	// The record, its number, digest and page count stay in the register.
	ErrIssueCancelled = errors.New("ISSUE_CANCELLED")
	// ErrQuota: admission would take the identity over one of its quotas (S04-D). The error that carries it names the quota
	// (QuotaOf) and nothing else.
	ErrQuota = errors.New("quota exceeded")
)

// The names of the quotas (S04-D, docs/S04D_SCOPE.md D1): the words of the 429 answer, of the capabilities and of the console.
const (
	QuotaJobsInFlight      = "jobs_in_flight"
	QuotaStoredResults     = "stored_results"
	QuotaAdmissionsPerHour = "admissions_per_hour"
	// QuotaQueriesInFlight (S04-G) is the one quota of this process rather than of the store: POST /queries and the row reads one identity
	// has running at once.
	QuotaQueriesInFlight = "queries_in_flight"
)

// QuotaError is ErrQuota naming the quota that was reached: its text carries the name, never a count or a value.
type QuotaError struct{ Quota string }

func (e *QuotaError) Error() string        { return ErrQuota.Error() + ": " + e.Quota }
func (e *QuotaError) Is(target error) bool { return target == ErrQuota }

// QuotaOf says which quota an error names, or "" when it is not a quota refusal.
func QuotaOf(e error) string {
	var q *QuotaError
	if errors.As(e, &q) {
		return q.Quota
	}
	return ""
}

// Quotas bound what one identity (tenant, issuer, subject) may hold at the store (S04-D). Zero is the default; a value outside its bounds
// is refused by Resolved, so by NewService.
type Quotas struct {
	JobsInFlight      int // queued and running jobs: default 8, 1..1000
	StoredResults     int // snapshots and artifacts not yet expired by the store's clock: default 64, 1..100000
	AdmissionsPerHour int // jobs admitted in the last hour by the store's clock: default 120, 1..100000
	// QueriesInFlight (S04-G) bounds the POST /queries and snapshot row reads the service runs at once for one identity: default 4, 1..64.
	// Unlike the three above it is enforced by the service, in this process (it protects this process's cores and its connections to the
	// source product), and the store never sees it.
	QueriesInFlight int
}

const (
	DefaultJobsInFlight      = 8
	DefaultStoredResults     = 64
	DefaultAdmissionsPerHour = 120
	DefaultQueriesInFlight   = 4
	MaxJobsInFlight          = 1000
	MaxStoredResults         = 100000
	MaxAdmissionsPerHour     = 100000
	MaxQueriesInFlight       = 64
)

// Resolved replaces each zero with its default and refuses (ErrInvalid) a value outside 1..its maximum.
func (q Quotas) Resolved() (Quotas, error) {
	one := func(v *int, def, max int) bool {
		if *v == 0 {
			*v = def
		}
		return *v >= 1 && *v <= max
	}
	if !one(&q.JobsInFlight, DefaultJobsInFlight, MaxJobsInFlight) || !one(&q.StoredResults, DefaultStoredResults, MaxStoredResults) || !one(&q.AdmissionsPerHour, DefaultAdmissionsPerHour, MaxAdmissionsPerHour) || !one(&q.QueriesInFlight, DefaultQueriesInFlight, MaxQueriesInFlight) {
		return Quotas{}, ErrInvalid
	}
	return q, nil
}

type Identity struct {
	Tenant  string
	Issuer  string
	Subject string
}
type Principal struct {
	Identity
	Integrations []string
	// Roles are the binding's (S12-A, ADR 0018), never a token's. nil is a binding that names none: the rights every bound identity had
	// before roles (DefaultRoles); administrator is never implied.
	Roles []string
}

// The roles of ADR 0018.
const (
	RoleViewer        = "viewer"
	RoleDesigner      = "designer"
	RolePublisher     = "publisher"
	RoleIssuer        = "issuer"
	RoleAdministrator = "administrator"
)

// AllRoles are the five role names, in their order.
var AllRoles = []string{RoleViewer, RoleDesigner, RolePublisher, RoleIssuer, RoleAdministrator}

// DefaultRoles are a binding's roles when it names none: what every bound identity could do before roles (ADR 0018 Q1).
var DefaultRoles = []string{RoleViewer, RoleDesigner, RolePublisher, RoleIssuer}

// ValidRoles says whether a binding's roles are 1..5 distinct known names.
func ValidRoles(roles []string) bool {
	if len(roles) == 0 || len(roles) > len(AllRoles) {
		return false
	}
	seen := map[string]bool{}
	for _, role := range roles {
		if seen[role] || !slices.Contains(AllRoles, role) {
			return false
		}
		seen[role] = true
	}
	return true
}

// EffectiveRoles are the principal's roles: its binding's, or DefaultRoles when the binding names none.
func (p Principal) EffectiveRoles() []string {
	if p.Roles == nil {
		return slices.Clone(DefaultRoles)
	}
	return slices.Clone(p.Roles)
}

// Has says whether the principal's roles include role.
func (p Principal) Has(role string) bool { return slices.Contains(p.EffectiveRoles(), role) }

type Owner struct {
	Identity
	Integration string
}

func (i Identity) Valid() bool {
	return i.Tenant != "" && i.Issuer != "" && i.Subject != "" && len(i.Tenant)+len(i.Issuer)+len(i.Subject) <= 1024
}

// TeamSubject is the subject of a tenant's shared design space (S12-C, ADR 0018 decision 2): a reserved owner beside the people of the
// tenant and issuer. No binding may name it (the bindings file and the access-token profile refuse it), so no person is the team. Row-level
// security keeps the space to its tenant like any identity's rows; who of the tenant may read, change or publish it is the server's roles.
const TeamSubject = "~team"

// TeamOf is the owner of the shared design space of an identity's tenant and issuer.
func TeamOf(i Identity) Identity {
	return Identity{Tenant: i.Tenant, Issuer: i.Issuer, Subject: TeamSubject}
}

// IsTeam says whether an owner is a shared design space.
func (i Identity) IsTeam() bool { return i.Subject == TeamSubject }

// ActorDigest is a person's mark in a version of the shared space (who saved it): the SHA-256 of the identity, never shown to another person.
func ActorDigest(i Identity) string {
	b, _ := json.Marshal(i)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type Ref struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}
type PageSpec struct {
	Limit int `json:"limit"`
}
type QuerySpec struct {
	SchemaVersion  string   `json:"schema_version"`
	Integration    string   `json:"integration_id"`
	Dataset        Ref      `json:"dataset"`
	Companies      []string `json:"company_ids"`
	Metrics        []Ref    `json:"metrics"`
	Dimensions     []string `json:"dimensions"`
	Filters        []Filter `json:"filters"`
	Sort           []Sort   `json:"sort"`
	AsOf           string   `json:"as_of"`
	Timezone       string   `json:"timezone"`
	CurrencyPolicy *Ref     `json:"currency_policy,omitempty"`
	Page           PageSpec `json:"page"`
}
type Filter struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}
type Sort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}
type Column struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Scale    int    `json:"scale,omitempty"`
	Currency string `json:"currency,omitempty"`
	Unit     string `json:"unit,omitempty"`
}

// Dataset is one published, approved query shape. Other shapes need a new
// catalog entry/version; an arbitrary client SQL or expression is never accepted.
//
// Filters and Sorts (S20-A, ADR 0023) are the columns a query of this version may narrow by and order by, with the operators and
// directions each takes: what a query's filters and sort are validated against (ValidateQuery), fail closed. A version without them is
// what it always was: no filter, no sort. They are part of the version, as the columns are: a change is a new version.
type Dataset struct {
	Ref            Ref          `json:"dataset"`
	Columns        []Column     `json:"columns"`
	Metrics        []Ref        `json:"metrics"`
	Dimensions     []string     `json:"dimensions"`
	CurrencyPolicy *Ref         `json:"currency_policy,omitempty"`
	Timezone       string       `json:"timezone"`
	Filters        []FilterSpec `json:"filters,omitempty"`
	Sorts          []SortSpec   `json:"sorts,omitempty"`
}

// FilterSpec is one filter a dataset version publishes (ADR 0023 decision 1): Field a column of the dataset; Operators a non-empty subset
// of FilterOperators that fit the column's type; Values, when set, the closed list of values the field accepts (1..MaxFilterValues, each
// well formed for the column), which an `eq`, `ne` or `in` filter must take its values from; Required true when a query must carry exactly
// one filter on the field (a mandatory parameter, such as a period).
type FilterSpec struct {
	Field     string   `json:"field"`
	Operators []string `json:"operators"`
	Values    []string `json:"values,omitempty"`
	Required  bool     `json:"required,omitempty"`
}

// SortSpec is one sort key a dataset version publishes: Field a column of the dataset, Directions a non-empty subset of SortDirections.
type SortSpec struct {
	Field      string   `json:"field"`
	Directions []string `json:"directions"`
}
type SourceContext struct {
	Owner        Owner
	SourceTenant string
}
type PreparedRead struct {
	Context        SourceContext
	Ref            string
	ScopeHandle    string
	QueryHash      string
	Dataset        Ref
	Companies      []string
	Timezone       string
	CurrencyPolicy *Ref
	Watermark      string
	// ExpiresAt is the read's own lifetime: until it passes the source serves the pages of the frozen data version.
	ExpiresAt time.Time
	// ScopeExpiresAt (ADR 0026) is until when the source goes on answering AuthorizeCurrent for the read's scope, after its pages are
	// gone. The zero time means ExpiresAt (ScopeEnd): a source that vouches for no scope, and every read prepared before the field
	// existed, whose encoding it leaves unchanged.
	ScopeExpiresAt time.Time `json:",omitzero"`
}
type SourcePage struct {
	Ref        string
	QueryHash  string
	Watermark  string
	Rows       [][]string
	Totals     map[string]string
	NextCursor string
}

// Adapter is implemented by the product-side trusted integration tier. It owns
// the business meaning, source snapshot and current whole-scope authorization.
type Adapter interface {
	DescribeCatalog(context.Context, SourceContext) ([]Dataset, error)
	PrepareRead(context.Context, SourceContext, QuerySpec, string) (PreparedRead, error)
	ReadPage(context.Context, SourceContext, PreparedRead, string, int) (SourcePage, error)
	AuthorizeCurrent(context.Context, SourceContext, PreparedRead) error
}
type Connection struct {
	Tenant       string
	Integration  string
	SourceTenant string
	Adapter      Adapter
}
type Result struct {
	Columns      []Column          `json:"columns"`
	Rows         [][]string        `json:"rows"`
	Totals       map[string]string `json:"totals"`
	Completeness string            `json:"completeness"`
	Issues       []string          `json:"issues"`
	Evidence     Evidence          `json:"evidence"`
}
type Evidence struct {
	QueryHash string `json:"query_hash"`
	SourceRef string `json:"source_snapshot_ref"`
	Watermark string `json:"source_watermark"`
	// DataClass says where the data comes from, in the word of the connection's adapter (S08-A, ADR 0012 decision 6, dataclass.go): one
	// of DataClasses. A frozen snapshot keeps the word it was frozen with.
	DataClass string `json:"data_class"`
}
type Snapshot struct {
	ID        string
	Owner     Owner
	Query     QuerySpec
	Prepared  PreparedRead
	Result    Result
	ExpiresAt time.Time
	SHA256    string
}

// Format names an export format. The empty value is the original workbook format, so jobs and
// artifacts stored before formats existed keep their exact encoding.
type Artifact struct {
	ID         string
	Owner      Owner
	SnapshotID string
	Format     string `json:",omitempty"`
	ExpiresAt  time.Time
	SHA256     string
	// Content is the file when the artifact is stored inline (every artifact before S04-E, and every one the memory store holds without an
	// object store); it is empty when Object is set. Exactly one of the two is present (ValidArtifactBody).
	Content []byte
	// Object is the key of the object that holds the file (S04-E, docs/ADR_0007_OBJECT_STORAGE.md): ObjectKey of the artifact's id. Size is
	// then the file's length in bytes. The row's SHA256 and Size are what the object must match before a byte of it is served.
	Object string `json:",omitempty"`
	Size   int    `json:",omitempty"`
	Issue  string `json:",omitempty"` // the issue record this artifact was issued under (S03-G)
	// LayoutID and LayoutVersion name the layout version the export was rendered through (S23-A, ADR 0024): what the job's presentation
	// named. Empty for an export of the whole snapshot and for an issued document.
	LayoutID      string `json:",omitempty"`
	LayoutVersion int    `json:",omitempty"`
	// Query and Prepared are the artifact's own basis (ADR 0026 decision 5): the frozen query and source read it was made from, by
	// which a file that outlives its read is authorized after its snapshot's rows are gone. Both are set, or neither: an artifact
	// without them lives as long as its read and is served through its snapshot, as every artifact was before.
	Query    *QuerySpec    `json:",omitempty"`
	Prepared *PreparedRead `json:",omitempty"`
}
type Job struct {
	ID          string
	Owner       Owner
	Kind        string
	State       string
	Key         string
	RequestHash string
	Query       QuerySpec
	Prepared    PreparedRead
	Columns     []Column
	SnapshotID  string
	Format      string `json:",omitempty"`
	ArtifactID  string
	ErrorCode   string
	Generation  int64
	LeaseUntil  time.Time
	// An issue job (S03-G) freezes the template it issues: the published version's canonical
	// configuration, its digest and the row. It carries no number: the source product gives the
	// number when the worker asks it to prepare the document (S03-H).
	TemplateID      string `json:",omitempty"`
	TemplateVersion int    `json:",omitempty"`
	TemplateSHA256  string `json:",omitempty"`
	// TemplateSpace is "team" when the template is the tenant's shared space's (S12-C): the worker reads its publication there.
	TemplateSpace string `json:",omitempty"`
	Template      string `json:",omitempty"` // the canonical JSON text, as a string: JSONB would reorder an object
	RowIndex      int    `json:",omitempty"`
	// Group is the row group a schema 2.0 template is issued for (S03-K): frozen at admission in place of the
	// row, so that RowIndex is 0 for a group job. Nil for a 1.x job.
	Group *IssueGroup `json:",omitempty"`
	// Presentation is what an export job shows of its snapshot (S23-A, ADR 0024): the layout's presentation, frozen at admission as an
	// issue job freezes its template, naming the layout version it came from. Nil for an export of the whole snapshot (the exports as they
	// were before S23-A) and for every other kind. Retention keeps it: the view names the layout after the results are gone.
	Presentation *Presentation `json:",omitempty"`
	// Reduced marks a terminal job whose frozen read was cleared by retention (S04-B): its query, its prepared source
	// read and its columns are gone (no query value survives; the request hash does), and so are the snapshot and the artifact
	// it named, which keep their ids in the job and its view. The job keeps what its view and the audit need: owner, kind,
	// key, request hash, ids, format, state, code and, for an issue job, the frozen template and its row or group.
	Reduced bool `json:",omitempty"`
	// KeepUntil is the expiry of the result the job makes when that result outlives the read (ADR 0026 decision 3): frozen at
	// admission as the read's expiry plus the time of the result's class, or the read's scope expiry when that is earlier. The zero
	// time means the read's own expiry (ResultEnd): a working snapshot, a read whose source vouches for no scope, and every job
	// admitted before the field existed, whose encoding it leaves unchanged.
	KeepUntil time.Time `json:",omitzero"`
}

// Issue is the permanent record of one officially issued document: which template version,
// which row of which frozen snapshot, the artifact that holds the PDF and what the PDF says
// about itself. Its ID is the ID of the job that issued it. The number, the issue time and the
// two references are the source product's (S03-H): the reservation it made when it authorized
// the document and the confirmation it gave when it accepted the file's digest.
type Issue struct {
	ID              string
	Owner           Owner
	Number          string
	TemplateID      string
	TemplateVersion int
	TemplateSHA256  string
	SnapshotID      string
	SnapshotSHA256  string
	RowIndex        int // 0 for a group issue
	// Group is what the record keeps in place of the row for a schema 2.0 issue (S03-K): the group's fields,
	// key values, snapshot row indices and the digest of those rows. Nil for a 1.x issue.
	Group           *IssueGroup `json:",omitempty"`
	ArtifactID      string
	SHA256          string
	Pages           int
	Stamp           string
	IssuedAt        time.Time // the product's issue time, UTC, whole second
	ReservationRef  string
	ConfirmationRef string
	ConfirmedAt     time.Time // the product's confirmation time, UTC, whole second
	// Cancellation is the cancellation the source product made of the document, when the platform has recorded one (S24-A, ADR 0025
	// decision 3). It is a row of its own that a repository reads with the record and never part of the record's payload: the record
	// keeps every field it had. A publication carries none.
	Cancellation *IssueCancellationRecord `json:"-"`
}
type JobView struct {
	ID         string        `json:"job_id"`
	Kind       string        `json:"kind"`
	State      string        `json:"state"`
	SnapshotID string        `json:"snapshot_id,omitempty"`
	Format     string        `json:"format,omitempty"`
	ArtifactID string        `json:"artifact_id,omitempty"`
	ErrorCode  string        `json:"error_code,omitempty"`
	Issue      *IssueSummary `json:"issue,omitempty"`
	// Expired is true for a job that succeeded and whose results are no longer served: the result's own expiry has passed
	// (the expiry of the source read it was made from, or the later time the job keeps its result until, ADR 0026), so its
	// snapshot or artifact is not downloadable and, once the retention grace has passed, is deleted (S04-B). State stays
	// the job's own state, the audit of what happened; the ids stay as the job recorded them.
	Expired bool `json:"expired,omitempty"`
	// Layout names the layout version an export job carries (S23-A, ADR 0024); absent for an export of the whole snapshot and for every
	// other kind.
	Layout *LayoutRef `json:"layout,omitempty"`
}

// IssueSummary is what an issue job says about itself before and after it ran. The number is
// the source product's and is known once the job succeeded: it is read from the record.
//
// A 1.x issue names its row and a schema 2.0 issue its group (S03-K), never both: RowIndex is nil for a group
// issue and Group nil for a row issue. The group's key values are the caller's own data, shown to the caller
// who asked for them, as the row's values are; the rows themselves are not repeated (RowCount and RowsSHA256
// say how many and which).
//
// Cancellation is present once the source product has cancelled the document and the platform has recorded it (S24-A, ADR 0025 decision
// 3): the state word stays the job's (`succeeded`), so that a client that does not know cancellations reads what it read.
type IssueSummary struct {
	Number          string                 `json:"number,omitempty"`
	TemplateID      string                 `json:"template_id"`
	TemplateVersion int                    `json:"template_version"`
	TemplateSHA256  string                 `json:"template_sha256"`
	RowIndex        *int                   `json:"row_index,omitempty"`
	Group           *IssueGroupView        `json:"group,omitempty"`
	Cancellation    *IssueCancellationView `json:"cancellation,omitempty"`
}

// IssueCancellationView is how a view shows the cancellation of an issued document: the product's reference and time (RFC 3339, UTC)
// and the reason the person gave, shown to the identity that owns the issue and to nobody else.
type IssueCancellationView struct {
	Ref         string `json:"ref"`
	CancelledAt string `json:"cancelled_at"`
	Reason      string `json:"reason"`
}

// IssueGroupView is how a view shows the group of a schema 2.0 issue: the template's group fields, the key
// values in their order, how many rows the group has and the digest of those rows.
type IssueGroupView struct {
	Fields     []string `json:"fields"`
	Key        []string `json:"key"`
	RowCount   int      `json:"row_count"`
	RowsSHA256 string   `json:"rows_sha256"`
}

// IssueView is one entry of the issue listing: the job and, once it succeeded, its record.
//
// Integration is the product the issue was made under (S24-A): the capabilities say per product whether a cancellation can be asked
// (ADR 0025 decision 5), and an entry of a listing that spans the caller's products must say which one it belongs to.
type IssueView struct {
	ID          string `json:"issue_id"`
	Integration string `json:"integration_id"`
	State       string `json:"state"`
	ErrorCode   string `json:"error_code,omitempty"`
	SnapshotID  string `json:"snapshot_id"`
	IssueSummary
	ArtifactID     string `json:"artifact_id,omitempty"`
	SnapshotSHA256 string `json:"snapshot_sha256,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Pages          int    `json:"pages,omitempty"`
	Stamp          string `json:"stamp,omitempty"`
	IssuedAt       string `json:"issued_at,omitempty"`
	// The product's references: its reservation of the number and its confirmation of the file.
	ReservationRef  string `json:"reservation_ref,omitempty"`
	ConfirmationRef string `json:"confirmation_ref,omitempty"`
	ConfirmedAt     string `json:"confirmed_at,omitempty"` // RFC 3339, UTC
	// Expired is true for an issue that succeeded whose file is no longer served: the file's own expiry has passed (the
	// expiry of the snapshot's source read, or the later time an issued document is kept until, ADR 0026), and once the
	// retention grace has passed the PDF is deleted (S04-B). The record itself, with the product's number and references,
	// the file's digest and page count, is permanent and unchanged.
	Expired bool `json:"expired,omitempty"`
}
type Claim struct{ Job Job }
type Publication struct {
	Snapshot *Snapshot
	Artifact *Artifact
	Issue    *Issue // with the artifact, for an issue job
}

// IssueStamp is the line an issued document carries instead of the preview banner: the
// product's number, the issue time in UTC to the second and the record id.
func IssueStamp(number string, at time.Time, id string) string {
	return fmt.Sprintf("ISSUED No. %s - %s - %s", number, at.UTC().Format("2006-01-02T15:04:05Z"), id)
}

// Issuer renders an issue job: the frozen template bound to the snapshot's row, stamped. It
// fails with ErrUnpublished when the template's published version is no longer the frozen one.
//
// Check is everything the issuer can verify about a job and its verified snapshot without
// rendering it and without a stamp: the frozen configuration is the template's published
// version, the row and the table bind, the background resolves and the renderer's layout accepts
// the values. The worker calls it before it asks the source product for a number (S03-H), so that
// a withdrawn template or a document the layout refuses costs the product no reservation to void;
// it fails exactly as Issue would for those reasons. Issue repeats every check it can, because
// the template may be withdrawn between the two calls: that race still voids.
type Issuer interface {
	Check(context.Context, Job, Snapshot) error
	Issue(context.Context, IssueRequest) (IssueResult, error)
}
type IssueRequest struct {
	Job      Job
	Snapshot Snapshot
	Stamp    string
	// Number is the product's document number, the one in the stamp (ValidIssueNumber). A schema 2.1 template prints it
	// wherever it shows the issue number (ADR 0005, S03-M); the issuer passes it to the render and nothing else changes.
	Number string
	At     time.Time // the product's issue time: printed in the stamp and the PDF's creation date (S03-H)
}
type IssueResult struct {
	PDF   []byte
	Pages int
}

// Repository owns only platform assets. Publish must atomically insert one immutable asset
// (two for an issue job: the artifact and the record) and finish a current, unexpired,
// uncancelled job generation. Numbers are never the repository's: an issue job is admitted
// without one and its record carries the number the source product gave (S03-H). Cancel
// cancels a queued job, and a running job of any kind but issue; a running issue job is
// returned unchanged, because the product may already hold a reservation for it. Issues lists
// an identity's issue jobs under the given integrations, newest first, at most limit of them;
// Jobs lists its jobs the same way (S07-C), narrowed and paged by a JobListing (S07-E).
//
// Fail and ResolveFailure are fenced by the claim. Fail fails a job that is running, at the
// claim's generation, with a lease that has not run out by the store's clock; anything else is
// ErrLease and changes nothing. ResolveFailure replaces the code ISSUE_UNRECONCILED of a failed
// issue job with the job's own failure code, once the source product has voided what it held
// (S03-H): it applies only to an issue job that is failed, at the claim's generation, with the
// code ISSUE_UNRECONCILED (a job is failed through Fail, never through it); anything else is
// ErrLease and changes nothing, and a code that is empty or ISSUE_UNRECONCILED itself is
// ErrInvalid. It needs no lease: a failed job has none.
//
// Admit stores a job for its identity, or answers the job already stored under its key (a replay: never refused by a quota, the
// answer is the job's view). Otherwise, in the same atomic step as the insert, it counts what the identity (tenant, issuer, subject; not
// the integration) holds against the quotas it is given (S04-D, docs/S04D_SCOPE.md): queued and running jobs, snapshots and artifacts
// whose expiry is still in the future by the store's clock (issue records never), and jobs admitted in the last hour by the store's clock.
// A job over a quota is ErrQuota (a *QuotaError naming it), and nothing is stored. A zero field takes its default, a field out of bounds
// is ErrInvalid.
//
// Sweep is the retention of S04-B (docs/S04B_SCOPE.md, SweepOptions): it deletes the artifacts, then the snapshots, whose expiry
// plus the grace is past by the store's clock, never an issue record, never anything a queued or running job names, then reduces the
// payload of the terminal jobs whose snapshot and artifact are gone; in bounded batches, each one transaction, and it returns counts only.
//
// Claim returns the oldest job that is queued, or running with a lease that has run out, claimed for the lease it is given (the
// generation one higher), or nil when there is none. A job that cannot be decoded is not claimed: it is failed in the same transaction as
// its claim, terminally, with UnreadableJobCode, and Claim goes on to the next job, so that nil still means that nothing is claimable.
//
// RecordCancellation keeps the cancellation the source product made of an issued document (S24-A, ADR 0025 decision 3): one immutable row
// per issue record, beside the record, which keeps every field it had, and, in the same atomic step, the audit event of the cancellation
// (CancellationAuditEvent, at the time the row was kept): both are kept or neither, and a store that cannot write the event answers
// ErrAuditUnavailable and keeps nothing. The identity must own the issue record (ErrNotFound otherwise, for another identity's as for
// none); the cancellation must be that record's and well formed (ValidateCancellation: ErrInvalid). A cancellation is kept once: the same
// receipt again (the product's reference and time) answers the row as it was first kept, with its first reason and its first time, and
// writes nothing, no second audit event either; another receipt for the same issue is ErrConflict and changes nothing. RecordedAt is the
// store's clock, whatever the caller put there. Issue answers the record with its cancellation (Issue.Cancellation, nil when there is
// none). Nothing updates or deletes a cancellation, and Sweep never touches one: the retention that removes the file leaves the
// cancellation with the record.
type Repository interface {
	Admit(context.Context, Job, Quotas) (Job, error)
	Job(context.Context, Identity, string) (Job, error)
	Cancel(context.Context, Identity, string) (Job, error)
	Claim(context.Context, time.Duration) (*Claim, error)
	Publish(context.Context, Claim, Publication) error
	Fail(context.Context, Claim, string) error
	ResolveFailure(context.Context, Claim, string) error
	Snapshot(context.Context, Identity, string) (Snapshot, error)
	Artifact(context.Context, Identity, string) (Artifact, error)
	Issue(context.Context, Identity, string) (Issue, error)
	RecordCancellation(context.Context, Identity, IssueCancellationRecord) (IssueCancellationRecord, error)
	Issues(context.Context, Identity, []string, int) ([]Job, error)
	Jobs(context.Context, Identity, JobListing) ([]Job, error)
	Sweep(context.Context, SweepOptions) (SweepResult, error)
}

// WorkbookWriter renders a snapshot in one export format. The name predates formats other than
// the workbook.
type WorkbookWriter interface {
	Write(context.Context, Snapshot) ([]byte, error)
}

// PresentedWriter is a WorkbookWriter that can also render a snapshot through a presentation (S23-A, ADR 0024): the columns it picks in its
// order with its labels, alignments and formats, its subtotal rows, its marks, its title and its footnote. The export worker calls
// WritePresented when the job carries a presentation and Write otherwise, so that an export without a layout is byte for byte what it was.
// A presentation that does not fit the snapshot is refused, never applied in part. An export with a presentation is admitted only for a
// format whose writer implements this.
type PresentedWriter interface {
	WorkbookWriter
	WritePresented(context.Context, Snapshot, Presentation) ([]byte, error)
}
type Config struct {
	Repository  Repository
	Connections []Connection
	// CursorKey is the ring of one; CursorKeys is the cursor key ring (S04-H): the newest key first, 1 to 4 keys of at least 32 bytes, pairwise
	// distinct. The first signs every new cursor and any of them verifies. Exactly one of the two fields is set.
	CursorKey  []byte
	CursorKeys [][]byte
	Writer     WorkbookWriter            // the default workbook format
	Formats    map[string]WorkbookWriter // further export formats by name, such as "pdf"
	Issuer     Issuer                    // renders issue jobs; without it issuing is unsupported
	Quotas     Quotas                    // per-identity admission limits (S04-D); zero fields take the defaults
	Retention  Retention                 // how long a result outlives its read, by class (ADR 0026); zero fields take the defaults
	// Objects, when set, is where the worker puts the content of an artifact before it publishes the row, and where a download reads it back
	// (S04-E). The repository must be one that accepts such rows (the durable store opened with the same object store, or the memory store);
	// without it the content stays inline in the row, as before.
	Objects ObjectStore
	Now     func() time.Time
}
type RowsPage struct {
	SnapshotID string `json:"snapshot_id"`
	TotalRows  int    `json:"total_row_count"`
	Result
	NextCursor string    `json:"next_cursor,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
}

const MaxRows = 1000
const MaxResultBytes = 1 << 20
const MaxArtifactBytes = 8 << 20
const MaxIssuePages = 100
const MaxIssueListing = 100
const MaxJobListing = 100

// JobListing is one page of an identity's jobs (S07-E): under Integrations, newest first, at most Limit (1..MaxJobListing) of them;
// of one Kind and one State when they are set; strictly older than the identity's job Before when it is set (the last job of the
// previous page). A Before that is not the identity's job, under these integrations, gives an empty page, not an error.
type JobListing struct {
	Integrations []string
	Limit        int
	Kind, State  string
	Before       string
}

// Valid says whether the listing can be run: a limit in range, integrations, a known kind and state or none, an id or none.
func (l JobListing) Valid() bool {
	return l.Limit >= 1 && l.Limit <= MaxJobListing && len(l.Integrations) > 0 &&
		(l.Kind == "" || l.Kind == "snapshot" || l.Kind == "export" || l.Kind == "issue") &&
		(l.State == "" || l.State == "queued" || l.State == "running" || l.State == "succeeded" || l.State == "failed" || l.State == "cancelled") &&
		(l.Before == "" || validID(l.Before))
}

const MaxTemplateBytes = 65536
const SourcePageSize = 50
const LeaseDuration = 30 * time.Second
const WorkTimeout = 20 * time.Second

// VoidTimeout bounds the call that voids a failed issue job at the source product (S03-H). It runs in
// a context of its own, after the failure is recorded: the order is fail first (fenced by the claim,
// within the lease: WorkTimeout + leaseCheckTimeout + failTimeout < LeaseDuration), then void, so a void
// that is delayed can only void a job that no other worker can claim any more.
const VoidTimeout = 5 * time.Second

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// DocumentAuthority is the optional capability of an adapter whose source product issues formal
// documents (S03-H, docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md). The product authorizes the issue,
// owns the number and confirms the file; the platform renders and keeps it. Every call is
// idempotent by the platform's job id, so that a re-claimed job reaches the same outcome.
type DocumentAuthority interface {
	// PrepareDocument authorizes issuing the document bound to one row of the product's source
	// snapshot and reserves its number. The same JobID with the same intent returns the same
	// reservation while it is reserved or confirmed; another intent under that JobID is
	// ErrConflict; a voided reservation is ErrConflict; an expired one is ErrDenied. The product
	// refuses (ErrDenied) when the principal may not issue, when the row is not the one its
	// source snapshot produced (RowSHA256), or when its own approval rules say no.
	PrepareDocument(context.Context, SourceContext, IssueIntent) (IssueReservation, error)
	// ConfirmIssue records the issued file under the reserved number. Repeating it with the same
	// digest returns the same receipt; another digest is ErrConflict; a voided reservation is
	// ErrConflict and an expired, unconfirmed one ErrDenied.
	ConfirmIssue(context.Context, SourceContext, IssueConfirmation) (IssueReceipt, error)
	// VoidIssue voids whatever the product holds for the job, reserved or confirmed, recording
	// the platform's reason; holding nothing is not an error, and voiding twice is not either.
	// A void is final for the job id: even when nothing was held, the product records it, and a
	// later PrepareDocument for that job id is ErrConflict (a stale worker that lost its lease
	// cannot reserve or confirm for a job another worker already failed and voided). It does not
	// depend on the principal's current grants: the platform cleans up after itself.
	VoidIssue(ctx context.Context, sc SourceContext, jobID, reason string) error
}

// IssueIntent is what the platform asks the product to authorize: one row of a frozen
// snapshot, printed through one published template version (contract version 1), or, for a schema 2.0
// template, one row group of it (contract version 2, S03-K, docs/ADR_0004_TEMPLATE_SCHEMA_2.md decision 7).
//
// A row intent has RowIndex and RowSHA256 and no Group. A group intent has Group and neither RowIndex nor
// RowSHA256 (0 and ""): the product checks every row of the group against its own source snapshot, as it
// checks a row, and refuses (ErrDenied) when they are not the rows its snapshot produced, when the group is not
// whole (its snapshot has another row with the group's key) or when a group field is not a dimension of the
// frozen read. The platform sends a group intent only to an adapter that declares contract version 2
// (VersionedAuthority); the intent of a 1.x template is the same as it was before version 2 existed.
type IssueIntent struct {
	JobID           string
	Prepared        PreparedRead // the frozen snapshot's source read
	RowIndex        int
	RowSHA256       string // Digest(row) of that row's values as the snapshot holds them
	TemplateID      string
	TemplateVersion int
	TemplateSHA256  string
	// Group is the row group of a schema 2.0 issue; nil for a row issue (and then absent from the intent's
	// digest, so that a version-1 intent digests as it always did).
	Group *IssueGroup `json:",omitempty"`
}

// IssueReservation is the product's answer to PrepareDocument.
type IssueReservation struct {
	Ref       string    // the product's reservation id: 1..128 printable ASCII characters (0x21..0x7E), no space
	JobID     string    // the intent's JobID
	Number    string    // the product's document number (ValidIssueNumber)
	IssuedAt  time.Time // the product's issue time: a whole second between 1970-01-01T00:00:00Z and 9999-12-31T23:59:59Z
	ExpiresAt time.Time // after it an unconfirmed reservation cannot be confirmed; after IssuedAt, within the same years
}

// IssueConfirmation tells the product which file now carries the reserved number.
type IssueConfirmation struct {
	JobID          string
	ReservationRef string
	SHA256         string
	Pages          int
}

// IssueReceipt is the product's record of the confirmation.
type IssueReceipt struct {
	Ref         string    // the product's confirmation id: 1..128 printable ASCII characters (0x21..0x7E), no space
	ConfirmedAt time.Time // the product's time: a whole second between IssuedAt and 9999-12-31T23:59:59Z
}

// CompanyDirectory is the optional capability of an adapter whose source product can list the companies
// the principal may currently choose (ADR 0015, S19-B). The list is advisory: PrepareRead and AuthorizeCurrent
// authorize every read as they do without it. The platform never stores the list, never caches it across
// requests and never logs a name; a frozen read keeps company ids (PreparedRead.Companies), never names.
type CompanyDirectory interface {
	// ListCompanies returns the companies the principal of the context may choose now, in the product's
	// order, each with the id the query names and the name a person reads. Partial is true when the
	// product cut the list (the platform accepts at most 1000 entries); then the person may still type
	// an id. An empty list is an answer: nothing may be chosen.
	ListCompanies(context.Context, SourceContext) (CompanyList, error)
}

// Company is one entry of the directory.
type Company struct {
	ID   string `json:"id"`   // 1–128 characters, the value PrepareRead receives in Companies
	Name string `json:"name"` // 1–128 characters, what the console shows; never stored by the platform
}

// CompanyList is a directory's answer.
type CompanyList struct {
	Companies []Company `json:"companies"`
	Partial   bool      `json:"partial"`
}

// CompanyDirectoryAnswer is what the platform answers for GET /companies (ADR 0015 decision 3): whether the product can list, whether
// it cut the list, and the list, exactly these three keys. An adapter without the capability answers supported false and an empty list;
// it is not an error.
type CompanyDirectoryAnswer struct {
	Supported bool      `json:"supported"`
	Partial   bool      `json:"partial"`
	Companies []Company `json:"companies"`
}

// The bounds of a company directory's answer (ADR 0015): at most MaxCompanies entries, each id and name 1..MaxCompanyBytes bytes of
// UTF-8 text without control characters (ValidateCompanyList).
const (
	MaxCompanies    = 1000
	MaxCompanyBytes = 128
)

// ValidIssueNumber: 1..32 characters of A-Z a-z 0-9 . _ -, starting with a letter or a digit,
// so that a number prints on one line and names a file.
func ValidIssueNumber(n string) bool {
	if len(n) < 1 || len(n) > 32 {
		return false
	}
	for i, c := range n {
		alnum := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '.' && c != '_' && c != '-') {
			return false
		}
	}
	return true
}
