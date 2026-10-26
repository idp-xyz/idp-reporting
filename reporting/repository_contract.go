package reporting

import (
	"encoding/json"
	"reflect"
	"time"
)

func ValidateAdmission(j Job) error {
	if !validID(j.ID) || !j.Owner.Identity.Valid() || j.Owner.Integration == "" || !validKey(j.Key) || j.State != "queued" || j.Generation != 0 || j.RequestHash == "" || j.Prepared.Context.Owner != j.Owner || j.Prepared.QueryHash != QueryHash(j.Query) {
		return ErrInvalid
	}
	switch j.Kind {
	case "snapshot":
		if j.SnapshotID != "" || j.RequestHash != Digest(Normalize(j.Query)) {
			return ErrInvalid
		}
	case "export":
		if !validID(j.SnapshotID) || j.RequestHash != Digest(j.SnapshotID) {
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
		if v == nil || p.Snapshot != nil || !validID(v.ID) || v.Owner != j.Owner || v.SnapshotID != j.SnapshotID || !v.ExpiresAt.Equal(j.Prepared.ExpiresAt) || len(v.Content) == 0 || len(v.Content) > MaxArtifactBytes || v.SHA256 != bytesDigest(v.Content) {
			return ErrIntegrity
		}
	default:
		return ErrIntegrity
	}
	return nil
}
