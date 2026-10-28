package httpapi

import (
	"fmt"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"reflect"
	"strings"
	"testing"
)

func TestDisplayWidthCountsEastAsianWideCharactersTwice(t *testing.T) {
	for _, tc := range []struct {
		value string
		width int
	}{
		{"", 0}, {"abc", 3}, {"9007199254740893.00", 19}, {"宽", 2}, {"宽宽宽", 6}, {"客户a", 5},
		{"ＡＢ", 4},         // fullwidth Latin letters
		{"한글", 4},         // Hangul syllables
		{"ひらがな", 8},       // Hiragana
		{"　", 2},          // the ideographic space
		{"é", 1},          // a Latin letter with an accent
		{"€", 1},          // the euro sign is ambiguous, not wide
		{"䷀", 1},          // a hexagram symbol sits between two ideograph blocks and is neutral
		{"一", 2},          // the first ideograph
		{"鿿", 2},          // a unified ideograph
		{"\U00020000", 2}, // an extension B ideograph
		{"a\xffb", 3},     // invalid UTF-8 counts as one character per byte
	} {
		if got := displayWidth(tc.value); got != tc.width {
			t.Errorf("displayWidth(%q) = %d, want %d", tc.value, got, tc.width)
		}
	}
}

func snapshotOf(columns []string, rows ...[]string) r.Snapshot {
	var s r.Snapshot
	for _, id := range columns {
		s.Result.Columns = append(s.Result.Columns, r.Column{ID: id, Type: "string"})
	}
	s.Result.Rows = rows
	return s
}

func boxes(fields ...string) doctemplates.Document {
	d := doctemplates.Document{Schema: "1.0", Kind: "document"}
	for i, f := range fields {
		e := doctemplates.Element{ID: fmt.Sprintf("n%d", i+1), Source: "row", Field: f, X: 10, Y: float64(25 + 10*i), Width: 190, Height: 10, FontSize: 12, Align: "left"}
		if f == "" {
			e.Source, e.Text = "literal", "title"
		}
		d.Elements = append(d.Elements, e)
	}
	return d
}

func indexes(picks []checkRowPick) []int {
	out := []int{}
	for _, p := range picks {
		out = append(out, p.index)
	}
	return out
}

func TestBoundaryRowsPickFirstLastAndTheLongestAndShortestOfEachRowFieldBox(t *testing.T) {
	snap := snapshotOf([]string{"a", "b"},
		[]string{"mid", "x"},    // 0: first
		[]string{"abcde", "yy"}, // 1: 5
		[]string{"宽宽宽", "zzzz"}, // 2: width 6 with 3 characters beats 5 with 5
		[]string{"ab", "wwwww"}, // 3
		[]string{"abcde", "q"},  // 4: ties with 1, loses to the lowest index
		[]string{"end", "yy"},   // 5: last
	)
	picks, e := boundaryRows(boxes("a", "b", ""), snap)
	if e != nil {
		t.Fatal(e)
	}
	got := map[int][]string{}
	for _, p := range picks {
		got[p.index] = p.reasons
	}
	want := map[int][]string{
		0: {"first", "shortest:n2"}, // b: "x" and "q" are both 1 wide, row 0 first
		2: {"longest:n1"},
		3: {"shortest:n1", "longest:n2"},
		5: {"last"},
	}
	// Row 4 holds "q", which ties row 0 for the shortest of b: the lowest index wins, so it is not picked.
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(indexes(picks), []int{0, 2, 3, 5}) {
		t.Fatalf("picks %v, want %v", got, want)
	}
}

func TestBoundaryRowsTiesGoToTheLowestIndexAndOneRowMayHaveSeveralReasons(t *testing.T) {
	// "宽" and "ab" are both 2 wide: every value ties, so every pick is row 0 and the last row.
	snap := snapshotOf([]string{"a"}, []string{"宽"}, []string{"ab"}, []string{"cd"})
	picks, e := boundaryRows(boxes("a"), snap)
	if e != nil {
		t.Fatal(e)
	}
	if len(picks) != 2 || picks[0].index != 0 || picks[1].index != 2 ||
		!reflect.DeepEqual(picks[0].reasons, []string{"first", "longest:n1", "shortest:n1"}) || !reflect.DeepEqual(picks[1].reasons, []string{"last"}) {
		t.Fatalf("%+v", picks)
	}
	// One row: it is the first, the last, the longest and the shortest.
	picks, e = boundaryRows(boxes("a"), snapshotOf([]string{"a"}, []string{"only"}))
	if e != nil || len(picks) != 1 || !reflect.DeepEqual(picks[0].reasons, []string{"first", "last", "longest:n1", "shortest:n1"}) {
		t.Fatalf("%+v %v", picks, e)
	}
	// No rows: nothing to check, and literals and totals pick nothing.
	if picks, e = boundaryRows(boxes("a"), snapshotOf([]string{"a"})); e != nil || len(picks) != 0 {
		t.Fatalf("%+v %v", picks, e)
	}
	picks, e = boundaryRows(boxes("", ""), snapshotOf([]string{"a"}, []string{"1"}, []string{"22"}, []string{"333"}))
	if e != nil || !reflect.DeepEqual(indexes(picks), []int{0, 2}) {
		t.Fatalf("%+v %v", picks, e)
	}
}

func TestBoundaryRowsNeverExceedTheCapAndRefuseWhatBindingRefuses(t *testing.T) {
	// 24 boxes on 24 different fields, each with its own longest and shortest row: 2 + 48 rows.
	var columns, fields []string
	for i := 0; i < 24; i++ {
		columns = append(columns, fmt.Sprintf("f%d", i))
		fields = append(fields, fmt.Sprintf("f%d", i))
	}
	var rows [][]string
	for i := 0; i < 60; i++ {
		row := make([]string, 24)
		for j := range row {
			row[j] = "mm"
		}
		rows = append(rows, row)
	}
	for j := 0; j < 24; j++ {
		rows[1+j][j] = strings.Repeat("x", 10) // longest of field j: row 1+j
		rows[30+j][j] = "z"                    // shortest of field j: row 30+j
	}
	picks, e := boundaryRows(boxes(fields...), snapshotOf(columns, rows...))
	if e != nil || len(picks) != MaxDocumentCheckRows || picks[0].index != 0 || picks[len(picks)-1].index != 59 {
		t.Fatalf("%d picks, %v", len(picks), e)
	}
	// A field the snapshot lacks is a denial; a row shorter than its columns is an integrity failure.
	if _, e = boundaryRows(boxes("nope"), snapshotOf([]string{"a"}, []string{"1"})); e != r.ErrDenied {
		t.Fatalf("missing field: %v", e)
	}
	if _, e = boundaryRows(boxes("b"), snapshotOf([]string{"a", "b"}, []string{"1"})); e != r.ErrIntegrity {
		t.Fatalf("short row: %v", e)
	}
}
