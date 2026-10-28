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
	// ErrUnreconciled: an issue job failed after the source product may have reserved or confirmed
	// a number for it, and the platform could not void that at the product (S03-H). The product
	// can find the reservation by the job id.
	ErrUnreconciled = errors.New("ISSUE_UNRECONCILED")
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
	// configuration, its digest and the row. It carries no number: the source product gives the
	// number when the worker asks it to prepare the document (S03-H).
	TemplateID      string `json:",omitempty"`
	TemplateVersion int    `json:",omitempty"`
	TemplateSHA256  string `json:",omitempty"`
	Template        string `json:",omitempty"` // the canonical JSON text, as a string: JSONB would reorder an object
	RowIndex        int    `json:",omitempty"`
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
	RowIndex        int
	ArtifactID      string
	SHA256          string
	Pages           int
	Stamp           string
	IssuedAt        time.Time // the product's issue time, UTC, whole second
	ReservationRef  string
	ConfirmationRef string
	ConfirmedAt     time.Time // the product's confirmation time, UTC, whole second
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

// IssueSummary is what an issue job says about itself before and after it ran. The number is
// the source product's and is known once the job succeeded: it is read from the record.
type IssueSummary struct {
	Number          string `json:"number,omitempty"`
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
	// The product's references: its reservation of the number and its confirmation of the file.
	ReservationRef  string `json:"reservation_ref,omitempty"`
	ConfirmationRef string `json:"confirmation_ref,omitempty"`
	ConfirmedAt     string `json:"confirmed_at,omitempty"` // RFC 3339, UTC
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
	At       time.Time // the product's issue time: printed in the stamp and the PDF's creation date (S03-H)
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
// an identity's issue jobs under the given integrations, newest first, at most limit of them.
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
	Issues(context.Context, Identity, []string, int) ([]Job, error)
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

// VoidTimeout bounds the call that voids a failed issue job at the source product (S03-H). It runs in
// a context of its own, after the job's work time may have run out; with the 2 s allowed to fail
// the job, a worker is done within its lease (WorkTimeout + VoidTimeout + 2 s < LeaseDuration).
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
// snapshot, printed through one published template version.
type IssueIntent struct {
	JobID           string
	Prepared        PreparedRead // the frozen snapshot's source read
	RowIndex        int
	RowSHA256       string // Digest(row) of that row's values as the snapshot holds them
	TemplateID      string
	TemplateVersion int
	TemplateSHA256  string
}

// IssueReservation is the product's answer to PrepareDocument.
type IssueReservation struct {
	Ref       string    // the product's reservation id, 1..128 bytes
	JobID     string    // the intent's JobID
	Number    string    // the product's document number (ValidIssueNumber)
	IssuedAt  time.Time // the product's issue time, UTC, whole second
	ExpiresAt time.Time // after it an unconfirmed reservation cannot be confirmed
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
	Ref         string    // the product's confirmation id, 1..128 bytes
	ConfirmedAt time.Time // the product's time, UTC, whole second
}

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
