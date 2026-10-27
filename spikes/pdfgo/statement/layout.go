package main

import (
	"strings"
	"unicode/utf8"
)

// GB/T 15834-2011: closing marks never start a line and opening marks never end one. The
// interpunct is kept with the character before it instead of being listed here.
const (
	noStart = "，。、；：？！）］｝」』】》〉”’％"
	noEnd   = "（［｛「『【《〈“‘"
)

// units splits text into the smallest pieces a line may break between: one CJK character, a run
// of ASCII non-space characters (words, numbers, codes), a space, or a whole two-em dash or
// ellipsis. An interpunct is glued to the piece before it.
func units(text string) []string {
	var out []string
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		j := i + size
		switch {
		case r == ' ':
		case r < utf8.RuneSelf:
			for j < len(text) && text[j] < utf8.RuneSelf && text[j] != ' ' {
				j++
			}
		case r == '—' || r == '…':
			for j < len(text) {
				next, n := utf8.DecodeRuneInString(text[j:])
				if next != r {
					break
				}
				j += n
			}
		}
		if r == '·' && len(out) > 0 && out[len(out)-1] != " " {
			out[len(out)-1] += text[i:j]
		} else {
			out = append(out, text[i:j])
		}
		i = j
	}
	return out
}

func startsWithAny(s, set string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return strings.ContainsRune(set, r)
}

func endsWithAny(s, set string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return strings.ContainsRune(set, r)
}

// wrap breaks text into lines no wider than width. A line never starts with a closing mark or
// ends with an opening mark, and a dash or ellipsis pair is never split; when honouring that
// would overflow the line, the plain break is kept, because the text must never exceed the cell.
func wrap(text string, width float64, measure func(string) float64) []string {
	var lines []string
	var cur []string
	curW := 0.0
	flush := func() {
		lines = append(lines, strings.TrimRight(strings.Join(cur, ""), " "))
		cur, curW = nil, 0
	}
	for _, u := range units(text) {
		if len(cur) == 0 && u == " " {
			continue
		}
		w := measure(u)
		if curW+w <= width {
			cur, curW = append(cur, u), curW+w
			continue
		}
		if u == " " {
			flush()
			continue
		}
		if len(cur) == 0 { // a single piece wider than the line, such as a very long number
			for _, r := range u {
				s := string(r)
				if rw := measure(s); curW+rw <= width || len(cur) == 0 {
					cur, curW = append(cur, s), curW+rw
				} else {
					flush()
					cur, curW = []string{s}, rw
				}
			}
			continue
		}
		keep, next := cur, []string{u}
		for len(keep) > 1 && (startsWithAny(next[0], noStart) || endsWithAny(keep[len(keep)-1], noEnd)) {
			next = append([]string{keep[len(keep)-1]}, next...)
			keep = keep[:len(keep)-1]
		}
		if measure(strings.Join(next, "")) > width {
			keep, next = cur, []string{u}
		}
		cur = keep
		flush()
		for _, n := range next {
			cur, curW = append(cur, n), curW+measure(n)
		}
		if curW > width { // only when u alone is wider than the line
			pending := strings.Join(cur, "")
			cur, curW = nil, 0
			lines = append(lines, wrap(pending, width, measure)...)
		}
	}
	if len(cur) > 0 {
		flush()
	}
	return lines
}

// fragment is the part of one statement row drawn on one page. The first fragment shows every
// column and owns the row's amount for the carried totals; later ones continue the tall columns.
type fragment struct {
	row   int
	first bool
	lines [][]string // per column
}

type page struct {
	fragments  []fragment
	carriedIn  string // running total carried from the previous page; "" when none
	carriedOut string // running total carried to the next page
	totals     bool   // the totals block is on this page
}

// paginate places rows (their wrapped lines per column) on pages whose table body is bodyH high.
// A row that does not fit moves to a new page; a row taller than a page is split across pages,
// never truncated. cums are the exact running totals of the rows.
func paginate(rows [][][]string, cums []string, lineH, padY, bodyH, totalsH float64) []page {
	pages := []page{{}}
	avail := bodyH
	newPage := func() {
		pages = append(pages, page{})
		avail = bodyH
	}
	owned := make([]string, 1) // last running total owned per page
	place := func(f fragment, n int) {
		p := &pages[len(pages)-1]
		p.fragments = append(p.fragments, f)
		avail -= float64(n)*lineH + 2*padY
		if f.first {
			for len(owned) < len(pages) {
				owned = append(owned, "")
			}
			owned[len(pages)-1] = cums[f.row]
		}
	}
	for i, cols := range rows {
		n := 1
		for _, c := range cols {
			n = max(n, len(c))
		}
		h := float64(n)*lineH + 2*padY
		if h > avail && h <= bodyH {
			newPage()
		}
		if h <= avail {
			place(fragment{row: i, first: true, lines: cols}, n)
			continue
		}
		for start := 0; start < n; {
			fit := int((avail - 2*padY) / lineH)
			if fit < 1 {
				newPage()
				continue
			}
			end := min(n, start+fit)
			part := make([][]string, len(cols))
			for c, ls := range cols {
				part[c] = ls[min(start, len(ls)):min(end, len(ls))]
			}
			place(fragment{row: i, first: start == 0, lines: part}, end-start)
			if start = end; start < n {
				newPage()
			}
		}
	}
	if totalsH > avail {
		newPage()
	}
	pages[len(pages)-1].totals = true
	for len(owned) < len(pages) {
		owned = append(owned, "")
	}
	carried := ""
	for k := range pages {
		pages[k].carriedIn = carried
		if owned[k] != "" {
			carried = owned[k]
		}
		pages[k].carriedOut = carried
	}
	return pages
}
