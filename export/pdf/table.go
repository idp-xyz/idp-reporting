package pdf

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// Document table geometry in points and font sizes (S03-F). Go owns every position: lines are
// wrapped, rows sized and pages filled here, and Typst only draws what it is given.
const (
	tableInsetX = 3.0
	tableInsetY = 2.0
	lineStep    = 1.5 // baseline to baseline, in font sizes
	descent     = 0.3 // below the last baseline, in font sizes (Noto Sans SC descends 0.29)
	maxPages    = 100 // a document of more pages is unsupported
	ruleStroke  = 0.5 // cell rules, in points
	totalStroke = 1.0 // the rule above the total row
)

// tableLabels are the printed words of the carried rows; they are data, checked against the
// fonts like the values.
var tableLabels = map[string]string{
	"forward_before": "承前（第 ", "forward_after": " 页）",
	"carry_before": "过次（转第 ", "carry_after": " 页）",
	"total": "合计",
}

// tableColumn is one laid-out column: its content edges and how its values are set.
type tableColumn struct {
	left, right float64 // the column's rule positions
	metric      bool    // a numeric metric: grouped digits, right-aligned, summed
	label       string
	align       string // schema 2.0: left, center or right; empty is right for a metric, else left
}

// alignment is how the column's text sits between its insets.
func (c tableColumn) alignment() string {
	switch {
	case c.align != "":
		return c.align
	case c.metric:
		return "right"
	}
	return "left"
}

// cell is one wrapped cell: its lines and their widths.
type cell struct {
	lines  []string
	widths []float64
}

// tableLayout is the table of one document, laid out page by page.
type tableLayout struct {
	m       *measurer
	size    float64
	left    float64 // region edges in points, with the print offsets
	top     float64
	right   float64
	bottom  float64
	full    bool // full mode draws rules, fills and the header text
	columns []tableColumn
	header  []cell
	rows    [][]cell
	heights []float64 // row heights
	// The place of the table, for the details of a refusal: the field id of each column, the table's
	// band and element id (schema 2.0; empty for 1.x) and its data, which knows the snapshot row
	// index of each row.
	fields  []string
	band    string
	element string
	data    *doctemplates.TableData
	// top gives the top of the table on a page (from 1), in points, when it is not lay.top on every
	// page; end is where the table ends on the last page, below the total row.
	top1 func(number int) float64
	end  float64
}

// at is the place of a refusal in the table: a column (or -1) and a table row (or -1).
func (lay *tableLayout) at(column, row int) doctemplates.Detail {
	d := doctemplates.Detail{Band: lay.band, Element: lay.element}
	if column >= 0 && column < len(lay.fields) {
		d.Column = lay.fields[column]
	}
	if row >= 0 && lay.data != nil {
		n := lay.data.SnapshotRow(row)
		d.Row = &n
	}
	return d
}

// pageTop is where the table starts on a page.
func (lay *tableLayout) pageTop(number int) float64 {
	if lay.top1 != nil {
		return lay.top1(number)
	}
	return lay.top
}

// regionReason is the reason a table that does not fit the space it has is refused: the table's
// region in 1.x, the detail band in 2.0.
func (lay *tableLayout) regionReason() string {
	if lay.band != "" {
		return doctemplates.ReasonBandOverflow
	}
	return doctemplates.ReasonDoesNotFit
}

// layOutTable adds the table's pages to p: the header on every page, rows never split, a
// brought-forward row from page 2 on, a carry-forward row on every page but the last, and the
// total row on the last page; the text boxes of the document on every page.
func layOutTable(p *page, d doctemplates.Document, profile doctemplates.PrintProfile, data *doctemplates.TableData, m *measurer) error {
	t := d.Table
	if data == nil || len(data.Columns) != len(t.Columns) {
		return r.ErrInvalid
	}
	spec := tableSpec{size: float64(t.FontSize), full: profile.Mode == "full", left: (t.X + profile.OffsetX) * mm, top: (t.Y + profile.OffsetY) * mm, width: t.Width, height: t.Height}
	for _, c := range t.Columns {
		spec.columns = append(spec.columns, specColumn{field: c.Field, width: c.Width})
	}
	lay, running, zero, err := newTableLayout(spec, data, m)
	if err != nil {
		return err
	}
	p.HeaderLines = lay.headerLines()
	return lay.paginate(p, running, zero)
}

