//go:build linux

package pdf

// A schema 2.0 template issued by group through the whole stack, with the real issuer, the real writer and the real
// pinned Typst (S03-K): the service freezes the group, the worker checks it, the contract simulator numbers and
// confirms it, the real issuer binds the group of the frozen snapshot and the writer renders it with the stamp on every
// page. A subtest of TestTypstChecksIssuesBeforeTheProductIsAsked, so that it skips and fails with it. With
// REPORTING_PDF_EVIDENCE set the issued documents are written with the record the service kept for them, which
// scripts/check_pdf_export.py verifies the PDF against.

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/issuance"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// statementOfAccount is a 2.0 template of the finance fixture's dataset: a page header with a title and the page number,
// a document header with the customer (the group field), the group's lines in a table, a document footer with the sum
// of the group's balances, and a page footer.
func statementOfAccount() doctemplates.Document {
	d := banded(doctemplates.Bands{
		PageHeader: fixedBand(14,
			bolded(textItem("e1", "literal", "Statement of account 对账单", 0, 0, 120, 9, 16, "left")),
			textItem("e2", "page", "Page {page} of {pages}", 130, 0, 60, 9, 10, "right"),
			lineItem("e3", 0, 12, 190, 12, 0.5)),
		DocumentHeader: fixedBand(14,
			textItem("e4", "literal", "Customer 客户", 0, 2, 40, 7, 10, "left"),
			bolded(textItem("e5", "field", "customer.id", 42, 2, 100, 8, 12, "left"))),
		Detail: detailOf(tableItem("e6", 0, 190, 9,
			doctemplates.ItemColumn{Field: "aging.bucket", Width: 60},
			doctemplates.ItemColumn{Field: "customer.id", Width: 60, Label: "Customer"},
			doctemplates.ItemColumn{Field: "ar.open_balance", Width: 70, Align: "right"})),
		DocumentFooter: fixedBand(18,
			rectItem("e7", 100, 2, 90, 10, 0.5, "grey2"),
			bolded(textItem("e8", "group_sum", "ar.open_balance", 102, 3, 86, 8, 12, "right"))),
		PageFooter: fixedBand(10, textItem("e9", "literal", "Issued by IDP Reporting", 0, 2, 120, 6, 8, "left")),
	})
	d.Binding = doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: contractsim.FinancialQuery().Dataset}
	d.Group = &doctemplates.GroupSpec{Fields: []string{"customer.id"}}
	return d
}

