// Package protocol is the wire of the reporting source protocol, version 1 (S19-A, docs/SOURCE_HTTP_PROTOCOL.md): the paths, the bodies,
// the error codes and their statuses. The platform's adapter (adapters/httpsource), the reference source (adapters/httpsource/sourcetest)
// and the conformance checker (cmd/reporting-source-check) share it, so that the three cannot drift apart; docs/source-openapi.json is held
// to it by a test. It is standard-library-only and holds no behaviour beyond the conversions.
package protocol

import (
	"errors"
	"net/url"
	"strings"
	"time"

	r "idp.local/reporting/reporting"
)

// Version is the protocol's version, the v1 of every path.
const Version = 1

// Revision is the minor revision of version 1: 1 since S19-B added the optional route companies (docs/SOURCE_HTTP_PROTOCOL.md). A minor
// revision only adds optional routes; a source of an earlier revision is served unchanged.
const Revision = 1

// Prefix is the path every route of version 1 starts with, under the connection's base URL.
const Prefix = "/reporting-source/v1"

// The paths. The read reference and the job id are path segments (RefPattern, JobPattern), always repeated in the body, where the source
// must find the same value. PathCompanies is optional (revision 1, S19-B): asked only of a connection that declares the capability.
const (
	PathCatalog   = Prefix + "/catalog"
	PathCompanies = Prefix + "/companies"
	PathReads     = Prefix + "/reads"
	PathPages     = Prefix + "/reads/{ref}/pages"
	PathAuthorize = Prefix + "/reads/{ref}/authorize"
	PathPrepare   = Prefix + "/issues/{job_id}/prepare"
	PathConfirm   = Prefix + "/issues/{job_id}/confirm"
	PathVoid      = Prefix + "/issues/{job_id}/void"
)

// RequestIDHeader carries the platform's random id of one request (32 lowercase hexadecimal characters): the source logs it, so that one
// refusal can be found on both sides. It identifies the request and nothing else.
const RequestIDHeader = "X-Request-Id"

// Bounds of the protocol.
const (
	MaxRequestBytes   = 1 << 20 // what the platform sends, and what a source must accept
	MaxResponseBytes  = 1 << 20 // the most the platform reads of an answer (the platform's whole result is bounded by the same number)
	MaxDatasets       = 100
	MaxRefBytes       = 128
	MaxScopeHandle    = 1024
	MaxWatermark      = 256
	MaxCursor         = 2048
	MaxPageRows       = 100
	MaxReadLifetime   = 15 * time.Minute // a read's expiry is at most this far ahead of the platform's clock
	MaxLabelBytes     = 256
	MaxReasonBytes    = 1024
	MaxCurrencyOrUnit = 32
	// MaxCompanies and MaxCompanyBytes bound the company directory's answer (ADR 0015, S19-B): the platform's own bounds.
	MaxCompanies    = r.MaxCompanies
	MaxCompanyBytes = r.MaxCompanyBytes
)

// Path fills a path's one parameter, escaped (the patterns allow nothing that needs escaping, which the adapter checks before it sends).
func Path(pattern, value string) string {
	v := url.PathEscape(value)
	return strings.Replace(strings.Replace(pattern, "{ref}", v, 1), "{job_id}", v, 1)
}

// Context is the source context every request carries: who asks (the platform tenant, the issuer and the subject of the access token
// the platform verified), through which product connection (integration, source tenant). The source maps (issuer, subject) of the
// platform tenant to its own actor on its own side, and trusts nothing else of a request for authority.
type Context struct {
	PlatformTenant string `json:"platform_tenant"`
	Issuer         string `json:"issuer"`
	Subject        string `json:"subject"`
	Integration    string `json:"integration"`
	SourceTenant   string `json:"source_tenant"`
}

// ContextOf is the wire form of a source context.
func ContextOf(sc r.SourceContext) Context {
	return Context{PlatformTenant: sc.Owner.Tenant, Issuer: sc.Owner.Issuer, Subject: sc.Owner.Subject, Integration: sc.Owner.Integration, SourceTenant: sc.SourceTenant}
}

// Source is the source context of the wire form.
func (c Context) Source() r.SourceContext {
	return r.SourceContext{Owner: r.Owner{Identity: r.Identity{Tenant: c.PlatformTenant, Issuer: c.Issuer, Subject: c.Subject}, Integration: c.Integration}, SourceTenant: c.SourceTenant}
}

// Valid: every field present, the identity within the platform's bounds, the integration and source tenant 1..128 bytes.
func (c Context) Valid() bool {
	sc := c.Source()
	return sc.Owner.Identity.Valid() && c.Integration != "" && len(c.Integration) <= 128 && c.SourceTenant != "" && len(c.SourceTenant) <= 128
}

