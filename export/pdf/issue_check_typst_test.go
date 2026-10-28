//go:build linux

package pdf

// The check that runs before the source product is asked for a number (S03-H), against the real
// writer: Writer.CheckIssue must refuse exactly what Writer.Issue refuses for the layout, with the
// same errors, without Typst; and through the service a document the layout refuses, or a template
// withdrawn while the job waited, costs the product no reservation. These run with the pinned Typst
// binary and fonts, as the other Typst tests do.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
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

// errorClass names which of the platform's errors an error is, so that two refusals can be compared
// by what they say and not by their wording.
func errorClass(err error) string {
	if err == nil {
		return "ok"
	}
	for _, known := range []error{r.ErrInvalid, r.ErrUnsupported, r.ErrIntegrity, r.ErrBudget, r.ErrDenied, r.ErrNotFound} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "other: " + err.Error()
}

func TestTypstChecksIssuesBeforeTheProductIsAsked(t *testing.T) {
	w := typstWriter(t, nil)
	t.Run("check_refuses_what_issue_refuses_for_the_layout", func(t *testing.T) { checkRefusesWhatIssueRefuses(t, w) })
	t.Run("the_service_asks_the_product_only_after_the_check", func(t *testing.T) { serviceChecksBeforeTheProduct(t, w) })
}

