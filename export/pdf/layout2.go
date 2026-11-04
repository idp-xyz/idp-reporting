package pdf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image/png"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// Schema 2.0 layout (ADR 0004, S03-J). A 2.0 document is a stack of bands inside the safe region of its page preset (A4: x 10-200 mm,
// y 25-270 mm; doctemplates.Presets): the page header on every page, the document header under it on the
// first page, the detail table in the space between, the page footer at the bottom of every page
// and the document footer once, directly under the table's total row, kept whole. Go places every
// glyph, rule and rectangle; Typst draws them. What does not fit is refused, never cut or moved.

const (
	// fitEpsilon absorbs the rounding of sums of lengths in points when a band is compared with the
	// space left on a page; it favours the fit by far less than anything a printer resolves.
	fitEpsilon = 1e-6
	// noFill is a rectangle without a fill.
	noFill = -1
)

// frame is a rectangle element: position and size in points, the outline width in points (zero for
// none) and the luma of its fill, or noFill.
type frame struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	W      float64 `json:"w"`
	H      float64 `json:"h"`
	Stroke float64 `json:"stroke"`
	Fill   int     `json:"fill"`
}

// bandedLayout is the state of laying out one 2.x document: its values, and for a 2.1 document (coded.go) how the issue
// number is set, the images by element id and the page whose media the layout adds to.
type bandedLayout struct {
	m      *measurer
	values map[string]string
	c      coded
	page   *page
	// x0 is where every band starts, in millimetres: the left edge of the preset's safe region plus the overprint offset.
	x0 float64
	// variables is a variables-only print: what is not a variable is left out.
	variables bool
}

// layOutBanded lays out a validated 2.0 document for the values bound by doctemplates.BindGroup and
// the rows of that group: the pages with their texts, rules and rectangles in points. The table
// paginates as in 1.3 (header on every page, carried-forward and carry-over rows, the total row,
// exact sums) between the bands above and below it.
func layOutBanded(d doctemplates.Document, values map[string]string, background []byte, table *doctemplates.TableData, m *measurer, c coded) (*page, error) {
	t := d.TableItem()
	if d.Form() {
		if t != nil || table != nil {
			return nil, r.ErrInvalid
		}
	} else if t == nil || table == nil || len(table.Columns) != len(t.Columns) || len(table.Metric) != len(t.Columns) {
		return nil, r.ErrInvalid
	}
	pre, ok := d.PagePreset()
	if !ok {
		return nil, r.ErrInvalid
	}
	ox, oy, variables, ref := d.Overprint()
	drawn, err := checkBackground2(pre, ref, variables, background)
	if err != nil {
		return nil, err
	}
	p := &page{Title: "Document preview", Fonts: m.families, Background: drawn, Pages: []pageData{newPage(nil)}, Banner: bannerFor(pre.Strip)}
	if pre.ID != doctemplates.DefaultPreset.ID {
		p.Page = &pageSize{Width: pre.Width, Height: pre.Height}
	}
	b := &bandedLayout{m: m, values: values, c: c, page: p, x0: pre.Left + ox, variables: variables}
	// The overprint offset moves every element, the table and its pagination with them; the strip stays where it is.
	safeTop, safeBottom := pre.Top+oy, pre.Bottom+oy
	if d.Coded() {
		p.Media = map[string][]byte{}
	}
	if _, err := m.width(bannerText, pre.Strip.BannerSize, false); err != nil {
		return nil, err
	}
	height := func(band *doctemplates.Band) float64 {
		if band == nil {
			return 0
		}
		return band.Height
	}
	ph, dh, df, pf := height(d.Bands.PageHeader), height(d.Bands.DocumentHeader), height(d.Bands.DocumentFooter), height(d.Bands.PageFooter)
	var footerPage int
	var footerTop, bottom float64
	if t == nil {
		// A form (S06-B): its bands are stacked on one page, the document footer directly under the document header; what does not fit the
		// page is refused, never cut.
		bottom = (safeBottom - pf) * mm
		footerTop = (safeTop + ph + dh) * mm
		if footerTop+df*mm > bottom+fitEpsilon {
			return nil, refuse(doctemplates.ReasonDoesNotFit, doctemplates.Detail{}, "pdf: the bands of the form do not fit the page")
		}
		if d.Bands.DocumentFooter != nil {
			footerPage = 1
		}
	} else {
		// The detail band's room on each page: below the page header (and on the first page the
		// document header) down to the page footer.
		spec := tableSpec{size: float64(t.FontSize), full: !variables, left: (b.x0 + t.X) * mm, top: (safeTop + ph + dh) * mm, width: t.Width, band: doctemplates.BandDetail, element: t.ID}
		for _, c := range t.Columns {
			spec.columns = append(spec.columns, specColumn{field: c.Field, width: c.Width, align: c.Align, label: c.Label})
		}
		lay, running, zero, err := newTableLayout(spec, table, m)
		if err != nil {
			return nil, err
		}
		lay.bottom = (safeBottom - pf) * mm
		lay.top1 = func(number int) float64 {
			if number == 1 {
				return (safeTop + ph + dh) * mm
			}
			return (safeTop + ph) * mm
		}
		if err := lay.paginate(p, running, zero); err != nil {
			return nil, err
		}
		bottom = lay.bottom
		// The document footer follows the total row, whole: on a page of its own, below the page
		// header, when the last table page has not room for it.
		if d.Bands.DocumentFooter != nil {
			fits := func(top float64) bool { return top+df*mm <= lay.bottom+fitEpsilon }
			footerPage, footerTop = len(p.Pages), lay.end
			if !fits(footerTop) {
				footerTop = (safeTop + ph) * mm
				if !fits(footerTop) {
					return nil, refuse(doctemplates.ReasonBandOverflow, doctemplates.Detail{Band: doctemplates.BandDocumentFooter}, "pdf: the document footer does not fit an empty page")
				}
				if len(p.Pages) >= maxPages {
					return nil, refuse(doctemplates.ReasonTooManyPages, doctemplates.Detail{}, "pdf: the document needs more than %d pages", maxPages)
				}
				p.Pages = append(p.Pages, newPage(nil))
				footerPage = len(p.Pages)
			}
		}
	}
	if footerPage > 0 {
		p.Footer = &footerPlace{Page: footerPage, Top: footerTop}
	}
	pages := len(p.Pages)
	for n := 1; n <= pages; n++ {
		pg := &p.Pages[n-1]
		if band := d.Bands.PageHeader; band != nil {
			if err := b.place(pg, doctemplates.BandPageHeader, band, safeTop*mm, n, pages); err != nil {
				return nil, err
			}
		}
		if band := d.Bands.DocumentHeader; band != nil && n == 1 {
			if err := b.place(pg, doctemplates.BandDocumentHeader, band, (safeTop+ph)*mm, n, pages); err != nil {
				return nil, err
			}
		}
		if band := d.Bands.DocumentFooter; band != nil && n == footerPage {
			if err := b.place(pg, doctemplates.BandDocumentFooter, band, footerTop, n, pages); err != nil {
				return nil, err
			}
		}
		if band := d.Bands.PageFooter; band != nil {
			if err := b.place(pg, doctemplates.BandPageFooter, band, bottom, n, pages); err != nil {
				return nil, err
			}
		}
	}
	return p, nil
}