// Read is a prepared read on the wire: the source's answer to POST /reads, which the platform keeps and sends back unchanged in every
// later request about it. ExpiresAt is RFC 3339 in the form Go's time.RFC3339Nano writes (no trailing zeros in the fraction), so that it
// survives the platform's store unchanged.
type Read struct {
	Ref            string   `json:"ref"`
	ScopeHandle    string   `json:"scope_handle"`
	QueryHash      string   `json:"query_hash"`
	Dataset        r.Ref    `json:"dataset"`
	Companies      []string `json:"company_ids"`
	Timezone       string   `json:"timezone"`
	CurrencyPolicy *r.Ref   `json:"currency_policy,omitempty"`
	Watermark      string   `json:"watermark"`
	ExpiresAt      string   `json:"expires_at"`
}

// ReadOf is the wire form of a prepared read.
func ReadOf(p r.PreparedRead) Read {
	return Read{Ref: p.Ref, ScopeHandle: p.ScopeHandle, QueryHash: p.QueryHash, Dataset: p.Dataset, Companies: p.Companies, Timezone: p.Timezone, CurrencyPolicy: p.CurrencyPolicy, Watermark: p.Watermark, ExpiresAt: FormatTime(p.ExpiresAt)}
}

// Prepared is the prepared read of the wire form, in the given context; the time must be canonical (ParseTime).
func (w Read) Prepared(sc r.SourceContext) (r.PreparedRead, bool) {
	at, ok := ParseTime(w.ExpiresAt)
	if !ok {
		return r.PreparedRead{}, false
	}
	return r.PreparedRead{Context: sc, Ref: w.Ref, ScopeHandle: w.ScopeHandle, QueryHash: w.QueryHash, Dataset: w.Dataset, Companies: w.Companies, Timezone: w.Timezone, CurrencyPolicy: w.CurrencyPolicy, Watermark: w.Watermark, ExpiresAt: at}, true
}

// FormatTime writes a time as the protocol does: RFC 3339 with the fraction Go's time.RFC3339Nano writes, in the time's own offset.
func FormatTime(t time.Time) string { return t.Format(time.RFC3339Nano) }

// ParseTime reads a protocol time and accepts it only in its canonical form: the text FormatTime writes for the time it parses to.
func ParseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || FormatTime(t) != s {
		return time.Time{}, false
	}
	return t, true
}

// The request and answer bodies. A pointer field is required: the strict decoder refuses null, so a nil pointer is a missing field.

type CatalogRequest struct {
	Context *Context `json:"context"`
}
type CatalogResponse struct {
	Datasets []r.Dataset `json:"datasets"`
}

// The company directory (ADR 0015, S19-B): the companies the context may choose now, each with its id and name, and whether the source
// cut the list. Both fields are required: an answer without a list, or without saying whether it is whole, does not verify.
type CompaniesRequest struct {
	Context *Context `json:"context"`
}
type CompaniesResponse struct {
	Companies []r.Company `json:"companies"`
	Partial   *bool       `json:"partial"`
}

type PrepareRequest struct {
	Context   *Context     `json:"context"`
	Query     *r.QuerySpec `json:"query"`
	QueryHash string       `json:"query_hash"`
}
type PrepareResponse struct {
	Read *Read `json:"read"`
}

type PageRequest struct {
	Context *Context `json:"context"`
	Read    *Read    `json:"read"`
	Cursor  string   `json:"cursor"`
	Limit   int      `json:"limit"`
}
type PageResponse struct {
	Ref        string            `json:"ref"`
	QueryHash  string            `json:"query_hash"`
	Watermark  string            `json:"watermark"`
	Rows       [][]string        `json:"rows"`
	Totals     map[string]string `json:"totals"`
	NextCursor *string           `json:"next_cursor"`
}

type AuthorizeRequest struct {
	Context *Context `json:"context"`
	Read    *Read    `json:"read"`
}
type AuthorizeResponse struct {
	Authorized *bool `json:"authorized"`
}

// Group is the row group of a schema 2.0 issue (reporting.IssueGroup).
type Group struct {
	Fields     []string `json:"fields"`
	Key        []string `json:"key"`
	Rows       []int    `json:"rows"`
	RowsSHA256 string   `json:"rows_sha256"`
}

// Intent is reporting.IssueIntent on the wire. A row intent has row_index and row_sha256 and no group; a group intent has a group,
// row_index 0 and row_sha256 "".
type Intent struct {
	JobID           string `json:"job_id"`
	Read            *Read  `json:"read"`
	RowIndex        int    `json:"row_index"`
	RowSHA256       string `json:"row_sha256"`
	TemplateID      string `json:"template_id"`
	TemplateVersion int    `json:"template_version"`
	TemplateSHA256  string `json:"template_sha256"`
	Group           *Group `json:"group,omitempty"`
}

// IntentOf is the wire form of an intent.
func IntentOf(in r.IssueIntent) Intent {
	read := ReadOf(in.Prepared)
	out := Intent{JobID: in.JobID, Read: &read, RowIndex: in.RowIndex, RowSHA256: in.RowSHA256, TemplateID: in.TemplateID, TemplateVersion: in.TemplateVersion, TemplateSHA256: in.TemplateSHA256}
	if in.Group != nil {
		g := in.Group.Clone()
		out.Group = &Group{Fields: g.Fields, Key: g.Key, Rows: g.Rows, RowsSHA256: g.RowsSHA256}
	}
	return out
}

