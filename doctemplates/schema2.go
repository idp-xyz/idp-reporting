package doctemplates

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"idp.local/reporting/export/barcode"
	r "idp.local/reporting/reporting"
)

// Schema 2.0 (ADR 0004, S03-J) is a closed JSON form beside 1.0-1.3: a document is made of bands
// (a page header and footer on every page, a document header on the first page, the detail table
// that continues over pages, a document footer after the last row) holding typed elements, and
// is bound to one row group of the frozen snapshot instead of one row. Positions are in
// millimetres on the 0.1 mm grid, relative to the band, which starts at x 10 mm inside the safe
// region (x 10-200, y 25-270). Templates 1.x do not use any of it.
//
// Schema 2.1 (ADR 0005, S03-M) is 2.0 plus the element types barcode, qrcode and image and the text
// source issue_number; a 2.0 document has none of them and renders as it always did.

// Schema 2.0 limits.
const (
	SchemaBanded   = "2.0"
	SchemaCoded    = "2.1" // 2.0 plus barcode, qrcode, image and the issue_number source (ADR 0005)
	MaxItems       = 64    // elements in a template, the table included
	MaxBandItems   = 24    // elements in one band
	MaxGroupFields = 3     // group fields
	BandWidth      = 190   // millimetres: the safe region's width, which every band spans
	MinBandHeight  = 5.0
	// MaxTextRunes bounds the text of a literal or a bound value: one line, or wrapped.
	MaxTextRunes     = 256
	MaxWrapTextRunes = 1024
	MaxLabelRunes    = 64
	// The safe region's vertical extent in tenths of a millimetre.
	safeTopTenths    = 250
	safeBottomTenths = 2700
	bandWidthTenths  = BandWidth * 10
)

// The band names, top to bottom.
const (
	BandPageHeader     = "page_header"
	BandDocumentHeader = "document_header"
	BandDetail         = "detail"
	BandDocumentFooter = "document_footer"
	BandPageFooter     = "page_footer"
)

// Element types, text sources, fills.
const (
	TypeText    = "text"
	TypeLine    = "line"
	TypeRect    = "rect"
	TypeTable   = "table"
	TypeBarcode = "barcode" // schema 2.1
	TypeQRCode  = "qrcode"  // schema 2.1
	TypeImage   = "image"   // schema 2.1

	SourceLiteral  = "literal"
	SourceField    = "field"
	SourceGroupSum = "group_sum"
	SourcePage     = "page"
	// SourceIssueNumber (schema 2.1): the number the source product assigned to the document. A preview prints the
	// placeholder barcode.PreviewNumber; the pre-issue check proves the fit for every legal number; the issue prints the record's.
	SourceIssueNumber = "issue_number"

	// SymbologyCode128 is the one symbology of a barcode element in 2.1.
	SymbologyCode128 = "code128"

	// Bounds of a code or image box, in millimetres.
	MinCodeBox   = 10.0
	MinImageBox  = 5.0
	MinBarHeight = 5.0
)

// Banded reports a document of the banded family: schema 2.0 or 2.1.
func (d Document) Banded() bool { return d.Schema == SchemaBanded || d.Schema == SchemaCoded }

// Coded reports a schema 2.1 document, which may hold barcodes, QR codes, images and the issue number.
func (d Document) Coded() bool { return d.Schema == SchemaCoded }

// fillLuma are the greys a rectangle may be filled with, as the luma (0 black, 255 white) the
// page program draws.
var fillLuma = map[string]int{"grey1": 245, "grey2": 232, "grey3": 215, "grey4": 190}

// FillLuma returns the luma of a rectangle's fill name; ok is false for "none" and unknown names.
func FillLuma(name string) (int, bool) { v, ok := fillLuma[name]; return v, ok }

// GroupSpec names the dimension fields whose exact frozen values partition the rows: one document
// per group. A template without a group is one document of the whole snapshot.
type GroupSpec struct {
	Fields []string `json:"fields"`
}

// Band is a band of fixed height. The detail band is a DetailBand: the only one that grows.
type Band struct {
	Height   float64 `json:"height"`
	Elements []Item  `json:"elements"`
}

// DetailBand holds the table, which fills the space between the bands above and below it and
// continues over pages.
type DetailBand struct {
	Elements []Item `json:"elements"`
}

