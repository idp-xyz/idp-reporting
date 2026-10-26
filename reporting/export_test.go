package reporting_test

import (
	"archive/zip"
	"bytes"
	"context"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"io"
	"strings"
	"testing"
	"time"
)

func TestExportPreservesPreciseTextAndNoFormulas(t *testing.T) {
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	a.ReplaceRecords([]contractsim.Record{{Company: "company_demo", Key: "r1", Values: []string{"=HYPERLINK(\"https://example.invalid\")", "current", "9007199254740993.01"}}})
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	j, e := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	j, e = s.Job(ctx, p, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	ex, e := s.SubmitExport(ctx, p, j.SnapshotID, "xls")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	ex, e = s.Job(ctx, p, ex.ID)
	if e != nil {
		t.Fatal(e)
	}
	file, e := s.Download(ctx, p, ex.ArtifactID)
	if e != nil {
		t.Fatal(e)
	}
	z, e := zip.NewReader(bytes.NewReader(file.Content), int64(len(file.Content)))
	if e != nil {
		t.Fatal(e)
	}
	var xml strings.Builder
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, ".xml") {
			body, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			b, e := io.ReadAll(body)
			body.Close()
			if e != nil {
				t.Fatal(e)
			}
			xml.Write(b)
		}
	}
	if !strings.Contains(xml.String(), "9007199254740993.01") || strings.Contains(xml.String(), "<f>") || strings.Contains(xml.String(), "<f ") {
		t.Fatal("precision lost or formula emitted")
	}
}
