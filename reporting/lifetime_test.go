package reporting_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	r "idp.local/reporting/reporting"
)

// The three lifetimes of ADR 0026 as values: what the scope is without a declaration, how a result's expiry is made from the read and
// the class, and that nothing of it changes the encoding of a read or a job that keeps nothing beyond its read.
func TestAResultIsKeptItsClasssTimeAfterTheReadAndNeverBeyondTheScope(t *testing.T) {
	read := time.Date(2026, 11, 11, 7, 40, 0, 123456789, time.UTC)
	plain := r.PreparedRead{ExpiresAt: read}
	vouched := r.PreparedRead{ExpiresAt: read, ScopeExpiresAt: read.Add(3 * time.Hour)}
	if !plain.ScopeEnd().Equal(read) || !vouched.ScopeEnd().Equal(read.Add(3*time.Hour)) {
		t.Fatal("the scope is the read without a declaration, and the declaration with one", plain.ScopeEnd(), vouched.ScopeEnd())
	}
	for _, c := range []struct {
		name  string
		p     r.PreparedRead
		class time.Duration
		want  time.Time
	}{
		{"no scope: the read's own expiry, whatever the class", plain, 7 * 24 * time.Hour, read},
		{"a working result has no time beyond the read", vouched, 0, read},
		{"the class's time when the scope is longer", vouched, time.Hour, read.Add(time.Hour)},
		{"the scope when the class's time is longer", vouched, 10 * 365 * 24 * time.Hour, read.Add(3 * time.Hour)},
		{"the scope exactly", vouched, 3 * time.Hour, read.Add(3 * time.Hour)},
	} {
		if got := r.KeepUntil(c.p, c.class); !got.Equal(c.want) {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// What a job says of its result and of its deadline.
	working := r.Job{Kind: "snapshot", Prepared: vouched}
	export := r.Job{Kind: "export", Prepared: vouched, KeepUntil: read.Add(time.Hour)}
	issue := r.Job{Kind: "issue", Prepared: vouched, KeepUntil: read.Add(3 * time.Hour)}
	keptSnapshot := r.Job{Kind: "snapshot", Prepared: vouched, KeepUntil: read.Add(time.Hour)}
	for _, c := range []struct {
		name          string
		j             r.Job
		end, deadline time.Time
	}{
		{"a working snapshot ends with its read", working, read, read},
		{"an export is kept, and may publish while it would be served", export, read.Add(time.Hour), read.Add(time.Hour)},
		{"an issued file is kept, and the job needs the read", issue, read.Add(3 * time.Hour), read},
		{"a kept snapshot is kept, and the job needs the read", keptSnapshot, read.Add(time.Hour), read},
	} {
		if !c.j.ResultEnd().Equal(c.end) || !c.j.PublishDeadline().Equal(c.deadline) {
			t.Errorf("%s: result end %s, deadline %s", c.name, c.j.ResultEnd(), c.j.PublishDeadline())
		}
	}
	// When the payload of a finished job may be reduced: with its result when it succeeded, with its read when it made nothing.
	for _, c := range []struct {
		name  string
		j     r.Job
		state string
		want  time.Time
	}{
		{"a succeeded export waits for its file's time", export, "succeeded", read.Add(time.Hour)},
		{"a failed export keeps nothing", export, "failed", read},
		{"a cancelled export keeps nothing", export, "cancelled", read},
		{"a succeeded issue waits for its file's time", issue, "succeeded", read.Add(3 * time.Hour)},
		{"a failed issue keeps nothing", issue, "failed", read},
		{"a succeeded working snapshot goes with its read", working, "succeeded", read},
		{"a succeeded kept snapshot waits for its time", keptSnapshot, "succeeded", read.Add(time.Hour)},
	} {
		c.j.State = c.state
		if got := c.j.ReduceAfter(); !got.Equal(c.want) {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// Literally: a read that declares no scope is encoded as it was before the field existed, and one that declares a scope says so last.
	fixed := r.PreparedRead{Context: r.SourceContext{Owner: r.Owner{Identity: r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}, Integration: "n"}, SourceTenant: "st"},
		Ref: "r", ScopeHandle: "h", QueryHash: "q", Dataset: r.Ref{ID: "d", Version: "1"}, Companies: []string{"c"}, Timezone: "UTC", Watermark: "w", ExpiresAt: read}
	const was = `{"Context":{"Owner":{"Tenant":"t","Issuer":"i","Subject":"s","Integration":"n"},"SourceTenant":"st"},"Ref":"r","ScopeHandle":"h","QueryHash":"q","Dataset":{"id":"d","version":"1"},"Companies":["c"],"Timezone":"UTC","CurrencyPolicy":null,"Watermark":"w","ExpiresAt":"2026-11-11T07:40:00.123456789Z"`
	if b, _ := json.Marshal(fixed); string(b) != was+"}" {
		t.Errorf("a read without a scope is encoded as\n%s\nwant\n%s", b, was+"}")
	}
	fixed.ScopeExpiresAt = read.Add(3 * time.Hour)
	if b, _ := json.Marshal(fixed); string(b) != was+`,"ScopeExpiresAt":"2026-11-11T10:40:00.123456789Z"}` {
		t.Errorf("a read with a scope is encoded as\n%s", b)
	}
	// A read without a scope, and a job and an artifact that keep nothing beyond the read, are encoded as they were before the fields existed.
	for name, v := range map[string]any{"read": plain, "job": working, "artifact": r.Artifact{ExpiresAt: read}} {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		for _, key := range []string{"ScopeExpiresAt", "KeepUntil", `"Query":null`, `"Prepared":null`} {
			if name == "job" && key == "ScopeExpiresAt" {
				continue // this job's read declares a scope
			}
			if strings.Contains(string(b), key) {
				t.Errorf("the %s is encoded with %s: %s", name, key, b)
			}
		}
	}
	if b, _ := json.Marshal(vouched); !strings.Contains(string(b), `"ScopeExpiresAt":"2026-11-11T10:40:00.123456789Z"`) {
		t.Errorf("a declared scope is not in the read's encoding: %s", b)
	}
}

// A job, a snapshot and an artifact that keep nothing beyond their read are stored as the bytes they were before ADR 0026, so that for a
// source that declares no scope neither a stored row nor its digest changes. The literals were written by the code before it (the same
// values, encoded on main at bfb0613), not derived from this code.
func TestAJobASnapshotAndAnArtifactThatKeepNothingAreEncodedAsBefore(t *testing.T) {
	const (
		ownerWas    = `{"Tenant":"t","Issuer":"i","Subject":"s","Integration":"n"}`
		queryWas    = `{"schema_version":"1.0","integration_id":"n","dataset":{"id":"d","version":"1"},"company_ids":["c"],"metrics":[{"id":"m","version":"1"}],"dimensions":["x"],"filters":[{"field":"x","operator":"eq","value":"v"}],"sort":[],"as_of":"2026-11-11","timezone":"UTC","page":{"limit":50}}`
		atWas       = `"2026-11-11T07:40:00.123456789Z"`
		preparedWas = `{"Context":{"Owner":` + ownerWas + `,"SourceTenant":"st"},"Ref":"r","ScopeHandle":"h","QueryHash":"q","Dataset":{"id":"d","version":"1"},"Companies":["c"],"Timezone":"UTC","CurrencyPolicy":null,"Watermark":"w","ExpiresAt":` + atWas + `}`
		columnsWas  = `[{"id":"x","label":"X","type":"text"}]`
		restWas     = `"ArtifactID":"","ErrorCode":"","Generation":0,"LeaseUntil":"0001-01-01T00:00:00Z"}`
	)
	owner := r.Owner{Identity: r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}, Integration: "n"}
	read := time.Date(2026, 11, 11, 7, 40, 0, 123456789, time.UTC)
	query := r.QuerySpec{SchemaVersion: "1.0", Integration: "n", Dataset: r.Ref{ID: "d", Version: "1"}, Companies: []string{"c"}, Metrics: []r.Ref{{ID: "m", Version: "1"}}, Dimensions: []string{"x"},
		Filters: []r.Filter{{Field: "x", Operator: "eq", Value: "v"}}, Sort: []r.Sort{}, AsOf: "2026-11-11", Timezone: "UTC", Page: r.PageSpec{Limit: 50}}
	prepared := r.PreparedRead{Context: r.SourceContext{Owner: owner, SourceTenant: "st"}, Ref: "r", ScopeHandle: "h", QueryHash: "q", Dataset: r.Ref{ID: "d", Version: "1"}, Companies: []string{"c"}, Timezone: "UTC", Watermark: "w", ExpiresAt: read}
	columns := []r.Column{{ID: "x", Label: "X", Type: "text"}}
	for name, c := range map[string]struct {
		v   any
		was string
	}{
		"a snapshot job": {r.Job{ID: "j1", Owner: owner, Kind: "snapshot", State: "queued", Key: "k", RequestHash: "h", Query: query, Prepared: prepared, Columns: columns},
			`{"ID":"j1","Owner":` + ownerWas + `,"Kind":"snapshot","State":"queued","Key":"k","RequestHash":"h","Query":` + queryWas + `,"Prepared":` + preparedWas + `,"Columns":` + columnsWas + `,"SnapshotID":"",` + restWas},
		"an export job": {r.Job{ID: "j2", Owner: owner, Kind: "export", State: "queued", Key: "k", RequestHash: "h", Query: query, Prepared: prepared, SnapshotID: "p", Format: "pdf"},
			`{"ID":"j2","Owner":` + ownerWas + `,"Kind":"export","State":"queued","Key":"k","RequestHash":"h","Query":` + queryWas + `,"Prepared":` + preparedWas + `,"Columns":null,"SnapshotID":"p","Format":"pdf",` + restWas},
		"a snapshot": {r.Snapshot{ID: "p", Owner: owner, Query: query, Prepared: prepared, Result: r.Result{Columns: columns, Rows: [][]string{{"v"}}, Totals: map[string]string{}, Completeness: "complete", Issues: []string{},
			Evidence: r.Evidence{QueryHash: "q", SourceRef: "r", Watermark: "w", DataClass: "contract-simulator"}}, ExpiresAt: read, SHA256: "0"},
			`{"ID":"p","Owner":` + ownerWas + `,"Query":` + queryWas + `,"Prepared":` + preparedWas + `,"Result":{"columns":` + columnsWas + `,"rows":[["v"]],"totals":{},"completeness":"complete","issues":[],"evidence":{"query_hash":"q","source_snapshot_ref":"r","source_watermark":"w","data_class":"contract-simulator"}},"ExpiresAt":` + atWas + `,"SHA256":"0"}`},
		"an artifact that holds its file": {r.Artifact{ID: "a", Owner: owner, SnapshotID: "p", Format: "pdf", ExpiresAt: read, SHA256: "0", Content: []byte("x")},
			`{"ID":"a","Owner":` + ownerWas + `,"SnapshotID":"p","Format":"pdf","ExpiresAt":` + atWas + `,"SHA256":"0","Content":"eA=="}`},
		"an issued artifact whose file is an object": {r.Artifact{ID: "a", Owner: owner, SnapshotID: "p", Format: "pdf", ExpiresAt: read, SHA256: "0", Object: "artifacts/a", Size: 1, Issue: "j3"},
			`{"ID":"a","Owner":` + ownerWas + `,"SnapshotID":"p","Format":"pdf","ExpiresAt":` + atWas + `,"SHA256":"0","Content":null,"Object":"artifacts/a","Size":1,"Issue":"j3"}`},
	} {
		if b, e := json.Marshal(c.v); e != nil || string(b) != c.was {
			t.Errorf("%s is encoded as\n%s\nand was\n%s (%v)", name, b, c.was, e)
		}
	}
}

func TestTheRetentionTimesAreBounded(t *testing.T) {
	got, e := r.Retention{}.Resolved()
	if e != nil || got.Results != 7*24*time.Hour || got.Issued != 3650*24*time.Hour {
		t.Fatal("the defaults are seven days and ten years", got, e)
	}
	if got, e = (r.Retention{Results: time.Minute, Issued: 36500 * 24 * time.Hour}).Resolved(); e != nil || got.Results != time.Minute || got.Issued != 36500*24*time.Hour {
		t.Fatal("the bounds themselves are allowed", got, e)
	}
	for name, v := range map[string]r.Retention{
		"results under a minute": {Results: time.Second},
		"results over 366 days":  {Results: 367 * 24 * time.Hour},
		"issued under a minute":  {Issued: 59 * time.Second},
		"issued over 100 years":  {Issued: 36501 * 24 * time.Hour},
		"a negative time":        {Results: -time.Hour},
	} {
		if _, e := v.Resolved(); !errors.Is(e, r.ErrInvalid) {
			t.Errorf("%s: %v", name, e)
		}
	}
}