// tableSpec is what a table is laid out from, whichever schema it is in: its type size, the left
// edge and top of its region in points with the print offsets, its width and height in millimetres
// and its columns. A 2.0 table names its band and element for the details of a refusal.
type tableSpec struct {
	size          float64
	full          bool
	left, top     float64
	width, height float64
	columns       []specColumn
	band, element string
}

// specColumn is a column's field, width in millimetres and, in schema 2.0, its alignment and the
// literal label that replaces the dataset's.
type specColumn struct {
	field        string
	width        float64
	align, label string
}

// newTableLayout checks the table's labels, columns and cells and measures every row; it returns the
// layout with the exact running sums after each row and the zero sums, ready to paginate.
func newTableLayout(spec tableSpec, data *doctemplates.TableData, m *measurer) (*tableLayout, [][]string, []string, error) {
	size := spec.size
	lay := &tableLayout{m: m, size: size, full: spec.full, left: spec.left, top: spec.top, band: spec.band, element: spec.element, data: data}
	lay.right, lay.bottom = lay.left+spec.width*mm, lay.top+spec.height*mm
	for _, c := range spec.columns {
		lay.fields = append(lay.fields, c.field)
	}
	for k, v := range tableLabels {
		if _, err := m.width(v+"0123456789", size, true); err != nil {
			return nil, nil, nil, fmt.Errorf("pdf: label %s: %w", k, err)
		}
	}
	// Columns: edges, kind and header label, as the table export labels them.
	x := lay.left
	for i, c := range spec.columns {
		col := data.Columns[i]
		label := printable(col.Label)
		switch {
		case col.Currency != "" && col.Unit != "":
			label += "（" + printable(col.Currency) + " · " + printable(col.Unit) + "）"
		case col.Currency != "":
			label += "（" + printable(col.Currency) + "）"
		case col.Unit != "":
			label += "（" + printable(col.Unit) + "）"
		}
		if c.label != "" {
			label = c.label
		}
		next := x + c.width*mm
		lay.columns = append(lay.columns, tableColumn{left: x, right: next, metric: data.Metric[i] && (col.Type == "decimal" || col.Type == "integer"), label: label, align: c.align})
		x = next
	}
	// The header, every row and the exact running sums.
	var err error
	if lay.header, err = lay.wrapRow(headerTexts(lay), true, -1); err != nil {
		return nil, nil, nil, err
	}
	sums := make([]*column, len(lay.columns))
	for j, c := range lay.columns {
		if c.metric && j > 0 {
			sums[j] = &column{scale: data.Columns[j].Scale}
		}
	}
	// The carried rows are one line each: their labels (the widest page number is maxPages) and
	// the sums they show must fit their columns, as must every amount, which is never cut.
	for _, label := range []string{tableLabels["forward_before"] + strconv.Itoa(maxPages) + tableLabels["forward_after"], tableLabels["carry_before"] + strconv.Itoa(maxPages) + tableLabels["carry_after"], tableLabels["total"]} {
		if err := lay.fits(label, 0, "a carried row's label"); err != nil {
			return nil, nil, nil, err
		}
	}
	lay.rows = make([][]cell, len(data.Rows))
	lay.heights = make([]float64, len(data.Rows))
	for i, row := range data.Rows {
		texts := make([]string, len(row))
		for j, v := range row {
			if lay.columns[j].metric {
				if !exactNumber.MatchString(v) {
					return nil, nil, nil, r.ErrIntegrity
				}
				texts[j] = groupDigits(v)
				if w, err := lay.m.width(texts[j], size, false); err != nil {
					return nil, nil, nil, placed(err, lay.at(j, i))
				} else if w > lay.content(j) {
					return nil, nil, nil, refuse(doctemplates.ReasonDoesNotFit, lay.at(j, i), "pdf: an amount does not fit its column")
				}
				if sums[j] != nil {
					if err := sums[j].add(v); err != nil {
						return nil, nil, nil, err
					}
				}
			} else {
				texts[j] = printable(v)
			}
		}
		if lay.rows[i], err = lay.wrapRow(texts, false, i); err != nil {
			return nil, nil, nil, err
		}
		lay.heights[i] = lay.rowHeight(lay.rows[i])
	}
	// The sum of the rows must be the snapshot's own total: a durable snapshot is complete.
	for j, s := range sums {
		if s == nil {
			continue
		}
		if total, ok := data.Totals[data.Columns[j].ID]; ok && !s.equals(total) {
			return nil, nil, nil, fmt.Errorf("pdf: the rows of %s do not add up to the snapshot's total: %w", data.Columns[j].ID, r.ErrIntegrity)
		}
	}
	// Running sums after each row, per metric column, as printed strings.
	running := make([][]string, len(data.Rows))
	partial := make([]*column, len(lay.columns))
	for j, s := range sums {
		if s != nil {
			partial[j] = &column{scale: s.scale, frac: s.frac}
		}
	}
	for i, row := range data.Rows {
		running[i] = make([]string, len(lay.columns))
		for j, s := range partial {
			if s == nil {
				continue
			}
			if err := s.add(row[j]); err != nil {
				return nil, nil, nil, err
			}
			running[i][j] = groupDigits(s.String())
		}
	}
	zero := make([]string, len(lay.columns))
	for j, s := range sums {
		if s != nil {
			zero[j] = groupDigits((&column{scale: s.scale, frac: s.frac}).String())
		}
	}
	return lay, running, zero, nil
}

