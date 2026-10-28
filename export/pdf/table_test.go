package pdf

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// tabled returns a schema 1.3 document with one box and a table of the given columns.
func tabled(mode string, x, y, w, h float64, size int, columns ...doctemplates.TableColumn) doctemplates.Document {
	d := form(mode, 0, 0, box("n1", "literal", "标题", 20, 30, 80, 12, "left"))
	d.Schema = "1.3"
	d.Table = &doctemplates.Table{X: x, Y: y, Width: w, Height: h, FontSize: size, Columns: columns}
	return d
}

// amounts returns rows of a string and a decimal column, their exact total and the data.
func amounts(n int) *doctemplates.TableData {
	data := &doctemplates.TableData{Columns: []r.Column{{ID: "customer", Label: "客户", Type: "string"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2, Currency: "CNY"}}, Metric: []bool{false, true}, Totals: map[string]string{}}
	cents := int64(0)
	for i := range n {
		v := int64(100*i*100 + 25) // 100i + 0.25
		cents += v
		data.Rows = append(data.Rows, []string{fmt.Sprintf("客户%d", i), fmt.Sprintf("%d.%02d", v/100, v%100)})
	}
	data.Totals["amount"] = fmt.Sprintf("%d.%02d", cents/100, cents%100)
	return data
}

func TestColumnSumsExactly(t *testing.T) {
	c := &column{scale: 2}
	for _, v := range []string{"1.50", "2", "-0.25", "9007199254740993.01"} {
		if err := c.add(v); err != nil {
			t.Fatal(v, err)
		}
	}
	if got := c.String(); got != "9007199254740996.26" {
		t.Errorf("sum %s", got)
	}
	if !c.equals("9007199254740996.260") || c.equals("9007199254740996.27") || c.equals("x") {
		t.Error("equals")
	}
	// A declared scale longer than any fraction seen: the sum prints and compares at that scale.
	c = &column{scale: 2}
	for _, v := range []string{"1", "2"} {
		_ = c.add(v)
	}
	if got := c.String(); got != "3.00" || !c.equals("3") || !c.equals("3.0") || c.equals("3.01") {
		t.Errorf("sum %s", got)
	}
	// A negative sum, and a column with no fraction at all.
	c = &column{}
	for _, v := range []string{"-5", "2"} {
		_ = c.add(v)
	}
	if got := c.String(); got != "-3" {
		t.Errorf("sum %s", got)
	}
	c = &column{scale: 3}
	_ = c.add("0.5")
	if got := c.String(); got != "0.500" {
		t.Errorf("sum %s", got)
	}
	if err := c.add("1,000"); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a grouped number: %v", err)
	}
}

func TestWrapBreaksCellsLikeTheExport(t *testing.T) {
	lay := &tableLayout{m: plainFake(), size: 10}
	// The fake font: ideographs 10 pt, other characters 5 pt. A 60 pt line holds six
	// ideographs or twelve Latin characters.
	for text, want := range map[string][]string{
		"甲乙丙丁戊己庚辛":                    {"甲乙丙丁戊己", "庚辛"},                       // broken between ideographs
		"one two three four":          {"one two", "three four"},              // broken at spaces, leading space dropped
		"abcdefghijklmnopqrstuvwxyz":  {"abcdefghijkl", "mnopqrstuvwx", "yz"}, // a run wider than the line, cut
		"ab\ncd":                      {"ab", "cd"},                           // a value's own line break
		"  lead":                      {"  lead"},                             // leading spaces of the value kept
		"":                            {""},
		strings.Repeat("甲", 6) + "ab": {"甲甲甲甲甲", "甲ab"}, // no certain break between an ideograph and a letter
		"trail   ":                    {"trail"},
	} {
		c, err := lay.wrap(text, 60, false)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if strings.Join(c.lines, "|") != strings.Join(want, "|") {
			t.Errorf("%q: %q, want %q", text, c.lines, want)
		}
		for i, l := range c.lines {
			w, _ := lay.m.width(l, 10, false)
			if math.Abs(w-c.widths[i]) > 1e-9 || w > 60 {
				t.Errorf("%q: line %q width %.1f", text, l, c.widths[i])
			}
		}
	}
	lay.m = plainFake('☃')
	if _, err := lay.wrap("☃", 60, false); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("an uncovered character: %v", err)
	}
}