type Bands struct {
	PageHeader     *Band       `json:"page_header,omitempty"`
	DocumentHeader *Band       `json:"document_header,omitempty"`
	Detail         *DetailBand `json:"detail"`
	DocumentFooter *Band       `json:"document_footer,omitempty"`
	PageFooter     *Band       `json:"page_footer,omitempty"`
}

// Item is an element of a band. Its JSON has the keys of its Type and no others:
//
//	text    id type source text field wrap x y width height font_size align bold
//	line    id type x1 y1 x2 y2 stroke
//	rect    id type x y width height stroke fill
//	table   id type x width font_size columns            (detail band only)
//	barcode id type source text field x y width height symbology human_readable [font_size]   (2.1; font_size iff human_readable)
//	qrcode  id type source text field x y size ecc                                           (2.1)
//	image   id type x y width height image                                                   (2.1)
//
// The struct is the union; Marshal and Unmarshal keep to the keys of the type.
type Item struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Source   string       `json:"source"`
	Text     string       `json:"text"`
	Field    string       `json:"field"`
	Wrap     bool         `json:"wrap"`
	X        float64      `json:"x"`
	Y        float64      `json:"y"`
	Width    float64      `json:"width"`
	Height   float64      `json:"height"`
	FontSize int          `json:"font_size"`
	Align    string       `json:"align"`
	Bold     bool         `json:"bold"`
	X1       float64      `json:"x1"`
	Y1       float64      `json:"y1"`
	X2       float64      `json:"x2"`
	Y2       float64      `json:"y2"`
	Stroke   float64      `json:"stroke"`
	Fill     string       `json:"fill"`
	Columns  []ItemColumn `json:"columns"`
	// Schema 2.1: the symbology and the printed value of a barcode, the side and the error correction level of a QR code
	// (Size is its side in millimetres), and the private image an image element draws.
	Symbology     string    `json:"symbology"`
	HumanReadable bool      `json:"human_readable"`
	Size          float64   `json:"size"`
	ECC           string    `json:"ecc"`
	Image         *ImageRef `json:"image"`
}

// ItemColumn is a column of the 2.0 table. Align and Label are optional: without them a metric
// column is right-aligned, any other left-aligned, and the header is the dataset's label.
type ItemColumn struct {
	Field string  `json:"field"`
	Width float64 `json:"width"`
	Align string  `json:"align,omitempty"`
	Label string  `json:"label,omitempty"`
}

var itemKeys = map[string][]string{
	TypeText:  {"id", "type", "source", "text", "field", "wrap", "x", "y", "width", "height", "font_size", "align", "bold"},
	TypeLine:  {"id", "type", "x1", "y1", "x2", "y2", "stroke"},
	TypeRect:  {"id", "type", "x", "y", "width", "height", "stroke", "fill"},
	TypeTable: {"id", "type", "x", "width", "font_size", "columns"},
	// font_size of a barcode is written, and required, only when it prints its value.
	TypeBarcode: {"id", "type", "source", "text", "field", "x", "y", "width", "height", "symbology", "human_readable"},
	TypeQRCode:  {"id", "type", "source", "text", "field", "x", "y", "size", "ecc"},
	TypeImage:   {"id", "type", "x", "y", "width", "height", "image"},
}

