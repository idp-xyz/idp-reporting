package pdf

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

//go:embed document.typ
var documentTemplate []byte

const (
	mm = 72 / 25.4
	// ascent places a text's baseline below the top of its box, in font sizes. Document
	// previews have always used it (it is Noto Sans SC's ascender), so calibrated templates
	// keep their positions.
	ascent = 1.16
	// fitMargin is room kept free at the end of a box: a value must be this much narrower.
	fitMargin = 2.0
	// The permanent banner, in millimetres and points.
	bannerText = "PREVIEW - NOT AN ISSUED DOCUMENT"
	bannerX    = 10.0
	bannerY    = 10.0
	bannerW    = 190.0
	bannerH    = 8.0
	bannerSize = 10.0
	// The message of the template's width assertion, which fails a render in Typst.
	widerThanMeasured = "reporting: a glyph is wider than measured"
)

// glyph is one piece of text placed at the x the plain advances of the fonts give it, as pdfme
// placed text: one grapheme cluster of a box value, or one line of a table cell; Typst checks
// that it draws no wider than Width.
type glyph struct {
	X        float64 `json:"x"` // points from the left edge of the page
	Baseline float64 `json:"baseline"`
	Size     float64 `json:"size"`
	Width    float64 `json:"width"`
	Text     string  `json:"text"`
	Bold     bool    `json:"bold"`
}

// rect and rule are the table's fills, cells and the rule above the total row, in points.
type rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type rule struct {
	X0     float64 `json:"x0"`
	Y0     float64 `json:"y0"`
	X1     float64 `json:"x1"`
	Y1     float64 `json:"y1"`
	Stroke float64 `json:"stroke"`
}

// pageData is one page: its texts, header fills, cell rectangles and rules. Every list is
// present in data.json, empty or not: the template loops over each.
type pageData struct {
	Texts []glyph `json:"texts"`
	Fills []rect  `json:"fills"`
	Cells []rect  `json:"cells"`
	Rules []rule  `json:"rules"`
}

func newPage(texts []glyph) pageData {
	return pageData{Texts: append([]glyph{}, texts...), Fills: []rect{}, Cells: []rect{}, Rules: []rule{}}
}

