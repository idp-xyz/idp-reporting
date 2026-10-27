package main

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeWidth: CJK and other wide characters are 1, ASCII is 0.5.
func fakeWidth(s string) float64 {
	w := 0.0
	for _, r := range s {
		if r < utf8.RuneSelf {
			w += 0.5
		} else {
			w++
		}
	}
	return w
}

func TestWrap(t *testing.T) {
	for _, c := range []struct {
		name, text string
		width      float64
		want       []string
	}{
		{"closing mark pulls the previous character down", "甲乙丙丁戊。", 5, []string{"甲乙丙丁", "戊。"}},
		{"two closing marks pull two characters", "甲乙丙丁戊。）", 5, []string{"甲乙丙丁", "戊。）"}},
		{"opening mark does not end a line", "甲乙丙丁（戊）", 5, []string{"甲乙丙丁", "（戊）"}},
		{"dash pair stays together and may start a line", "甲乙丙丁——戊", 5, []string{"甲乙丙丁", "——戊"}},
		{"ellipsis pair stays together", "甲乙丙丁……", 5, []string{"甲乙丙丁", "……"}},
		{"interpunct stays with the character before it", "甲乙丙丁·戊己", 5, []string{"甲乙丙丁·", "戊己"}},
		{"interpunct moves down with its character", "甲乙丙丁戊·己", 5, []string{"甲乙丙丁", "戊·己"}},
		{"code stays whole", "付款BC-2026-017条", 6, []string{"付款", "BC-2026-017", "条"}},
		{"overlong number is split by characters", strings.Repeat("9", 20), 3, []string{"999999", "999999", "999999", "99"}},
		{"spaces break and are dropped at line edges", "abc def", 2, []string{"abc", "def"}},
		{"hostile text is plain text", `#panic("x") 甲`, 20, []string{`#panic("x") 甲`}},
		{"empty", "", 5, nil},
	} {
		if got := wrap(c.text, c.width, fakeWidth); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: wrap(%q, %v) = %q; want %q", c.name, c.text, c.width, got, c.want)
		}
	}
}

func TestWrapNeverLosesOrReordersText(t *testing.T) {
	text := "根据《补充协议》（编号BC-2026-017），甲方应于收到发票后30日内付款；逾期未付的，按日万分之五计收违约金。预收款核销——见附件1、附件2……华东大区·上海"
	for width := 2.0; width <= 30; width++ {
		lines := wrap(text, width, fakeWidth)
		if strings.Join(lines, "") != text {
			t.Fatalf("width %v: lines %q do not rebuild the text", width, lines)
		}
		for _, l := range lines {
			if fakeWidth(l) > width {
				t.Fatalf("width %v: line %q overflows", width, l)
			}
		}
	}
}

func TestPaginate(t *testing.T) {
	row := func(n int) [][]string { // n lines in the description column, one line elsewhere
		desc := make([]string, n)
		for i := range desc {
			desc[i] = "x"
		}
		return [][]string{{"1"}, desc, {"9.00"}}
	}
	// lineH 1, padY 0.5: a one-line row is 2 high; the body holds 6, the totals need 2.
	pages := paginate([][][]string{row(1), row(1), row(1), row(1)}, []string{"1.00", "2.00", "3.00", "4.00"}, 1, 0.5, 6, 2)
	if len(pages) != 2 || len(pages[0].fragments) != 3 || len(pages[1].fragments) != 1 || !pages[1].totals {
		t.Fatalf("pages = %+v", pages)
	}
	if pages[0].carriedIn != "" || pages[0].carriedOut != "3.00" || pages[1].carriedIn != "3.00" || pages[1].carriedOut != "4.00" {
		t.Fatalf("carried totals = %+v", pages)
	}

	// A row of 12 lines is taller than the 6-high body: it is split, never truncated, and its
	// amount stays with its first fragment while continuation pages carry the same total.
	pages = paginate([][][]string{row(1), row(12), row(1)}, []string{"1.00", "3.00", "6.00"}, 1, 0.5, 6, 2)
	lines := 0
	for _, p := range pages {
		for _, f := range p.fragments {
			if f.row == 1 {
				lines += len(f.lines[1])
			}
		}
	}
	if lines != 12 {
		t.Fatalf("tall row kept %d of 12 lines", lines)
	}
	if !pages[0].fragments[1].first || pages[1].fragments[0].first {
		t.Fatalf("first-fragment flags wrong: %+v", pages)
	}
	if pages[0].carriedOut != "3.00" || pages[1].carriedIn != "3.00" || pages[1].carriedOut != "3.00" {
		t.Fatalf("carried totals across the split row = %+v", pages)
	}
	last := pages[len(pages)-1]
	if !last.totals || last.carriedOut != "6.00" {
		t.Fatalf("last page = %+v", last)
	}

	// No rows still yields one page with the totals.
	if pages = paginate(nil, nil, 1, 0.5, 6, 2); len(pages) != 1 || !pages[0].totals {
		t.Fatalf("empty statement pages = %+v", pages)
	}
}
