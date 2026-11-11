package pdf

import (
	"errors"
	"maps"
	"slices"
	"testing"

	r "idp.local/reporting/reporting"
)

// footerClasses is what the footer must print for each data class, said here and not taken from the writer.
var footerClasses = map[string]string{
	"contract-simulator": "合同模拟器 / 合成数据（非真实业务数据）",
	"manual-entry":       "手工录入（非来源产品数据）",
	"business-data":      "来源产品数据",
}

// TestTheFooterPrintsTheDataClassOfTheSnapshot (S08-A, ADR 0012 decision 6): the footer of every page says where the data comes from,
// in the snapshot's own word: one text for each class, the simulator's being what every export printed before the class was the
// adapter's word. Nothing else of the document depends on the class. A stored word outside the set is an integrity failure: it is
// never printed as it is, and never measured.
func TestTheFooterPrintsTheDataClassOfTheSnapshot(t *testing.T) {
	// One text for each class of the closed set and for nothing else.
	if got := slices.Sorted(maps.Keys(dataClasses)); !slices.Equal(got, slices.Sorted(slices.Values(r.DataClasses))) || len(footerClasses) != len(r.DataClasses) {
		t.Fatalf("the footer has texts for %q, the classes are %q", got, r.DataClasses)
	}
	rows := [][]string{{"华东", "2026", "1234567.89", "-0.50"}, {"华西", "2025", "0.00", "1000.00"}}
	simulator, err := build(table(2, 2, rows...), fakeMetrics(), created)
	if err != nil || simulator.Footer != plainFooter {
		t.Fatalf("the simulator's footer: %v (%v)", simulator.Footer, err)
	}
	const rest = " · 快照 0123456789abcdef0123456789abcdef · finance.ledger@2026.10 · 截至 2026-10-27 · 来源水位 wm-42"
	for class, text := range footerClasses {
		s := table(2, 2, rows...)
		s.Result.Evidence.DataClass = class
		d, err := build(s, fakeMetrics(), created)
		if err != nil {
			t.Fatal(class, err)
		}
		if d.Footer != text+rest {
			t.Errorf("%s: footer %q, want %q", class, d.Footer, text+rest)
		}
		// The same document but for the footer.
		d.Footer = simulator.Footer
		if !jsonEqual(d, simulator) {
			t.Errorf("%s: the class changed something else of the document", class)
		}
		// Through a presentation, and with a narrowed query (S20-C): the class's text, then the snapshot with its mark.
		p := presentation()
		if d, err = buildTable(s, &p, fakeMetrics(), created); err != nil || d.Footer != text+rest {
			t.Errorf("%s, presented: footer %v (%v)", class, d.Footer, err)
		}
		s.Query.Filters = []r.Filter{{Field: "d0", Operator: "eq", Value: "华东"}}
		if d, err = build(s, fakeMetrics(), created); err != nil || d.Footer != text+" · 快照 0123456789abcdef0123456789abcdef（已按 1 项条件筛选） · finance.ledger@2026.10 · 截至 2026-10-27 · 来源水位 wm-42" {
			t.Errorf("%s, filtered: footer %v (%v)", class, d.Footer, err)
		}
	}
	// The text of the class is data like every label: a font that lacks a character of it cannot print that class, and prints the others.
	manual := table(2, 2, rows...)
	manual.Result.Evidence.DataClass = "manual-entry"
	if _, err := build(manual, fakeMetrics('录'), created); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a font without a character of the manual class's text: %v; want unsupported", err)
	}
	if _, err := build(table(2, 2, rows...), fakeMetrics('录'), created); err != nil {
		t.Errorf("the simulator's document with that font: %v", err)
	}
	// A word the platform does not know: an integrity failure before anything is measured (the fonts are never asked about it).
	for _, class := range []string{"", "real-data", "合同模拟器", "Contract-Simulator", "business-data ", "☃"} {
		s := table(2, 2, rows...)
		s.Result.Evidence.DataClass = class
		if _, err := build(s, fakeMetrics('☃'), created); !errors.Is(err, r.ErrIntegrity) || errors.Is(err, r.ErrUnsupported) {
			t.Errorf("a snapshot whose data class is %q: %v; want an integrity failure", class, err)
		}
		p := presentation()
		if _, err := buildTable(s, &p, fakeMetrics(), created); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("a snapshot whose data class is %q, presented: %v; want an integrity failure", class, err)
		}
	}
}
