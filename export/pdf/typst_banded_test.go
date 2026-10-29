//go:build linux

package pdf

// Schema 2.0 documents through the real pinned Typst and fonts (S03-J): subtests of
// TestTypstRendersDocuments, so that they skip and fail with it. With REPORTING_PDF_EVIDENCE set each
// document is written with what scripts/check_pdf_export.py must find in it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// invoiceSnapshot is a frozen snapshot of order lines: the order, its customer and address (which
// every line of the order repeats), the item, its quantity, amount and weight (a metric the table does not list).
func invoiceSnapshot(rows [][]string) r.Snapshot {
	return r.Snapshot{
		ID:       "0123456789abcdef0123456789abcdef",
		Owner:    r.Owner{Integration: "i"},
		Query:    r.QuerySpec{Dataset: r.Ref{ID: "d", Version: "1"}, Dimensions: []string{"order", "customer", "address", "item"}, Metrics: []r.Ref{{ID: "qty", Version: "1"}, {ID: "amount", Version: "1"}, {ID: "weight", Version: "1"}}},
		Prepared: r.PreparedRead{Watermark: "wm"},
		Result: r.Result{Totals: map[string]string{}, Rows: rows, Columns: []r.Column{
			{ID: "order", Label: "订单", Type: "string"}, {ID: "customer", Label: "客户", Type: "string"}, {ID: "address", Label: "地址", Type: "string"}, {ID: "item", Label: "品名", Type: "string"},
			{ID: "qty", Label: "数量", Type: "integer", Unit: "pcs"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2, Currency: "CNY"}, {ID: "weight", Label: "重量", Type: "decimal", Scale: 3, Unit: "kg"}}},
	}
}

// invoiceTemplate is an invoice-like 2.0 document: a page header with the title, the page number and a
// rule; a document header with the order, the customer and a wrapped address in a frame; the lines of the
// order in a table; a document footer with the sum, a signature rule and wrapped terms; a page footer.
func invoiceTemplate() doctemplates.Document {
	table := tableItem("e12", 0, 190, 9,
		doctemplates.ItemColumn{Field: "item", Width: 100},
		doctemplates.ItemColumn{Field: "qty", Width: 30, Label: "数量 Qty"},
		doctemplates.ItemColumn{Field: "amount", Width: 60, Align: "right"})
	return doctemplates.Document{Schema: "2.0", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
		Group: &doctemplates.GroupSpec{Fields: []string{"order"}},
		Bands: &doctemplates.Bands{
			PageHeader: fixedBand(16,
				bolded(textItem("e1", "literal", "销售发票 Invoice", 0, 0, 110, 9, 16, "left")),
				textItem("e2", "page", "第 {page} 页 / 共 {pages} 页", 120, 2, 70, 7, 9, "right"),
				lineItem("e3", 0, 14, 190, 14, 0.5)),
			DocumentHeader: fixedBand(36,
				rectItem("e4", 0, 0, 95, 9, 0, "grey1"),
				textItem("e5", "literal", "发票号 No.", 1, 1.5, 28, 6, 9, "left"),
				bolded(textItem("e6", "field", "order", 30, 1.5, 62, 6, 10, "left")),
				textItem("e7", "field", "customer", 100, 1.5, 90, 7, 11, "left"),
				rectItem("e8", 0, 12, 110, 22, 0.5, "none"),
				wrapped(textItem("e9", "field", "address", 2, 13, 106, 20, 10, "left")),
			),
			Detail: detailOf(table),
			DocumentFooter: fixedBand(34,
				textItem("e20", "literal", "总重 Total weight (kg)", 0, 1, 45, 6, 9, "left"),
				textItem("e21", "group_sum", "weight", 45, 1, 50, 6, 10, "right"),
				rectItem("e13", 100, 1, 90, 9, 0.5, "grey2"),
				bolded(textItem("e14", "literal", "合计 Total", 102, 2, 40, 6, 9, "left")),
				bolded(textItem("e15", "group_sum", "amount", 142, 2, 46, 6, 10, "right")),
				wrapped(textItem("e16", "literal", "付款条件：收到发票后三十日内付清；逾期按日千分之五计收滞纳金。 Net 30 days; late payment is charged at 0.5% a day.", 0, 12, 110, 14, 8, "left")),
				lineItem("e17", 120, 28, 190, 28, 0.25),
				textItem("e18", "literal", "签章 Authorised signature", 120, 28.8, 70, 5, 8, "left")),
			PageFooter: fixedBand(10,
				lineItem("e10", 0, 0, 190, 0, 0.25),
				textItem("e11", "literal", "IDP Reporting · 本文件由系统生成", 0, 2, 120, 6, 8, "left"),
				textItem("e19", "page", "{page}/{pages}", 150, 2, 40, 6, 8, "right")),
		}}
}

