//go:build linux

package pdf

// Schema 2.0 documents issued by group through the real pinned Typst and fonts (S03-K): a subtest of
// TestTypstIssuesDocuments, so that it skips and fails with it. The stamp of the product's number is drawn in the strip of
// every page, the bytes depend only on what is frozen and on the product's issue time, and with REPORTING_PDF_EVIDENCE
// set each issued document is written with what scripts/check_pdf_export.py must find in it (a "document2" expectation
// with the issue record), beside the preview of the same group, which must not pass as the issue.

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// issueInvoiceRows is the order lines of three customers of the banded tests, interleaved, and a fourth order of 90 lines that
// takes many pages.
func issueInvoiceRows() (rows [][]string, address string) {
	address = "上海市浦东新区张江路 123 号 A 座 8 层\nBuilding A, 123 Zhangjiang Road, Pudong, Shanghai 201203, China"
	rows = [][]string{
		invoiceLine("INV-0001", "华东贸易有限公司", address, "笔记本电脑 Laptop 14\"", 3, "26997.00", "1234.500"),
		invoiceLine("INV-0002", "Aurora Trading Ltd", "5 Harbour Way, London E14 9RT", "显示器 Monitor 27 英寸", 2, "3598.50", "250.5"),
		invoiceLine("INV-0001", "华东贸易有限公司", address, "无线键鼠套装", 10, "1290.00", "987.250"),
		invoiceLine("INV-0003", "北方物流（集团）", "北京市朝阳区建国路 88 号 SOHO 现代城 C 座 2201 室，邮编 100022", "服务费", 1, "-0.05", "0.5"),
		invoiceLine("INV-0001", "华东贸易有限公司", address, "扩展坞 USB-C", 5, "1495.25", "0.125"),
		invoiceLine("INV-0002", "Aurora Trading Ltd", "5 Harbour Way, London E14 9RT", "Shipping & handling", 1, "120", "12"),
	}
	for i := range 90 {
		item := fmt.Sprintf("物料 %03d %s", i, strings.Repeat("规格甲乙丙丁", i%4))
		if i%9 == 4 {
			item += " averyveryverylongwordwithoutanyspaces_abcdefghijklmnopqrstuvwxyz0123456789"
		}
		rows = append(rows, invoiceLine("INV-LONG", "华东贸易有限公司", address, item, i+1, fmt.Sprintf("%d.%02d", 100*i+7, (i*37)%100), fmt.Sprintf("%d.%03d", 3*i, (i*91)%1000)))
	}
	return rows, address
}

