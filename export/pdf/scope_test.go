package pdf

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	r "idp.local/reporting/reporting"
)

// narrowedTable is table(2, 2) with two rows and a frozen query that carries two filters, in an order that is not the normalized one,
// and a sort key.
func narrowedTable() r.Snapshot {
	s := table(2, 2, []string{"华东", "2026", "1234567.89", "-0.50"}, []string{"华西", "2025", "0.00", "1000.00"})
	s.Query.Filters = []r.Filter{{Field: "m0", Operator: "gt", Value: "-1000000.00"}, {Field: "d0", Operator: "in", Value: `["华东","华西"]`}}
	s.Query.Sort = []r.Sort{{Field: "m0", Direction: "desc"}}
	return s
}

// plainFooter is the footer of table()'s snapshots as every export printed it before S20-C.
const plainFooter = "合同模拟器 / 合成数据（非真实业务数据） · 快照 0123456789abcdef0123456789abcdef · finance.ledger@2026.10 · 截至 2026-10-27 · 来源水位 wm-42"

func TestBuildSaysTheScopeOfAFilteredSnapshot(t *testing.T) {
	s := narrowedTable()
	d, err := build(s, fakeMetrics(), created)
	if err != nil {
		t.Fatal(err)
	}
	// Under the meta line: the filters in the query's normalized order, each the column's label, the operator in a word and the values
	// as the query has them (a number is not grouped: the table's cells are, the person's input is not), then the sort keys.
	if want := []string{"筛选：维度0 属于 华东、华西；金额0 大于 -1000000.00", "排序：金额0 降序"}; !slices.Equal(d.Scope, want) {
		t.Errorf("scope lines %q, want %q", d.Scope, want)
	}
	// In the footer of every page, beside the snapshot the footer names: how many filters and sort keys narrowed and ordered it.
	if want := "合同模拟器 / 合成数据（非真实业务数据） · 快照 0123456789abcdef0123456789abcdef（已按 2 项条件筛选、1 项排序） · finance.ledger@2026.10 · 截至 2026-10-27 · 来源水位 wm-42"; d.Footer != want {
		t.Errorf("footer %q, want %q", d.Footer, want)
	}
	if b, _ := json.Marshal(d); !strings.Contains(string(b), `"scope":["筛选：维度0 属于 华东、华西；金额0 大于 -1000000.00","排序：金额0 降序"]`) {
		t.Errorf("the data lacks the scope lines: %s", b)
	}
	// Nothing else of the document changes: the writer says the scope and applies nothing. The same snapshot without the filters and
	// the sort is the same document but for the scope lines and the footer's mark.
	whole := narrowedTable()
	whole.Query.Filters, whole.Query.Sort = nil, nil
	plain, err := build(whole, fakeMetrics(), created)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Scope != nil || plain.Footer != plainFooter || plain.Meta != d.Meta {
		t.Errorf("the unfiltered document: scope %q footer %q", plain.Scope, plain.Footer)
	}
	d.Scope, d.Footer = nil, plain.Footer
	if !jsonEqual(d, plain) {
		t.Error("the filters or the sort changed something else of the document")
	}
	// Each list on its own.
	for _, c := range []struct {
		filters []r.Filter
		sort    []r.Sort
		scope   []string
		mark    string
	}{
		{[]r.Filter{{Field: "d1", Operator: "prefix", Value: "20"}}, nil, []string{"筛选：维度1 开头为 20"}, "（已按 1 项条件筛选）"},
		{nil, []r.Sort{{Field: "d0", Direction: "asc"}, {Field: "m1", Direction: "desc"}}, []string{"排序：维度0 升序、金额1 降序"}, "（已按 2 项排序）"},
		{[]r.Filter{{Field: "m1", Operator: "between", Value: `["-0.50","1000.00"]`}, {Field: "d0", Operator: "eq", Value: "华东"}, {Field: "d0", Operator: "ne", Value: "甲、乙"}}, []r.Sort{{Field: "m0", Direction: "asc"}},
			[]string{"筛选：维度0 等于 华东；维度0 不等于 “甲、乙”；金额1 介于 -0.50 和 1000.00 之间", "排序：金额0 升序"}, "（已按 3 项条件筛选、1 项排序）"},
	} {
		s := narrowedTable()
		s.Query.Filters, s.Query.Sort = c.filters, c.sort
		d, err := build(s, fakeMetrics(), created)
		if err != nil {
			t.Fatal(err)
		}
		if footer := d.Footer.(string); !slices.Equal(d.Scope, c.scope) || !strings.Contains(footer, "快照 0123456789abcdef0123456789abcdef"+c.mark+" · finance.ledger@2026.10") {
			t.Errorf("scope %q footer %q, want %q and the mark %q", d.Scope, footer, c.scope, c.mark)
		}
	}
}