// content is a column's content width: between its rules, less the insets.
func (lay *tableLayout) content(j int) float64 {
	return lay.columns[j].right - lay.columns[j].left - 2*tableInsetX
}

// fits refuses bold text wider than a column's content width.
func (lay *tableLayout) fits(text string, j int, what string) error {
	w, err := lay.m.width(text, lay.size, true)
	if err != nil {
		return placed(err, lay.at(j, -1))
	}
	if w > lay.content(j) {
		return refuse(doctemplates.ReasonDoesNotFit, lay.at(j, -1), "pdf: %s does not fit its column", what)
	}
	return nil
}

// headerLines is the height of the header in lines: the tallest wrapped label.
func (lay *tableLayout) headerLines() int {
	n := 1
	for _, c := range lay.header {
		n = max(n, len(c.lines))
	}
	return n
}

// headerTexts are the column labels.
func headerTexts(lay *tableLayout) []string {
	out := make([]string, len(lay.columns))
	for j, c := range lay.columns {
		out[j] = c.label
	}
	return out
}

// paginate fills the pages. Each page holds the header, the brought-forward row (from page 2),
// as many whole rows as fit, and the carry-forward row, or on the last page the total row.
func (lay *tableLayout) paginate(p *page, running [][]string, zero []string) error {
	n := len(lay.rows)
	headerH := lay.rowHeight(lay.header)
	// The carried rows are single lines of bold text.
	carryH := 2*tableInsetY + (ascent+descent)*lay.size
	// The space a page leaves the table: from the page's top (the same on every page, unless a 2.0
	// document's first page has more above the table) to the bottom of the region.
	if headerH+carryH > lay.bottom-lay.pageTop(1) {
		return refuse(lay.regionReason(), lay.at(-1, -1), "pdf: the table's header and total row are taller than its region")
	}
	base := *p
	pages := []pageData{}
	i, number := 0, 1
	for {
		top := lay.pageTop(number)
		avail := lay.bottom - top - headerH
		if number > 1 {
			avail -= carryH
		}
		// The most rows that fit with their tail: the total row after the last row, else the
		// carry-forward row.
		k, used := 0, 0.0
		for i+k < n && used+lay.heights[i+k] <= avail {
			used += lay.heights[i+k]
			k++
		}
		for k > 0 && used+carryH > avail {
			k--
			used -= lay.heights[i+k]
		}
		if k == 0 && i < n {
			return refuse(lay.regionReason(), lay.at(-1, i), "pdf: a table row is taller than the table's pages can hold")
		}
		if number > maxPages {
			return refuse(doctemplates.ReasonTooManyPages, doctemplates.Detail{}, "pdf: the document needs more than %d pages", maxPages)
		}
		last := i+k == n
		pg := newPage(base.Pages[0].Texts)
		y := top
		y = lay.drawRow(&pg, y, lay.header, true, true)
		if number > 1 {
			row, err := lay.carryRow(tableLabels["forward_before"]+strconv.Itoa(number)+tableLabels["forward_after"], running[i-1])
			if err != nil {
				return err
			}
			y = lay.drawRow(&pg, y, row, true, false)
		}
		for _, row := range lay.rows[i : i+k] {
			y = lay.drawRow(&pg, y, row, false, false)
		}
		if last {
			sums := zero
			if n > 0 {
				sums = running[n-1]
			}
			row, err := lay.carryRow(tableLabels["total"], sums)
			if err != nil {
				return err
			}
			if lay.full {
				pg.Rules = append(pg.Rules, rule{X0: lay.left, Y0: y, X1: lay.right, Y1: y, Stroke: totalStroke})
			}
			lay.end = lay.drawRow(&pg, y, row, true, false)
		} else {
			row, err := lay.carryRow(tableLabels["carry_before"]+strconv.Itoa(number+1)+tableLabels["carry_after"], running[i+k-1])
			if err != nil {
				return err
			}
			lay.drawRow(&pg, y, row, true, false)
		}
		pages = append(pages, pg)
		i += k
		number++
		if last {
			break
		}
	}
	p.Pages = pages
	return nil
}

