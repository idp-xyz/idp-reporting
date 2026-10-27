package pdf

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// form returns a schema 1.1 document with the given elements.
func form(mode string, ox, oy float64, elements ...doctemplates.Element) doctemplates.Document {
	return doctemplates.Document{Schema: "1.1", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
		Elements: elements, Print: &doctemplates.PrintProfile{Mode: mode, OffsetX: ox, OffsetY: oy}}
}

func box(id, source, text string, x, y, w float64, size int, align string) doctemplates.Element {
	field := ""
	if source != "literal" {
		field, text = "f", ""
	}
	return doctemplates.Element{ID: id, Source: source, Field: field, Text: text, X: x, Y: y, Width: w, Height: 10, FontSize: size, Align: align}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLayOutPlacesValuesByTheDocumentRule(t *testing.T) {
	d := form("full", 1.2, -0.5, box("n1", "literal", "标题", 20, 30, 80, 12, "left"), box("n2", "row", "", 20, 50, 60, 10, "right"))
	p, err := layOut(d, map[string]string{"n1": "标题", "n2": "1234.50"}, nil, fakeMetrics())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Elements) != 2 || p.Background {
		t.Fatalf("%+v", p)
	}
	// 1 mm is 72/25.4 pt; the baseline is 1.16 font sizes below the top of the box.
	title, amount := p.Elements[0], p.Elements[1]
	if !near(title.Left, 21.2*72/25.4) || !near(title.Baseline, 29.5*72/25.4+1.16*12) || !near(title.Width, 80*72/25.4) || title.Text != "标题" || title.Align != "left" {
		t.Errorf("title %+v", title)
	}
	if !near(amount.Baseline, 49.5*72/25.4+11.6) || amount.Align != "right" || amount.Text != "1234.50" {
		t.Errorf("amount %+v", amount)
	}
	if b := p.Banner; b.Text != "PREVIEW - NOT AN ISSUED DOCUMENT" || !near(b.Left, 10*72/25.4) || !near(b.Baseline, 10*72/25.4+11.6) {
		t.Errorf("banner %+v", b)
	}
	// The variable layer leaves out literal text.
	if p, err = layOut(form("variables", 0, 0, d.Elements...), map[string]string{"n1": "标题", "n2": "1"}, nil, fakeMetrics()); err != nil || len(p.Elements) != 1 || p.Elements[0].Text != "1" {
		t.Errorf("variables mode: %+v %v", p, err)
	}
}

func TestLayOutRefusesWhatItCannotPrintExactly(t *testing.T) {
	d := form("full", 0, 0, box("n1", "row", "", 20, 30, 20, 10, "left"))
	for name, value := range map[string]string{
		"a value wider than its box": strings.Repeat("宽", 20),
		"an uncovered character":     "☃",
		"a soft hyphen":              "a­b",
		"a line break":               "a\nb",
		"a control character":        "a\x7fb",
		"too long":                   strings.Repeat("a", 257),
	} {
		_, err := layOut(d, map[string]string{"n1": value}, nil, fakeMetrics('☃'))
		want := r.ErrUnsupported
		if name == "a line break" || name == "a control character" || name == "too long" {
			want = r.ErrInvalid
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: %v; want %v", name, err, want)
		}
	}
	if _, err := layOut(d, map[string]string{}, nil, fakeMetrics()); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a missing value: %v", err)
	}
	// A value just inside the box fits; one character more does not.
	room := (20*72/25.4 - fitMargin) / (0.5 * 10 * slack) // 10.6 half-em characters
	fits := strings.Repeat("a", int(room))
	if _, err := layOut(d, map[string]string{"n1": fits}, nil, fakeMetrics()); err != nil {
		t.Errorf("%d characters: %v", len(fits), err)
	}
	if _, err := layOut(d, map[string]string{"n1": fits + "a"}, nil, fakeMetrics()); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("%d characters: %v", len(fits)+1, err)
	}
}

func TestLayOutDrawsOnlyTheNamedBackgroundInFullMode(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	sum := sha256.Sum256(png)
	ref := &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: hex.EncodeToString(sum[:])}
	full := form("full", 0, 0, box("n1", "row", "", 20, 30, 60, 10, "left"))
	full.Schema, full.Background = "1.2", ref
	variables := full
	variables.Print = &doctemplates.PrintProfile{Mode: "variables"}
	values := map[string]string{"n1": "1"}
	if p, err := layOut(full, values, png, fakeMetrics()); err != nil || !p.Background {
		t.Errorf("full mode with its background: %+v %v", p, err)
	}
	other := append([]byte{}, png...)
	other[20] = 1
	for name, c := range map[string]struct {
		d    doctemplates.Document
		png  []byte
		want error
	}{
		"full mode without the background": {full, nil, r.ErrInvalid},
		"another background":               {full, other, r.ErrIntegrity},
		"a background in variables mode":   {variables, png, r.ErrInvalid},
		"a background the template lacks":  {form("full", 0, 0, full.Elements...), png, r.ErrInvalid},
		"a background over the size limit": {full, make([]byte, doctemplates.MaxBackgroundBytes+1), r.ErrBudget},
	} {
		if _, err := layOut(c.d, values, c.png, fakeMetrics()); !errors.Is(err, c.want) {
			t.Errorf("%s: %v; want %v", name, err, c.want)
		}
	}
	notPNG := []byte("GIF89a not a png")
	sum = sha256.Sum256(notPNG)
	full.Background = &doctemplates.BackgroundRef{ID: ref.ID, SHA256: hex.EncodeToString(sum[:])}
	if _, err := layOut(full, values, notPNG, fakeMetrics()); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a background that is not a PNG: %v", err)
	}
	if p, err := layOut(variables, values, nil, fakeMetrics()); err != nil || p.Background {
		t.Errorf("variables mode: %+v %v", p, err)
	}
}