func serviceIssuesGroups(t *testing.T, w *Writer) {
	ctx := t.Context()
	p := contractsim.DemoPrincipal()
	finance := contractsim.Financial()
	owner := contractsim.DemoOwner(p, contractsim.FinanceIntegration)
	// The product's data: three customers, one of them with 150 lines, which take several pages.
	grant := contractsim.DefaultGrant()
	grant.Rows = nil
	var records []contractsim.Record
	add := func(customer, bucket, amount string) {
		key := fmt.Sprintf("k%03d", len(records))
		grant.Rows = append(grant.Rows, key)
		records = append(records, contractsim.Record{Company: "company_demo", Key: key, Values: []string{customer, bucket, amount}})
	}
	add("华东贸易有限公司", "current", "100.25")
	add("Aurora Trading Ltd", "1_30", "-0.24")
	add("华东贸易有限公司", "31_60", "9007199254740893.00")
	for i := range 150 {
		add("北方物流（集团）", fmt.Sprintf("%d_%d", 30*(i%4), 30*(i%4)+30), fmt.Sprintf("%d.%02d", 100*i+7, (i*37)%100))
	}
	finance.ReplaceRecords(records)
	finance.Grant(owner, grant)
	// The product's clock is fixed: the issue time, and so the file, is the same on every run.
	issuedAt := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
	finance.SetClock(func() time.Time { return issuedAt })
	store, err := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	issuer, err := issuance.New(store, nil, w)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.NewService(r.Config{Repository: memory.New(func() time.Time { return issuedAt }), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": w}, Issuer: issuer, Now: func() time.Time { return issuedAt }})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	if frozen, err = s.Job(ctx, p, frozen.ID); err != nil || frozen.SnapshotID == "" {
		t.Fatal(frozen, err)
	}
	snap, err := s.AuthorizedSnapshot(ctx, p, frozen.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	d := statementOfAccount()
	saved, err := store.Save(p.Identity, "", 0, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Publish(p.Identity, saved.ID, "publish", 1, 0); err != nil {
		t.Fatal(err)
	}
	canonical, err := doctemplates.Canonical(saved.Layout)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := doctemplates.Groups(d, snap)
	if err != nil || len(groups) != 3 || len(groups[2].Rows) != 150 {
		t.Fatalf("groups %+v %v", groups, err)
	}
	for _, c := range []struct {
		name  string
		group int
		many  bool
	}{{"banded_issue_service_two_lines", 0, false}, {"banded_issue_service_one_line", 1, false}, {"banded_issue_service_multipage", 2, true}} {
		t.Run(c.name, func(t *testing.T) {
			rg := groups[c.group]
			g, err := r.NewIssueGroup(snap, []string{"customer.id"}, rg.Key, rg.Rows)
			if err != nil {
				t.Fatal(err)
			}
			// The check the worker runs before the product is asked accepts the group, without Typst.
			values, table, err := doctemplates.BindGroup(d, snap, rg)
			if err != nil {
				t.Fatal(err)
			}
			if err = w.CheckIssue(ctx, d, values, nil, table); err != nil {
				t.Fatal(err)
			}
			job, err := s.SubmitIssue(ctx, p, r.IssueInput{TemplateID: saved.ID, TemplateVersion: 1, Template: canonical, SnapshotID: snap.ID, Group: &g}, c.name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.RunOne(ctx); err != nil {
				var re *RenderError
				if errors.As(err, &re) {
					t.Fatalf("%v\n%s", err, re.output())
				}
				t.Fatal(err)
			}
			done, err := s.Job(ctx, p, job.ID)
			if err != nil || done.State != "succeeded" || done.Issue == nil || done.Issue.Group == nil || done.Issue.RowIndex != nil || done.Issue.Group.RowCount != len(rg.Rows) {
				t.Fatalf("%+v %v", done, err)
			}
			rec, err := s.Issue(ctx, p, job.ID)
			if err != nil || rec.Group == nil || rec.Group.Key[0] != rg.Key[0] || rec.Number != done.Issue.Number || (c.many && rec.Pages < 3) || (!c.many && rec.Pages != 1) {
				t.Fatalf("%+v %v", rec, err)
			}
			art, err := s.Download(ctx, p, done.ArtifactID)
			if err != nil || !bytes.HasPrefix(art.Content, []byte("%PDF-")) || art.SHA256 != rec.SHA256 || !bytes.Contains(art.Content, infoCreation(issuedAt)) {
				t.Fatalf("%v", err)
			}
			// The real issuer and writer give the same bytes for the same job again.
			stored := r.Job{ID: job.ID, Owner: snap.Owner, Kind: "issue", TemplateID: saved.ID, TemplateVersion: 1, Template: string(canonical), SnapshotID: snap.ID, Group: &g}
			again, err := issuer.Issue(ctx, r.IssueRequest{Job: stored, Snapshot: snap, Stamp: rec.Stamp, At: issuedAt})
			if err != nil || !bytes.Equal(again.PDF, art.Content) || again.Pages != rec.Pages {
				t.Fatalf("a job issued again gives other bytes (%v)", err)
			}
			if strings.Contains(string(art.Content), "NOT AN ISSUED") {
				t.Error("the issued file says it is not an issued document")
			}
			preview, err := w.Render(ctx, d, values, nil, table)
			if err != nil || bytes.Equal(preview, art.Content) {
				t.Fatalf("the issue is the preview (%v)", err)
			}
			sums := groupSums(d, snap, rg)
			bandedEvidence(t, c.name, art.Content, d, values, table, sums, w)
			issueEvidence(t, c.name, art.Content, preview, rec.Number, issuedAt, job.ID, rec.Pages)
		})
	}
}
