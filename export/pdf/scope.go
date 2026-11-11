package pdf

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"idp.local/reporting/export/present"
	r "idp.local/reporting/reporting"
)

// The scope of a table export (S20-C, docs/ADR_0023_PUBLISHED_FILTERS_AND_SORTS.md decision 6). A snapshot made with filters is a
// narrowed scope and its totals are that scope's; one made with a sort is ordered as asked. The export says both where a person reads
// the report:
//
//   - under the meta line of the first page, in words: `筛选：账龄区间 属于 1_30、31_60；应收余额 大于 1000.00` and
//     `排序：应收余额 降序` (the column's label, the operator in a word, the values as the query has them; present.ScopeFilter.Chinese);
//   - in the footer of every page, beside the snapshot it names: `快照 <id>（已按 2 项条件筛选、1 项排序）`, so that the page that
//     carries the total row says that the total is a narrowed scope's.
//
// A query may carry 16 filters of 32 values of 128 characters, more than a page of words, and a PDF never cuts silently: the filter
// sentence takes at most maxFilterLines lines of the meta block and the sort sentence at most maxSortLines. A sentence that does not
// fit says how many items it has, lists as many whole items as fit (in the query's order, never a part of one) and says that the rest
// are in the snapshot's query and in the Metadata sheet of its workbook export, which holds every one exactly.
//
// The lines are composed here, not by Typst: each is one printed line that fits the width by the server's measure, so the number of
// lines is the server's to bound and the same input gives the same lines. Every character of them, the words included, is checked
// against the fonts by that measuring, and so is every character of the whole scope even when the bound keeps a part of it off the
// page: a filter value the fonts do not cover is unsupported, as a value of the table is, never dropped. A query without filters and
// without a sort has no scope block and no mark: its document is what it was.
const (
	maxFilterLines = 6
	maxSortLines   = 2

	scopeFilters  = "筛选"
	scopeSort     = "排序"
	scopeListed   = "%s（共 %d 项，此处列出前 %d 项）：%s；其余 %d 项见%s。"
	scopeUnlisted = "%s：共 %d 项，此处未能列出，见%s。"
	// scopeElsewhere is where the whole scope is when the page does not list all of it.
	scopeElsewhere = "本快照的查询及其 XLSX 导出的 Metadata 工作表"
)

// scopeBlock is what a table export says of its query's scope: the lines under the meta line, for a box of the given width, and the
// mark the footer sets beside the snapshot's id. Both are empty for a query without filters and without a sort.
func scopeBlock(sc present.Scope, m *measurer, width float64) (lines []string, mark string, err error) {
	if len(sc.Filters) > 0 {
		items := make([]string, len(sc.Filters))
		for i, f := range sc.Filters {
			items[i] = f.Chinese()
		}
		sentence, err := m.scopeSentence(scopeFilters, items, "；", maxFilterLines, width)
		if err != nil {
			return nil, "", err
		}
		lines = append(lines, sentence...)
	}
	if len(sc.Sort) > 0 {
		items := make([]string, len(sc.Sort))
		for i, k := range sc.Sort {
			items[i] = k.Chinese()
		}
		sentence, err := m.scopeSentence(scopeSort, items, "、", maxSortLines, width)
		if err != nil {
			return nil, "", err
		}
		lines = append(lines, sentence...)
	}
	return lines, scopeMark(sc), nil
}

// scopeMark is what the footer of every page says beside the snapshot's id when the query narrowed or ordered it: how many filters and
// sort keys. The words are on the first page.
func scopeMark(sc present.Scope) string {
	switch filters, keys := len(sc.Filters), len(sc.Sort); {
	case filters > 0 && keys > 0:
		return fmt.Sprintf("（已按 %d 项条件筛选、%d 项排序）", filters, keys)
	case filters > 0:
		return fmt.Sprintf("（已按 %d 项条件筛选）", filters)
	case keys > 0:
		return fmt.Sprintf("（已按 %d 项排序）", keys)
	}
	return ""
}

// scopeSentence composes one sentence of the scope block within limit lines of the width: the word and all its items when they fit;
// else the count, the first items that fit whole, and where the rest are; else, when not even the first item fits, the count and where
// they all are. The whole sentence is measured first, so that every character of every item is checked against the fonts whether or
// not it is printed in the end.
func (m *measurer) scopeSentence(word string, items []string, separator string, limit int, width float64) ([]string, error) {
	n := len(items)
	for k := n; k >= 0; k-- {
		text := word + "：" + strings.Join(items, separator)
		switch {
		case k == 0:
			text = fmt.Sprintf(scopeUnlisted, word, n, scopeElsewhere)
		case k < n:
			text = fmt.Sprintf(scopeListed, word, n, k, strings.Join(items[:k], separator), n-k, scopeElsewhere)
		}
		lines, err := m.lines(printable(text), metaSize, false, width)
		if err != nil {
			return nil, err
		}
		if len(lines) <= limit {
			return lines, nil
		}
	}
	return nil, fmt.Errorf("pdf: the scope of the query does not fit the meta block: %w", r.ErrUnsupported)
}

// lines breaks a text into the lines it is printed on in a box of the given width: filled greedily, broken only where a line may
// certainly break (runs) and at the text's own line breaks, a run wider than the box cut into pieces that each fit (cut). Every character
// is checked against the fonts. Each line fits the box by the server's measure, with the slack that absorbs what Typst adds, so Typst
// sets it on one line (a piece of a cut run keeps the combining marks that follow its last character, as in a table cell, whatever
// advance the font gives them); the lines joined are the text, nothing lost and nothing added.
func (m *measurer) lines(text string, size float64, bold bool, width float64) ([]string, error) {
	k := slack
	if m.plain {
		k = 1
	}
	var out []string
	for _, l := range strings.Split(text, "\n") {
		var line strings.Builder
		w := 0.0      // the width of the line so far, in points, before the slack
		var last rune // its last character
		flush := func() {
			out = append(out, line.String())
			line.Reset()
			w, last = 0, 0
		}
		err := runs(l, func(run string) error {
			rw, err := m.width(run, size, bold)
			if err != nil {
				return err
			}
			first, _ := utf8.DecodeRuneInString(run)
			joined := w + rw
			if !m.plain {
				joined += gap(last, first) * size
			}
			if line.Len() > 0 && joined*k > width {
				flush()
				joined = rw
			}
			if joined*k > width {
				// The run alone is wider than the box: it is cut, and every piece but the last is a line of its own.
				cut, err := m.cut(run, size, bold, width)
				if err != nil {
					return err
				}
				out = append(out, cut[:len(cut)-1]...)
				run = cut[len(cut)-1]
				if joined, err = m.width(run, size, bold); err != nil {
					return err
				}
			}
			line.WriteString(run)
			w = joined
			last, _ = utf8.DecodeLastRuneInString(run)
			return nil
		})
		if err != nil {
			return nil, err
		}
		flush()
	}
	return out, nil
}
