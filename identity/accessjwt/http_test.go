package accessjwt_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/identity/accessjwt"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// The signer is local and the sources are synthetic. This is a real TLS/HTTP
// protocol exercise, not evidence of a deployed identity provider or real ledger.
func TestJWTHTTPSnapshotExportAndRevocation(t *testing.T) {
	k := key(t)
	c := config(t, k)
	c.Bindings[0].Integrations = []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration}
	c.Bindings = append(c.Bindings,
		accessjwt.Binding{Subject: "subject-b", ClientID: "web-a", Tenant: "platform-a", Integrations: []string{contractsim.FinanceIntegration}},
		accessjwt.Binding{Subject: "subject-a", ClientID: "other-client", Tenant: "platform-b", Integrations: []string{contractsim.FinanceIntegration}})
	v, err := accessjwt.New(c)
	if err != nil {
		t.Fatal(err)
	}
	token := sign(t, k, claims())
	p, err := authenticate(v, token)
	if err != nil {
		t.Fatal(err)
	}
	finance, shipment := contractsim.Financial(), contractsim.Shipment()
	financeOwner, shipmentOwner := contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DemoOwner(p, contractsim.ShipmentIntegration)
	finance.Grant(financeOwner, contractsim.DefaultGrant())
	grant := contractsim.DefaultGrant()
	grant.CurrencyPolicy = nil
	shipment.Grant(shipmentOwner, grant)
	cursorKey := make([]byte, 32)
	if _, err := rand.Read(cursorKey); err != nil {
		t.Fatal(err)
	}
	s, err := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{
		{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance},
		{Tenant: p.Tenant, Integration: contractsim.ShipmentIntegration, SourceTenant: "tenant_a", Adapter: shipment},
	}, CursorKey: cursorKey, Writer: xlsx.Writer{}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := httpapi.Handler(s, v.Authenticate)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h)
	defer ts.Close()
	client := ts.Client()
	client.Timeout = 10 * time.Second
	request := func(method, path string, input any, bearer string, want int) ([]byte, http.Header) {
		t.Helper()
		var body []byte
		if input != nil {
			var err error
			body, err = json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(context.Background(), method, ts.URL+"/api/reporting/v1"+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "jwt-fixture-"+path)
		req.Header.Set("X-Tenant-ID", "attacker")
		req.Header.Set("X-User-ID", "administrator")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(io.LimitReader(res.Body, r.MaxArtifactBytes+1))
		if err != nil || len(data) > r.MaxArtifactBytes {
			t.Fatal("invalid bounded response")
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s status=%d want=%d body=%.160s", method, path, res.StatusCode, want, data)
		}
		if bytes.Contains(data, []byte(bearer)) {
			t.Fatal("response leaked token")
		}
		return data, res.Header
	}
	job := func(data []byte) r.JobView {
		t.Helper()
		var j r.JobView
		if json.Unmarshal(data, &j) != nil || j.ID == "" {
			t.Fatal("invalid job response")
		}
		return j
	}
	run := func() {
		t.Helper()
		ran, err := s.RunOne(context.Background())
		if err != nil || !ran {
			t.Fatalf("worker did not finish: %v", err)
		}
	}
	type output struct {
		Snapshot, Artifact, Job string
		SHA256                  string
		Bytes                   int
	}
	outputs := make(map[string]output)
	for _, tc := range []struct {
		name, total string
		query       r.QuerySpec
	}{
		{"finance", "9007199254740993.01", contractsim.FinancialQuery()},
		{"shipments", "9007199254741000", contractsim.ShipmentQuery()},
	} {
		data, _ := request("POST", "/snapshot-jobs", tc.query, token, 202)
		j := job(data)
		run()
		data, _ = request("GET", "/jobs/"+j.ID, nil, token, 200)
		done := job(data)
		if done.State != "succeeded" || done.SnapshotID == "" {
			t.Fatal("snapshot not published")
		}
		data, _ = request("GET", "/snapshots/"+done.SnapshotID+"/rows?limit=1", nil, token, 200)
		var page r.RowsPage
		if json.Unmarshal(data, &page) != nil || page.TotalRows < 2 || page.NextCursor == "" {
			t.Fatal("missing frozen pagination")
		}
		request("GET", "/snapshots/"+done.SnapshotID+"/rows?limit=1&cursor="+page.NextCursor, nil, token, 200)
		data, _ = request("POST", "/exports", map[string]string{"snapshot_id": done.SnapshotID}, token, 202)
		export := job(data)
		run()
		data, _ = request("GET", "/jobs/"+export.ID, nil, token, 200)
		export = job(data)
		if export.State != "succeeded" || export.ArtifactID == "" {
			t.Fatal("export not published")
		}
		file, headers := request("GET", "/artifacts/"+export.ArtifactID+"/content", nil, token, 200)
		if headers.Get("X-Reporting-Data-Class") != "contract-simulator" {
			t.Fatal("synthetic data class lost")
		}
		digest := sha256.Sum256(file)
		sum := hex.EncodeToString(digest[:])
		if sum != headers.Get("X-Content-SHA256") {
			t.Fatal("download digest mismatch")
		}
		checkWorkbook(t, file, tc.total)
		outputs[tc.name] = output{Snapshot: done.SnapshotID, Artifact: export.ArtifactID, Job: export.ID, SHA256: sum, Bytes: len(file)}
		if dir := os.Getenv("REPORTING_S02A_EVIDENCE_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "s02a_"+tc.name+".xlsx"), file, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	financial := outputs["finance"]
	logistics := outputs["shipments"]
	for _, change := range []func(map[string]any){func(c map[string]any) { c["sub"] = "subject-b" }, func(c map[string]any) { c["client_id"] = "other-client"; c["tenant"] = "platform-a" }} {
		other := claims()
		change(other)
		otherToken := sign(t, k, other)
		request("GET", "/jobs/"+financial.Job, nil, otherToken, 404)
		request("GET", "/snapshots/"+financial.Snapshot+"/rows", nil, otherToken, 404)
		request("GET", "/artifacts/"+financial.Artifact+"/content", nil, otherToken, 404)
	}
	wrong := claims()
	wrong["aud"] = "different-resource"
	_, headers := request("GET", "/artifacts/"+financial.Artifact+"/content", nil, sign(t, k, wrong), 401)
	if headers.Get("WWW-Authenticate") != `Bearer realm="reporting", error="invalid_token"` {
		t.Fatal("missing invalid token challenge")
	}
	wrong = claims()
	wrong["scope"] = "openid profile reporting.reader"
	_, headers = request("GET", "/artifacts/"+financial.Artifact+"/content", nil, sign(t, k, wrong), 403)
	if headers.Get("WWW-Authenticate") != `Bearer realm="reporting", error="insufficient_scope"` {
		t.Fatal("missing scope challenge")
	}
	finance.Revoke(financeOwner)
	denied, headers := request("GET", "/artifacts/"+financial.Artifact+"/content", nil, token, 403)
	if bytes.HasPrefix(denied, []byte("PK")) || strings.Contains(headers.Get("Content-Type"), "spreadsheet") {
		t.Fatal("revoked file disclosed")
	}
	request("GET", "/snapshots/"+financial.Snapshot+"/rows", nil, token, 403)
	request("GET", "/jobs/"+financial.Job, nil, token, 403)
	still, _ := request("GET", "/artifacts/"+logistics.Artifact+"/content", nil, token, 200)
	digest := sha256.Sum256(still)
	if hex.EncodeToString(digest[:]) != logistics.SHA256 {
		t.Fatal("unrelated product file changed")
	}
	c.Bindings = nil
	if err := v.Replace(c); err != nil {
		t.Fatal(err)
	}
	request("GET", "/artifacts/"+logistics.Artifact+"/content", nil, token, 401)
	if dir := os.Getenv("REPORTING_S02A_EVIDENCE_DIR"); dir != "" {
		evidence := map[string]any{"status": "PASS", "transport": "TLS loopback HTTPS", "identity": "locally signed RSA access JWT; no deployed IdP", "source": "two in-memory contract simulators", "outputs": outputs, "checks": []string{"signed JWT HTTP snapshot/export chains", "frozen pagination", "different subject and mapped tenant cannot read existing assets", "wrong audience rejected with 401", "missing exact scope rejected with 403", "source revocation blocks job/rows/file", "unrelated product file unchanged", "binding removal rejects subsequent authentication"}}
		data, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "http_checks.json"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func checkWorkbook(t *testing.T, data []byte, wantTotal string) {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) > 64 {
		t.Fatal("too many workbook entries")
	}
	found := false
	var expanded int64
	for _, f := range z.File {
		if f.UncompressedSize64 > 32<<20 {
			t.Fatal("oversized ZIP entry")
		}
		expanded += int64(f.UncompressedSize64)
		if expanded > 32<<20 {
			t.Fatal("expanded ZIP exceeds budget")
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(io.LimitReader(rc, 32<<20+1))
		_ = rc.Close()
		if err != nil || len(b) > 32<<20 {
			t.Fatal("workbook CRC/size failure")
		}
		if !strings.HasPrefix(f.Name, "xl/") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			token, err := d.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if start, ok := token.(xml.StartElement); ok && start.Name.Local == "f" {
				t.Fatal("unexpected workbook formula")
			}
			if text, ok := token.(xml.CharData); ok && string(text) == wantTotal {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("independent exact workbook total missing")
	}
}