// TestAPresentedBuildCarriesTheSameScope: the filters are the query's, whatever the layout shows. The presentation leaves out a column
// the query filters by and relabels another; the scope lines are those of the plain export, with the dataset's labels.
func TestAPresentedBuildCarriesTheSameScope(t *testing.T) {
	s := narrowedTable()
	plain, err := build(s, fakeMetrics(), created)
	if err != nil {
		t.Fatal(err)
	}
	p := r.Presentation{LayoutID: "0123456789abcdef0123456789abcdef", LayoutVersion: 2, Title: "分区账龄", Columns: []r.PresentedColumn{{ID: "d1", Align: r.AlignLeft}, {ID: "m0", Label: "应收", Align: r.AlignRight}}}
	d, err := buildTable(s, &p, fakeMetrics(), created)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.Scope, plain.Scope) || len(d.Scope) != 2 || d.Footer != plain.Footer {
		t.Errorf("presented scope %q footer %q, plain %q %q", d.Scope, d.Footer, plain.Scope, plain.Footer)
	}
	if meta := d.Meta.(string); !strings.HasSuffix(meta, " · 布局 0123456789abcdef0123456789abcdef@2") || !jsonEqual(d.Header, []any{"维度1", "应收"}) {
		t.Errorf("meta %q header %v", meta, d.Header)
	}
	// Without filters a presented document has no scope either.
	s.Query.Filters, s.Query.Sort = nil, nil
	if d, err = buildTable(s, &p, fakeMetrics(), created); err != nil || d.Scope != nil || d.Footer != plainFooter {
		t.Errorf("an unfiltered presented document: scope %q footer %v (%v)", d.Scope, d.Footer, err)
	}
}

// TestABuildWithoutFiltersOrSortIsWhatItWas: no scope key in the data, the footer to the character what it was, and the same data for
// a query whose lists are nil as for one whose lists are empty (the stored form of a query without them).
func TestABuildWithoutFiltersOrSortIsWhatItWas(t *testing.T) {
	s := table(2, 2, []string{"华东", "2026", "1234567.89", "-0.50"})
	d, err := build(s, fakeMetrics(), created)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), `"scope"`) || d.Scope != nil || d.Footer != plainFooter {
		t.Errorf("a plain document carries a scope, or its footer changed: %s", b)
	}
	s.Query.Filters, s.Query.Sort = []r.Filter{}, []r.Sort{}
	empty, err := build(s, fakeMetrics(), created)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := json.Marshal(empty); string(c) != string(b) {
		t.Error("empty lists of filters and sort keys change the data")
	}
	// The words of the scope are checked against the fonts only when they are printed: a font without them still renders a query
	// without filters, as it always did.
	if _, err := build(s, fakeMetrics('筛', '排', '按'), created); err != nil {
		t.Errorf("an unfiltered document with a font that lacks the scope's words: %v", err)
	}
}

// continued reads a sentence that lists only its first items: how many it says there are, how many it lists, the listed text and how
// many it says are elsewhere.
var continued = regexp.MustCompile(`^(筛选|排序)（共 (\d+) 项，此处列出前 (\d+) 项）：(.*)；其余 (\d+) 项见本快照的查询及其 XLSX 导出的 Metadata 工作表。$`)