func (i Item) MarshalJSON() ([]byte, error) {
	switch i.Type {
	case TypeText:
		return json.Marshal(struct {
			ID       string  `json:"id"`
			Type     string  `json:"type"`
			Source   string  `json:"source"`
			Text     string  `json:"text"`
			Field    string  `json:"field"`
			Wrap     bool    `json:"wrap"`
			X        float64 `json:"x"`
			Y        float64 `json:"y"`
			Width    float64 `json:"width"`
			Height   float64 `json:"height"`
			FontSize int     `json:"font_size"`
			Align    string  `json:"align"`
			Bold     bool    `json:"bold"`
		}{i.ID, i.Type, i.Source, i.Text, i.Field, i.Wrap, i.X, i.Y, i.Width, i.Height, i.FontSize, i.Align, i.Bold})
	case TypeLine:
		return json.Marshal(struct {
			ID     string  `json:"id"`
			Type   string  `json:"type"`
			X1     float64 `json:"x1"`
			Y1     float64 `json:"y1"`
			X2     float64 `json:"x2"`
			Y2     float64 `json:"y2"`
			Stroke float64 `json:"stroke"`
		}{i.ID, i.Type, i.X1, i.Y1, i.X2, i.Y2, i.Stroke})
	case TypeRect:
		return json.Marshal(struct {
			ID     string  `json:"id"`
			Type   string  `json:"type"`
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
			Stroke float64 `json:"stroke"`
			Fill   string  `json:"fill"`
		}{i.ID, i.Type, i.X, i.Y, i.Width, i.Height, i.Stroke, i.Fill})
	case TypeTable:
		columns := i.Columns
		if columns == nil {
			columns = []ItemColumn{}
		}
		return json.Marshal(struct {
			ID       string       `json:"id"`
			Type     string       `json:"type"`
			X        float64      `json:"x"`
			Width    float64      `json:"width"`
			FontSize int          `json:"font_size"`
			Columns  []ItemColumn `json:"columns"`
		}{i.ID, i.Type, i.X, i.Width, i.FontSize, columns})
	case TypeBarcode:
		head := struct {
			ID            string  `json:"id"`
			Type          string  `json:"type"`
			Source        string  `json:"source"`
			Text          string  `json:"text"`
			Field         string  `json:"field"`
			X             float64 `json:"x"`
			Y             float64 `json:"y"`
			Width         float64 `json:"width"`
			Height        float64 `json:"height"`
			Symbology     string  `json:"symbology"`
			HumanReadable bool    `json:"human_readable"`
		}{i.ID, i.Type, i.Source, i.Text, i.Field, i.X, i.Y, i.Width, i.Height, i.Symbology, i.HumanReadable}
		if !i.HumanReadable {
			return json.Marshal(head)
		}
		return json.Marshal(struct {
			ID            string  `json:"id"`
			Type          string  `json:"type"`
			Source        string  `json:"source"`
			Text          string  `json:"text"`
			Field         string  `json:"field"`
			X             float64 `json:"x"`
			Y             float64 `json:"y"`
			Width         float64 `json:"width"`
			Height        float64 `json:"height"`
			Symbology     string  `json:"symbology"`
			HumanReadable bool    `json:"human_readable"`
			FontSize      int     `json:"font_size"`
		}{head.ID, head.Type, head.Source, head.Text, head.Field, head.X, head.Y, head.Width, head.Height, head.Symbology, head.HumanReadable, i.FontSize})
	case TypeQRCode:
		return json.Marshal(struct {
			ID     string  `json:"id"`
			Type   string  `json:"type"`
			Source string  `json:"source"`
			Text   string  `json:"text"`
			Field  string  `json:"field"`
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
			Size   float64 `json:"size"`
			ECC    string  `json:"ecc"`
		}{i.ID, i.Type, i.Source, i.Text, i.Field, i.X, i.Y, i.Size, i.ECC})
	case TypeImage:
		if i.Image == nil {
			return nil, r.ErrInvalid
		}
		return json.Marshal(struct {
			ID     string   `json:"id"`
			Type   string   `json:"type"`
			X      float64  `json:"x"`
			Y      float64  `json:"y"`
			Width  float64  `json:"width"`
			Height float64  `json:"height"`
			Image  ImageRef `json:"image"`
		}{i.ID, i.Type, i.X, i.Y, i.Width, i.Height, *i.Image})
	}
	// An unknown type has no form to write.
	return nil, r.ErrInvalid
}

func (i *Item) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil {
		return r.ErrInvalid
	}
	var kind string
	if json.Unmarshal(fields["type"], &kind) != nil {
		return r.ErrInvalid
	}
	want, ok := itemKeys[kind]
	// A barcode that prints its value has one key more, font_size, which a barcode that does not has no place for.
	extra := 0
	if kind == TypeBarcode && fields["font_size"] != nil {
		extra = 1
	}
	if !ok || len(fields) != len(want)+extra {
		return r.ErrInvalid
	}
	for _, k := range want {
		if fields[k] == nil {
			return r.ErrInvalid
		}
	}
	type wire Item
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	if kind == TypeBarcode && (extra == 1) != value.HumanReadable {
		return r.ErrInvalid
	}
	*i = Item(value)
	return nil
}

