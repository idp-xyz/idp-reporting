package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

type fakePDF struct{}

func (fakePDF) Write(context.Context, r.Snapshot) ([]byte, error) {
	return []byte("%PDF-1.7 fake"), nil
}

func formatSetup(t *testing.T, formats map[string]r.WorkbookWriter) (*r.Service, http.Handler, string) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}, Formats: formats})
	if e != nil {
		t.Fatal(e)
	}
	auth, e := httpapi.DemoBearer(secret, p)
	if e != nil {
		t.Fatal(e)
	}
	h, e := httpapi.Handler(s, auth)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(contractsim.FinancialQuery())
	w := request(h, "POST", "/api/reporting/v1/snapshot-jobs", b, true)
	var j r.JobView
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &j) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return s, h, finish(t, s, h, j.ID).SnapshotID
}

func finish(t *testing.T, s *r.Service, h http.Handler, id string) r.JobView {
	t.Helper()
	if _, e := s.RunOne(context.Background()); e != nil {
		t.Fatal(e)
	}
	w := request(h, "GET", "/api/reporting/v1/jobs/"+id, nil, true)
	var j r.JobView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &j) != nil || j.State != "succeeded" {
		t.Fatal(w.Code, w.Body.String())
	}
	return j
}

func exportRequest(h http.Handler, key string, body map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	q := httptest.NewRequest("POST", "/api/reporting/v1/exports", bytes.NewReader(b))
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("Idempotency-Key", key)
	q.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, q)
	return w
}

func TestHTTPServesEachExportFormatWithItsMediaType(t *testing.T) {
	s, h, snapshot := formatSetup(t, map[string]r.WorkbookWriter{"pdf": fakePDF{}})
	w := request(h, "GET", "/api/reporting/v1/capabilities", nil, true)
	var caps struct{ Formats []string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &caps) != nil || !slices.Equal(caps.Formats, []string{"xlsx", "pdf"}) {
		t.Fatalf("capabilities: %d %s", w.Code, w.Body.String())
	}
	for _, c := range []struct {
		body             map[string]string
		format           string
		media, extension string
	}{
		{map[string]string{"snapshot_id": snapshot, "format": "pdf"}, "pdf", "application/pdf", "pdf"},
		{map[string]string{"snapshot_id": snapshot, "format": "xlsx"}, "", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx"},
		{map[string]string{"snapshot_id": snapshot}, "", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx"},
	} {
		w := exportRequest(h, "key-"+c.body["format"], c.body)
		var j r.JobView
		if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &j) != nil || j.Format != c.format {
			t.Fatalf("%v: %d %s", c.body, w.Code, w.Body.String())
		}
		j = finish(t, s, h, j.ID)
		if j.Format != c.format || j.ArtifactID == "" {
			t.Fatalf("%v: finished job %+v", c.body, j)
		}
		w = request(h, "GET", "/api/reporting/v1/artifacts/"+j.ArtifactID+"/content", nil, true)
		sum := sha256.Sum256(w.Body.Bytes())
		if w.Code != 200 || w.Header().Get("Content-Type") != c.media || w.Header().Get("Content-Disposition") != `attachment; filename="report-`+j.ArtifactID+`.`+c.extension+`"` || w.Header().Get("X-Content-SHA256") != hex.EncodeToString(sum[:]) {
			t.Errorf("%v: %d %v", c.body, w.Code, w.Header())
		}
	}
}

func TestHTTPRefusesToServeAnArtifactOfAnUnknownFormat(t *testing.T) {
	// The service accepts any well-formed format name; HTTP serves only the ones it knows.
	s, h, snapshot := formatSetup(t, map[string]r.WorkbookWriter{"csv": fakePDF{}})
	w := exportRequest(h, "csv", map[string]string{"snapshot_id": snapshot, "format": "csv"})
	var j r.JobView
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &j) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	j = finish(t, s, h, j.ID)
	w = request(h, "GET", "/api/reporting/v1/artifacts/"+j.ArtifactID+"/content", nil, true)
	if w.Code != 409 || w.Header().Get("Content-Disposition") != "" {
		t.Fatalf("an artifact of format csv: %d %v; want 409 without a file", w.Code, w.Header())
	}
}

func TestHTTPRefusesFormatsTheServiceDoesNotOffer(t *testing.T) {
	_, h, snapshot := formatSetup(t, nil)
	w := request(h, "GET", "/api/reporting/v1/capabilities", nil, true)
	var caps struct{ Formats []string }
	if json.Unmarshal(w.Body.Bytes(), &caps) != nil || !slices.Equal(caps.Formats, []string{"xlsx"}) {
		t.Fatalf("capabilities without PDF: %s", w.Body.String())
	}
	for i, format := range []string{"pdf", "PDF", "doc", "../pdf"} {
		w := exportRequest(h, "refused-"+string(rune('a'+i)), map[string]string{"snapshot_id": snapshot, "format": format})
		if w.Code != 422 {
			t.Errorf("format %q: %d %s; want 422", format, w.Code, w.Body.String())
		}
	}
}
