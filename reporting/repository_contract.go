package reporting

import (
	"encoding/json"
	"reflect"
	"time"
)

// validFormat accepts "" (the workbook) or a short lowercase name of another export format.
func validFormat(f string) bool {
	if len(f) > 16 || f == "xlsx" {
		return false
	}
	for _, c := range f {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// ExportRequestHash binds an export job to its snapshot and format. The workbook keeps the
// original digest of the snapshot ID, so export jobs admitted before formats existed stay valid.
func ExportRequestHash(snapshotID, format string) string {
	if format == "" {
		return Digest(snapshotID)
	}
	return Digest(struct{ SnapshotID, Format string }{snapshotID, format})
}

// ExportRequestHashWithLayout binds an export job to its snapshot, its format and the layout version it carries (S23-A, ADR 0024), so that
// a replay of the key with the same layout version answers the same job and one with another version, or none, is a conflict. Without a
// layout it is ExportRequestHash, so that every export job admitted before layouts existed keeps its hash.
func ExportRequestHashWithLayout(snapshotID, format, layoutID string, layoutVersion int) string {
	if layoutID == "" {
		return ExportRequestHash(snapshotID, format)
	}
	return Digest(struct {
		SnapshotID, Format, LayoutID string
		LayoutVersion                int
	}{snapshotID, format, layoutID, layoutVersion})
}

// exportFields checks what an export job freezes apart from its snapshot and format: its request hash binds the layout version its
// presentation names, the presentation (when there is one) is whole and names its layout, and nothing of an issue job is set.
func exportFields(j Job) bool {
	layoutID, layoutVersion := layoutOf(j.Presentation)
	if j.RequestHash != ExportRequestHashWithLayout(j.SnapshotID, j.Format, layoutID, layoutVersion) || j.TemplateID != "" || j.Template != "" {
		return false
	}
	return j.Presentation == nil || (layoutID != "" && j.Presentation.Validate() == nil)
}

// IssueRequestHash binds an issue job to everything it freezes.
func IssueRequestHash(templateID string, version int, templateSHA256, snapshotID string, row int) string {
	return Digest(struct {
		TemplateID      string
		TemplateVersion int
		TemplateSHA256  string
		SnapshotID      string
		RowIndex        int
	}{templateID, version, templateSHA256, snapshotID, row})
}

// issueFields checks what an issue job freezes, apart from its request hash: the template and, in place of one
// another, the row (a 1.x template) or the group (a schema 2.0 template, S03-K), which has no row.
func issueFields(j Job) bool {
	template := validID(j.TemplateID) && j.TemplateVersion >= 1 && len(j.Template) >= 2 && len(j.Template) <= MaxTemplateBytes && json.Valid([]byte(j.Template)) && j.TemplateSHA256 == bytesDigest([]byte(j.Template))
	if j.Group != nil {
		return template && j.RowIndex == 0 && ValidGroupShape(*j.Group)
	}
	return template && j.RowIndex >= 0 && j.RowIndex < MaxRows
}

// issueRequestHash is the request hash an admitted issue job must carry: a row job's, or a group job's.
func issueRequestHash(j Job) string {
	if j.Group != nil {
		return IssueGroupRequestHash(j.TemplateID, j.TemplateVersion, j.TemplateSHA256, j.SnapshotID, *j.Group)
	}
	return IssueRequestHash(j.TemplateID, j.TemplateVersion, j.TemplateSHA256, j.SnapshotID, j.RowIndex)
}

// ValidProductRef: a reference the source product gave (its reservation or its confirmation) is 1..128
// printable ASCII characters (0x21..0x7E): no space, control character, NUL, DEL, byte outside ASCII or
// bidirectional mark. The platform keeps it in the record, prints it in the console and gives it back to
// the product, so it must be the same string in every encoding, store and screen.
func ValidProductRef(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7E {
			return false
		}
	}
	return true
}

var (
	// The years a product's time may name: what a PDF can carry as its creation date (export/pdf:
	// the writer's issue-time range) and what JSON can encode. The renderer refuses any other time
	// after the product has reserved the number, so the worker refuses it first.
	earliestProductTime = time.Unix(0, 0)
	latestProductTime   = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
)

// inProductYears: the instant lies between 1970-01-01T00:00:00Z and 9999-12-31T23:59:59Z.
func inProductYears(t time.Time) bool {
	return !t.Before(earliestProductTime) && !t.After(latestProductTime)
}

// ValidProductTime: a time the source product gave (the issue time, the confirmation time) is a whole
// second between 1970-01-01T00:00:00Z and 9999-12-31T23:59:59Z. It is judged by value, never by zone or
// representation (a time read back from a database may carry another location); the platform keeps it
// as the same instant in UTC.
func ValidProductTime(t time.Time) bool { return inProductYears(t) && t.Equal(t.Truncate(time.Second)) }

func ValidateAdmission(j Job) error {
	if !validID(j.ID) || !j.Owner.Identity.Valid() || j.Owner.Integration == "" || !validKey(j.Key) || j.State != "queued" || j.Generation != 0 || j.RequestHash == "" || j.Prepared.Context.Owner != j.Owner || j.Prepared.QueryHash != QueryHash(j.Query) || j.Reduced {
		return ErrInvalid
	}
	// The three lifetimes (ADR 0026): the scope a read declares is not before the read, and what the job keeps is within the scope.
	if !j.Prepared.validLifetimes() || !validKeep(j) {
		return ErrInvalid
	}
	switch j.Kind {
	case "snapshot":
		if j.SnapshotID != "" || j.Format != "" || j.RequestHash != Digest(Normalize(j.Query)) {
			return ErrInvalid
		}
	case "export":
		if !validID(j.SnapshotID) || !validFormat(j.Format) || !exportFields(j) {
			return ErrInvalid
		}
	case "issue":
		if !validID(j.SnapshotID) || j.Format != "pdf" || !issueFields(j) || j.RequestHash != issueRequestHash(j) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if j.Kind != "issue" && (j.TemplateID != "" || j.TemplateVersion != 0 || j.TemplateSHA256 != "" || j.Template != "" || j.RowIndex != 0 || j.Group != nil) {
		return ErrInvalid
	}
	// Only an export job presents its snapshot (S23-A).
	if j.Kind != "export" && j.Presentation != nil {
		return ErrInvalid
	}
	return nil
}

// UnreadableJobCode is the failure code of a job that a store claimed and cannot decode (its payload is corrupt, or was written by a
// binary this one cannot read). Such a job is failed terminally in the transaction of its claim, never handed to a worker and never left
// running for the next claim to take again: INTEGRITY_FAILURE, except for an issue job that had been claimed before (priorGeneration, the
// generation the job had before this claim, is at least 1): an earlier attempt may have reserved a number at the source product, which
// cannot be voided because the job cannot be read, so the job is ISSUE_UNRECONCILED and the product finds the reservation by the job id.
// A job that was never claimed (generation 0) cannot have reached the product. kind and priorGeneration are the store's own plain
// fields (columns), never read from the payload that cannot be decoded.
func UnreadableJobCode(kind string, priorGeneration int64) string {
	if kind == "issue" && priorGeneration >= 1 {
		return ErrUnreconciled.Error()
	}
	return ErrIntegrity.Error()
}

// ValidateCancellation holds a cancellation to the issue record it cancels, for a repository about to keep it and for whoever reads one
// back (S24-A, ADR 0025 decision 3): it names the record, carries the product's reference (ValidProductRef) and a cancellation time that
// is a whole second between the record's confirmation time and 9999, judged by value, and a reason (ValidCancellationReason). Anything
// else is ErrInvalid; RecordedAt is the store's and is not judged.
func ValidateCancellation(rec Issue, c IssueCancellationRecord) error {
	if !validID(c.IssueID) || c.IssueID != rec.ID || !ValidProductRef(c.Ref) || !ValidProductTime(c.CancelledAt) || c.CancelledAt.Before(rec.ConfirmedAt) || !ValidCancellationReason(c.Reason) {
		return ErrInvalid
	}
	return nil
}
func ClaimValid(actual Job, claim Claim, now time.Time) bool {
	return actual.ID == claim.Job.ID && actual.Owner == claim.Job.Owner && actual.State == "running" && actual.Generation == claim.Job.Generation && actual.LeaseUntil.After(now)
}

// ValidatePublication holds what a job publishes to the job. A result's expiry is the one the job froze (Job.ResultEnd, ADR 0026): the
// read's own expiry unless the job keeps its result longer, and a file that is kept longer carries its own basis (carriesBasis).
func ValidatePublication(j Job, p Publication) error {
	switch j.Kind {
	case "snapshot":
		v := p.Snapshot
		if v == nil || p.Artifact != nil || !validID(v.ID) || v.Owner != j.Owner || !reflect.DeepEqual(v.Query, j.Query) || !reflect.DeepEqual(v.Prepared, j.Prepared) || !reflect.DeepEqual(v.Result.Columns, j.Columns) || !v.ExpiresAt.Equal(j.ResultEnd()) || v.SHA256 != Digest(v.Result) || v.Result.Completeness != "complete" || v.Result.Evidence.QueryHash != j.Prepared.QueryHash || v.Result.Evidence.SourceRef != j.Prepared.Ref || v.Result.Evidence.Watermark != j.Prepared.Watermark {
			return ErrIntegrity
		}
		// The evidence says where the data comes from in one of the three words (S08-A, ADR 0012 decision 6): a snapshot is never
		// stored with another, or with none.
		if !ValidDataClass(v.Result.Evidence.DataClass) {
			return ErrIntegrity
		}
		if len(v.Result.Rows) > MaxRows || validateRows(v.Result.Rows, j.Columns) != nil || validateTotals(v.Result.Totals, j.Query, j.Columns) != nil {
			return ErrIntegrity
		}
		b, err := json.Marshal(v.Result)
		if err != nil || len(b) > MaxResultBytes {
			return ErrBudget
		}
	case "export":
		v := p.Artifact
		if v == nil || p.Snapshot != nil || p.Issue != nil || !validID(v.ID) || v.Owner != j.Owner || v.SnapshotID != j.SnapshotID || v.Format != j.Format || !v.ExpiresAt.Equal(j.ResultEnd()) || !carriesBasis(j, *v) || !ValidArtifactBody(*v) || v.Issue != "" {
			return ErrIntegrity
		}
		// The artifact records the layout version the job's presentation named, and none when the job has none (S23-A).
		if layoutID, layoutVersion := layoutOf(j.Presentation); v.LayoutID != layoutID || v.LayoutVersion != layoutVersion {
			return ErrIntegrity
		}
	case "issue":
		v, rec := p.Artifact, p.Issue
		if v == nil || rec == nil || p.Snapshot != nil || !validID(v.ID) || v.Owner != j.Owner || v.SnapshotID != j.SnapshotID || v.Format != "pdf" || !v.ExpiresAt.Equal(j.ResultEnd()) || !carriesBasis(j, *v) || !ValidArtifactBody(*v) || v.Issue != j.ID || v.LayoutID != "" || v.LayoutVersion != 0 {
			return ErrIntegrity
		}
		// A record is published as issued: a cancellation is the product's later decision and a row of its own (S24-A), never part of
		// what a worker publishes.
		if rec.Cancellation != nil {
			return ErrIntegrity
		}
		if rec.ID != j.ID || rec.Owner != j.Owner || rec.TemplateID != j.TemplateID || rec.TemplateVersion != j.TemplateVersion || rec.TemplateSHA256 != j.TemplateSHA256 || rec.SnapshotID != j.SnapshotID || len(rec.SnapshotSHA256) != 64 || rec.RowIndex != j.RowIndex || !rec.Group.Equal(j.Group) || rec.ArtifactID != v.ID || rec.SHA256 != v.SHA256 || rec.Pages < 1 || rec.Pages > MaxIssuePages {
			return ErrIntegrity
		}
		// What the source product gave (S03-H): its number, the two references of its reservation
		// and confirmation (printable ASCII), the issue time and the confirmation time (never before
		// it), each a whole second between 1970 and 9999; the stamp is the one the number, the issue
		// time and the record id make.
		if !ValidIssueNumber(rec.Number) || !ValidProductRef(rec.ReservationRef) || !ValidProductRef(rec.ConfirmationRef) || !ValidProductTime(rec.IssuedAt) || !ValidProductTime(rec.ConfirmedAt) || rec.ConfirmedAt.Before(rec.IssuedAt) || rec.Stamp != IssueStamp(rec.Number, rec.IssuedAt, rec.ID) {
			return ErrIntegrity
		}
	default:
		return ErrIntegrity
	}
	return nil
}