func (c *ItemColumn) UnmarshalJSON(data []byte) error {
	type wire ItemColumn
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil {
		return r.ErrInvalid
	}
	// The optional keys are written only when they are set, so an explicit empty one is not canonical.
	if fields["field"] == nil || fields["width"] == nil || (fields["align"] != nil && value.Align == "") || (fields["label"] != nil && value.Label == "") {
		return r.ErrInvalid
	}
	*c = ItemColumn(value)
	return nil
}

func (g *GroupSpec) UnmarshalJSON(data []byte) error {
	type wire GroupSpec
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil || len(fields) != 1 || fields["fields"] == nil {
		return r.ErrInvalid
	}
	*g = GroupSpec(value)
	return nil
}

func (b *Band) UnmarshalJSON(data []byte) error {
	type wire Band
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil || len(fields) != 2 || fields["height"] == nil || fields["elements"] == nil {
		return r.ErrInvalid
	}
	*b = Band(value)
	return nil
}

func (b *DetailBand) UnmarshalJSON(data []byte) error {
	type wire DetailBand
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil || len(fields) != 1 || fields["elements"] == nil {
		return r.ErrInvalid
	}
	*b = DetailBand(value)
	return nil
}

// bandList is the bands of a document that are present, top to bottom, with their names.
func (b *Bands) bandList() []struct {
	Name string
	Band *Band
} {
	return []struct {
		Name string
		Band *Band
	}{{BandPageHeader, b.PageHeader}, {BandDocumentHeader, b.DocumentHeader}, {BandDocumentFooter, b.DocumentFooter}, {BandPageFooter, b.PageFooter}}
}

// BandItem is an element with the band it is in.
type BandItem struct {
	Band string
	Item Item
}

// Items lists the elements of a 2.0 document, band by band from the top, in the order of each band.
func (d Document) Items() []BandItem {
	var out []BandItem
	if d.Bands == nil {
		return nil
	}
	add := func(name string, items []Item) {
		for _, it := range items {
			out = append(out, BandItem{name, it})
		}
	}
	if d.Bands.PageHeader != nil {
		add(BandPageHeader, d.Bands.PageHeader.Elements)
	}
	if d.Bands.DocumentHeader != nil {
		add(BandDocumentHeader, d.Bands.DocumentHeader.Elements)
	}
	if d.Bands.Detail != nil {
		add(BandDetail, d.Bands.Detail.Elements)
	}
	if d.Bands.DocumentFooter != nil {
		add(BandDocumentFooter, d.Bands.DocumentFooter.Elements)
	}
	if d.Bands.PageFooter != nil {
		add(BandPageFooter, d.Bands.PageFooter.Elements)
	}
	return out
}

// TableItem returns the detail table of a 2.0 document, or nil.
func (d Document) TableItem() *Item {
	if d.Bands == nil || d.Bands.Detail == nil || len(d.Bands.Detail.Elements) != 1 || d.Bands.Detail.Elements[0].Type != TypeTable {
		return nil
	}
	return &d.Bands.Detail.Elements[0]
}

// Table row geometry shared with the layout (export/pdf/table.go): a one-line row is 2 * 2 pt of
// inset plus 1.46 font sizes (the ascent 1.16 and the descent 0.3), so in hundredths of a point
// 400 + 146 per point of type size.

// MinDetailHeightTenths is the least height, in tenths of a millimetre, the detail band must be
// left on the first page: the table's header, the carried-forward row, one body row and the
// carry-forward (or total) row, each one line high at the table's type size.
func MinDetailHeightTenths(fontSize int) int {
	hundredths := (400 + 146*fontSize) * 4 // four one-line rows, in hundredths of a point
	// pt * 25.4 / 72 mm, in tenths: hundredths * 254 / (100 * 72), rounded up.
	return (hundredths*254 + 7199) / 7200
}

func inRange(n, lo, hi float64) bool { return n >= lo && n <= hi }

// onGrid reports a finite number of whole tenths.
func onGrid(n float64) bool { return grid(n) }

// stroke widths are multiples of a quarter point so that they are exact in binary and in JSON.
func strokeOK(n, lo, hi float64) bool {
	return inRange(n, lo, hi) && math.Abs(n*4-math.Round(n*4)) < 1e-9
}