// carryRow is a carried row: the label in the first column, the sums in the metric columns.
// Only the sums that are printed are required to fit.
func (lay *tableLayout) carryRow(label string, sums []string) ([]cell, error) {
	row := make([]cell, len(lay.columns))
	for j := range lay.columns {
		text := ""
		if j == 0 {
			text = label
		} else if sums[j] != "" {
			text = sums[j]
			if err := lay.fits(text, j, "a carried sum"); err != nil {
				return nil, err
			}
		}
		w, err := lay.m.width(text, lay.size, true)
		if err != nil {
			return nil, placed(err, lay.at(j, -1))
		}
		row[j] = cell{lines: []string{text}, widths: []float64{w}}
	}
	return row, nil
}

// rowHeight is the height of a row from its tallest cell.
func (lay *tableLayout) rowHeight(row []cell) float64 {
	lines := 1
	for _, c := range row {
		lines = max(lines, len(c.lines))
	}
	return 2*tableInsetY + (ascent+descent)*lay.size + float64(lines-1)*lineStep*lay.size
}

// drawRow adds a row's rules, fill and text lines to the page at y and returns the next y.
func (lay *tableLayout) drawRow(pg *pageData, y float64, row []cell, bold, header bool) float64 {
	h := lay.rowHeight(row)
	for j, col := range lay.columns {
		if lay.full {
			if header {
				pg.Fills = append(pg.Fills, rect{X: col.left, Y: y, W: col.right - col.left, H: h})
			}
			pg.Cells = append(pg.Cells, rect{X: col.left, Y: y, W: col.right - col.left, H: h})
		}
		if header && !lay.full {
			continue
		}
		c := row[j]
		for k, line := range c.lines {
			if line == "" {
				continue
			}
			x := col.left + tableInsetX
			switch col.alignment() {
			case "right":
				x = col.right - tableInsetX - c.widths[k]
			case "center":
				x = (col.left+col.right)/2 - c.widths[k]/2
			}
			baseline := y + tableInsetY + ascent*lay.size + float64(k)*lineStep*lay.size
			// A line is drawn as one text object per piece, so that Typst shapes nothing
			// across consecutive punctuation and every character stays at its plain-advance
			// position (the widths were measured when the line was wrapped).
			for _, piece := range splitShaped(line) {
				w, _ := lay.m.width(piece, lay.size, bold)
				pg.Texts = append(pg.Texts, glyph{X: x, Baseline: baseline, Size: lay.size, Width: w, Text: piece, Bold: bold})
				x += w
			}
		}
	}
	return y + h
}

// splitShaped splits a line between two consecutive characters that Typst would shape together
// within one text object: CJK punctuation, which it compresses when it follows another mark,
// and dashes and ellipses, which the font may join. An ellipsis (U+2026, U+22EF) is split from
// whatever stands next to it, a letter and a space as well as a mark: the font kerns a Latin
// letter into it (the "l" of "Total…" draws 0.59 pt wider than the plain advances), so an
// ellipsis is always a piece of its own. Each piece is placed by the server, so Typst never
// sees two of them in one run; an opening mark at the start of a piece keeps its position
// thanks to the template's empty inline box.
func splitShaped(line string) []string {
	var out []string
	start := 0
	var prev rune
	for i, ch := range line {
		if i > 0 && ((shaped(prev) && shaped(ch)) || ellipsis(prev) || ellipsis(ch)) {
			out = append(out, line[start:i])
			start = i
		}
		prev = ch
	}
	return append(out, line[start:])
}

// ellipsis reports the horizontal ellipsis and the midline horizontal ellipsis.
func ellipsis(ch rune) bool { return ch == 0x2026 || ch == 0x22EF }

