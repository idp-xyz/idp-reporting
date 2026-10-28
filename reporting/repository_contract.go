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

func ValidateAdmission(j Job) error {
	if !validID(j.ID) || !j.Owner.Identity.Valid() || j.Owner.Integration == "" || !validKey(j.Key) || j.State != "queued" || j.Generation != 0 || j.RequestHash == "" || j.Prepared.Context.Owner != j.Owner || j.Prepared.QueryHash != QueryHash(j.Query) {
		return ErrInvalid
	}
	switch j.Kind {
	case "snapshot":
		if j.SnapshotID != "" || j.Format != "" || j.RequestHash != Digest(Normalize(j.Query)) {
			return ErrInvalid
		}
	case "export":
		if !validID(j.SnapshotID) || !validFormat(j.Format) || j.RequestHash != ExportRequestHash(j.SnapshotID, j.Format) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func ClaimValid(actual Job, claim Claim, now time.Time) bool {
	return actual.ID == claim.Job.ID && actual.Owner == claim.Job.Owner && actual.State == "running" && actual.Generation == claim.Job.Generation && actual.LeaseUntil.After(now)
}
func ValidatePublication(j Job, p Publication) error {
	switch j.Kind {
	case "snapshot":
		v := p.Snapshot
		if v == nil || p.Artifact != nil || !validID(v.ID) || v.Owner != j.Owner || !reflect.DeepEqual(v.Query, j.Query) || !reflect.DeepEqual(v.Prepared, j.Prepared) || !reflect.DeepEqual(v.Result.Columns, j.Columns) || !v.ExpiresAt.Equal(j.Prepared.ExpiresAt) || v.SHA256 != Digest(v.Result) || v.Result.Completeness != "complete" || v.Result.Evidence.QueryHash != j.Prepared.QueryHash || v.Result.Evidence.SourceRef != j.Prepared.Ref || v.Result.Evidence.Watermark != j.Prepared.Watermark {
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
		if v == nil || p.Snapshot != nil || !validID(v.ID) || v.Owner != j.Owner || v.SnapshotID != j.SnapshotID || v.Format != j.Format || !v.ExpiresAt.Equal(j.Prepared.ExpiresAt) || len(v.Content) == 0 || len(v.Content) > MaxArtifactBytes || v.SHA256 != bytesDigest(v.Content) {
			return ErrIntegrity
		}
	default:
		return ErrIntegrity
	}
	return nil
}