// placeholders: a page text may hold {page} and {pages}, and no other brace.
func pagePlaceholders(text string) (count int, ok bool) {
	rest := text
	for {
		i := strings.IndexAny(rest, "{}")
		if i < 0 {
			return count, true
		}
		switch {
		case strings.HasPrefix(rest[i:], "{pages}"):
			rest = rest[i+len("{pages}"):]
		case strings.HasPrefix(rest[i:], "{page}"):
			rest = rest[i+len("{page}"):]
		default:
			return count, false
		}
		count++
	}
}

func printableText(text string, wrap bool, limit int) bool {
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > limit {
		return false
	}
	for _, c := range text {
		if c < 32 || c == 0x7F {
			if wrap && c == '\n' {
				continue
			}
			return false
		}
	}
	return true
}

// validateBanded checks a schema 2.0 document: every rule of the closed form, in the order of
// ADR 0004. It never looks at data; the snapshot's fields are checked where the document is bound.
func validateBanded(d Document) error {
	if !d.Banded() || d.Kind != "document" || !validBinding(d.Binding) || len(d.Elements) != 0 || d.Print != nil || d.Background != nil || d.Table != nil || d.Bands == nil || d.Bands.Detail == nil {
		return r.ErrInvalid
	}
	if d.Group != nil {
		if len(d.Group.Fields) < 1 || len(d.Group.Fields) > MaxGroupFields {
			return r.ErrInvalid
		}
		seen := map[string]bool{}
		for _, f := range d.Group.Fields {
			if f == "" || len(f) > 128 || seen[f] || !utf8.ValidString(f) {
				return r.ErrInvalid
			}
			seen[f] = true
		}
	}
	// The first page's stack: the bands that are on it whatever the rows are. The document footer
	// is placed after the last row, on that page or the next, and the layout refuses one that does not
	// fit an empty page.
	heights := 0
	for _, band := range d.Bands.bandList() {
		if band.Band == nil {
			continue
		}
		h := band.Band.Height
		if !onGrid(h) || !inRange(h, MinBandHeight, float64(safeBottomTenths-safeTopTenths)/10) || band.Band.Elements == nil || len(band.Band.Elements) > MaxBandItems {
			return r.ErrInvalid
		}
		if band.Name != BandDocumentFooter {
			heights += tenths(h)
		}
	}
	detail := d.Bands.Detail
	if len(detail.Elements) != 1 || detail.Elements[0].Type != TypeTable {
		return r.ErrInvalid
	}
	total, ids := 0, map[string]bool{}
	for _, band := range d.Bands.bandList() {
		if band.Band == nil {
			continue
		}
		total += len(band.Band.Elements)
		for i, it := range band.Band.Elements {
			if !validItemID(it.ID) || ids[it.ID] {
				return r.ErrInvalid
			}
			ids[it.ID] = true
			if it.Type == TypeTable || !validItem(it, tenths(band.Band.Height), d.Coded()) {
				return r.ErrInvalid
			}
			// A text, a code and an image each own their box: the quiet zone of a code and the picture of an image are not
			// printed over. Lines and rectangles are decoration and may lie under or around them.
			if boxed(it.Type) {
				for _, prior := range band.Band.Elements[:i] {
					if boxed(prior.Type) && overlaps(it, prior) {
						return r.ErrInvalid
					}
				}
			}
		}
	}
	table := detail.Elements[0]
	total++
	if total > MaxItems || !validItemID(table.ID) || ids[table.ID] || validTable(table) != nil {
		return r.ErrInvalid
	}
	if heights+MinDetailHeightTenths(table.FontSize) > safeBottomTenths-safeTopTenths {
		return r.ErrInvalid
	}
	b, e := json.Marshal(d)
	if e != nil || len(b) > 65536 {
		return r.ErrBudget
	}
	return nil
}

// boxed reports the element types that own a rectangle of the band.
func boxed(kind string) bool {
	return kind == TypeText || kind == TypeBarcode || kind == TypeQRCode || kind == TypeImage
}

// extent is the width and height of an element's box in millimetres: a QR code is square.
func extent(it Item) (w, h float64) {
	if it.Type == TypeQRCode {
		return it.Size, it.Size
	}
	return it.Width, it.Height
}