// checkRefusesWhatIssueRefuses: a table of documents, each given to CheckIssue and to Issue with a good
// stamp and issue time. They refuse the same, with the same error, or both accept; the refusals are the
// layout's, found before any Typst process runs. Issue's own arguments (the stamp, the issue time) are
// not the check's: a bad stamp or time fails Issue alone.
func checkRefusesWhatIssueRefuses(t *testing.T, w *Writer) {
	png, err := os.ReadFile("../../doctemplates/testdata/a4-background.png")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(png)
	background := &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: hex.EncodeToString(sum[:])}
	columns := []doctemplates.TableColumn{{Field: "customer", Width: 40}, {Field: "note", Width: 80}, {Field: "amount", Width: 60}}
	statement := func(mode string, change func(*doctemplates.Document)) doctemplates.Document {
		d := doctemplates.Document{Schema: "1.3", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
			Elements: []doctemplates.Element{
				{ID: "n1", Source: "literal", Text: "对账单 Statement", X: 20, Y: 30, Width: 120, Height: 12, FontSize: 14, Align: "left"},
				{ID: "n2", Source: "row", Field: "customer", X: 20, Y: 46, Width: 80, Height: 8, FontSize: 10, Align: "left"},
			},
			Print: &doctemplates.PrintProfile{Mode: mode},
			Table: &doctemplates.Table{X: 15, Y: 60, Width: 180, Height: 200, FontSize: 9, Columns: columns}}
		if change != nil {
			change(&d)
		}
		return d
	}
	single := func(change func(*doctemplates.Document)) doctemplates.Document {
		d := singleBox()
		if change != nil {
			change(&d)
		}
		return d
	}
	values := map[string]string{"n1": "对账单 Statement", "n2": "华东区客户"}
	oneValue := func(v string) map[string]string { return map[string]string{"n1": v} }
	rows := func(n int, change func(*doctemplates.TableData)) *doctemplates.TableData {
		data := statementRows(n)
		if change != nil {
			change(data)
		}
		return data
	}
	withBackground := func(mode string) doctemplates.Document {
		return statement(mode, func(d *doctemplates.Document) { d.Background = background })
	}
	cases := []struct {
		name       string
		d          doctemplates.Document
		values     map[string]string
		background []byte
		table      *doctemplates.TableData
		want       error // nil: both accept
	}{
		{"a value that fits", single(nil), oneValue("华东区客户"), nil, nil, nil},
		{"a statement with a table that fits", statement("full", nil), values, nil, rows(60, nil), nil},
		{"a statement on the background it names", withBackground("full"), values, png, rows(8, nil), nil},
		{"variables mode on the background it names", withBackground("variables"), values, nil, rows(8, nil), nil},
		{"the banner text as a literal in variables mode", statement("variables", func(d *doctemplates.Document) { d.Elements[0].Text = bannerText }), map[string]string{"n1": bannerText, "n2": "x"}, nil, rows(3, nil), nil},
		{"a value wider than its box", single(func(d *doctemplates.Document) { d.Elements[0].Width = 20 }), oneValue(strings.Repeat("宽", 20)), nil, nil, r.ErrUnsupported},
		{"a character the fonts lack", single(nil), oneValue("x 😀"), nil, nil, r.ErrUnsupported},
		{"a soft hyphen", single(nil), oneValue("a­b"), nil, nil, r.ErrUnsupported},
		{"a control character", single(nil), oneValue("a\x7fb"), nil, nil, r.ErrUnsupported},
		{"a value over 256 characters", single(nil), oneValue(strings.Repeat("a", 257)), nil, nil, r.ErrUnsupported},
		{"invalid UTF-8", single(nil), oneValue("a\xffb"), nil, nil, r.ErrInvalid},
		{"a missing value", single(nil), map[string]string{}, nil, nil, r.ErrInvalid},
		{"the banner text as a value", single(nil), oneValue("x " + bannerText), nil, nil, r.ErrUnsupported},
		{"the banner text as a table cell", statement("full", nil), values, nil, rows(3, func(d *doctemplates.TableData) { d.Rows[1][1] = bannerText }), r.ErrUnsupported},
		{"an amount wider than its column", statement("full", func(d *doctemplates.Document) {
			d.Table.Columns = []doctemplates.TableColumn{{Field: "customer", Width: 40}, {Field: "note", Width: 128}, {Field: "amount", Width: 12}}
		}), values, nil, rows(60, nil), r.ErrUnsupported},
		{"a row taller than the table's pages", statement("full", func(d *doctemplates.Document) { d.Table.Height = 30 }), values, nil, rows(3, func(d *doctemplates.TableData) { d.Rows[1][1] = strings.Repeat("甲\n", 40) }), r.ErrUnsupported},
		{"more than 100 pages", statement("full", func(d *doctemplates.Document) { d.Table.Height = 30 }), values, nil, rows(150, nil), r.ErrUnsupported},
		{"a total that is not the sum of the rows", statement("full", nil), values, nil, rows(3, func(d *doctemplates.TableData) { d.Totals["amount"] = "1.00" }), r.ErrIntegrity},
		{"a malformed amount", statement("full", nil), values, nil, rows(3, func(d *doctemplates.TableData) { d.Rows[1][2] = "1e3" }), r.ErrIntegrity},
		{"a table without its rows", statement("full", nil), values, nil, nil, r.ErrInvalid},
		{"a table with other columns", statement("full", nil), values, nil, rows(3, func(d *doctemplates.TableData) { d.Columns = d.Columns[:2] }), r.ErrInvalid},
		{"a background the template does not name", statement("full", nil), values, png, rows(3, nil), r.ErrInvalid},
		{"a named background without its bytes", withBackground("full"), values, nil, rows(3, nil), r.ErrInvalid},
		{"background bytes that are not the named digest", withBackground("full"), values, append(append([]byte{}, png...), 0), rows(3, nil), r.ErrIntegrity},
		{"background bytes in variables mode", withBackground("variables"), values, png, rows(3, nil), r.ErrInvalid},
		{"a background over the limit", withBackground("full"), values, make([]byte, doctemplates.MaxBackgroundBytes+1), rows(3, nil), r.ErrBudget},
		{"a document that is not valid", single(func(d *doctemplates.Document) { d.Elements = append(d.Elements, d.Elements[0]) }), oneValue("x"), nil, nil, r.ErrInvalid},
	}
	id := strings.Repeat("7", 32)
	at := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
	stamp := issueStamp("AR-000001", at, id)
	for _, c := range cases {
		checked := w.CheckIssue(t.Context(), c.d, c.values, c.background, c.table)
		out, pages, issued := w.Issue(t.Context(), c.d, c.values, c.background, c.table, stamp, at)
		if errorClass(checked) != errorClass(c.want) || errorClass(issued) != errorClass(c.want) {
			t.Errorf("%s: check %v, issue %v; want both %v", c.name, checked, issued, c.want)
		}
		if c.want == nil && (issued != nil || !bytes.HasPrefix(out, []byte("%PDF-")) || pages < 1) {
			t.Errorf("%s: a document the check accepts was not issued: %v", c.name, issued)
		}
		if c.want != nil && out != nil {
			t.Errorf("%s: a refused document came out", c.name)
		}
	}
	// The check is the layout alone: what is wrong with Issue's own arguments is not its business.
	good := single(nil)
	for name, bad := range map[string]struct {
		stamp string
		at    time.Time
	}{
		"an empty stamp":               {"", at},
		"a stamp naming the banner":    {bannerText, at},
		"a stamp the fonts lack":       {"ISSUED 😀", at},
		"no issue time":                {stamp, time.Time{}},
		"an issue time with a fracion": {stamp, at.Add(time.Millisecond)},
	} {
		if e := w.CheckIssue(t.Context(), good, oneValue("x"), nil, nil); e != nil {
			t.Fatalf("%s: the check refused a good document: %v", name, e)
		}
		if _, _, e := w.Issue(t.Context(), good, oneValue("x"), nil, nil, bad.stamp, bad.at); e == nil {
			t.Errorf("%s: Issue accepted it", name)
		}
	}
	// The check starts no Typst and holds no slot: with every slot taken it still answers at once,
	// where an issue would wait for one; a context that has ended is refused first.
	for range cap(w.slots) {
		w.slots <- struct{}{}
	}
	waiting, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if e := w.CheckIssue(waiting, good, oneValue("x"), nil, nil); e != nil {
		t.Errorf("the check waited for a slot or failed with every slot taken: %v", e)
	}
	for range cap(w.slots) {
		release(w.slots)
	}
	ended, stop := context.WithCancel(t.Context())
	stop()
	if e := w.CheckIssue(ended, good, oneValue("x"), nil, nil); !errors.Is(e, context.Canceled) {
		t.Errorf("a context that has ended: %v", e)
	}
}

