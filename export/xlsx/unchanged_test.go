package xlsx_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
)

// cellsGolden holds two workbooks of a query without filters and without a sort, cell by cell, as the writer rendered them before S20-C
// (recorded at 43f8db5, the commit the slice started from): the ledger fixture written plain and through a presentation. It is the
// workbook's side of the slice's rule that an export of a query without filters and sort stays what it is: S20-C adds exactly one row
// to such a workbook, the `query_scope` row at the end of the Metadata sheet, and nothing else of it changes (the PDF's side is
// export/pdf/testdata/plain_sha256.json). Set REPORTING_XLSX_WRITE_GOLDEN=1 to rewrite the file after a reviewed change of what a
// workbook without filters shows.
const cellsGolden = "testdata/cells_before_s20c.json"

var cellTypes = map[excelize.CellType]string{
	excelize.CellTypeUnset: "unset", excelize.CellTypeBool: "bool", excelize.CellTypeDate: "date", excelize.CellTypeError: "error", excelize.CellTypeFormula: "formula",
	excelize.CellTypeInlineString: "inline", excelize.CellTypeNumber: "number", excelize.CellTypeSharedString: "shared",
}

// cells lists a workbook as text, one fact a line, in a fixed order: for each sheet its name with its frozen panes, the widths of its
// first columns, then every cell of its used rectangle that holds or shows anything: its address, its type, its raw value, its formula
// and what its style says (number format, weight, font colour, fill). A cell that was never written is not listed.
func cells(t *testing.T, b []byte) []string {
	t.Helper()
	f := open(t, b)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	var out []string
	for _, sheet := range f.GetSheetList() {
		rows, e := f.GetRows(sheet, excelize.Options{RawCellValue: true})
		must(e)
		width := 0
		for _, row := range rows {
			width = max(width, len(row))
		}
		panes, e := f.GetPanes(sheet)
		must(e)
		out = append(out, fmt.Sprintf("sheet %s: frozen %v, split y %d, top left %q, active %q", sheet, panes.Freeze, panes.YSplit, panes.TopLeftCell, panes.ActivePane))
		for c := 1; c <= max(width, 6); c++ {
			name, e := excelize.ColumnNumberToName(c)
			must(e)
			w, e := f.GetColWidth(sheet, name)
			must(e)
			out = append(out, fmt.Sprintf("%s!%s: width %g", sheet, name, w))
		}
		for n := 1; n <= len(rows); n++ {
			for c := 1; c <= width; c++ {
				ref, e := excelize.CoordinatesToCellName(c, n)
				must(e)
				v, kind, st := cell(t, f, sheet, ref)
				formula, e := f.GetCellFormula(sheet, ref)
				must(e)
				bold, colour, fill := false, "", ""
				if st != nil {
					if st.Font != nil {
						bold, colour = st.Font.Bold, st.Font.Color
					}
					fill = strings.Join(st.Fill.Color, ",")
				}
				if v == "" && kind == excelize.CellTypeUnset && formula == "" && numFmt(st) == "" && !bold && colour == "" && fill == "" {
					continue
				}
				out = append(out, fmt.Sprintf("%s!%s: %s %q formula %q format %q bold %v colour %q fill %q", sheet, ref, cellTypes[kind], v, formula, numFmt(st), bold, colour, fill))
			}
		}
	}
	return out
}

// ledgerPresentation is a presentation of the ledger fixture with a label, a format, subtotals and marks.
func ledgerPresentation() r.Presentation {
	parens := r.NumberFormat{Grouping: true, Negative: r.NegativeParentheses, Zero: r.ZeroDash}
	return r.Presentation{
		LayoutID: "fedcba9876543210fedcba9876543210", LayoutVersion: 4, Title: "客户账龄", Footnote: "金额含税。",
		Columns:    []r.PresentedColumn{{ID: "customer", Label: "客户名称", Align: r.AlignLeft}, {ID: "amount", Label: "应收", Align: r.AlignRight, Format: &parens}, {ID: "count", Align: r.AlignCenter}},
		SubtotalBy: "customer",
		Marks:      []r.Mark{{Column: "amount", When: r.WhenLess, Value: "0", Mark: r.MarkBad}, {Column: "customer", When: r.WhenEqual, Value: "甲", Mark: r.MarkNote}},
	}
}

func TestAWorkbookWithoutFiltersOrSortIsWhatItWasButForTheScopeRow(t *testing.T) {
	s := ledger()
	if len(s.Query.Filters) != 0 || len(s.Query.Sort) != 0 {
		t.Fatal("the fixture has filters or a sort")
	}
	plain, e := xlsx.Writer{}.Write(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	presented, e := xlsx.Writer{}.WritePresented(context.Background(), s, ledgerPresentation())
	if e != nil {
		t.Fatal(e)
	}
	got := map[string][]string{"plain": cells(t, plain), "presented": cells(t, presented)}
	if os.Getenv("REPORTING_XLSX_WRITE_GOLDEN") == "1" {
		b, _ := json.MarshalIndent(got, "", " ")
		if e := os.WriteFile(cellsGolden, append(b, '\n'), 0o644); e != nil {
			t.Fatal(e)
		}
		t.Logf("wrote %s", cellsGolden)
		return
	}
	raw, e := os.ReadFile(cellsGolden)
	if e != nil {
		t.Fatal(e)
	}
	var want map[string][]string
	if e := json.Unmarshal(raw, &want); e != nil {
		t.Fatal(e)
	}
	if len(want) != len(got) {
		t.Fatalf("the golden file holds %d workbooks, want %d", len(want), len(got))
	}
	// What S20-C adds to a workbook whose query has no filter and no sort, and all it adds: the scope row, after the last row the
	// Metadata sheet had (the fixture's four columns end at row 22), five text cells that count no filter and no sort key.
	added := []string{
		`Metadata!A23: shared "query_scope" formula "" format "" bold false colour "" fill ""`,
		`Metadata!B23: shared "filters" formula "" format "" bold false colour "" fill ""`,
		`Metadata!C23: shared "0" formula "" format "" bold false colour "" fill ""`,
		`Metadata!D23: shared "sort_keys" formula "" format "" bold false colour "" fill ""`,
		`Metadata!E23: shared "0" formula "" format "" bold false colour "" fill ""`,
	}
	for name, lines := range got {
		before := want[name]
		if len(before) == 0 {
			t.Fatalf("%s: the golden file holds no cells", name)
		}
		if len(lines) != len(before)+len(added) {
			t.Errorf("%s: %d facts, want the %d of the golden file and the scope row's %d", name, len(lines), len(before), len(added))
		}
		// Every sheet, pane, width and cell that was there is there unchanged, in its place ...
		for i := range min(len(lines), len(before)) {
			if lines[i] != before[i] {
				t.Errorf("%s: fact %d differs from the golden file:\n got  %s\n want %s", name, i+1, lines[i], before[i])
				break
			}
		}
		// ... and what follows is the scope row, nothing else.
		if len(lines) >= len(before) && !slices.Equal(lines[len(before):], added) {
			t.Errorf("%s: after the cells of the golden file come\n %s\nwant the scope row\n %s", name, strings.Join(lines[len(before):], "\n "), strings.Join(added, "\n "))
		}
	}
}