func overlaps(a, b Item) bool {
	aw, ah := extent(a)
	bw, bh := extent(b)
	return tenths(a.X) < tenths(b.X)+tenths(bw) && tenths(b.X) < tenths(a.X)+tenths(aw) && tenths(a.Y) < tenths(b.Y)+tenths(bh) && tenths(b.Y) < tenths(a.Y)+tenths(ah)
}

// validItemID: e1 to e64, without leading zeros.
func validItemID(id string) bool {
	number, e := strconv.Atoi(strings.TrimPrefix(id, "e"))
	return e == nil && strings.HasPrefix(id, "e") && id == "e"+strconv.Itoa(number) && number >= 1 && number <= MaxItems
}

// validItem checks a text, line, rectangle or, in a 2.1 document (coded), a barcode, QR code or image inside a band of the given
// height (tenths).
func validItem(it Item, bandHeight int, coded bool) bool {
	switch it.Type {
	case TypeText:
		return validText(it, bandHeight, coded)
	case TypeBarcode:
		return coded && validBarcode(it, bandHeight)
	case TypeQRCode:
		return coded && validQRCode(it, bandHeight)
	case TypeImage:
		return coded && validImage(it, bandHeight)
	case TypeLine:
		pts := []float64{it.X1, it.Y1, it.X2, it.Y2}
		for _, n := range pts {
			if !onGrid(n) {
				return false
			}
		}
		return inRange(it.X1, 0, BandWidth) && inRange(it.X2, 0, BandWidth) && inRange(it.Y1, 0, float64(bandHeight)/10) && inRange(it.Y2, 0, float64(bandHeight)/10) &&
			(tenths(it.X1) != tenths(it.X2) || tenths(it.Y1) != tenths(it.Y2)) && strokeOK(it.Stroke, 0.25, 2)
	case TypeRect:
		for _, n := range []float64{it.X, it.Y, it.Width, it.Height} {
			if !onGrid(n) {
				return false
			}
		}
		if !inRange(it.X, 0, BandWidth) || !inRange(it.Y, 0, float64(bandHeight)/10) || !inRange(it.Width, 0.1, BandWidth) || !inRange(it.Height, 0.1, float64(bandHeight)/10) ||
			tenths(it.X)+tenths(it.Width) > bandWidthTenths || tenths(it.Y)+tenths(it.Height) > bandHeight || !strokeOK(it.Stroke, 0, 2) {
			return false
		}
		_, filled := fillLuma[it.Fill]
		return (filled || it.Fill == "none") && (it.Stroke > 0 || filled)
	}
	return false
}

func validText(it Item, bandHeight int, coded bool) bool {
	for _, n := range []float64{it.X, it.Y, it.Width, it.Height} {
		if !onGrid(n) {
			return false
		}
	}
	if !inRange(it.X, 0, BandWidth) || !inRange(it.Y, 0, float64(bandHeight)/10) || !inRange(it.Width, 10, BandWidth) || !inRange(it.Height, 5, float64(bandHeight)/10) ||
		tenths(it.X)+tenths(it.Width) > bandWidthTenths || tenths(it.Y)+tenths(it.Height) > bandHeight ||
		it.FontSize < 8 || it.FontSize > 28 || tenths(it.Height)*72 < it.FontSize*400 || (it.Align != "left" && it.Align != "center" && it.Align != "right") {
		return false
	}
	switch it.Source {
	case SourceLiteral:
		limit := MaxTextRunes
		if it.Wrap {
			limit = MaxWrapTextRunes
		}
		return it.Field == "" && printableText(it.Text, it.Wrap, limit)
	case SourcePage:
		n, ok := pagePlaceholders(it.Text)
		return it.Field == "" && !it.Wrap && ok && n >= 1 && printableText(it.Text, false, MaxTextRunes)
	case SourceField, SourceGroupSum:
		return it.Text == "" && it.Field != "" && len(it.Field) <= 128 && utf8.ValidString(it.Field)
	case SourceIssueNumber:
		// The number is one line of at most 32 characters, and the layout proves it fits for every legal number.
		return coded && it.Text == "" && it.Field == "" && !it.Wrap
	}
	return false
}

