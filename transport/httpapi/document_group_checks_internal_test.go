package httpapi

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// bandedWide is a 2.0 template of many field texts over a snapshot of many groups: the groups differ in the width
// of every column, so that every text and column has its own extremes.
func bandedWide(groups, columns int) (doctemplates.Document, r.Snapshot) {
	s := r.Snapshot{Owner: r.Owner{Integration: "i"}, Query: r.QuerySpec{Dataset: r.Ref{ID: "d", Version: "1"}}}
	d := doctemplates.Document{Schema: "2.0", Kind: "document", Group: &doctemplates.GroupSpec{Fields: []string{"c0"}}, Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}}}
	for j := range columns {
		id := fmt.Sprintf("c%d", j)
		s.Query.Dimensions = append(s.Query.Dimensions, id)
		s.Result.Columns = append(s.Result.Columns, r.Column{ID: id, Type: "string"})
	}
	for g := range groups {
		for line := 0; line < 1+g%3; line++ {
			row := make([]string, columns)
			row[0] = fmt.Sprintf("g%03d", g)
			for j := 1; j < columns; j++ {
				// Group 2j of every column is the widest and group 2j+1 the narrowest.
				switch width := 50; {
				case g == (2*j)%groups:
					row[j] = strings.Repeat("x", 100)
				case g == (2*j+1)%groups:
					row[j] = "x"
				default:
					row[j] = strings.Repeat("x", width)
				}
			}
			s.Result.Rows = append(s.Result.Rows, row)
		}
	}
	// A page header and a document footer of 24 field texts each and a table of 12 columns: 49 elements.
	band := func(first int) *doctemplates.Band {
		b := &doctemplates.Band{Height: 120, Elements: []doctemplates.Item{}}
		for i := range 24 {
			b.Elements = append(b.Elements, doctemplates.Item{ID: fmt.Sprintf("e%d", first+i), Type: "text", Source: "field", Field: fmt.Sprintf("c%d", first+i), X: 0, Y: float64(i) * 5, Width: 100, Height: 5, FontSize: 8, Align: "left"})
		}
		return b
	}
	table := doctemplates.Item{ID: "e50", Type: "table", X: 0, Width: 96, FontSize: 9}
	for j := range 12 {
		table.Columns = append(table.Columns, doctemplates.ItemColumn{Field: fmt.Sprintf("c%d", j), Width: 8})
	}
	d.Bands = &doctemplates.Bands{PageHeader: band(1), DocumentFooter: band(25), Detail: &doctemplates.DetailBand{Elements: []doctemplates.Item{table}}}
	return d, s
}

func TestBoundaryGroupsAreCappedAndKeepTheFirstAndTheLastGroup(t *testing.T) {
	d, s := bandedWide(200, 60)
	groups, err := doctemplates.Groups(d, s)
	if err != nil {
		t.Fatal(err)
	}
	picks, err := boundaryGroups(d, s, groups)
	if err != nil {
		t.Fatal(err)
	}
	if len(picks) != MaxDocumentCheckRows {
		t.Fatalf("%d picks, want the cap of %d", len(picks), MaxDocumentCheckRows)
	}
	index := []int{}
	for _, p := range picks {
		index = append(index, p.index)
		if len(p.reasons) == 0 {
			t.Errorf("a pick without a reason: %+v", p)
		}
	}
	if !sort.IntsAreSorted(index) {
		t.Errorf("picks not in row order: %v", index)
	}
	// The first and the last group, the one with the most rows and the one with the fewest come first in the order of
	// picking, so the cap never drops them.
	first := map[int][]string{}
	for _, p := range picks {
		first[p.index] = p.reasons
	}
	last := groups[len(groups)-1].Rows[0]
	has := func(row int, reason string) {
		t.Helper()
		for _, have := range first[row] {
			if have == reason {
				return
			}
		}
		t.Errorf("row %d lacks %q: %v", row, reason, first[row])
	}
	has(0, "first")
	has(last, "last")
	most, fewest := -1, 1<<30
	for _, g := range groups {
		most, fewest = max(most, len(g.Rows)), min(fewest, len(g.Rows))
	}
	var gotMost, gotFewest bool
	for _, p := range picks {
		for _, have := range p.reasons {
			gotMost = gotMost || have == "most_rows"
			gotFewest = gotFewest || have == "fewest_rows"
		}
	}
	if !gotMost || !gotFewest {
		t.Errorf("most rows %v, fewest rows %v", gotMost, gotFewest)
	}
	// A pick is a group's first row.
	firsts := map[int]bool{}
	for _, g := range groups {
		firsts[g.Rows[0]] = true
	}
	for _, i := range index {
		if !firsts[i] {
			t.Errorf("pick %d is not a group's first row", i)
		}
	}
	// A field the snapshot lacks is denied and a short row an integrity failure, as for the rows of 1.x.
	denied := d
	denied.Bands = &doctemplates.Bands{PageHeader: &doctemplates.Band{Height: 20, Elements: []doctemplates.Item{{ID: "e1", Type: "text", Source: "field", Field: "ghost", X: 0, Y: 0, Width: 50, Height: 8, FontSize: 10, Align: "left"}}}, Detail: d.Bands.Detail}
	if _, err := boundaryGroups(denied, s, groups); err != r.ErrDenied {
		t.Errorf("a field the snapshot lacks: %v", err)
	}
	short := s
	short.Result.Rows = append([][]string{}, s.Result.Rows...)
	short.Result.Rows[0] = []string{"g000"}
	if _, err := boundaryGroups(d, short, groups); err != r.ErrIntegrity {
		t.Errorf("a short row: %v", err)
	}
	// No rows, no groups: nothing to pick.
	if picks, err := boundaryGroups(d, r.Snapshot{}, nil); err != nil || picks != nil {
		t.Errorf("an empty snapshot: %v %v", picks, err)
	}
}