// place draws a band's elements on a page with the band's top edge at top (points): texts as glyphs,
// lines as rules, rectangles as boxes. {page} and {pages} are filled in the page texts first.
func (b *bandedLayout) place(pg *pageData, name string, band *doctemplates.Band, top float64, number, pages int) error {
	for _, it := range band.Elements {
		if b.variables && doctemplates.OmittedInVariables(it) {
			continue
		}
		switch it.Type {
		case doctemplates.TypeText:
			var v string
			if it.Source == doctemplates.SourceIssueNumber {
				// The number is the layout's, not a value bound from the snapshot (coded.go).
				n, err := b.number(doctemplates.Detail{Band: name, Element: it.ID}, float64(it.FontSize), it.Bold)
				if err != nil {
					return err
				}
				v = n
			} else {
				var ok bool
				if v, ok = b.values[it.ID]; !ok || !utf8.ValidString(v) {
					return r.ErrInvalid
				}
			}
			switch it.Source {
			case doctemplates.SourcePage:
				v = strings.NewReplacer("{page}", strconv.Itoa(number), "{pages}", strconv.Itoa(pages)).Replace(v)
			case doctemplates.SourceGroupSum:
				// The platform computed this number, and prints it as the table prints its totals: grouped digits,
				// the scale the sum was bound with, whether or not the field is a column of the table.
				if !exactNumber.MatchString(v) {
					return r.ErrIntegrity
				}
				v = groupDigits(v)
			}
			glyphs, err := b.text(name, it, v)
			if err != nil {
				return err
			}
			for _, g := range glyphs {
				g.Baseline += top
				pg.Texts = append(pg.Texts, g)
			}
		case doctemplates.TypeBarcode:
			if err := b.barcode(pg, name, it, top); err != nil {
				return err
			}
		case doctemplates.TypeQRCode:
			if err := b.qrcode(pg, name, it, top); err != nil {
				return err
			}
		case doctemplates.TypeImage:
			if err := b.image(pg, name, it, top); err != nil {
				return err
			}
		case doctemplates.TypeLine:
			pg.Rules = append(pg.Rules, rule{X0: (b.x0 + it.X1) * mm, Y0: top + it.Y1*mm, X1: (b.x0 + it.X2) * mm, Y1: top + it.Y2*mm, Stroke: it.Stroke})
		case doctemplates.TypeRect:
			fill := noFill
			if luma, ok := doctemplates.FillLuma(it.Fill); ok {
				fill = luma
			}
			pg.Boxes = append(pg.Boxes, frame{X: (b.x0 + it.X) * mm, Y: top + it.Y*mm, W: it.Width * mm, H: it.Height * mm, Stroke: it.Stroke, Fill: fill})
		}
	}
	return nil
}