// shaped reports a character Typst shapes differently next to another such character.
func shaped(ch rune) bool {
	return (ch >= 0x3000 && ch <= 0x303F) || (ch >= 0xFF00 && ch <= 0xFF0F) || (ch >= 0xFF1A && ch <= 0xFF20) || (ch >= 0xFF3B && ch <= 0xFF40) || (ch >= 0xFF5B && ch <= 0xFF65) ||
		ch == 0x2013 || ch == 0x2014 || ch == 0x2026 || ch == 0x2E3A || ch == 0x2E3B || ch == 0x00B7 || ch == 0x2010 || ch == 0x2015
}

// wrapRow wraps every cell of a row into its column; table is the row's index in the table, or -1
// for the header, for the place of a refusal.
func (lay *tableLayout) wrapRow(texts []string, bold bool, table int) ([]cell, error) {
	row := make([]cell, len(texts))
	for j, text := range texts {
		c, err := lay.wrap(text, lay.columns[j].right-lay.columns[j].left-2*tableInsetX, bold)
		if err != nil {
			return nil, placed(err, lay.at(j, table))
		}
		row[j] = c
	}
	return row, nil
}

// wrap breaks a cell's text into lines no wider than width, at the places the export's
// line-break model knows a line may break, cutting runs wider than the line. Spaces at a line
// break are dropped, as a line break drops them, and trailing spaces are not drawn; a value's
// own line breaks and leading spaces are kept.
func (lay *tableLayout) wrap(text string, width float64, bold bool) (cell, error) {
	var c cell
	for _, paragraph := range strings.Split(text, "\n") {
		line, lw := "", 0.0
		var flushErr error
		flush := func() {
			line = strings.TrimRightFunc(line, unicode.IsSpace)
			if w, err := lay.m.width(line, lay.size, bold); err == nil {
				lw = w
			} else {
				flushErr = err
			}
			c.lines = append(c.lines, line)
			c.widths = append(c.widths, lw)
			line, lw = "", 0
		}
		err := runs(paragraph, func(run string) error {
			w, err := lay.m.width(run, lay.size, bold)
			if err != nil {
				return err
			}
			if w > width {
				if line != "" {
					flush()
				}
				pieces, err := lay.m.cut(run, lay.size, bold, width)
				if err != nil {
					return err
				}
				for _, piece := range pieces[:len(pieces)-1] {
					line = piece
					if lw, err = lay.m.width(piece, lay.size, bold); err != nil {
						return err
					}
					flush()
				}
				line = pieces[len(pieces)-1]
				lw, err = lay.m.width(line, lay.size, bold)
				return err
			}
			if line != "" && lw+w > width {
				flush()
			}
			line += run
			lw += w
			return nil
		})
		if err != nil {
			return cell{}, err
		}
		flush()
		if flushErr != nil {
			return cell{}, flushErr
		}
	}
	return c, nil
}

// column accumulates exact decimal strings: integers scaled to the longest fraction seen (or
// the column's declared scale), so no digit is ever rounded.
type column struct {
	scale int // the column's declared scale, if any
	frac  int // the longest fraction seen; sum is scaled by 10^frac
	sum   big.Int
}

func (c *column) add(v string) error {
	if !exactNumber.MatchString(v) {
		return r.ErrIntegrity
	}
	_, f, _ := strings.Cut(v, ".")
	if len(f) > c.frac {
		c.sum.Mul(&c.sum, pow10(len(f)-c.frac))
		c.frac = len(f)
	}
	c.sum.Add(&c.sum, scaled(v, c.frac))
	return nil
}

func pow10(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }

// scaled is v as an integer with frac fraction digits.
func scaled(v string, frac int) *big.Int {
	whole, f, _ := strings.Cut(v, ".")
	f += strings.Repeat("0", frac-len(f))
	n, _ := new(big.Int).SetString(whole+f, 10)
	return n
}

// String is the sum with the column's fraction digits.
func (c *column) String() string {
	frac := max(c.frac, c.scale)
	digits := new(big.Int).Abs(&c.sum)
	if frac > c.frac {
		digits.Mul(digits, pow10(frac-c.frac))
	}
	s := digits.String()
	if frac > 0 {
		for len(s) <= frac {
			s = "0" + s
		}
		s = s[:len(s)-frac] + "." + s[len(s)-frac:]
	}
	if c.sum.Sign() < 0 {
		s = "-" + s
	}
	return s
}

// equals compares the sum with another exact decimal string numerically.
func (c *column) equals(v string) bool {
	if !exactNumber.MatchString(v) {
		return false
	}
	_, f, _ := strings.Cut(v, ".")
	frac := max(c.frac, c.scale, len(f))
	return scaled(c.String(), frac).Cmp(scaled(v, frac)) == 0
}