// TestTheScopeLinesAreBoundAndSayWhenTheyContinue: the filter sentence takes at most six lines of the meta block and the sort sentence
// at most two. A sentence that does not fit is never cut: it says the count, lists the first whole items that fit and says where the
// rest are; when not even the first fits, it says the count and where they all are.
func TestTheScopeLinesAreBoundAndSayWhenTheyContinue(t *testing.T) {
	if maxFilterLines != 6 || maxSortLines != 2 {
		t.Fatalf("the bound is %d and %d lines; docs/S20C_SCOPE.md and docs/API.md say 6 and 2", maxFilterLines, maxSortLines)
	}
	m := fakeMetrics()
	const width = a4Short - 2*marginX // the text width of a portrait page, the narrowest a table export has
	fits := func(what string, lines []string, limit int) {
		t.Helper()
		if len(lines) == 0 || len(lines) > limit {
			t.Errorf("%s: %d lines, want 1..%d", what, len(lines), limit)
		}
		for _, line := range lines {
			if w, err := m.width(line, metaSize, false); err != nil || w*slack > width {
				t.Errorf("%s: a line is %.1f pt wide in a box of %.1f pt (%v)", what, w*slack, width, err)
			}
		}
	}
	value := func(i, n int) string { return strings.Repeat(string(rune('甲'+i)), n) }
	filtered := func(n, length int) r.Snapshot {
		s := table(1, 1, []string{"a", "1.00"})
		s.Result.Columns[0].Label = "客户"
		for i := range n {
			s.Query.Filters = append(s.Query.Filters, r.Filter{Field: "d0", Operator: "ne", Value: value(i, length)})
		}
		return s
	}
	conditions := func(n, length int) []string {
		var out []string
		for i := range n {
			out = append(out, "客户 不等于 "+value(i, length))
		}
		slices.Sort(out) // the normalized order: by value, as the field and the operator are the same
		return out
	}

	// Sixteen short filters fit: every one is listed, in one sentence, and nothing says "elsewhere".
	d, err := build(filtered(16, 4), m, created)
	if err != nil {
		t.Fatal(err)
	}
	fits("16 short filters", d.Scope, maxFilterLines)
	if got, want := strings.Join(d.Scope, ""), "筛选："+strings.Join(conditions(16, 4), "；"); got != want {
		t.Errorf("16 short filters: %q, want %q", got, want)
	}
	if footer := d.Footer.(string); !strings.Contains(footer, "（已按 16 项条件筛选）") {
		t.Errorf("footer %q", footer)
	}

	// Sixteen filters of sixty characters do not: the sentence says sixteen, lists the first that fit whole and says where the rest are.
	d, err = build(filtered(16, 60), m, created)
	if err != nil {
		t.Fatal(err)
	}
	fits("16 long filters", d.Scope, maxFilterLines)
	got := continued.FindStringSubmatch(strings.Join(d.Scope, ""))
	if got == nil {
		t.Fatalf("16 long filters: %q is not a sentence that says it continues", d.Scope)
	}
	total, _ := strconv.Atoi(got[2])
	listed, _ := strconv.Atoi(got[3])
	rest, _ := strconv.Atoi(got[5])
	all := conditions(16, 60)
	if got[1] != "筛选" || total != 16 || listed < 1 || listed > 15 || rest != 16-listed || got[4] != strings.Join(all[:listed], "；") {
		t.Fatalf("16 long filters: %d of %d listed, %d elsewhere, the listed text %q", listed, total, rest, got[4])
	}
	// As many as fit: with one more the sentence would take a seventh line. (The fake font sets an ideograph one em wide: a line of
	// the portrait page holds 68 em, a condition here is 66 em, and the sentence's own words take about 50 em: five conditions fill
	// the six lines, six do not fit.)
	if listed != 5 {
		t.Errorf("16 long filters: %d listed, want 5", listed)
	}
	more := fmt.Sprintf("筛选（共 16 项，此处列出前 %d 项）：%s；其余 %d 项见本快照的查询及其 XLSX 导出的 Metadata 工作表。", listed+1, strings.Join(all[:listed+1], "；"), 15-listed)
	if lines, err := m.lines(more, metaSize, false, width); err != nil || len(lines) <= maxFilterLines {
		t.Errorf("one more filter would have fitted: %d lines (%v)", len(lines), err)
	}
	// The footer still counts every filter: the bound is the first page's, the count is the query's.
	if footer := d.Footer.(string); !strings.Contains(footer, "（已按 16 项条件筛选）") {
		t.Errorf("footer %q", footer)
	}

	// A query at its bounds, 16 filters of 32 values of 128 characters each (more than sixty lines a filter): not even the first fits,
	// and the sentence says so in one line. It is built in well under the time a job has.
	huge := table(1, 1, []string{"a", "1.00"})
	list := `["` + strings.Join(slices.Repeat([]string{strings.Repeat("值", r.MaxFilterValueChars)}, r.MaxInValues), `","`) + `"]`
	huge.Query.Filters = slices.Repeat([]r.Filter{{Field: "d0", Operator: "in", Value: list}}, r.MaxQueryFilters)
	start := time.Now()
	if d, err = build(huge, m, created); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("a query at its bounds took %s to lay out", took)
	}
	if want := []string{"筛选：共 16 项，此处未能列出，见本快照的查询及其 XLSX 导出的 Metadata 工作表。"}; !slices.Equal(d.Scope, want) {
		t.Errorf("a query at its bounds: %q, want %q", d.Scope, want)
	}
	fits("a query at its bounds", d.Scope, 1)
	// One filter too long for the block is not listed in part either.
	huge.Query.Filters = huge.Query.Filters[:1]
	if d, err = build(huge, m, created); err != nil || !slices.Equal(d.Scope, []string{"筛选：共 1 项，此处未能列出，见本快照的查询及其 XLSX 导出的 Metadata 工作表。"}) {
		t.Errorf("one filter longer than the block: %q (%v)", d.Scope, err)
	}

	// The sort sentence has two lines of its own, beside the filter sentence's six. Three keys on columns whose labels are sixty
	// characters long (a label has no bound of its own): the first key is listed and the rest are elsewhere; with labels of two hundred
	// characters not even the first fits. (The sentence is composed for the portrait width here: columns with such labels make the
	// table itself landscape.)
	keys := func(length int) []string {
		return []string{value(0, length) + " 降序", value(1, length) + " 升序", value(2, length) + " 降序"}
	}
	lines, err := m.scopeSentence(scopeSort, keys(4), "、", maxSortLines, width)
	if err != nil || !slices.Equal(lines, []string{"排序：" + strings.Join(keys(4), "、")}) {
		t.Errorf("three short sort keys: %q (%v)", lines, err)
	}
	if lines, err = m.scopeSentence(scopeSort, keys(60), "、", maxSortLines, width); err != nil {
		t.Fatal(err)
	}
	fits("three long sort keys", lines, maxSortLines)
	if want := "排序（共 3 项，此处列出前 1 项）：" + keys(60)[0] + "；其余 2 项见本快照的查询及其 XLSX 导出的 Metadata 工作表。"; strings.Join(lines, "") != want {
		t.Errorf("three long sort keys: %q, want %q", lines, want)
	}
	if lines, err = m.scopeSentence(scopeSort, keys(200), "、", maxSortLines, width); err != nil || !slices.Equal(lines, []string{"排序：共 3 项，此处未能列出，见本快照的查询及其 XLSX 导出的 Metadata 工作表。"}) {
		t.Errorf("three sort keys longer than the block: %q (%v)", lines, err)
	}
	// Through the build: a filter and three sort keys on columns with such labels give both sentences, each within its own lines of
	// the page the table chose, and the footer counts both.
	s := table(3, 1, []string{"a", "b", "c", "1.00"})
	for i := range 3 {
		s.Result.Columns[i].Label = value(i, 200)
		s.Query.Sort = append(s.Query.Sort, r.Sort{Field: "d" + strconv.Itoa(i), Direction: []string{"desc", "asc", "desc"}[i]})
	}
	s.Query.Filters = []r.Filter{{Field: "m0", Operator: "ge", Value: "0.00"}}
	if d, err = build(s, m, created); err != nil {
		t.Fatal(err)
	}
	if len(d.Scope) < 2 || len(d.Scope) > 1+maxSortLines || d.Scope[0] != "筛选：金额0 不小于 0.00" || !strings.HasPrefix(d.Scope[1], "排序") {
		t.Errorf("a filter and a long sort: %q", d.Scope)
	}
	if footer := flatten(d.Footer); !strings.Contains(footer, "（已按 1 项条件筛选、3 项排序）") {
		t.Errorf("footer %q", footer)
	}
	// The sentence that says "elsewhere" is one line on the narrowest page, whatever it counts.
	for _, n := range []int{1, 16} {
		for _, word := range []string{scopeFilters, scopeSort} {
			text := fmt.Sprintf(scopeUnlisted, word, n, scopeElsewhere)
			if lines, err := m.lines(text, metaSize, false, width); err != nil || len(lines) != 1 {
				t.Errorf("%q takes %d lines (%v)", text, len(lines), err)
			}
		}
	}
}

