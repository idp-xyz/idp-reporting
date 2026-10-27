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
	"os"
	"path/filepath"
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
)

type placed struct {
	Left     float64 `json:"left"` // points from the left edge of the page
	Baseline float64 `json:"baseline"`
	Width    float64 `json:"width"`
	Size     float64 `json:"size"`
	Align    string  `json:"align"`
	Text     string  `json:"text"`
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
	Title      string   `json:"title"`
	Fonts      []string `json:"fonts"`
	Background bool     `json:"background"`
	Elements   []placed `json:"elements"`
	Banner     banner   `json:"banner"`
}

// Render draws one document template preview: an A4 page with the template's background in
// full mode, each element's value on one line in its box, and a permanent preview banner. It
// implements httpapi.DocumentRenderer. A value is never shrunk, clipped or wrapped: one that
// does not fit its box, or holds a character the fonts lack, is unsupported.
func (w *Writer) Render(ctx context.Context, d doctemplates.Document, values map[string]string, background []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := layOut(d, values, background, newMeasurer(w.fonts))
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
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
	out, err := w.render(ctx, dir, w.cfg.Now())
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		return nil, errors.New("pdf: Typst wrote no PDF")
	}
	return out, nil
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// layOut checks a document and its values and computes where each value goes.
func layOut(d doctemplates.Document, values map[string]string, background []byte, m *measurer) (*page, error) {
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
	p := &page{Title: "Document preview", Fonts: m.families, Background: len(background) > 0, Elements: []placed{},
		Banner: banner{Left: bannerX * mm, Top: bannerY * mm, Baseline: bannerY*mm + ascent*bannerSize, Width: bannerW * mm, Height: bannerH * mm, Size: bannerSize, Text: bannerText}}
	if _, err := m.width(bannerText, bannerSize, false); err != nil {
		return nil, err
	}
	for _, e := range d.Elements {
		if !full && e.Source == "literal" {
			continue
		}
		v, ok := values[e.ID]
		if !ok || !utf8.ValidString(v) || utf8.RuneCountInString(v) > 256 || hasControl(v) {
			return nil, r.ErrInvalid
		}
		size := float64(e.FontSize)
		width, err := m.width(v, size, false)
		if err != nil {
			return nil, err
		}
		if width*slack > e.Width*mm-fitMargin {
			return nil, fmt.Errorf("pdf: a value does not fit its box on one line: %w", r.ErrUnsupported)
		}
		p.Elements = append(p.Elements, placed{
			Left: (e.X + profile.OffsetX) * mm, Baseline: (e.Y+profile.OffsetY)*mm + ascent*size,
			Width: e.Width * mm, Size: size, Align: e.Align, Text: v,
		})
	}
	return p, nil
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
// documents fails at startup.
func documentProbe() (doctemplates.Document, map[string]string) {
	d := doctemplates.Document{Schema: "1.1", Kind: "document", Binding: doctemplates.Binding{Integration: "probe", Dataset: r.Ref{ID: "probe", Version: "1"}},
		Elements: []doctemplates.Element{{ID: "n1", Source: "literal", Text: "单据 Probe 0123456789", X: 20, Y: 30, Width: 120, Height: 10, FontSize: 10, Align: "left"}},
		Print:    &doctemplates.PrintProfile{Mode: "full"}}
	return d, map[string]string{"n1": "单据 Probe 0123456789"}
}