// text places one text element's value, with baselines relative to the band's top. Without wrap the
// value is one line as in 1.x: its width is the sum of its clusters' plain advances, it must be
// narrower than the box by fitMargin, and it starts at the left edge, ends at the right edge or is
// centred. With wrap the value is broken into lines at the places the table cells break (a value's
// own line breaks kept, spaces at a break dropped, a run wider than the line cut), each line
// narrower than the box by fitMargin and set like the single line; more lines than the box's height
// holds, at 1.5 font sizes apart, are refused.
func (b *bandedLayout) text(band string, it doctemplates.Item, v string) ([]glyph, error) {
	place := doctemplates.Detail{Band: band, Element: it.ID}
	size := float64(it.FontSize)
	left, right := (b.x0+it.X)*mm, (b.x0+it.X+it.Width)*mm
	var lines []string
	if it.Wrap {
		// The line breaks and tabs a bound value may hold, as in table cells.
		v = printable(v)
		if utf8.RuneCountInString(v) > doctemplates.MaxWrapTextRunes {
			return nil, refuse(doctemplates.ReasonDoesNotFit, place, "pdf: a value the preview cannot print")
		}
		for _, c := range v {
			if (c < 32 && c != '\n') || c == 0x7F {
				return nil, refuse(doctemplates.ReasonNotCovered, place, "pdf: a value the preview cannot print")
			}
		}
		wrapper := &tableLayout{m: b.m, size: size}
		c, err := wrapper.wrap(v, it.Width*mm-fitMargin, it.Bold)
		if err != nil {
			return nil, placed(err, place)
		}
		// n lines need an ascent and descent plus n-1 steps; one line always fits a valid box.
		room := it.Height*mm/size - (ascent + descent)
		capacity := max(1, 1+int(room/lineStep+1e-9))
		if len(c.lines) > capacity {
			return nil, refuse(doctemplates.ReasonDoesNotFit, place, "pdf: a value has more lines than its box holds")
		}
		lines = c.lines
	} else {
		if utf8.RuneCountInString(v) > doctemplates.MaxTextRunes {
			return nil, refuse(doctemplates.ReasonDoesNotFit, place, "pdf: a value the preview cannot print")
		}
		if hasControl(v) {
			return nil, refuse(doctemplates.ReasonNotCovered, place, "pdf: a value the preview cannot print")
		}
		lines = []string{strings.TrimRightFunc(v, unicode.IsSpace)}
	}
	var out []glyph
	for k, line := range lines {
		pieces := clusters(strings.TrimRightFunc(line, unicode.IsSpace))
		if len(pieces) > 0 && unicode.Is(unicode.M, []rune(pieces[0])[0]) {
			return nil, refuse(doctemplates.ReasonNotCovered, place, "pdf: a value starts with a combining mark")
		}
		widths := make([]float64, len(pieces))
		total := 0.0
		for i, c := range pieces {
			w, err := b.m.width(c, size, it.Bold)
			if err != nil {
				return nil, placed(err, place)
			}
			widths[i], total = w, total+w
		}
		if total > it.Width*mm-fitMargin {
			return nil, refuse(doctemplates.ReasonDoesNotFit, place, "pdf: a value does not fit its box on one line")
		}
		x := left
		switch it.Align {
		case "right":
			x = right - total
		case "center":
			x = (left+right)/2 - total/2
		}
		baseline := it.Y*mm + ascent*size + float64(k)*lineStep*size
		for i, c := range pieces {
			out = append(out, glyph{X: x, Baseline: baseline, Size: size, Width: widths[i], Text: c, Bold: it.Bold})
			x += widths[i]
		}
	}
	return out, nil
}

// checkBackground2 decides whether the page of a 2.x document is drawn on its background, as a 1.x document is: in full mode the
// template's background must be given, the PNG it names (by digest) and of the page's aspect ratio; in a variables-only print or
// without one none may be. It reports whether the background is drawn.
func checkBackground2(pre doctemplates.Preset, ref *doctemplates.BackgroundRef, variables bool, background []byte) (bool, error) {
	switch {
	case len(background) > doctemplates.MaxBackgroundBytes:
		return false, r.ErrBudget
	case ref == nil && len(background) > 0, variables && len(background) > 0, ref != nil && !variables && len(background) == 0:
		return false, r.ErrInvalid
	case len(background) == 0:
		return false, nil
	}
	sum := sha256.Sum256(background)
	if hex.EncodeToString(sum[:]) != ref.SHA256 || !bytes.HasPrefix(background, pngSignature) {
		return false, r.ErrIntegrity
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(background))
	if err != nil || !doctemplates.BackgroundMatchesPreset(pre, cfg.Width, cfg.Height) {
		return false, r.ErrInvalid
	}
	return true, nil
}
