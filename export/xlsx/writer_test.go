package xlsx_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"idp.local/reporting/export/present"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
)

// ledger is a snapshot of a string dimension, an integer dimension and two metrics, with a value that does not survive a float64.
func ledger() r.Snapshot {
	return r.Snapshot{
		ID:        "0123456789abcdef0123456789abcdef",
		Owner:     r.Owner{Identity: r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}, Integration: "erp"},
		Query:     r.QuerySpec{Dataset: r.Ref{ID: "finance.ledger", Version: "1"}, Dimensions: []string{"customer", "year"}, Metrics: []r.Ref{{ID: "amount", Version: "1"}, {ID: "count", Version: "1"}}, AsOf: "2026-10-27", Timezone: "Asia/Shanghai"},
		Prepared:  r.PreparedRead{QueryHash: "qh", Watermark: "wm", Ref: "ref"},
		ExpiresAt: time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC),
		Result: r.Result{
			Columns: []r.Column{{ID: "customer", Label: "客户", Type: "string"}, {ID: "year", Label: "年份", Type: "integer"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2, Currency: "CNY"}, {ID: "count", Label: "笔数", Type: "integer", Unit: "笔"}},
			Rows: [][]string{
				{"甲", "2026", "100.25", "3"},
				{"乙", "2026", "-0.24", "0"},
				{"甲", "2025", "9007199254740993.01", "9007199254740993"},
				{"=SUM(A1)", "2025", "0.00", "1"},
			},
			Totals: map[string]string{"amount": "9007199254741093.02", "count": "9007199254740997"},
		},
	}
}