func testTypstIssuesBandedDocuments(t *testing.T, w, later *Writer) {
	d := invoiceTemplate()
	rows, _ := issueInvoiceRows()
	snap := invoiceSnapshot(rows)
	groups, values, tables, sums := bandedGroups(t, d, snap)
	if len(groups) != 4 || groups[1].Key[0] != "INV-0002" || groups[3].Key[0] != "INV-LONG" || len(groups[3].Rows) != 90 {
		t.Fatalf("groups %+v", groups)
	}
	id := strings.Repeat("7", 32)
	at := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
	for _, c := range []struct {
		name   string
		group  int
		number string
		pages  func(int) bool
	}{
		{"banded_issue_single", 1, "AR-000012", func(n int) bool { return n == 1 }},
		{"banded_issue_lines", 0, "SH-000001", func(n int) bool { return n == 1 }},
		{"banded_issue_multipage", 3, "SH-999999", func(n int) bool { return n >= 3 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			v, table, sum := values[c.group], tables[c.group], sums[c.group]
			stamp := issueStamp(c.number, at, id)
			out, pages, err := w.Issue(t.Context(), d, v, nil, table, stamp, at)
			if err != nil {
				var re *RenderError
				if errors.As(err, &re) {
					t.Fatalf("%v\n%s", err, re.output())
				}
				t.Fatal(err)
			}
			if !c.pages(pages) {
				t.Fatalf("%d pages", pages)
			}
			// The same inputs give the same bytes, whatever the writer's clock; the product's issue time dates the file; the
			// preview of the group is another file.
			again, _, err := w.Issue(t.Context(), d, v, nil, table, stamp, at)
			if err != nil || !bytes.Equal(out, again) {
				t.Fatalf("two issues of the same inputs differ (%v)", err)
			}
			fromLater, _, err := later.Issue(t.Context(), d, v, nil, table, stamp, at)
			if err != nil || !bytes.Equal(out, fromLater) {
				t.Fatalf("writers whose clocks differ issue different bytes (%v)", err)
			}
			otherTime, _, err := w.Issue(t.Context(), d, v, nil, table, stamp, at.Add(time.Second))
			if err != nil || bytes.Equal(out, otherTime) {
				t.Fatalf("another issue time gives the same bytes (%v)", err)
			}
			preview, err := w.Render(t.Context(), d, v, nil, table)
			if err != nil || bytes.Equal(preview, out) {
				t.Fatalf("the issue is the preview (%v)", err)
			}
			if !bytes.Contains(out, infoCreation(at)) || bytes.Contains(out, infoCreation(created)) || !bytes.Contains(preview, infoCreation(created)) {
				t.Error("the issue is not dated with the issue time or the preview not with the writer's clock")
			}
			// The stamp is on every page, in the text colour, and no page draws the banner's text; the layout the worker
			// checks before the product is asked accepts what was issued.
			p, err := w.issuePage(d, v, nil, table, stamp)
			if err != nil || p.Banner.Text != stamp || p.Banner.Fill != stampFill || p.Banner.Size != stampSize || p.Title != "Issued document" || len(p.Pages) != pages {
				t.Fatalf("issue page %+v %v", p.Banner, err)
			}
			for n, pg := range p.Pages {
				texts := ""
				for _, g := range pg.Texts {
					texts += g.Text
				}
				if strings.Contains(texts, bannerText) {
					t.Errorf("page %d draws the banner text", n+1)
				}
			}
			if err := w.CheckIssue(t.Context(), d, v, nil, table); err != nil {
				t.Errorf("the check refuses what was issued: %v", err)
			}
			bandedEvidence(t, c.name, out, d, v, table, sum, w)
			issueEvidence(t, c.name, out, preview, c.number, at, id, pages)
		})
	}
	// What an issued document must never say: the preview's banner text as a value or as a cell is refused with its place,
	// by the issue and by the check alike; the preview prints it. A background is not part of a 2.0 document.
	t.Run("refusals", func(t *testing.T) {
		stamp := issueStamp("AR-000001", at, id)
		v := map[string]string{}
		for k, val := range values[1] {
			v[k] = val
		}
		v["e7"] = "x " + bannerText
		_, _, issueErr := w.Issue(t.Context(), d, v, nil, tables[1], stamp, at)
		checkErr := w.CheckIssue(t.Context(), d, v, nil, tables[1])
		for who, err := range map[string]error{"the issue": issueErr, "the check": checkErr} {
			det, ok := doctemplates.DetailOf(err)
			if !errors.Is(err, r.ErrUnsupported) || !ok || det.Reason != doctemplates.ReasonNotCovered || det.Band != doctemplates.BandDocumentHeader || det.Element != "e7" {
				t.Errorf("the banner text as a value, %s: %v %+v", who, err, det)
			}
		}
		if _, err := w.Render(t.Context(), d, v, nil, tables[1]); err != nil {
			t.Errorf("the preview refused the banner text: %v", err)
		}
		cells := &doctemplates.TableData{Columns: tables[1].Columns, Metric: tables[1].Metric, Totals: map[string]string{}, Index: tables[1].Index}
		for _, row := range tables[1].Rows {
			cells.Rows = append(cells.Rows, append([]string{}, row...))
		}
		cells.Rows[1][0] = bannerText
		_, _, issueErr = w.Issue(t.Context(), d, values[1], nil, cells, stamp, at)
		checkErr = w.CheckIssue(t.Context(), d, values[1], nil, cells)
		for who, err := range map[string]error{"the issue": issueErr, "the check": checkErr} {
			det, ok := doctemplates.DetailOf(err)
			if !errors.Is(err, r.ErrUnsupported) || !ok || det.Reason != doctemplates.ReasonNotCovered || det.Band != doctemplates.BandDetail || det.Element != "e12" || det.Column != "item" || rowDetail(det) != groups[1].Rows[1] {
				t.Errorf("the banner text as a cell, %s: %v %+v", who, err, det)
			}
		}
		if _, _, err := w.Issue(t.Context(), d, values[1], []byte("\x89PNG\r\n\x1a\n"), tables[1], stamp, at); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("a background for a 2.0 document: %v", err)
		}
		// The product's time is checked as for a row.
		if _, _, err := w.Issue(t.Context(), d, values[1], nil, tables[1], stamp, at.Add(500*time.Millisecond)); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("a fractional issue time: %v", err)
		}
	})
}
