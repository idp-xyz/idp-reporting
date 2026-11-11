package xlsx_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
)

// TestTheMetadataSheetSaysTheDataClassOfTheSnapshot (S08-A, ADR 0012 decision 6): the `data_class` row is the snapshot's own word in the
// sheet's words, one note for each class. A simulator's workbook says what every workbook said before the class was the adapter's
// word (the cell-for-cell golden of unchanged_test.go holds it to the workbook of 43f8db5); a workbook of another class differs from
// it in that one cell and nowhere else; a stored word outside the set is an integrity failure and no file is written.
func TestTheMetadataSheetSaysTheDataClassOfTheSnapshot(t *testing.T) {
	notes := map[string]string{
		"contract-simulator": "CONTRACT SIMULATOR / NOT REAL BUSINESS DATA",
		"manual-entry":       "MANUAL ENTRY / NOT FROM A SOURCE PRODUCT",
		"business-data":      "SOURCE PRODUCT DATA",
	}
	// One note for each class of the closed set and for nothing else.
	if got := slices.Sorted(maps.Keys(xlsx.DataClassNotes)); !slices.Equal(got, slices.Sorted(slices.Values(r.DataClasses))) || len(notes) != len(r.DataClasses) {
		t.Fatalf("the notes are for %q, the classes are %q", got, r.DataClasses)
	}
	simulator, e := xlsx.Writer{}.Write(context.Background(), ledger())
	if e != nil {
		t.Fatal(e)
	}
	base := cells(t, simulator)
	for class, note := range notes {
		s := ledger()
		s.Result.Evidence.DataClass = class
		for name, write := range map[string]func() ([]byte, error){
			"plain": func() ([]byte, error) { return xlsx.Writer{}.Write(context.Background(), s) },
			"presented": func() ([]byte, error) {
				return xlsx.Writer{}.WritePresented(context.Background(), s, ledgerPresentation())
			},
		} {
			b, e := write()
			if e != nil {
				t.Fatal(class, name, e)
			}
			f := open(t, b)
			if got := strings.Join(row(t, f, "Metadata", 1), "|"); got != "data_class|"+note {
				t.Errorf("%s, %s: row 1 = %q, want %q", class, name, got, "data_class|"+note)
			}
			if v, kind, _ := cell(t, f, "Metadata", "B1"); v != note || isNumber(kind) {
				t.Errorf("%s, %s: B1 = %q %v", class, name, v, kind)
			}
		}
		// The plain workbook of this class against the simulator's: the same facts but for the one cell.
		b, _ := xlsx.Writer{}.Write(context.Background(), s)
		got := cells(t, b)
		if len(got) != len(base) {
			t.Fatalf("%s: %d facts, the simulator's workbook has %d", class, len(got), len(base))
		}
		var differing []string
		for i := range got {
			if got[i] != base[i] {
				differing = append(differing, got[i])
			}
		}
		want := []string{`Metadata!B1: shared "` + note + `" formula "" format "" bold false colour "" fill ""`}
		if class == "contract-simulator" {
			want = nil
		}
		if !slices.Equal(differing, want) {
			t.Errorf("%s: the workbook differs from the simulator's in %q, want %q", class, differing, want)
		}
	}
	// A word the platform does not know is never written as it is.
	for _, class := range []string{"", "real-data", "CONTRACT SIMULATOR / NOT REAL BUSINESS DATA", "Business-Data", "business-data "} {
		s := ledger()
		s.Result.Evidence.DataClass = class
		if b, e := (xlsx.Writer{}).Write(context.Background(), s); !errors.Is(e, r.ErrIntegrity) || b != nil {
			t.Errorf("a snapshot whose data class is %q: %v, want an integrity failure and no file", class, e)
		}
		if b, e := (xlsx.Writer{}).WritePresented(context.Background(), s, ledgerPresentation()); !errors.Is(e, r.ErrIntegrity) || b != nil {
			t.Errorf("a snapshot whose data class is %q, presented: %v, want an integrity failure and no file", class, e)
		}
	}
}