func TestLayOutTablePaginatesWithCarriedTotals(t *testing.T) {
	// With the fake font at 10 pt every row is one line, 18.6 pt high; the 60 mm region holds
	// the header and 7 rows plus a carry-forward row on page 1, 6 rows between a brought-forward
	// and a carry-forward row on later pages, and the total row on the last page.
	d := tabled("full", 20, 40, 120, 60, 10, doctemplates.TableColumn{Field: "customer", Width: 60}, doctemplates.TableColumn{Field: "amount", Width: 60})
	data := amounts(20)
	p, err := layOut(d, map[string]string{"n1": "标题"}, nil, data, plainFake())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Pages) != 4 {
		t.Fatalf("%d pages", len(p.Pages))
	}
	texts := func(i int) []string {
		var out []string
		for _, g := range p.Pages[i].Texts {
			out = append(out, g.Text)
		}
		return out
	}
	has := func(i int, want ...string) {
		t.Helper()
		all := strings.Join(texts(i), "|")
		for _, w := range want {
			if !strings.Contains(all, w) {
				t.Errorf("page %d lacks %q: %s", i+1, w, all)
			}
		}
	}
	// The box and the header on every page; the carried rows with the page numbers; the sums.
	for i := range 4 {
		has(i, "标", "题", "客户", "金额（CNY）")
	}
	has(0, "客户0", "客户6", "过次（转第 2 页）", "2,101.75") // 7 rows: 0.25 + 100.25 + ... + 600.25
	has(1, "承前（第 2 页）", "2,101.75", "客户7", "客户12", "过次（转第 3 页）", "7,803.25")
	has(2, "承前（第 3 页）", "客户13", "客户18", "过次（转第 4 页）", "17,104.75")
	has(3, "承前（第 4 页）", "客户19", "合计", "19,005.00")
	if strings.Contains(strings.Join(texts(0), "|"), "客户7") || strings.Contains(strings.Join(texts(3), "|"), "客户18") {
		t.Error("rows on the wrong page")
	}
	// Rules, fills and cells in full mode: a header fill per column on every page, cells for
	// every drawn row, the rule above the total row on the last page only.
	if len(p.Pages[0].Fills) != 2 || len(p.Pages[0].Cells) != 2*(1+7+1) || len(p.Pages[0].Rules) != 0 || len(p.Pages[3].Rules) != 1 || len(p.Pages[3].Cells) != 2*(1+1+1+1) {
		t.Errorf("page furniture %+v", p.Pages[0].Cells)
	}
	// Amounts are right-aligned: the sum ends at the column's right content edge.
	right := (20+120)*mm - tableInsetX
	for _, g := range p.Pages[3].Texts {
		if g.Text == "19,005.00" && math.Abs(g.X+g.Width-right) > 1e-9 {
			t.Errorf("total at %.2f+%.2f, want end %.2f", g.X, g.Width, right)
		}
		if g.Text == "合计" && (!g.Bold || math.Abs(g.X-(20*mm+tableInsetX)) > 1e-9) {
			t.Errorf("label %+v", g)
		}
	}
	// Variables mode: values and carried rows only, no header text, no furniture.
	v := tabled("variables", 20, 40, 120, 60, 10, d.Table.Columns...)
	p, err = layOut(v, map[string]string{"n1": "标题"}, nil, data, plainFake())
	if err != nil || len(p.Pages) != 4 || len(p.Pages[0].Fills)+len(p.Pages[0].Cells)+len(p.Pages[3].Rules) != 0 {
		t.Fatalf("variables mode: %v %d pages", err, len(p.Pages))
	}
	if all := strings.Join(texts(0), "|"); strings.Contains(all, "金额") || !strings.Contains(all, "客户0") || !strings.Contains(all, "过次（转第 2 页）") || strings.Contains(all, "标题") || strings.Contains(all, "标") {
		t.Errorf("variables page 1: %s", all)
	}
	// An empty table is one page with the header and a zero total.
	p, err = layOut(d, map[string]string{"n1": "标题"}, nil, amounts(0), plainFake())
	if err != nil || len(p.Pages) != 1 {
		t.Fatalf("empty: %v %d", err, len(p.Pages))
	}
	has(0, "合计", "0.00")
}