// TestTheScopeIsCheckedAgainstTheFontsAndTheColumns: every character of the scope is checked against the fonts like a value of the
// table (the values, the labels and the words; the whole scope, whether or not the bound lets all of it be printed), and a frozen query
// that does not fit the frozen columns is an integrity failure.
func TestTheScopeIsCheckedAgainstTheFontsAndTheColumns(t *testing.T) {
	const missing = '☃'
	for name, change := range map[string]func(*r.Snapshot){
		"a filter value": func(s *r.Snapshot) { s.Query.Filters[1].Value = `["华东","` + string(missing) + `"]` },
		"a value the bound keeps off the page": func(s *r.Snapshot) {
			// Sixteen filters in all: thirteen of a hundred characters fill the block, and the one with the character is the last of
			// its field in the normalized order, far beyond the sixth line.
			for i := range 13 {
				s.Query.Filters = append(s.Query.Filters, r.Filter{Field: "d1", Operator: "ne", Value: strings.Repeat(string(rune('甲'+i)), 100)})
			}
			s.Query.Filters = append(s.Query.Filters, r.Filter{Field: "d1", Operator: "ne", Value: "龥" + string(missing)})
		},
	} {
		s := narrowedTable()
		change(&s)
		if _, err := build(s, fakeMetrics(missing), created); !errors.Is(err, r.ErrUnsupported) {
			t.Errorf("%s the fonts do not cover: %v; want unsupported", name, err)
		}
		if _, err := build(s, fakeMetrics(), created); err != nil {
			t.Errorf("%s the fonts cover: %v", name, err)
		}
	}
	// The words of the sentences and of the footer's mark are data too.
	for _, ch := range []rune{'筛', '属', '排', '降', '按', '项'} {
		if _, err := build(narrowedTable(), fakeMetrics(ch), created); !errors.Is(err, r.ErrUnsupported) {
			t.Errorf("a font without %c: %v; want unsupported", ch, err)
		}
	}
	// The label of a filtered column the layout does not show is printed in the scope line, so it is checked there.
	s := narrowedTable()
	s.Result.Columns[0].Label = "区域" + string(missing)
	p := r.Presentation{LayoutID: "0123456789abcdef0123456789abcdef", LayoutVersion: 1, Columns: []r.PresentedColumn{{ID: "m0", Align: r.AlignRight}}}
	if _, err := buildTable(s, &p, fakeMetrics(missing), created); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a label only the scope prints: %v; want unsupported", err)
	}
	// A line break or a tab in a label is printed as the headers print it: a break ends the line, a tab is a space.
	s = narrowedTable()
	s.Result.Columns[0].Label = "区域\r\n（大区）\t甲"
	if d, err := build(s, fakeMetrics('\t', '\r'), created); err != nil || !slices.Equal(d.Scope, []string{"筛选：区域", "（大区） 甲 属于 华东、华西；金额0 大于 -1000000.00", "排序：金额0 降序"}) {
		t.Errorf("a label with a line break: %q (%v)", d.Scope, err)
	}
	for name, change := range map[string]func(*r.Snapshot){
		"a filter on no column":   func(s *r.Snapshot) { s.Query.Filters[0].Field = "nope" },
		"an unknown operator":     func(s *r.Snapshot) { s.Query.Filters[0].Operator = "like" },
		"a grouped number":        func(s *r.Snapshot) { s.Query.Filters[0].Value = "1,000.00" },
		"a list that is not one":  func(s *r.Snapshot) { s.Query.Filters[1].Value = "华东" },
		"an unknown direction":    func(s *r.Snapshot) { s.Query.Sort[0].Direction = "down" },
		"a sort key on no column": func(s *r.Snapshot) { s.Query.Sort[0].Field = "nope" },
	} {
		s := narrowedTable()
		change(&s)
		if _, err := build(s, fakeMetrics(), created); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("%s: %v; want an integrity failure", name, err)
		}
		p := presentation()
		if _, err := buildTable(s, &p, fakeMetrics(), created); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("%s, presented: %v; want an integrity failure", name, err)
		}
	}
}

