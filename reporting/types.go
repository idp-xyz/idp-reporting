// Package reporting owns domain-neutral report execution and private assets.
package reporting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalid     = errors.New("INVALID_REQUEST")
	ErrDenied      = errors.New("AUTH_SCOPE_DENIED")
	ErrNotFound    = errors.New("NOT_FOUND")
	ErrConflict    = errors.New("IDEMPOTENCY_CONFLICT")
	ErrUnsupported = errors.New("UNSUPPORTED_COMBINATION")
	ErrIntegrity   = errors.New("INTEGRITY_FAILURE")
	ErrBudget      = errors.New("BUDGET_EXCEEDED")
	ErrLease       = errors.New("LEASE_LOST")
	ErrUnavailable = errors.New("AUTHORITY_UNAVAILABLE")
	// ErrUnpublished: an issue names a template version that is not the published one.
	ErrUnpublished = errors.New("TEMPLATE_NOT_PUBLISHED")
)

type Identity struct {
	Tenant  string
	Issuer  string
	Subject string
}
type Principal struct {
	Identity
	Integrations []string
}
type Owner struct {
	Identity
	Integration string
}

func (i Identity) Valid() bool {
	return i.Tenant != "" && i.Issuer != "" && i.Subject != "" && len(i.Tenant)+len(i.Issuer)+len(i.Subject) <= 1024
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
type Dataset struct {
	Ref            Ref      `json:"dataset"`
	Columns        []Column `json:"columns"`
	Metrics        []Ref    `json:"metrics"`
	Dimensions     []string `json:"dimensions"`
	CurrencyPolicy *Ref     `json:"currency_policy,omitempty"`
	Timezone       string   `json:"timezone"`
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
	ExpiresAt      time.Time
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
	Content    []byte
	Issue      string `json:",omitempty"` // the issue record this artifact was issued under (S03-G)
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
	// configuration, its digest, the row and the number the repository assigned at admission.
	TemplateID      string `json:",omitempty"`
	TemplateVersion int    `json:",omitempty"`
	TemplateSHA256  string `json:",omitempty"`
	Template        string `json:",omitempty"` // the canonical JSON text, as a string: JSONB would reorder an object
	RowIndex        int    `json:",omitempty"`
	Number          int64  `json:",omitempty"`
}

// Issue is the permanent record of one officially issued document: which template version,
// which row of which frozen snapshot, the artifact that holds the PDF and what the PDF says
// about itself. Its ID is the ID of the job that issued it.
type Issue struct {
	ID              string
	Owner           Owner
	Number          int64
	TemplateID      string
	TemplateVersion int
	TemplateSHA256  string
	SnapshotID      string
	SnapshotSHA256  string
	RowIndex        int
	ArtifactID      string
	SHA256          string
	Pages           int
	Stamp           string
	IssuedAt        time.Time
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
}

// IssueSummary is what an issue job says about itself before and after it ran.
type IssueSummary struct {
	Number          int64  `json:"number"`
	TemplateID      string `json:"template_id"`
	TemplateVersion int    `json:"template_version"`
	TemplateSHA256  string `json:"template_sha256"`
	RowIndex        int    `json:"row_index"`
}

// IssueView is one entry of the issue listing: the job and, once it succeeded, its record.
type IssueView struct {
	ID         string `json:"issue_id"`
	State      string `json:"state"`
	ErrorCode  string `json:"error_code,omitempty"`
	SnapshotID string `json:"snapshot_id"`
	IssueSummary
	ArtifactID     string `json:"artifact_id,omitempty"`
	SnapshotSHA256 string `json:"snapshot_sha256,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Pages          int    `json:"pages,omitempty"`
	Stamp          string `json:"stamp,omitempty"`
	IssuedAt       string `json:"issued_at,omitempty"`
}
type Claim struct{ Job Job }
type Publication struct {
	Snapshot *Snapshot
	Artifact *Artifact
	Issue    *Issue // with the artifact, for an issue job
}

// IssueStamp is the line an issued document carries instead of the preview banner: the
// number, the issue time in UTC to the second and the record id.
func IssueStamp(number int64, at time.Time, id string) string {
	return fmt.Sprintf("ISSUED No. %06d - %s - %s", number, at.UTC().Format("2006-01-02T15:04:05Z"), id)
}

// Issuer renders an issue job: the frozen template bound to the snapshot's row, stamped. It
// fails with ErrUnpublished when the template's published version is no longer the frozen one.
type Issuer interface {
	Issue(context.Context, IssueRequest) (IssueResult, error)
}
type IssueRequest struct {
	Job      Job
	Snapshot Snapshot
	Stamp    string
}
type IssueResult struct {
	PDF   []byte
	Pages int
}

// Repository owns only platform assets. Publish must atomically insert one immutable asset
// (two for an issue job: the artifact and the record) and finish a current, unexpired,
// uncancelled job generation. Admit assigns the number of an issue job: consecutive per tenant
// and integration, never reused. Issues lists an identity's issue jobs, newest first.
type Repository interface {
	Admit(context.Context, Job) (Job, error)
	Job(context.Context, Identity, string) (Job, error)
	Cancel(context.Context, Identity, string) (Job, error)
	Claim(context.Context, time.Duration) (*Claim, error)
	Publish(context.Context, Claim, Publication) error
	Fail(context.Context, Claim, string) error
	Snapshot(context.Context, Identity, string) (Snapshot, error)
	Artifact(context.Context, Identity, string) (Artifact, error)
	Issue(context.Context, Identity, string) (Issue, error)
	Issues(context.Context, Identity, int) ([]Job, error)
}

// WorkbookWriter renders a snapshot in one export format. The name predates formats other than
// the workbook.
type WorkbookWriter interface {
	Write(context.Context, Snapshot) ([]byte, error)
}
type Config struct {
	Repository  Repository
	Connections []Connection
	CursorKey   []byte
	Writer      WorkbookWriter            // the default workbook format
	Formats     map[string]WorkbookWriter // further export formats by name, such as "pdf"
	Issuer      Issuer                    // renders issue jobs; without it issuing is unsupported
	Now         func() time.Time
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
const MaxTemplateBytes = 65536
const SourcePageSize = 50
const LeaseDuration = 30 * time.Second
const WorkTimeout = 20 * time.Second

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