func TestLayOutTableRefusesWhatItCannotLayOut(t *testing.T) {
	columns := []doctemplates.TableColumn{{Field: "customer", Width: 60}, {Field: "amount", Width: 60}}
	d := tabled("full", 20, 40, 120, 60, 10, columns...)
	// The rows must add up to the snapshot's total.
	data := amounts(3)
	data.Totals["amount"] = "1.00"
	if _, err := layOut(d, map[string]string{"n1": "x"}, nil, data, plainFake()); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a total that is not the sum: %v", err)
	}
	// A metric value that is not an exact number.
	data = amounts(3)
	data.Rows[1][1] = "1e3"
	if _, err := layOut(d, map[string]string{"n1": "x"}, nil, data, plainFake()); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a malformed number: %v", err)
	}
	// A row taller than a page: 40 lines of 15 pt each in a 60 mm region.
	data = amounts(3)
	data.Rows[1][0] = strings.Repeat("甲\n", 40)
	if _, err := layOut(d, map[string]string{"n1": "x"}, nil, data, plainFake()); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a row taller than the region: %v", err)
	}
	// More than 100 pages: 1000 rows of two lines in a 30 mm region.
	data = amounts(1000)
	for _, row := range data.Rows {
		row[0] = "a\nb"
	}
	short := tabled("full", 20, 40, 120, 30, 10, columns...)
	if _, err := layOut(short, map[string]string{"n1": "x"}, nil, data, plainFake()); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("over %d pages: %v", maxPages, err)
	}
	// A table without its rows, or with other columns, is invalid.
	if _, err := layOut(d, map[string]string{"n1": "x"}, nil, nil, plainFake()); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("no rows: %v", err)
	}
	data = amounts(3)
	data.Columns = data.Columns[:1]
	if _, err := layOut(d, map[string]string{"n1": "x"}, nil, data, plainFake()); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("other columns: %v", err)
	}
	// A cell character the fonts lack.
	data = amounts(3)
	data.Rows[0][0] = "☃"
	if _, err := layOut(d, map[string]string{"n1": "x"}, nil, data, plainFake('☃')); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("an uncovered character: %v", err)
	}
	// Amounts and carried sums are never cut: one wider than its column is unsupported, as is a
	// carried row's label wider than the first column. With the fake font at 10 pt a 16 mm
	// column holds 39.4 pt of content: six bold characters (6 pt each); the grouped sum of 20
	// rows is nine characters.
	narrow := tabled("full", 20, 40, 120, 60, 10, doctemplates.TableColumn{Field: "customer", Width: 104}, doctemplates.TableColumn{Field: "amount", Width: 16})
	data = amounts(3)
	if _, err := layOut(narrow, map[string]string{"n1": "x"}, nil, data, plainFake()); err != nil {
		t.Errorf("three small amounts in a narrow column: %v", err)
	}
	data = amounts(20)
	if _, err := layOut(narrow, map[string]string{"n1": "x"}, nil, data, plainFake()); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a carried sum wider than its column: %v", err)
	}
	data = amounts(1)
	data.Rows[0][1] = "123456789.00"
	data.Totals["amount"] = "123456789.00"
	if _, err := layOut(narrow, map[string]string{"n1": "x"}, nil, data, plainFake()); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("an amount wider than its column: %v", err)
	}
	label := tabled("full", 20, 40, 120, 60, 10, doctemplates.TableColumn{Field: "customer", Width: 12}, doctemplates.TableColumn{Field: "amount", Width: 108})
	if _, err := layOut(label, map[string]string{"n1": "x"}, nil, amounts(3), plainFake()); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a carried label wider than the first column: %v", err)
	}
	// An empty table whose header and total row are taller than the region.
	tall := tabled("full", 20, 40, 120, 30, 10, columns...)
	data = amounts(0)
	data.Columns[0].Label = strings.Repeat("甲", 200)
	if _, err := layOut(tall, map[string]string{"n1": "x"}, nil, data, plainFake()); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a header taller than the region: %v", err)
	}
}