func open(t *testing.T, b []byte) *excelize.File {
	t.Helper()
	f, e := excelize.OpenReader(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// cell reads a cell: its raw value, its type and its style.
func cell(t *testing.T, f *excelize.File, sheet, ref string) (string, excelize.CellType, *excelize.Style) {
	t.Helper()
	v, e := f.GetCellValue(sheet, ref, excelize.Options{RawCellValue: true})
	if e != nil {
		t.Fatal(ref, e)
	}
	kind, e := f.GetCellType(sheet, ref)
	if e != nil {
		t.Fatal(ref, e)
	}
	id, e := f.GetCellStyle(sheet, ref)
	if e != nil {
		t.Fatal(ref, e)
	}
	st, e := f.GetStyle(id)
	if e != nil {
		t.Fatal(ref, e)
	}
	return v, kind, st
}

// isNumber says whether a cell is a numeric cell: excelize reports a number written without a type attribute as CellTypeUnset.
func isNumber(kind excelize.CellType) bool {
	return kind == excelize.CellTypeNumber || kind == excelize.CellTypeUnset
}

func numFmt(st *excelize.Style) string {
	if st == nil || st.CustomNumFmt == nil {
		return ""
	}
	return *st.CustomNumFmt
}

func row(t *testing.T, f *excelize.File, sheet string, n int) []string {
	t.Helper()
	rows, e := f.GetRows(sheet, excelize.Options{RawCellValue: true})
	if e != nil {
		t.Fatal(e)
	}
	if n > len(rows) {
		return nil
	}
	return rows[n-1]
}

func metadata(t *testing.T, f *excelize.File) map[string]string {
	t.Helper()
	rows, e := f.GetRows("Metadata", excelize.Options{RawCellValue: true})
	if e != nil {
		t.Fatal(e)
	}
	out := map[string]string{}
	for _, row := range rows {
		if len(row) == 2 {
			out[row[0]] = row[1]
		} else if len(row) == 1 {
			out[row[0]] = ""
		}
	}
	return out
}

func xmlOf(t *testing.T, b []byte) string {
	t.Helper()
	z, e := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if e != nil {
		t.Fatal(e)
	}
	var out strings.Builder
	for _, file := range z.File {
		if strings.HasSuffix(file.Name, ".xml") {
			body, e := file.Open()
			if e != nil {
				t.Fatal(e)
			}
			b, e := io.ReadAll(body)
			body.Close()
			if e != nil {
				t.Fatal(e)
			}
			out.Write(b)
		}
	}
	return out.String()
}

func TestEveryWorkbookWritesTypedCellsBesideExactColumns(t *testing.T) {
	s := ledger()
	b, e := xlsx.Writer{}.Write(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	f := open(t, b)
	if got := row(t, f, "Data", 1); strings.Join(got, "|") != "客户|年份|年份（精确）|金额|金额（精确）|笔数|笔数（精确）" {
		t.Fatalf("header %q", got)
	}
	// A decimal that survives a float64 is a number with the column's format; its exact twin is the frozen string.
	v, kind, st := cell(t, f, "Data", "D2")
	if v != "100.25" || !isNumber(kind) || numFmt(st) != "#,##0.00;-#,##0.00" || st.Font != nil && st.Font.Bold {
		t.Errorf("D2 = %q %v %q", v, kind, numFmt(st))
	}
	if v, kind, _ := cell(t, f, "Data", "E2"); v != "100.25" || isNumber(kind) {
		t.Errorf("E2 = %q %v", v, kind)
	}
	if v, kind, st := cell(t, f, "Data", "D3"); v != "-0.24" || !isNumber(kind) || numFmt(st) != "#,##0.00;-#,##0.00" {
		t.Errorf("D3 = %q %v %q", v, kind, numFmt(st))
	}
	// A value beyond float64's precision is text in the numeric column, as formatted, and exact in its twin.
	if v, kind, st := cell(t, f, "Data", "D4"); v != "9,007,199,254,740,993.01" || isNumber(kind) || numFmt(st) != "" {
		t.Errorf("D4 = %q %v %q", v, kind, numFmt(st))
	}
	if v, _, _ := cell(t, f, "Data", "E4"); v != "9007199254740993.01" {
		t.Errorf("E4 = %q", v)
	}
	// An integer metric is a number with a whole-number format; an integer dimension is a number without grouping.
	if v, kind, st := cell(t, f, "Data", "F2"); v != "3" || !isNumber(kind) || numFmt(st) != "#,##0;-#,##0" {
		t.Errorf("F2 = %q %v %q", v, kind, numFmt(st))
	}
	if v, kind, st := cell(t, f, "Data", "B2"); v != "2026" || !isNumber(kind) || numFmt(st) != "0;-0" {
		t.Errorf("B2 = %q %v %q", v, kind, numFmt(st))
	}
	if v, kind, _ := cell(t, f, "Data", "F4"); v != "9,007,199,254,740,993" || isNumber(kind) {
		t.Errorf("F4 = %q %v", v, kind)
	}
	// A string that looks like a formula is text, and the file has no formula.
	if v, kind, _ := cell(t, f, "Data", "A5"); v != "=SUM(A1)" || kind == excelize.CellTypeFormula {
		t.Errorf("A5 = %q %v", v, kind)
	}
	if xml := xmlOf(t, b); strings.Contains(xml, "<f>") || strings.Contains(xml, "<f ") || !strings.Contains(xml, "9007199254740993.01") {
		t.Error("a formula, or the exact digits lost")
	}
	// The Totals sheet: value beside exact_value under the same rule.
	if got := row(t, f, "Totals", 1); strings.Join(got, "|") != "metric|value|exact_value|currency|unit" {
		t.Errorf("totals header %q", got)
	}
	if got := row(t, f, "Totals", 2); strings.Join(got, "|") != "amount|9,007,199,254,741,093.02|9007199254741093.02|CNY" {
		t.Errorf("amount total %q", got)
	}
	if v, kind, _ := cell(t, f, "Totals", "B2"); isNumber(kind) || v != "9,007,199,254,741,093.02" {
		t.Errorf("an inexact total is text: %q %v", v, kind)
	}
	if got := row(t, f, "Totals", 3); strings.Join(got, "|") != "count|9,007,199,254,740,997|9007199254740997||笔" {
		t.Errorf("count total %q", got)
	}
	// The Metadata sheet counts the inexact cells (two in Data, two totals) and names no layout.
	m := metadata(t, f)
	if m["inexact_cells"] != "4" || m["layout_id"] != "" || m["layout_version"] != "" || m["layout_title"] != "" || m["precision"] != xlsx.PrecisionNote || m["snapshot_id"] != s.ID || m["dataset"] != "finance.ledger@1" {
		t.Errorf("metadata %v", m)
	}
	if !strings.Contains(m["precision"], "inexact_cells") || !strings.Contains(m["precision"], "（精确）") || strings.Contains(m["precision"], "All values are exact text cells") {
		t.Errorf("precision note %q", m["precision"])
	}
	// The whole snapshot presented is the plain workbook, byte for byte.
	same, e := xlsx.Writer{}.WritePresented(context.Background(), s, present.Whole(s))
	if e != nil || !bytes.Equal(same, b) {
		t.Error("the whole presentation differs from the plain workbook", e)
	}
	again, _ := xlsx.Writer{}.Write(context.Background(), s)
	if !bytes.Equal(again, b) {
		t.Error("two renders differ")
	}
}

func TestAPresentedWorkbookCarriesLayoutSubtotalsAndMarks(t *testing.T) {
	s := ledger()
	parens := r.NumberFormat{Grouping: true, Negative: r.NegativeParentheses, Zero: r.ZeroDash}
	p := r.Presentation{
		LayoutID: "fedcba9876543210fedcba9876543210", LayoutVersion: 4, Title: "客户账龄", Footnote: "金额含税。",
		Columns:    []r.PresentedColumn{{ID: "customer", Label: "客户名称", Align: r.AlignLeft}, {ID: "amount", Label: "应收", Align: r.AlignRight, Format: &parens}, {ID: "count", Align: r.AlignCenter}},
		SubtotalBy: "customer",
		Marks:      []r.Mark{{Column: "amount", When: r.WhenLess, Value: "0", Mark: r.MarkBad}, {Column: "customer", When: r.WhenEqual, Value: "甲", Mark: r.MarkNote}},
	}
	b, e := xlsx.Writer{}.WritePresented(context.Background(), s, p)
	if e != nil {
		t.Fatal(e)
	}
	f := open(t, b)
	if got := row(t, f, "Data", 1); strings.Join(got, "|") != "客户名称|应收|应收（精确）|笔数|笔数（精确）" {
		t.Fatalf("header %q", got)
	}
	// Rows: 甲 (2 rows), subtotal, 乙, subtotal, =SUM(A1), subtotal. The count subtotal 9007199254740996 is even and survives a float64
	// (a numeric cell, its raw digits); the amount subtotal does not (text, grouped).
	want := []string{
		"甲|100.25|100.25|3|3",
		"甲|9,007,199,254,740,993.01|9007199254740993.01|9,007,199,254,740,993|9007199254740993",
		"小计|9,007,199,254,741,093.26|9007199254741093.26|9007199254740996|9007199254740996",
		"乙|-0.24|-0.24|0|0",
		"小计|-0.24|-0.24|0|0",
		"=SUM(A1)|0.00|0.00|1|1",
		"小计|0.00|0.00|1|1",
	}
	for i, line := range want {
		if got := strings.Join(row(t, f, "Data", i+2), "|"); got != line {
			t.Errorf("row %d = %q, want %q", i+2, got, line)
		}
	}
	// The subtotal row is bold values, never formulas; its sums are numbers when they survive a float64 and text otherwise.
	if v, kind, st := cell(t, f, "Data", "B4"); isNumber(kind) || v != "9,007,199,254,741,093.26" || st.Font == nil || !st.Font.Bold {
		t.Errorf("B4 = %q %v bold %v", v, kind, st.Font != nil && st.Font.Bold)
	}
	if v, kind, st := cell(t, f, "Data", "B6"); !isNumber(kind) || v != "-0.24" || st.Font == nil || !st.Font.Bold || numFmt(st) != `#,##0.00;(#,##0.00);"—"` {
		t.Errorf("B6 = %q %v %q", v, kind, numFmt(st))
	}
	if _, _, st := cell(t, f, "Data", "A4"); st.Font == nil || !st.Font.Bold {
		t.Error("the subtotal label is not bold")
	}
	if xml := xmlOf(t, b); strings.Contains(xml, "<f>") || strings.Contains(xml, "<f ") {
		t.Error("a formula in the workbook")
	}
	// The marks fill the cell, from the closed palette; a subtotal row is never marked.
	if _, _, st := cell(t, f, "Data", "B5"); len(st.Fill.Color) != 1 || st.Fill.Color[0] != present.Palette[r.MarkBad] {
		t.Errorf("B5 fill %v", st.Fill)
	}
	if _, _, st := cell(t, f, "Data", "A2"); len(st.Fill.Color) != 1 || st.Fill.Color[0] != present.Palette[r.MarkNote] {
		t.Errorf("A2 fill %v", st.Fill)
	}
	if _, _, st := cell(t, f, "Data", "B2"); len(st.Fill.Color) != 0 {
		t.Errorf("B2 fill %v", st.Fill)
	}
	if _, _, st := cell(t, f, "Data", "A4"); len(st.Fill.Color) != 0 {
		t.Errorf("A4 fill %v", st.Fill)
	}
	// The format of a presented column follows into the Totals sheet; the Metadata sheet names the layout.
	if _, _, st := cell(t, f, "Totals", "B2"); numFmt(st) != "" {
		t.Errorf("an inexact total has a number format: %q", numFmt(st))
	}
	if got := row(t, f, "Totals", 3); strings.Join(got, "|") != "count|9,007,199,254,740,997|9007199254740997||笔" {
		t.Errorf("count total %q", got)
	}
	m := metadata(t, f)
	if _, kind, st := cell(t, f, "Data", "D4"); !isNumber(kind) || st.Font == nil || !st.Font.Bold || numFmt(st) != "#,##0;-#,##0" {
		t.Errorf("D4: the even count subtotal is not a bold number: %v %q", kind, numFmt(st))
	}
	// Three inexact cells in Data (the amount and count of 甲 in 2025, the amount subtotal of 甲) and both totals.
	if m["layout_id"] != p.LayoutID || m["layout_version"] != "4" || m["layout_title"] != "客户账龄" || m["layout_footnote"] != "金额含税。" || m["subtotal_by"] != "customer" || m["inexact_cells"] != "5" {
		t.Errorf("metadata %v", m)
	}
	plain, _ := xlsx.Writer{}.Write(context.Background(), s)
	if bytes.Equal(plain, b) {
		t.Error("the presented workbook equals the plain one")
	}
}

func TestAPresentationThatDoesNotFitIsRefusedWhole(t *testing.T) {
	s := ledger()
	for name, p := range map[string]r.Presentation{
		"unknown column":   {Columns: []r.PresentedColumn{{ID: "nope", Align: r.AlignLeft}}},
		"subtotal by sum":  {Columns: []r.PresentedColumn{{ID: "amount", Align: r.AlignRight}}, SubtotalBy: "amount"},
		"order on a label": {Columns: []r.PresentedColumn{{ID: "customer", Align: r.AlignLeft}}, Marks: []r.Mark{{Column: "customer", When: r.WhenGreater, Value: "1", Mark: r.MarkBad}}},
	} {
		if _, e := (xlsx.Writer{}).WritePresented(context.Background(), s, p); e == nil {
			t.Errorf("%s: rendered", name)
		}
	}
	many := ledger()
	for len(many.Result.Rows) <= r.MaxRows {
		many.Result.Rows = append(many.Result.Rows, many.Result.Rows[0])
	}
	if _, e := (xlsx.Writer{}).Write(context.Background(), many); !errors.Is(e, r.ErrBudget) {
		t.Errorf("over MaxRows: %v", e)
	}
}