type banner struct {
	Left     float64 `json:"left"`
	Top      float64 `json:"top"`
	Baseline float64 `json:"baseline"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	Size     float64 `json:"size"`
	Text     string  `json:"text"`
}

type page struct {
	Title      string     `json:"title"`
	Fonts      []string   `json:"fonts"`
	Background bool       `json:"background"` // drawn on every page
	Pages      []pageData `json:"pages"`
	Banner     banner     `json:"banner"` // on every page
}

// Render draws one document template preview: an A4 page with the template's background in
// full mode, each element's value on one line in its box, and a permanent preview banner. It
// implements httpapi.DocumentRenderer. A value is never shrunk, clipped or wrapped: one that
// does not fit its box, or holds a character the fonts lack, is unsupported. A document with a
// table (schema 1.3) takes as many pages as its rows need; table is then the rows bound by
// doctemplates.BindTable. Previews hold at most all but one of the Config.Concurrency Typst
// processes, so that they never keep the export worker waiting for more than one render; a
// preview whose wait and render together take longer than the render timeout is a budget
// error.
func (w *Writer) Render(ctx context.Context, d doctemplates.Document, values map[string]string, background []byte, table *doctemplates.TableData) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := layOut(d, values, background, table, newPlainMeasurer(w.fonts))
	if err != nil {
		return nil, err
	}
	return w.renderDocument(ctx, p, background)
}

// renderDocument runs Typst on a laid-out page.
func (w *Writer) renderDocument(ctx context.Context, p *page, background []byte) ([]byte, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	bounded, cancel, budget := w.bounded(ctx)
	defer cancel()
	if err := acquire(bounded, w.previews); err != nil {
		return nil, budget(err)
	}
	defer release(w.previews)
	if err := acquire(bounded, w.slots); err != nil {
		return nil, budget(err)
	}
	defer release(w.slots)
	if err := w.unchanged(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(w.cfg.WorkDir, "reporting-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	files := map[string][]byte{"main.typ": documentTemplate, "data.json": data}
	if p.Background {
		files["background.png"] = background
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return nil, err
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "packages"), 0o700); err != nil {
		return nil, err
	}
	out, err := w.render(bounded, dir, w.cfg.Now())
	if err != nil {
		return nil, budget(widerThanMeasuredError(err))
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		return nil, errors.New("pdf: Typst wrote no PDF")
	}
	return out, nil
}

// widerThanMeasuredError turns the template's own assertion failure into an unsupported-value
// error: Typst would draw a cluster wider than the fonts' advances, so the value cannot be
// placed exactly.
func widerThanMeasuredError(err error) error {
	var re *RenderError
	if errors.As(err, &re) && bytes.Contains(re.stderr, []byte(widerThanMeasured)) {
		return fmt.Errorf("pdf: Typst would draw a value wider than measured: %w", r.ErrUnsupported)
	}
	return err
}

// BackgroundFits renders a probe document on the given PNG background and reports whether the
// result stays within the artifact limit, so that a background is refused at upload rather
// than failing every later preview. Its errors are those of Render.
func (w *Writer) BackgroundFits(ctx context.Context, background []byte) error {
	sum := sha256.Sum256(background)
	d, values, _ := documentProbe()
	d.Schema, d.Background = "1.2", &doctemplates.BackgroundRef{ID: strings.Repeat("0", 32), SHA256: hex.EncodeToString(sum[:])}
	_, err := w.Render(ctx, d, values, background, nil)
	return err
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// layOut checks a document and its values and computes where every grapheme cluster goes:
// the value's width is the sum of its characters' plain advances, it starts at the box's left
// edge, ends at its right edge or is centred, and its baseline is ascent font sizes below the
// top of the box. Trailing white space is not drawn, as the previews never drew it. A table
// adds its pages after the boxes are placed; the boxes repeat on every page.
func layOut(d doctemplates.Document, values map[string]string, background []byte, table *doctemplates.TableData, m *measurer) (*page, error) {
	if err := doctemplates.Validate(d); err != nil {
		return nil, err
	}
	profile := doctemplates.PrintProfile{Mode: "full"}
	if d.Print != nil {
		profile = *d.Print
	}
	// The background is drawn in full mode only, and must be the one the template names.
	full := profile.Mode == "full"
	switch {
	case len(background) > doctemplates.MaxBackgroundBytes:
		return nil, r.ErrBudget
	case d.Background == nil && len(background) > 0, !full && len(background) > 0, d.Background != nil && full && len(background) == 0:
		return nil, r.ErrInvalid
	case len(background) > 0:
		sum := sha256.Sum256(background)
		if hex.EncodeToString(sum[:]) != d.Background.SHA256 || !bytes.HasPrefix(background, pngSignature) {
			return nil, r.ErrIntegrity
		}
	}
	p := &page{Title: "Document preview", Fonts: m.families, Background: len(background) > 0, Pages: []pageData{newPage(nil)},
		Banner: banner{Left: bannerX * mm, Top: bannerY * mm, Baseline: bannerY*mm + ascent*bannerSize, Width: bannerW * mm, Height: bannerH * mm, Size: bannerSize, Text: bannerText}}
	first := &p.Pages[0]
	if _, err := m.width(bannerText, bannerSize, false); err != nil {
		return nil, err
	}
	for _, e := range d.Elements {
		if !full && e.Source == "literal" {
			continue
		}
		v, ok := values[e.ID]
		if !ok || !utf8.ValidString(v) {
			return nil, r.ErrInvalid
		}
		// What the single-line preview cannot print: controls, over 256 characters (as Bind
		// refuses them) and a value starting with a combining mark, which Typst and pdfme
		// would draw over the next character.
		if utf8.RuneCountInString(v) > 256 || hasControl(v) {
			return nil, fmt.Errorf("pdf: a value the preview cannot print: %w", r.ErrUnsupported)
		}
		size := float64(e.FontSize)
		pieces := clusters(strings.TrimRightFunc(v, unicode.IsSpace))
		if len(pieces) > 0 && unicode.Is(unicode.M, []rune(pieces[0])[0]) {
			return nil, fmt.Errorf("pdf: a value starts with a combining mark: %w", r.ErrUnsupported)
		}
		widths := make([]float64, len(pieces))
		total := 0.0
		for i, c := range pieces {
			w, err := m.width(c, size, false)
			if err != nil {
				return nil, err
			}
			widths[i], total = w, total+w
		}
		left, right := (e.X+profile.OffsetX)*mm, (e.X+profile.OffsetX+e.Width)*mm
		if total > e.Width*mm-fitMargin {
			return nil, fmt.Errorf("pdf: a value does not fit its box on one line: %w", r.ErrUnsupported)
		}
		x := left
		switch e.Align {
		case "right":
			x = right - total
		case "center":
			x = (left+right)/2 - total/2
		}
		baseline := (e.Y+profile.OffsetY)*mm + ascent*size
		for i, c := range pieces {
			first.Texts = append(first.Texts, glyph{X: x, Baseline: baseline, Size: size, Width: widths[i], Text: c})
			x += widths[i]
		}
	}
	if d.Table != nil {
		if err := layOutTable(p, d, profile, table, m); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// clusters splits s into grapheme clusters: a character and the combining marks after it.
func clusters(s string) []string {
	var out []string
	for _, c := range s {
		if len(out) > 0 && unicode.Is(unicode.M, c) {
			out[len(out)-1] += string(c)
		} else {
			out = append(out, string(c))
		}
	}
	return out
}

func hasControl(s string) bool {
	for _, c := range s {
		if c < 32 || c == 0x7F {
			return true
		}
	}
	return false
}

// documentProbe is rendered by New next to the table probe, so that a host that cannot draw
// documents fails at startup: every alignment, Chinese punctuation, a dash and a background.
func documentProbe() (doctemplates.Document, map[string]string, []byte) {
	background := probeBackground()
	sum := sha256.Sum256(background)
	d := doctemplates.Document{Schema: "1.2", Kind: "document", Binding: doctemplates.Binding{Integration: "probe", Dataset: r.Ref{ID: "probe", Version: "1"}},
		Elements: []doctemplates.Element{
			{ID: "n1", Source: "literal", Text: "单据 Probe 0123456789", X: 20, Y: 30, Width: 120, Height: 10, FontSize: 10, Align: "left"},
			{ID: "n2", Source: "literal", Text: "（甲）。", X: 20, Y: 50, Width: 120, Height: 10, FontSize: 12, Align: "right"},
			{ID: "n3", Source: "literal", Text: "a—b", X: 20, Y: 70, Width: 120, Height: 10, FontSize: 8, Align: "center"},
		},
		Print:      &doctemplates.PrintProfile{Mode: "full"},
		Background: &doctemplates.BackgroundRef{ID: strings.Repeat("0", 32), SHA256: hex.EncodeToString(sum[:])}}
	return d, map[string]string{"n1": "单据 Probe 0123456789", "n2": "（甲）。", "n3": "a—b"}, background
}

// probeBackground is the smallest A4 PNG the templates accept: white with a grey frame.
func probeBackground() []byte {
	im := image.NewGray(image.Rect(0, 0, 210, 297))
	for y := 0; y < 297; y++ {
		for x := 0; x < 210; x++ {
			if x < 4 || y < 4 || x >= 206 || y >= 293 {
				im.SetGray(x, y, color.Gray{Y: 160})
			} else {
				im.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		panic(err)
	}
	return b.Bytes()
}
