package pdf

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

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
}

// layOutTable adds the table's pages to p: the header on every page, rows never split, a
// brought-forward row from page 2 on, a carry-forward row on every page but the last, and the
// total row on the last page; the text boxes of the document on every page.
func layOutTable(p *page, d doctemplates.Document, profile doctemplates.PrintProfile, data *doctemplates.TableData, m *measurer) error {
	t := d.Table
	if data == nil || len(data.Columns) != len(t.Columns) {
		return r.ErrInvalid
	}
	size := float64(t.FontSize)
	lay := &tableLayout{m: m, size: size, full: profile.Mode == "full",
		left: (t.X + profile.OffsetX) * mm, top: (t.Y + profile.OffsetY) * mm}
	lay.right, lay.bottom = lay.left+t.Width*mm, lay.top+t.Height*mm
	for k, v := range tableLabels {
		if _, err := m.width(v+"0123456789", size, true); err != nil {
			return fmt.Errorf("pdf: label %s: %w", k, err)
		}
	}
	// Columns: edges, kind and header label, as the table export labels them.
	x := lay.left
	for i, c := range t.Columns {
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
		next := x + c.Width*mm
		lay.columns = append(lay.columns, tableColumn{left: x, right: next, metric: data.Metric[i] && (col.Type == "decimal" || col.Type == "integer"), label: label})
		x = next
	}
	// The header, every row and the exact running sums.
	var err error
	if lay.header, err = lay.wrapRow(headerTexts(lay), true); err != nil {
		return err
	}
	p.HeaderLines = lay.headerLines()
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
			return err
		}
	}
	lay.rows = make([][]cell, len(data.Rows))
	lay.heights = make([]float64, len(data.Rows))
	for i, row := range data.Rows {
		texts := make([]string, len(row))
		for j, v := range row {
			if lay.columns[j].metric {
				if !exactNumber.MatchString(v) {
					return r.ErrIntegrity
				}
				texts[j] = groupDigits(v)
				if w, err := lay.m.width(texts[j], size, false); err != nil {
					return err
				} else if w > lay.content(j) {
					return fmt.Errorf("pdf: an amount does not fit its column: %w", r.ErrUnsupported)
				}
				if sums[j] != nil {
					if err := sums[j].add(v); err != nil {
						return err
					}
				}
			} else {
				texts[j] = printable(v)
			}
		}
		if lay.rows[i], err = lay.wrapRow(texts, false); err != nil {
			return err
		}
		lay.heights[i] = lay.rowHeight(lay.rows[i])
	}
	// The sum of the rows must be the snapshot's own total: a durable snapshot is complete.
	for j, s := range sums {
		if s == nil {
			continue
		}
		if total, ok := data.Totals[data.Columns[j].ID]; ok && !s.equals(total) {
			return fmt.Errorf("pdf: the rows of %s do not add up to the snapshot's total: %w", data.Columns[j].ID, r.ErrIntegrity)
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
				return err
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
	return lay.paginate(p, running, zero)
}

// content is a column's content width: between its rules, less the insets.
func (lay *tableLayout) content(j int) float64 {
	return lay.columns[j].right - lay.columns[j].left - 2*tableInsetX
}

// fits refuses bold text wider than a column's content width.
func (lay *tableLayout) fits(text string, j int, what string) error {
	w, err := lay.m.width(text, lay.size, true)
	if err != nil {
		return err
	}
	if w > lay.content(j) {
		return fmt.Errorf("pdf: %s does not fit its column: %w", what, r.ErrUnsupported)
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
	usable := lay.bottom - lay.top - headerH
	if headerH+carryH > lay.bottom-lay.top {
		return fmt.Errorf("pdf: the table's header and total row are taller than its region: %w", r.ErrUnsupported)
	}
	base := *p
	pages := []pageData{}
	i, number := 0, 1
	for {
		avail := usable
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
			return fmt.Errorf("pdf: a table row is taller than the table's pages can hold: %w", r.ErrUnsupported)
		}
		if number > maxPages {
			return fmt.Errorf("pdf: the document needs more than %d pages: %w", maxPages, r.ErrUnsupported)
		}
		last := i+k == n
		pg := newPage(base.Pages[0].Texts)
		y := lay.top
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
			lay.drawRow(&pg, y, row, true, false)
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
			return nil, err
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
			if col.metric {
				x = col.right - tableInsetX - c.widths[k]
			}
			pg.Texts = append(pg.Texts, glyph{X: x, Baseline: y + tableInsetY + ascent*lay.size + float64(k)*lineStep*lay.size, Size: lay.size, Width: c.widths[k], Text: line, Bold: bold})
		}
	}
	return y + h
}

// wrapRow wraps every cell of a row into its column.
func (lay *tableLayout) wrapRow(texts []string, bold bool) ([]cell, error) {
	row := make([]cell, len(texts))
	for j, text := range texts {
		c, err := lay.wrap(text, lay.columns[j].right-lay.columns[j].left-2*tableInsetX, bold)
		if err != nil {
			return nil, err
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
			line = strings.TrimRight(line, " ")
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