// inBand reports a box whose position and size are on the grid and lie inside a band of the given height (tenths) and width.
func inBand(x, y, w, h float64, bandHeight int) bool {
	for _, n := range []float64{x, y, w, h} {
		if !onGrid(n) {
			return false
		}
	}
	return inRange(x, 0, BandWidth) && inRange(y, 0, float64(bandHeight)/10) && tenths(x)+tenths(w) <= bandWidthTenths && tenths(y)+tenths(h) <= bandHeight
}

// validCodeValue checks where the value of a barcode or QR code comes from: a literal of at most max characters (a QR
// code's limit is in bytes, which also bounds the characters), a field of the group, or the issue number. The symbology's
// own alphabet is the layout's to check, as the fonts' coverage is for a text.
func validCodeValue(it Item, maxRunes, maxBytes int) bool {
	switch it.Source {
	case SourceLiteral:
		return it.Field == "" && it.Text != "" && len(it.Text) <= maxBytes && printableText(it.Text, false, maxRunes)
	case SourceField:
		return it.Text == "" && it.Field != "" && len(it.Field) <= 128 && utf8.ValidString(it.Field)
	case SourceIssueNumber:
		return it.Text == "" && it.Field == ""
	}
	return false
}

// validBarcode: a Code 128 box in the band, its value, and, when it prints its value under the bars, the type size of that
// line (the key is written only then). Whether the symbol fits the box at the least module width is the layout's.
func validBarcode(it Item, bandHeight int) bool {
	if !inBand(it.X, it.Y, it.Width, it.Height, bandHeight) || !inRange(it.Width, MinCodeBox, BandWidth) || !inRange(it.Height, MinBarHeight, float64(bandHeight)/10) || it.Symbology != SymbologyCode128 {
		return false
	}
	if it.HumanReadable != (it.FontSize != 0) || (it.HumanReadable && (it.FontSize < 8 || it.FontSize > 28)) {
		return false
	}
	return validCodeValue(it, barcode.Code128MaxRunes, 4*barcode.Code128MaxRunes)
}

// validQRCode: a square box in the band, an error correction level and a value.
func validQRCode(it Item, bandHeight int) bool {
	return inBand(it.X, it.Y, it.Size, it.Size, bandHeight) && inRange(it.Size, MinCodeBox, BandWidth) && barcode.ValidECC(it.ECC) &&
		validCodeValue(it, barcode.QRMaxBytes, barcode.QRMaxBytes)
}

// validImage: a box in the band and the reference of a private image; that the image exists, is the caller's and belongs to
// the template's binding is checked where the template is authorized.
func validImage(it Item, bandHeight int) bool {
	return inBand(it.X, it.Y, it.Width, it.Height, bandHeight) && inRange(it.Width, MinImageBox, BandWidth) && inRange(it.Height, MinImageBox, float64(bandHeight)/10) &&
		it.Image != nil && validImageRef(*it.Image)
}

// validTable checks the detail table: a position and width in the band, a type size, and 1 to 12
// distinct columns of at least 8 mm that add up to the width.
func validTable(t Item) error {
	if !onGrid(t.X) || !onGrid(t.Width) || !inRange(t.X, 0, BandWidth) || !inRange(t.Width, MinColumnWidth, BandWidth) || tenths(t.X)+tenths(t.Width) > bandWidthTenths ||
		t.FontSize < MinTableFontSize || t.FontSize > MaxTableFontSize || len(t.Columns) < 1 || len(t.Columns) > MaxTableColumns {
		return r.ErrInvalid
	}
	sum, seen := 0, map[string]bool{}
	for _, c := range t.Columns {
		if c.Field == "" || len(c.Field) > 128 || !utf8.ValidString(c.Field) || seen[c.Field] || !onGrid(c.Width) || !inRange(c.Width, MinColumnWidth, BandWidth) {
			return r.ErrInvalid
		}
		if c.Align != "" && c.Align != "left" && c.Align != "center" && c.Align != "right" {
			return r.ErrInvalid
		}
		if c.Label != "" && !printableText(c.Label, false, MaxLabelRunes) {
			return r.ErrInvalid
		}
		seen[c.Field] = true
		sum += tenths(c.Width)
	}
	if sum != tenths(t.Width) {
		return r.ErrInvalid
	}
	return nil
}