// TestLinesFillGreedilyAndLoseNothing: the lines the scope block is printed on. A line breaks only where a line may certainly break, a
// run wider than the box is cut into pieces that fit, every line fits the box by the measure with its slack, and the lines joined are
// the text.
func TestLinesFillGreedilyAndLoseNothing(t *testing.T) {
	m := fakeMetrics()
	// At 10 pt the fake font sets an ideograph 10 pt wide and a Latin character or a space 5 pt; a line holds width / 1.03.
	for _, c := range []struct {
		text  string
		width float64
		want  []string
	}{
		{"甲乙丙丁 abc def", 35, []string{"甲乙丙", "丁 ", "abc ", "def"}},
		{"甲乙丙丁 abc def", 36.1, []string{"甲乙丙", "丁 abc ", "def"}},
		{"甲乙丙丁 abc def", 1000, []string{"甲乙丙丁 abc def"}},
		{"甲乙\n丙丁", 1000, []string{"甲乙", "丙丁"}},
		{"WWWWWWWWWW", 22, []string{"WWWW", "WWWW", "WW"}},
		{"WWWWWWWWWW x", 22, []string{"WWWW", "WWWW", "WW x"}},
		{"甲 WWWWWWWWWW", 22, []string{"甲 ", "WWWW", "WWWW", "WW"}},
		// A mark that may not start a line stays with the character before it.
		{"甲乙丙，丁", 31, []string{"甲乙", "丙，", "丁"}},
		{"", 100, []string{""}},
	} {
		got, err := m.lines(c.text, 10, false, c.width)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("lines(%q, %v) = %q (%v), want %q", c.text, c.width, got, err, c.want)
		}
	}
	if _, err := m.lines("中中", 10, false, 5); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a character wider than the box: %v; want unsupported", err)
	}
	if _, err := fakeMetrics('☃').lines("a ☃", 10, false, 100); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a character the fonts do not cover: %v; want unsupported", err)
	}
	// Text of every kind at random, with a fixed seed. A combining mark is never parted from the character before it (cut), so a
	// piece that ends with marks may be wider than the box by their advance, as in a table cell; the configured fonts give a mark no
	// advance, the fake one half an em. Every other line fits.
	alphabet := []rune("aZ09-_/.:,;!?@#$%^&*()[]{}<>\"'\\| \n中文，。（）「」；：、“”ﬁé")
	marked := append(slices.Clone(alphabet), '\u0301')
	rnd := rand.New(rand.NewPCG(5, 6))
	for n := range 4000 {
		from := alphabet
		if n%4 == 3 {
			from = marked
		}
		b := make([]rune, rnd.IntN(160))
		for i := range b {
			b[i] = from[rnd.IntN(len(from))]
		}
		in, width, bold := string(b), 16+rnd.Float64()*200, rnd.IntN(2) == 0
		got, err := m.lines(in, bodySize, bold, width)
		if err != nil {
			t.Fatal(err)
		}
		if out := strings.Join(got, ""); out != strings.ReplaceAll(in, "\n", "") {
			t.Fatalf("lines(%q) printed %q", in, out)
		}
		if len(got) < strings.Count(in, "\n")+1 {
			t.Fatalf("lines(%q) lost a line break: %q", in, got)
		}
		for _, line := range got {
			bare := strings.TrimRight(line, "\u0301")
			if w, _ := m.width(bare, bodySize, bold); w*slack > width+1e-9 {
				t.Fatalf("a line of lines(%q) is %.2f pt wide in a box of %.2f pt: %q", in, w*slack, width, line)
			}
		}
	}
	// A combining mark stays with the character before it when a run is cut.
	got, err := m.lines(strings.Repeat("e\u0301", 40), bodySize, false, 30)
	if err != nil || len(got) < 2 {
		t.Fatal(got, err)
	}
	for _, line := range got {
		if strings.HasPrefix(line, "\u0301") {
			t.Fatalf("a line starts with a combining mark: %q", line)
		}
	}
}