// IssueIntent is the intent of the wire form, in the given context.
func (w Intent) IssueIntent(sc r.SourceContext) (r.IssueIntent, bool) {
	if w.Read == nil {
		return r.IssueIntent{}, false
	}
	p, ok := w.Read.Prepared(sc)
	if !ok {
		return r.IssueIntent{}, false
	}
	in := r.IssueIntent{JobID: w.JobID, Prepared: p, RowIndex: w.RowIndex, RowSHA256: w.RowSHA256, TemplateID: w.TemplateID, TemplateVersion: w.TemplateVersion, TemplateSHA256: w.TemplateSHA256}
	if w.Group != nil {
		g := r.IssueGroup{Fields: w.Group.Fields, Key: w.Group.Key, Rows: w.Group.Rows, RowsSHA256: w.Group.RowsSHA256}.Clone()
		in.Group = &g
	}
	return in, true
}

type PrepareIssueRequest struct {
	Context *Context `json:"context"`
	Intent  *Intent  `json:"intent"`
}
type Reservation struct {
	Ref       string `json:"ref"`
	JobID     string `json:"job_id"`
	Number    string `json:"number"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
}
type PrepareIssueResponse struct {
	Reservation *Reservation `json:"reservation"`
}

type Confirmation struct {
	JobID          string `json:"job_id"`
	ReservationRef string `json:"reservation_ref"`
	SHA256         string `json:"sha256"`
	Pages          int    `json:"pages"`
}
type ConfirmRequest struct {
	Context      *Context      `json:"context"`
	Confirmation *Confirmation `json:"confirmation"`
}
type Receipt struct {
	Ref         string `json:"ref"`
	ConfirmedAt string `json:"confirmed_at"`
}
type ConfirmResponse struct {
	Receipt *Receipt `json:"receipt"`
}

type VoidRequest struct {
	Context *Context `json:"context"`
	JobID   string   `json:"job_id"`
	Reason  string   `json:"reason"`
}
type VoidResponse struct {
	Voided *bool `json:"voided"`
}

// ErrorBody is every refusal's body: one code of the closed set, nothing else.
type ErrorBody struct {
	Code string `json:"code"`
}

// The error codes, each with the one status it is sent with and the platform error it becomes.
const (
	CodeInvalid     = "invalid"
	CodeDenied      = "denied"
	CodeExpired     = "expired"
	CodeNotFound    = "not_found"
	CodeConflict    = "conflict"
	CodeUnsupported = "unsupported"
	CodeIntegrity   = "integrity"
	CodeBudget      = "budget"
	CodeUnavailable = "unavailable"
)

// Code is one row of the error table.
type Code struct {
	Name   string
	Status int
	Err    error
	// IssueOnly: the code may be answered by the issue routes only (the read routes have no job to be unknown or to conflict).
	IssueOnly bool
}

// Codes is the closed set, in the order of the protocol's table.
var Codes = []Code{
	{CodeInvalid, 400, r.ErrInvalid, false},
	{CodeDenied, 403, r.ErrDenied, false},
	{CodeNotFound, 404, r.ErrNotFound, true},
	{CodeConflict, 409, r.ErrConflict, true},
	{CodeExpired, 410, r.ErrDenied, false},
	{CodeUnsupported, 422, r.ErrUnsupported, false},
	{CodeIntegrity, 422, r.ErrIntegrity, false},
	{CodeBudget, 429, r.ErrBudget, false},
	{CodeUnavailable, 503, r.ErrUnavailable, false},
}

// Lookup finds a code by name.
func Lookup(name string) (Code, bool) {
	for _, c := range Codes {
		if c.Name == name {
			return c, true
		}
	}
	return Code{}, false
}

// CodeOf is the code a source answers for one of the platform's errors (what the reference source does with its adapter's errors); ok is
// false for an error that has no code, which a source answers as a 500 without a body.
func CodeOf(err error) (Code, bool) {
	for _, name := range []string{CodeInvalid, CodeDenied, CodeNotFound, CodeConflict, CodeUnsupported, CodeIntegrity, CodeBudget, CodeUnavailable} {
		c, _ := Lookup(name)
		if errors.Is(err, c.Err) {
			return c, true
		}
	}
	return Code{}, false
}

// ValidRef: a read reference is 1..128 characters of A-Z a-z 0-9 . _ ~ - (unreserved in a URL path, so it is its own escape).
func ValidRef(s string) bool {
	if len(s) < 1 || len(s) > MaxRefBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '~' || c == '-') {
			return false
		}
	}
	return s != "." && s != ".."
}

// ValidJobID: the platform's job id, 32 lowercase hexadecimal characters.
func ValidJobID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Printable: 1..max bytes of printable ASCII without a space (0x21..0x7E).
func Printable(s string, max int) bool {
	if len(s) < 1 || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
