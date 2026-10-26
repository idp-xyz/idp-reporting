// reporting-recovery-probe qualifies only the explicitly guarded local fixture.
// It is a test client, not a server or production administrative API.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/contractsim/pgsource"
	"idp.local/reporting/internal/fixturedb"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type process struct {
	PID        int
	StartTicks string
}
type asset struct{ SnapshotJob, Snapshot, ExportJob, Artifact, SHA256, Total, Cursor, SecondValue, SourceRef, Watermark string }
type proof struct {
	Status                                    string
	ProductionApproved                        bool
	PGStartBefore, PGStartAfter               string
	Processes                                 []process
	Existing                                  []asset
	Abandoned                                 r.Claim
	PendingSnapshot, PendingExport, Cancelled r.JobView
	Checks                                    []string
	OfflineHTTPStatus                         int
	RecoveredGeneration                       int64
}
type probe struct {
	base, token, state string
	client             *http.Client
	ctx                context.Context
	proof              proof
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "recovery probe failed:", e)
		os.Exit(1)
	}
}
func run() error {
	phase := flag.String("phase", "", "bootstrap/existing/queued/offline/verify/revoke/verify-revoked")
	state := flag.String("state", "", "private proof JSON path")
	flag.Parse()
	if os.Getenv("REPORTING_S01_REQUIRE_PG") != "1" || *state == "" {
		return fmt.Errorf("explicit required-PG fixture and proof path required")
	}
	base := os.Getenv("REPORTING_PROBE_URL")
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return r.ErrDenied
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	p := probe{base: strings.TrimRight(base, "/") + "/api/reporting/v1", token: os.Getenv("REPORTING_DEMO_TOKEN"), state: *state, client: &http.Client{Timeout: 25 * time.Second}, ctx: ctx}
	if len(p.token) < 32 {
		return r.ErrInvalid
	}
	if *phase != "bootstrap" {
		b, e := os.ReadFile(*state)
		if e != nil {
			return e
		}
		if json.Unmarshal(b, &p.proof) != nil {
			return r.ErrIntegrity
		}
	}
	switch *phase {
	case "bootstrap":
		e = p.bootstrap()
	case "existing":
		e = p.existing()
	case "queued":
		e = p.queued()
	case "offline":
		e = p.offline()
	case "verify":
		e = p.verify()
	case "revoke":
		e = p.revoke()
	case "verify-revoked":
		e = p.verifyRevoked()
	default:
		e = r.ErrInvalid
	}
	if e != nil {
		return e
	}
	b, e := json.MarshalIndent(p.proof, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(*state, append(b, '\n'), 0600); e != nil {
		return e
	}
	fmt.Printf("RECOVERY_PHASE_PASS=%s checks=%d\n", *phase, len(p.proof.Checks))
	return nil
}
func (p *probe) call(method, path string, payload any, key string) (int, http.Header, []byte, error) {
	var body io.Reader
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return 0, nil, nil, e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(p.ctx, method, p.base+path, body)
	if e != nil {
		return 0, nil, nil, e
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, e := p.client.Do(req)
	if e != nil {
		return 0, nil, nil, e
	}
	defer response.Body.Close()
	b, e := io.ReadAll(io.LimitReader(response.Body, r.MaxArtifactBytes+1))
	if e != nil {
		return 0, nil, nil, e
	}
	if len(b) > r.MaxArtifactBytes {
		return 0, nil, nil, r.ErrBudget
	}
	return response.StatusCode, response.Header, b, nil
}
func (p *probe) job(id string) (r.JobView, error) {
	code, _, b, e := p.call("GET", "/jobs/"+id, nil, "")
	if e != nil {
		return r.JobView{}, e
	}
	if code != 200 {
		return r.JobView{}, fmt.Errorf("job HTTP %d", code)
	}
	var j r.JobView
	e = json.Unmarshal(b, &j)
	return j, e
}
func (p *probe) wait(id string) (r.JobView, error) {
	for i := 0; i < 500; i++ {
		j, e := p.job(id)
		if e != nil {
			return j, e
		}
		if j.State == "succeeded" {
			return j, nil
		}
		if j.State == "failed" || j.State == "cancelled" {
			return j, fmt.Errorf("unexpected terminal job %s", j.State)
		}
		select {
		case <-p.ctx.Done():
			return j, p.ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return r.JobView{}, fmt.Errorf("job timeout")
}
func (p *probe) submit(path string, payload any, key string) (r.JobView, error) {
	code, _, b, e := p.call("POST", path, payload, key)
	if e != nil {
		return r.JobView{}, e
	}
	if code != 202 {
		return r.JobView{}, fmt.Errorf("admission HTTP %d", code)
	}
	var j r.JobView
	if json.Unmarshal(b, &j) != nil || j.State != "queued" {
		return j, r.ErrIntegrity
	}
	return j, nil
}
func (p *probe) rows(id, cursor string) (r.RowsPage, error) {
	path := "/snapshots/" + id + "/rows?limit=1"
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	code, _, b, e := p.call("GET", path, nil, "")
	if e != nil {
		return r.RowsPage{}, e
	}
	if code != 200 {
		return r.RowsPage{}, fmt.Errorf("rows HTTP %d", code)
	}
	var v r.RowsPage
	e = json.Unmarshal(b, &v)
	return v, e
}
func (p *probe) file(id, name string) (string, error) {
	code, h, b, e := p.call("GET", "/artifacts/"+id+"/content", nil, "")
	if e != nil {
		return "", e
	}
	if code != 200 || !strings.Contains(h.Get("Content-Type"), "spreadsheetml") {
		return "", fmt.Errorf("artifact HTTP %d", code)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(b))
	if sum != h.Get("X-Content-SHA256") {
		return "", r.ErrIntegrity
	}
	z, e := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if e != nil {
		return "", e
	}
	for _, f := range z.File {
		reader, e := f.Open()
		if e != nil {
			return "", e
		}
		_, e = io.Copy(io.Discard, reader)
		reader.Close()
		if e != nil {
			return "", e
		}
	}
	if e = os.WriteFile(filepath.Join(filepath.Dir(p.state), name), b, 0600); e != nil {
		return "", e
	}
	return sum, nil
}
func (p *probe) recordProcess() error {
	pid, e := strconv.Atoi(os.Getenv("REPORTING_PROBE_SERVER_PID"))
	if e != nil || pid <= 0 {
		return r.ErrInvalid
	}
	raw, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return e
	}
	end := strings.LastIndex(string(raw), ")")
	if end < 0 {
		return r.ErrIntegrity
	}
	fields := strings.Fields(string(raw)[end+1:])
	if len(fields) < 20 {
		return r.ErrIntegrity
	}
	v := process{PID: pid, StartTicks: fields[19]}
	for _, old := range p.proof.Processes {
		if old == v {
			return fmt.Errorf("HTTP server process was not restarted")
		}
	}
	p.proof.Processes = append(p.proof.Processes, v)
	return nil
}
func (p *probe) pgStart() (string, error) {
	pool, e := fixturedb.Open(p.ctx, os.Getenv("REPORTING_S01_TEST_DSN"), "reporting_test_s01", "reporting_test_s01_race")
	if e != nil {
		return "", e
	}
	defer pool.Close()
	var t time.Time
	e = pool.QueryRow(p.ctx, "SELECT pg_postmaster_start_time()").Scan(&t)
	return t.UTC().Format(time.RFC3339Nano), e
}
func (p *probe) bootstrap() error {
	repo, e := postgres.Open(p.ctx, os.Getenv("REPORTING_S01_TEST_DSN"), true)
	if e != nil {
		return e
	}
	defer repo.Close()
	source, e := pgsource.Open(p.ctx, os.Getenv("REPORTING_S01_SOURCE_DSN"), true)
	if e != nil {
		return e
	}
	defer source.Close()
	if e = repo.ResetFixture(p.ctx); e != nil {
		return e
	}
	if e = source.ResetFixture(p.ctx); e != nil {
		return e
	}
	p.proof = proof{Status: "RUNNING", ProductionApproved: false, Checks: []string{"isolated fixture initialized"}}
	return nil
}
func (p *probe) existing() error {
	if e := p.recordProcess(); e != nil {
		return e
	}
	start, e := p.pgStart()
	if e != nil {
		return e
	}
	p.proof.PGStartBefore = start
	for i, q := range []r.QuerySpec{contractsim.FinancialQuery(), contractsim.ShipmentQuery()} {
		j, e := p.submit("/snapshot-jobs", q, "initial")
		if e != nil {
			return e
		}
		j, e = p.wait(j.ID)
		if e != nil {
			return e
		}
		rows, e := p.rows(j.SnapshotID, "")
		if e != nil || rows.NextCursor == "" {
			return fmt.Errorf("initial rows/cursor: %w", e)
		}
		expected := "9007199254740993.01"
		second := "customer_b"
		if i == 1 {
			expected = "9007199254741000"
			second = "DE"
		}
		if rows.Totals[q.Metrics[0].ID] != expected {
			return r.ErrIntegrity
		}
		ex, e := p.submit("/exports", map[string]string{"snapshot_id": j.SnapshotID}, "initial")
		if e != nil {
			return e
		}
		ex, e = p.wait(ex.ID)
		if e != nil {
			return e
		}
		sum, e := p.file(ex.ArtifactID, fmt.Sprintf("recovery_before_%d.xlsx", i+1))
		if e != nil {
			return e
		}
		p.proof.Existing = append(p.proof.Existing, asset{SnapshotJob: j.ID, Snapshot: j.SnapshotID, ExportJob: ex.ID, Artifact: ex.ArtifactID, SHA256: sum, Total: expected, Cursor: rows.NextCursor, SecondValue: second, SourceRef: rows.Evidence.SourceRef, Watermark: rows.Evidence.Watermark})
	}
	p.proof.Checks = append(p.proof.Checks, "two completed HTTP snapshot/export chains before restart")
	return nil
}
func (p *probe) queued() error {
	if e := p.recordProcess(); e != nil {
		return e
	}
	if e := p.verifyExisting("process_restart"); e != nil {
		return e
	}
	j, e := p.submit("/snapshot-jobs", contractsim.FinancialQuery(), "abandoned")
	if e != nil {
		return e
	}
	repo, e := postgres.Open(p.ctx, os.Getenv("REPORTING_S01_TEST_DSN"), false)
	if e != nil {
		return e
	}
	defer repo.Close()
	claim, e := repo.Claim(p.ctx, time.Second)
	if e != nil || claim == nil || claim.Job.ID != j.ID {
		return fmt.Errorf("abandoned lease not claimed: %w", e)
	}
	p.proof.Abandoned = *claim
	p.proof.PendingSnapshot, e = p.submit("/snapshot-jobs", contractsim.FinancialQuery(), "queued")
	if e != nil {
		return e
	}
	p.proof.PendingExport, e = p.submit("/exports", map[string]string{"snapshot_id": p.proof.Existing[1].Snapshot}, "queued")
	if e != nil {
		return e
	}
	p.proof.Cancelled, e = p.submit("/snapshot-jobs", contractsim.FinancialQuery(), "cancelled")
	if e != nil {
		return e
	}
	code, _, b, e := p.call("POST", "/jobs/"+p.proof.Cancelled.ID+"/cancel", nil, "")
	if e != nil || code != 200 {
		return fmt.Errorf("cancel HTTP %d: %w", code, e)
	}
	if json.Unmarshal(b, &p.proof.Cancelled) != nil || p.proof.Cancelled.State != "cancelled" {
		return r.ErrIntegrity
	}
	for _, id := range []string{p.proof.PendingSnapshot.ID, p.proof.PendingExport.ID} {
		state, e := p.job(id)
		if e != nil || state.State != "queued" {
			return fmt.Errorf("paused worker executed queued job: %w", e)
		}
	}
	p.proof.Checks = append(p.proof.Checks, "queued snapshot/export, abandoned claim and cancellation persisted")
	return nil
}
func (p *probe) offline() error {
	code, h, b, e := p.call("GET", "/artifacts/"+p.proof.Existing[0].Artifact+"/content", nil, "")
	if e != nil {
		return e
	}
	if code < 400 || strings.Contains(h.Get("Content-Type"), "spreadsheetml") || len(b) > 1024 {
		return fmt.Errorf("database outage disclosed output")
	}
	var body map[string]string
	if json.Unmarshal(b, &body) != nil || body["error"] == "" {
		return r.ErrIntegrity
	}
	p.proof.OfflineHTTPStatus = code
	p.proof.Checks = append(p.proof.Checks, "actual PostgreSQL stop: existing artifact fails closed")
	return nil
}
func (p *probe) verifyExisting(label string) error {
	for i, a := range p.proof.Existing {
		sum, e := p.file(a.Artifact, fmt.Sprintf("recovery_%s_%d.xlsx", label, i+1))
		if e != nil || sum != a.SHA256 {
			return fmt.Errorf("immutable artifact continuity: %w", e)
		}
		rows, e := p.rows(a.Snapshot, a.Cursor)
		if e != nil || len(rows.Rows) != 1 || rows.Rows[0][0] != a.SecondValue || rows.Evidence.SourceRef != a.SourceRef || rows.Evidence.Watermark != a.Watermark {
			return fmt.Errorf("frozen cursor/source continuity: %w", e)
		}
	}
	return nil
}
func (p *probe) verify() error {
	if e := p.recordProcess(); e != nil {
		return e
	}
	start, e := p.pgStart()
	if e != nil {
		return e
	}
	if start == p.proof.PGStartBefore {
		return fmt.Errorf("PostgreSQL did not actually restart")
	}
	p.proof.PGStartAfter = start
	if e = p.verifyExisting("database_restart"); e != nil {
		return e
	}
	j, e := p.wait(p.proof.PendingSnapshot.ID)
	if e != nil {
		return e
	}
	p.proof.PendingSnapshot = j
	rows, e := p.rows(j.SnapshotID, "")
	if e != nil || rows.Totals["ar.open_balance"] != "9007199254740993.01" {
		return r.ErrIntegrity
	}
	ex, e := p.wait(p.proof.PendingExport.ID)
	if e != nil {
		return e
	}
	p.proof.PendingExport = ex
	if _, e = p.file(ex.ArtifactID, "recovery_pending_export.xlsx"); e != nil {
		return e
	}
	abandoned, e := p.wait(p.proof.Abandoned.Job.ID)
	if e != nil {
		return e
	}
	repo, e := postgres.Open(p.ctx, os.Getenv("REPORTING_S01_TEST_DSN"), false)
	if e != nil {
		return e
	}
	defer repo.Close()
	state, e := repo.Job(p.ctx, contractsim.DemoPrincipal().Identity, abandoned.ID)
	if e != nil || state.Generation != p.proof.Abandoned.Job.Generation+1 {
		return fmt.Errorf("abandoned generation did not advance: %w", e)
	}
	p.proof.RecoveredGeneration = state.Generation
	if !errors.Is(repo.Publish(p.ctx, p.proof.Abandoned, r.Publication{}), r.ErrLease) {
		return fmt.Errorf("old generation can publish")
	}
	cancelled, e := p.job(p.proof.Cancelled.ID)
	if e != nil || cancelled.State != "cancelled" || cancelled.SnapshotID != "" {
		return fmt.Errorf("cancel did not survive restart: %w", e)
	}
	code, _, b, e := p.call("POST", "/snapshot-jobs", contractsim.FinancialQuery(), "queued")
	if e != nil || code != 202 {
		return fmt.Errorf("replay HTTP %d: %w", code, e)
	}
	var replay r.JobView
	if json.Unmarshal(b, &replay) != nil || replay.ID != j.ID {
		return r.ErrIntegrity
	}
	p.proof.Checks = append(p.proof.Checks, "changed PostgreSQL start time and HTTP server process", "old files/cursors/source watermark survive restart", "queued snapshot/export complete", "abandoned generation reclaimed; old generation fenced", "cancelled job and idempotency survive restart")
	return nil
}
func (p *probe) revoke() error {
	source, e := pgsource.Open(p.ctx, os.Getenv("REPORTING_S01_SOURCE_DSN"), false)
	if e != nil {
		return e
	}
	defer source.Close()
	sc := r.SourceContext{Owner: contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), SourceTenant: "tenant_a"}
	if e = source.Revoke(p.ctx, sc); e != nil {
		return e
	}
	return p.denied()
}
func (p *probe) denied() error {
	a := p.proof.Existing[0]
	for _, path := range []string{"/artifacts/" + a.Artifact + "/content", "/snapshots/" + a.Snapshot + "/rows", "/jobs/" + a.SnapshotJob} {
		code, h, b, e := p.call("GET", path, nil, "")
		if e != nil {
			return e
		}
		if code != 403 || strings.Contains(h.Get("Content-Type"), "spreadsheetml") || len(b) > 1024 {
			return fmt.Errorf("revocation did not deny %s: %d", path, code)
		}
	}
	return nil
}
func (p *probe) verifyRevoked() error {
	if e := p.recordProcess(); e != nil {
		return e
	}
	if e := p.denied(); e != nil {
		return e
	}
	code, _, _, e := p.call("POST", "/snapshot-jobs", contractsim.FinancialQuery(), "after-revoke")
	if e != nil || code != 403 {
		return fmt.Errorf("startup seed restored revoked grant: HTTP %d", code)
	}
	sum, e := p.file(p.proof.Existing[1].Artifact, "recovery_other_product.xlsx")
	if e != nil || sum != p.proof.Existing[1].SHA256 {
		return fmt.Errorf("revocation crossed product namespace: %w", e)
	}
	p.proof.Checks = append(p.proof.Checks, "revocation persists across reinitializing server restart", "other product remains authorized")
	p.proof.Status = "PASS"
	return nil
}