func invoiceLine(order, customer, address, item string, qty int, amount, weight string) []string {
	return []string{order, customer, address, item, strconv.Itoa(qty), amount, weight}
}

// sumInput is what the checker recomputes a group sum from: the group's frozen values of the field, and the
// scale of the column.
type sumInput struct {
	Values []string `json:"values"`
	Scale  int      `json:"scale"`
}

// groupSums collects, for each group_sum text of the template, the frozen values of its field over the group's rows.
func groupSums(d doctemplates.Document, s r.Snapshot, g doctemplates.RowGroup) map[string]sumInput {
	out := map[string]sumInput{}
	for _, bi := range d.Items() {
		if bi.Item.Type != doctemplates.TypeText || bi.Item.Source != doctemplates.SourceGroupSum {
			continue
		}
		for j, c := range s.Result.Columns {
			if c.ID != bi.Item.Field {
				continue
			}
			in := sumInput{Values: []string{}, Scale: c.Scale}
			for _, i := range g.Rows {
				in.Values = append(in.Values, s.Result.Rows[i][j])
			}
			out[bi.Item.ID] = in
		}
	}
	return out
}

// bandedGroups binds every group of the snapshot as a preview does.
func bandedGroups(t *testing.T, d doctemplates.Document, s r.Snapshot) (groups []doctemplates.RowGroup, values []map[string]string, tables []*doctemplates.TableData, sums []map[string]sumInput) {
	t.Helper()
	groups, err := doctemplates.Groups(d, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		v, table, err := doctemplates.BindGroup(d, s, g)
		if err != nil {
			t.Fatal(err)
		}
		values, tables, sums = append(values, v), append(tables, table), append(sums, groupSums(d, s, g))
	}
	return
}

// bandedExpectation is what the checker verifies a 2.0 document against: the bands of the template in
// millimetres with the values bound to its texts, the table with its raw rows, and where the layout put
// the document footer. The checker derives every position and the sums itself.
type bandedExpectation struct {
	Kind       string           `json:"kind"`
	File       string           `json:"file"`
	Bands      []map[string]any `json:"bands"`
	Table      map[string]any   `json:"table"`
	TablePages int              `json:"table_pages"`
	Footer     *footerExpect    `json:"footer,omitempty"`
}

type footerExpect struct {
	Page int     `json:"page"`
	Top  float64 `json:"top"` // points from the top of the page
}

func bandedElement(it doctemplates.Item, values map[string]string) map[string]any {
	out := map[string]any{"id": it.ID, "type": it.Type}
	switch it.Type {
	case doctemplates.TypeText:
		out["source"], out["text"], out["wrap"], out["bold"], out["align"], out["size"] = it.Source, values[it.ID], it.Wrap, it.Bold, it.Align, it.FontSize
		out["x"], out["y"], out["width"], out["height"] = it.X, it.Y, it.Width, it.Height
	case doctemplates.TypeLine:
		out["x1"], out["y1"], out["x2"], out["y2"], out["stroke"] = it.X1, it.Y1, it.X2, it.Y2, it.Stroke
	case doctemplates.TypeRect:
		out["x"], out["y"], out["width"], out["height"], out["stroke"], out["fill"] = it.X, it.Y, it.Width, it.Height, it.Stroke, it.Fill
	}
	return out
}