// serviceChecksBeforeTheProduct runs the worker with the real issuer and writer against the contract
// simulator as the product: a value too wide for its box fails the job with UNSUPPORTED_COMBINATION and
// a template withdrawn while the job waited with TEMPLATE_NOT_PUBLISHED, each before the product is
// asked, so that it holds nothing for the job and the next document takes the next number.
func serviceChecksBeforeTheProduct(t *testing.T, w *Writer) {
	ctx := t.Context()
	p := contractsim.DemoPrincipal()
	finance := contractsim.Financial()
	finance.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	store, err := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	issuer, err := issuance.New(store, nil, w)
	if err != nil {
		t.Fatal(err)
	}
	repo := memory.New(time.Now)
	s, err := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": w}, Issuer: issuer})
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
	// published saves a one-box template for a field of the snapshot and publishes it.
	published := func(field string, width float64) (doctemplates.Entry, []byte) {
		d := doctemplates.Document{Schema: "1.0", Kind: "document", Binding: doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: contractsim.FinancialQuery().Dataset},
			Elements: []doctemplates.Element{{ID: "n1", Source: "row", Field: field, X: 20, Y: 30, Width: width, Height: 10, FontSize: 12, Align: "left"}}}
		v, e := store.Save(p.Identity, "", 0, d)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = store.Publish(p.Identity, v.ID, "publish", 1, 0); e != nil {
			t.Fatal(e)
		}
		canonical, e := doctemplates.Canonical(v.Layout)
		if e != nil {
			t.Fatal(e)
		}
		return v, canonical
	}
	submit := func(v doctemplates.Entry, canonical []byte, key string) r.JobView {
		j, e := s.SubmitIssue(ctx, p, r.IssueInput{TemplateID: v.ID, TemplateVersion: v.Version, Template: canonical, SnapshotID: frozen.SnapshotID, RowIndex: 0}, key)
		if e != nil {
			t.Fatal(e)
		}
		return j
	}
	// A value too wide for its box: the customer of row 0 is ten characters, the box 15 mm.
	narrow, narrowBytes := published("customer.id", 15)
	refused := submit(narrow, narrowBytes, "narrow")
	if _, e := s.RunOne(ctx); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal("a value too wide for its box was not refused as unsupported:", e)
	}
	if got, e := s.Job(ctx, p, refused.ID); e != nil || got.State != "failed" || got.ErrorCode != "UNSUPPORTED_COMBINATION" {
		t.Fatalf("%+v %v", got, e)
	}
	if rec, held := finance.IssueRecord(refused.ID); held {
		t.Fatalf("the product holds %+v for a document its renderer cannot lay out", rec)
	}
	// The same row on a box wide enough is issued, and takes the first number.
	wide, wideBytes := published("customer.id", 100)
	first := submit(wide, wideBytes, "wide")
	if _, e := s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	got, e := s.Job(ctx, p, first.ID)
	if e != nil || got.State != "succeeded" || got.Issue == nil || got.Issue.Number != "AR-000001" {
		t.Fatalf("%+v %v", got, e)
	}
	if art, e := s.Download(ctx, p, got.ArtifactID); e != nil || !bytes.HasPrefix(art.Content, []byte("%PDF-")) {
		t.Fatal(e)
	}
	// A template withdrawn while the job waited: refused before the product is asked.
	late, lateBytes := published("customer.id", 100)
	waiting := submit(late, lateBytes, "withdrawn")
	if _, e := store.Publish(p.Identity, late.ID, "withdraw", 0, 1); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RunOne(ctx); !errors.Is(e, r.ErrUnpublished) {
		t.Fatal("a withdrawn template was not refused:", e)
	}
	if got, e := s.Job(ctx, p, waiting.ID); e != nil || got.State != "failed" || got.ErrorCode != "TEMPLATE_NOT_PUBLISHED" {
		t.Fatalf("%+v %v", got, e)
	}
	if rec, held := finance.IssueRecord(waiting.ID); held {
		t.Fatalf("the product holds %+v for a job whose template was withdrawn", rec)
	}
	// Neither refusal spent a number: the next document is AR-000002.
	again, againBytes := published("customer.id", 100)
	next := submit(again, againBytes, "next")
	if _, e := s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	if got, e := s.Job(ctx, p, next.ID); e != nil || got.State != "succeeded" || got.Issue.Number != "AR-000002" {
		t.Fatalf("%+v %v", got, e)
	}
}