func bandedEvidence(t *testing.T, name string, pdf []byte, d doctemplates.Document, values map[string]string, table *doctemplates.TableData, sums map[string]sumInput, w *Writer) {
	t.Helper()
	dir := os.Getenv("REPORTING_PDF_EVIDENCE")
	if dir == "" {
		return
	}
	p, err := layOut(d, values, nil, table, newPlainMeasurer(w.fonts))
	if err != nil {
		t.Fatal(err)
	}
	e := bandedExpectation{Kind: "document2", File: name + ".pdf", TablePages: len(p.Pages)}
	if p.Footer != nil {
		e.Footer = &footerExpect{Page: p.Footer.Page, Top: p.Footer.Top}
		// A footer on a page of its own is not a table page.
		if p.Footer.Page == len(p.Pages) && p.Footer.Top == (safeTop+d.Bands.PageHeader.Height)*mm && !pageHasTable(p.Pages[len(p.Pages)-1]) {
			e.TablePages--
		}
	}
	for _, band := range []struct {
		name string
		b    *doctemplates.Band
	}{{doctemplates.BandPageHeader, d.Bands.PageHeader}, {doctemplates.BandDocumentHeader, d.Bands.DocumentHeader}, {doctemplates.BandDocumentFooter, d.Bands.DocumentFooter}, {doctemplates.BandPageFooter, d.Bands.PageFooter}} {
		if band.b == nil {
			continue
		}
		items := []map[string]any{}
		for _, it := range band.b.Elements {
			el := bandedElement(it, values)
			if in, ok := sums[it.ID]; ok {
				// The checker sums these itself and prints the sum as the table prints its totals.
				el["sum_values"], el["scale"] = in.Values, in.Scale
			}
			items = append(items, el)
		}
		e.Bands = append(e.Bands, map[string]any{"name": band.name, "height": band.b.Height, "elements": items})
	}
	t2 := d.TableItem()
	columns := []map[string]any{}
	for i, c := range t2.Columns {
		col := table.Columns[i]
		label := col.Label
		switch {
		case col.Currency != "" && col.Unit != "":
			label += "（" + col.Currency + " · " + col.Unit + "）"
		case col.Currency != "":
			label += "（" + col.Currency + "）"
		case col.Unit != "":
			label += "（" + col.Unit + "）"
		}
		if c.Label != "" {
			label = c.Label
		}
		columns = append(columns, map[string]any{"field": c.Field, "label": label, "width": c.Width, "align": c.Align, "metric": table.Metric[i] && (col.Type == "decimal" || col.Type == "integer"), "scale": col.Scale})
	}
	e.Table = map[string]any{"id": t2.ID, "x": t2.X, "width": t2.Width, "size": t2.FontSize, "columns": columns, "rows": table.Rows, "totals": map[string]string{}}
	b, _ := json.MarshalIndent(e, "", " ")
	for file, content := range map[string][]byte{name + ".pdf": pdf, name + ".json": b} {
		if err := os.WriteFile(filepath.Join(dir, file), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func rowDetail(d doctemplates.Detail) int {
	if d.Row == nil {
		return -1
	}
	return *d.Row
}

// pageHasTable reports a page that carries a table header (its cells).
func pageHasTable(pg pageData) bool { return len(pg.Cells) > 0 }

// renderBanded renders a bound group twice (the bytes must not differ) and records it.
func renderBanded(t *testing.T, w *Writer, name string, d doctemplates.Document, values map[string]string, table *doctemplates.TableData, sums map[string]sumInput) []byte {
	t.Helper()
	out, err := w.Render(t.Context(), d, values, nil, table)
	if err != nil {
		var re *RenderError
		if errors.As(err, &re) {
			t.Fatalf("%s: %v\n%s", name, err, re.output())
		}
		t.Fatalf("%s: %v", name, err)
	}
	again, err := w.Render(t.Context(), d, values, nil, table)
	if err != nil || !bytes.Equal(out, again) {
		t.Fatalf("%s: two renders with the same clock differ (%v)", name, err)
	}
	bandedEvidence(t, name, out, d, values, table, sums, w)
	return out
}

func testTypstRendersBandedDocuments(t *testing.T, w *Writer) {
	d := invoiceTemplate()
	address := "上海市浦东新区张江路 123 号 A 座 8 层\nBuilding A, 123 Zhangjiang Road, Pudong, Shanghai 201203, China"
	// An invoice-like snapshot of three orders (three groups): the lines of an order are not consecutive in the
	// snapshot, and each document has its own lines, address and sum.
	rows := [][]string{
		invoiceLine("INV-0001", "华东贸易有限公司", address, "笔记本电脑 Laptop 14\"", 3, "26997.00", "1234.500"),
		invoiceLine("INV-0002", "Aurora Trading Ltd", "5 Harbour Way, London E14 9RT", "显示器 Monitor 27 英寸", 2, "3598.50", "250.5"),
		invoiceLine("INV-0001", "华东贸易有限公司", address, "无线键鼠套装", 10, "1290.00", "987.250"),
		invoiceLine("INV-0003", "北方物流（集团）", "北京市朝阳区建国路 88 号 SOHO 现代城 C 座 2201 室，邮编 100022，联系人：张三，电话 010-12345678，传真 010-87654321", "服务费", 1, "-0.05", "0.5"),
		invoiceLine("INV-0001", "华东贸易有限公司", address, "扩展坞 USB-C", 5, "1495.25", "0.125"),
		invoiceLine("INV-0002", "Aurora Trading Ltd", "5 Harbour Way, London E14 9RT", "Shipping & handling", 1, "120", "12"),
		invoiceLine("INV-0001", "华东贸易有限公司", address, "笔记本包（黑色）", 3, "897.00", "15.2"),
		invoiceLine("INV-0001", "华东贸易有限公司", address, "延保服务 36 个月", 3, "2400.00", "2000.4"),
		invoiceLine("INV-0001", "华东贸易有限公司", address, "HDMI 线缆 2 m", 6, "209.70", "10"),
	}
	snap := invoiceSnapshot(rows)
	groups, values, tables, sums := bandedGroups(t, d, snap)
	if len(groups) != 3 || groups[0].Key[0] != "INV-0001" || groups[1].Key[0] != "INV-0002" || groups[2].Key[0] != "INV-0003" || len(groups[0].Rows) != 6 || groups[0].Rows[1] != 2 {
		t.Fatalf("groups %+v", groups)
	}
	for i, name := range []string{"banded_invoice_1", "banded_invoice_2", "banded_invoice_3"} {
		t.Run(name, func(t *testing.T) {
			out := renderBanded(t, w, name, d, values[i], tables[i], sums[i])
			if !bytes.HasPrefix(out, []byte("%PDF-")) {
				t.Fatal("no PDF")
			}
			// The check the routes and the worker run, with the fonts alone and no Typst process, accepts what renders.
			if err := w.CheckIssue(t.Context(), d, values[i], nil, tables[i]); err != nil {
				t.Errorf("the check refuses what renders: %v", err)
			}
		})
	}
	// What the layout refuses, the preview and the check refuse alike, with the same reason and place: a customer
	// too wide for its box, an uncovered character in the address, a table cell no font covers, an amount too wide for
	// its column.
	t.Run("refusals", func(t *testing.T) {
		for name, c := range map[string]struct {
			change func(v map[string]string, tb *doctemplates.TableData)
			want   doctemplates.Detail
		}{
			"a customer too wide": {func(v map[string]string, tb *doctemplates.TableData) { v["e7"] = strings.Repeat("宽", 40) },
				doctemplates.Detail{Reason: doctemplates.ReasonDoesNotFit, Band: doctemplates.BandDocumentHeader, Element: "e7"}},
			"an uncovered character in the address": {func(v map[string]string, tb *doctemplates.TableData) { v["e9"] = "street 😀" },
				doctemplates.Detail{Reason: doctemplates.ReasonNotCovered, Band: doctemplates.BandDocumentHeader, Element: "e9"}},
			"an address of too many lines": {func(v map[string]string, tb *doctemplates.TableData) { v["e9"] = "1\n2\n3\n4\n5\n6" },
				doctemplates.Detail{Reason: doctemplates.ReasonDoesNotFit, Band: doctemplates.BandDocumentHeader, Element: "e9"}},
			"an uncovered cell": {func(v map[string]string, tb *doctemplates.TableData) { tb.Rows[1][0] = "😀" },
				doctemplates.Detail{Reason: doctemplates.ReasonNotCovered, Band: doctemplates.BandDetail, Element: "e12", Column: "item", Row: ptr(2)}},
			"an amount too wide": {func(v map[string]string, tb *doctemplates.TableData) { tb.Rows[3][2] = strings.Repeat("9", 40) + ".00" },
				doctemplates.Detail{Reason: doctemplates.ReasonDoesNotFit, Band: doctemplates.BandDetail, Element: "e12", Column: "amount", Row: ptr(6)}},
		} {
			// The first order's lines are snapshot rows 0, 2, 4, 6, 7, 8.
			v, tb := map[string]string{}, &doctemplates.TableData{Columns: tables[0].Columns, Metric: tables[0].Metric, Totals: map[string]string{}, Index: tables[0].Index}
			for k, val := range values[0] {
				v[k] = val
			}
			for _, row := range tables[0].Rows {
				tb.Rows = append(tb.Rows, append([]string{}, row...))
			}
			c.change(v, tb)
			_, renderErr := w.Render(t.Context(), d, v, nil, tb)
			checkErr := w.CheckIssue(t.Context(), d, v, nil, tb)
			for who, err := range map[string]error{"the preview": renderErr, "the check": checkErr} {
				det, ok := doctemplates.DetailOf(err)
				if !errors.Is(err, r.ErrUnsupported) || !ok || det.Reason != c.want.Reason || det.Band != c.want.Band || det.Element != c.want.Element || det.Column != c.want.Column || rowDetail(det) != rowDetail(c.want) {
					t.Errorf("%s, %s: %v; detail %+v, want %+v", name, who, err, det, c.want)
				}
			}
		}
	})
	// A group of many lines: pages with the header, the brought-forward and carry-forward rows and the total,
	// the page header and footer on each, the document header on the first and the footer after the total.
	long := make([][]string, 0, 90)
	for i := range 90 {
		item := fmt.Sprintf("物料 %03d %s", i, strings.Repeat("规格甲乙丙丁", i%4))
		if i%9 == 4 {
			item += " averyveryverylongwordwithoutanyspaces_abcdefghijklmnopqrstuvwxyz0123456789"
		}
		long = append(long, invoiceLine("INV-LONG", "华东贸易有限公司", address, item, i+1, fmt.Sprintf("%d.%02d", 100*i+7, (i*37)%100), fmt.Sprintf("%d.%03d", 3*i, (i*91)%1000)))
	}
	longGroups, longValues, longTables, longSums := bandedGroups(t, d, invoiceSnapshot(long))
	if len(longGroups) != 1 {
		t.Fatal("one group expected")
	}
	t.Run("banded_multipage", func(t *testing.T) {
		out := renderBanded(t, w, "banded_multipage", d, longValues[0], longTables[0], longSums[0])
		_ = out
		p, err := layOut(d, longValues[0], nil, longTables[0], newPlainMeasurer(w.fonts))
		if err != nil || len(p.Pages) < 3 {
			t.Fatalf("a multi-page group: %v", err)
		}
	})
	// A group whose last table page leaves too little room for the footer: the footer moves, whole, to a page
	// of its own after the total row.
	moved := -1
	for n := 1; n <= 60 && moved < 0; n++ {
		g, v, tb, _ := bandedGroups(t, d, invoiceSnapshot(long[:n]))
		with, err := layOut(d, v[0], nil, tb[0], newPlainMeasurer(w.fonts))
		if err != nil {
			t.Fatal(err)
		}
		bare := d
		bare.Bands = &doctemplates.Bands{PageHeader: d.Bands.PageHeader, DocumentHeader: d.Bands.DocumentHeader, Detail: d.Bands.Detail, PageFooter: d.Bands.PageFooter}
		without, err := layOut(bare, v[0], nil, tb[0], newPlainMeasurer(w.fonts))
		if err != nil {
			t.Fatal(err)
		}
		if len(g) == 1 && len(with.Pages) == len(without.Pages)+1 {
			moved = n
		}
	}
	if moved < 0 {
		t.Fatal("no group length moves the footer to a new page")
	}
	_, movedValues, movedTables, movedSums := bandedGroups(t, d, invoiceSnapshot(long[:moved]))
	t.Run("banded_footer_moved", func(t *testing.T) {
		out := renderBanded(t, w, "banded_footer_moved", d, movedValues[0], movedTables[0], movedSums[0])
		_ = out
		p, err := layOut(d, movedValues[0], nil, movedTables[0], newPlainMeasurer(w.fonts))
		if err != nil || p.Footer == nil || p.Footer.Page != len(p.Pages) || pageHasTable(p.Pages[len(p.Pages)-1]) {
			t.Fatalf("the footer is not on a page of its own: %v %+v", err, p.Footer)
		}
	})
	// A group with one line fits one page; an empty snapshot is one document with an empty table.
	ungrouped := d
	ungrouped.Group = nil
	_, emptyValues, emptyTables, emptySums := bandedGroups(t, ungrouped, invoiceSnapshot(nil))
	t.Run("banded_empty", func(t *testing.T) {
		// Without a group the whole snapshot is one document, here of no row: its fields are empty and its
		// table has a header and a zero total.
		renderBanded(t, w, "banded_empty", ungrouped, emptyValues[0], emptyTables[0], emptySums[0])
	})
	// The hard cases of the boxes of 1.x as texts of every alignment, bold and wrapped, lines of every width and
	// diagonal, rectangles of every fill and outline, and a table with centred columns.
	t.Run("banded_elements", func(t *testing.T) { testBandedElements(t, w) })
}

func testBandedElements(t *testing.T, w *Writer) {
	hard := []string{"张江路123号A座", "客户：华东区「重点」，联系人：张三。", "a—a—a⸺b⸻c", "ab   ", "  ab  ", "café é", "甲&甲&乙、Receivable。", "……——", "ー#ー%Ⅰ文", "「（中）」。。、", "（甲）", "。", "ab　　", "　ab", "9007199254740893.00", "(j(j(j fV", "W"}
	var items []doctemplates.Item
	values := map[string]string{}
	aligns := []string{"left", "right", "center"}
	for i, v := range hard {
		id := "e" + strconv.Itoa(i+1)
		it := textItem(id, "literal", v, float64(i%2)*95, float64(i/2)*9, 90, 8, 12, aligns[i%3])
		it.Bold = i%4 == 1
		items = append(items, it)
		values[id] = v
	}
	var shapes []doctemplates.Item
	for i, fill := range []string{"none", "grey1", "grey2", "grey3", "grey4"} {
		stroke := 0.25 * float64(i)
		if fill == "none" && stroke == 0 {
			stroke = 2
		}
		shapes = append(shapes, rectItem("e"+strconv.Itoa(30+i), float64(i)*38, 2, 36, 12, stroke, fill))
	}
	shapes = append(shapes, lineItem("e40", 0, 20, 190, 20, 0.25), lineItem("e41", 0, 24, 190, 24, 2), lineItem("e42", 5, 28, 185, 45, 1), lineItem("e43", 185, 28, 5, 45, 0.5), lineItem("e44", 95, 28, 95, 50, 1.25))
	wrappedTexts := []doctemplates.Item{
		wrapped(textItem("e50", "literal", "甲乙丙丁戊己庚辛壬癸子丑寅卯辰巳午未申酉戌亥，客户：华东区「重点」，联系人：张三。ABC def ghi jkl mno pqr stu vwx yz 0123456789", 0, 0, 80, 40, 11, "left")),
		wrapped(bolded(textItem("e51", "literal", "Right-aligned bold wrapped text with 中文混排 and a verylongwordwithoutanyspacesatalljustkeepsgoingandgoing", 90, 0, 90, 40, 10, "right"))),
		wrapped(textItem("e52", "literal", "First paragraph.\n\nThird paragraph after an empty line，居中。", 0, 42, 80, 25, 9, "center")),
	}
	d := banded(doctemplates.Bands{
		PageHeader:     fixedBand(82, items...),
		DocumentHeader: fixedBand(60, shapes...),
		Detail: detailOf(tableItem("e60", 5, 180, 10,
			doctemplates.ItemColumn{Field: "customer", Width: 70, Align: "center", Label: "客户名称 Customer"},
			doctemplates.ItemColumn{Field: "note", Width: 60, Align: "right"},
			doctemplates.ItemColumn{Field: "amount", Width: 50, Align: "left", Label: "Amount"})),
		DocumentFooter: fixedBand(70, wrappedTexts...),
		PageFooter:     fixedBand(12, textItem("e64", "page", "{page} of {pages}", 0, 3, 190, 6, 9, "center")),
	})
	for _, it := range wrappedTexts {
		values[it.ID] = it.Text
	}
	values["e64"] = "{page} of {pages}"
	data := &doctemplates.TableData{Columns: []r.Column{{ID: "customer", Label: "客户", Type: "string"}, {ID: "note", Label: "备注", Type: "decimal", Scale: 3}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2, Currency: "CNY"}}, Metric: []bool{false, true, true}, Totals: map[string]string{}}
	for i, v := range hard {
		data.Rows = append(data.Rows, []string{v, fmt.Sprintf("%d.%d", i, i%7), fmt.Sprintf("%d.%02d", i*10, i)})
		data.Index = append(data.Index, i)
	}
	if err := doctemplates.Validate(d); err != nil {
		t.Fatal(err)
	}
	renderBanded(t, w, "banded_elements", d, values, data, nil)
}
