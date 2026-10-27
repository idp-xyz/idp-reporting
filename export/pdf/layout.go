package pdf

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	r "idp.local/reporting/reporting"
)

// Page geometry and type sizes in points. The template uses the same values from data.json,
// so the widths computed here are the widths Typst lays out.
const (
	a4Short   = 595.28
	a4Long    = 841.89
	marginX   = 34.02 // 12 mm
	insetX    = 4.0
	bodySize  = 8.0
	metaSize  = 7.5
	footSize  = 6.5
	titleSize = 13.0
	// footPage is room kept on the right of the footer for the page number.
	footPage = 80.0
	// minColumn is the narrowest column: two body ideographs and the cell insets.
	minColumn = 2*bodySize + 2*insetX
	// maxToken caps how wide one unbreakable run of text may make its column before it is
	// broken; maxNumber does the same for a number, which is only broken when it must be.
	maxToken  = 96.0
	maxNumber = 200.0
	// slack absorbs kerning and the extra space Typst puts between ideographs and Latin text.
	slack = 1.03
)

// MaxColumns is the most columns a PDF export lays out on a landscape page; wider snapshots are
// refused as unsupported and remain available as a workbook.
const MaxColumns = 32 // floor((a4Long - 2*marginX) / minColumn), checked by a test

// labels are all printed words of the template. They are data, so they are checked against the
// fonts like the snapshot values.
var labels = map[string]string{
	"page_before":  "第 ",
	"page_between": " 页 / 共 ",
	"page_after":   " 页",
	"total":        "合计",
	"empty":        "无数据行",
}

var dataClasses = map[string]string{"contract-simulator": "合同模拟器 / 合成数据（非真实业务数据）"}

// document is data.json. Every printed string in it was checked against the fonts: the values
// by measurer.segment, the labels by measurer.width.
type document struct {
	Title     string            `json:"title"` // PDF metadata
	Fonts     []string          `json:"fonts"`
	Landscape bool              `json:"landscape"`
	Heading   any               `json:"heading"`
	Meta      any               `json:"meta"`
	Footer    any               `json:"footer"`
	Labels    map[string]string `json:"labels"`
	Widths    []float64         `json:"widths"`
	Align     []string          `json:"align"`
	Header    []any             `json:"header"`
	Rows      [][]any           `json:"rows"`
	TotalRow  []any             `json:"total_row"`
	TotalList []any             `json:"total_list"`
	Empty     bool              `json:"empty"`
}

// pieces is a run too wide for its box, cut into pieces that each fit one line. Typst may
// break between pieces, and nowhere else inside the run.
type pieces struct {
	Pieces []string `json:"pieces"`
}

// build turns a snapshot into the template's data. It refuses characters the fonts do not
// cover and snapshots too wide for a page, and never computes or rounds a value.
func build(s r.Snapshot, m *measurer, created time.Time) (*document, error) {
	cols := s.Result.Columns
	if len(cols) == 0 || len(cols) != len(s.Query.Dimensions)+len(s.Query.Metrics) {
		return nil, r.ErrIntegrity
	}
	if len(cols) > MaxColumns {
		return nil, fmt.Errorf("pdf: %d columns do not fit a page: %w", len(cols), r.ErrUnsupported)
	}
	dims := len(s.Query.Dimensions)
	numeric := func(j int) bool { return cols[j].Type == "decimal" || cols[j].Type == "integer" }
	value := func(j int, v string) (string, error) {
		if j < dims || !numeric(j) {
			return printable(v), nil
		}
		if !exactNumber.MatchString(v) {
			return "", r.ErrIntegrity
		}
		return groupDigits(v), nil
	}

	header := make([]string, len(cols))
	for j, c := range cols {
		header[j] = printable(c.Label)
		switch {
		case c.Currency != "" && c.Unit != "":
			header[j] += "（" + printable(c.Currency) + " · " + printable(c.Unit) + "）"
		case c.Currency != "":
			header[j] += "（" + printable(c.Currency) + "）"
		case c.Unit != "":
			header[j] += "（" + printable(c.Unit) + "）"
		}
	}
	rows := make([][]string, len(s.Result.Rows))
	for i, row := range s.Result.Rows {
		if len(row) != len(cols) {
			return nil, r.ErrIntegrity
		}
		rows[i] = make([]string, len(cols))
		for j, v := range row {
			var err error
			if rows[i][j], err = value(j, v); err != nil {
				return nil, err
			}
		}
	}
	var totals []string
	for i, metric := range s.Query.Metrics {
		v, ok := s.Result.Totals[metric.ID]
		if !ok || cols[dims+i].ID != metric.ID {
			return nil, r.ErrIntegrity
		}
		v, err := value(dims+i, v)
		if err != nil {
			return nil, err
		}
		totals = append(totals, v)
	}
	var totalRow []string
	if dims > 0 && len(totals) > 0 {
		totalRow = make([]string, len(cols))
		totalRow[0] = labels["total"]
		copy(totalRow[dims:], totals)
	}

	// Column widths: natural widths when they fit, else see fit. A column's least width is its
	// longest unbreakable run, capped, so that runs only break when they must.
	natural, least := make([]float64, len(cols)), make([]float64, len(cols))
	grow := func(j int, text string, bold bool) error {
		n, t, err := m.extent(text, bodySize, bold)
		if err != nil {
			return err
		}
		limit := maxToken
		if numeric(j) {
			limit = maxNumber
		}
		// The extra hundredth of a point survives fit rounding widths down to hundredths.
		natural[j] = math.Max(natural[j], n*slack+2*insetX+0.01)
		least[j] = math.Max(least[j], math.Min(t*slack, limit)+2*insetX+0.01)
		return nil
	}
	for j := range cols {
		if err := grow(j, header[j], true); err != nil {
			return nil, err
		}
		for _, row := range rows {
			if err := grow(j, row[j], false); err != nil {
				return nil, err
			}
		}
		if totalRow != nil {
			if err := grow(j, totalRow[j], true); err != nil {
				return nil, err
			}
		}
	}
	landscape := sum(clampAll(natural, minColumn)) > a4Short-2*marginX
	page := a4Short
	if landscape {
		page = a4Long
	}
	avail := page - 2*marginX
	widths, err := fit(natural, least, avail)
	if err != nil {
		return nil, err
	}

	d := &document{Title: printable(s.Query.Dataset.ID), Fonts: m.families, Landscape: landscape, Labels: labels, Widths: widths, Rows: make([][]any, len(rows)), Empty: len(rows) == 0}
	for k, v := range labels {
		if _, err := m.width(v+"0123456789", footSize, false); err != nil {
			return nil, fmt.Errorf("pdf: label %s: %w", k, err)
		}
	}
	class := s.Result.Evidence.DataClass
	if label, ok := dataClasses[class]; ok {
		class = label
	}
	dataset := s.Query.Dataset.ID + "@" + s.Query.Dataset.Version
	meta := fmt.Sprintf("%s · 截至 %s（%s）· 公司 %s · %d 行 · 生成于 %s UTC", dataset, s.Query.AsOf, s.Query.Timezone, strings.Join(s.Query.Companies, "、"), len(rows), created.UTC().Format("2006-01-02 15:04:05"))
	footer := fmt.Sprintf("%s · 快照 %s · %s · 截至 %s · 来源水位 %s", class, s.ID, dataset, s.Query.AsOf, s.Prepared.Watermark)
	if d.Heading, err = m.segment(printable(s.Query.Dataset.ID), titleSize, true, avail); err != nil {
		return nil, err
	}
	if d.Meta, err = m.segment(printable(meta), metaSize, false, avail); err != nil {
		return nil, err
	}
	if d.Footer, err = m.segment(printable(footer), footSize, false, avail-footPage); err != nil {
		return nil, err
	}
	content := func(j int) float64 { return widths[j] - 2*insetX }
	for j := range cols {
		align := "left"
		if numeric(j) {
			align = "right"
		}
		d.Align = append(d.Align, align)
		h, err := m.segment(header[j], bodySize, true, content(j))
		if err != nil {
			return nil, err
		}
		d.Header = append(d.Header, h)
	}
	for i, row := range rows {
		d.Rows[i] = make([]any, len(row))
		for j, v := range row {
			if d.Rows[i][j], err = m.segment(v, bodySize, false, content(j)); err != nil {
				return nil, err
			}
		}
	}
	for j, v := range totalRow {
		c, err := m.segment(v, bodySize, true, content(j))
		if err != nil {
			return nil, err
		}
		d.TotalRow = append(d.TotalRow, c)
	}
	if dims == 0 {
		for i, v := range totals {
			c, err := m.segment(header[dims+i]+"："+v, bodySize, true, avail)
			if err != nil {
				return nil, err
			}
			d.TotalList = append(d.TotalList, c)
		}
	}
	return d, nil
}

// fit returns column widths that sum to at most avail.
func fit(natural, least []float64, avail float64) ([]float64, error) {
	n := float64(len(natural))
	if n*minColumn > avail {
		return nil, fmt.Errorf("pdf: %d columns do not fit a page: %w", len(natural), r.ErrUnsupported)
	}
	// least <= natural for every column, so base <= nat.
	nat, base := clampAll(natural, minColumn), clampAll(least, minColumn)
	out := make([]float64, len(natural))
	switch {
	case sum(nat) <= avail:
		copy(out, nat)
	case sum(base) <= avail:
		// Fill to a common level: each column gets its natural width up to the level and at
		// least its least width, so narrow columns keep their natural width and the widest
		// share what is left. widths(level) grows with the level; find the level that fills.
		widths := func(level float64) []float64 {
			w := make([]float64, len(nat))
			for j := range nat {
				w[j] = math.Min(nat[j], math.Max(base[j], level))
			}
			return w
		}
		lo, hi := 0.0, slices.Max(nat)
		for range 60 {
			if mid := (lo + hi) / 2; sum(widths(mid)) <= avail {
				lo = mid
			} else {
				hi = mid
			}
		}
		copy(out, widths(lo))
	default:
		// Even the least widths do not fit: every column gets minColumn and the rest in
		// proportion to its least width; long runs then break anywhere.
		spare, extra := avail-n*minColumn, sum(base)-n*minColumn
		for j := range base {
			out[j] = minColumn + spare*(base[j]-minColumn)/extra
		}
	}
	for j := range out {
		out[j] = math.Floor(out[j]*100) / 100
	}
	return out, nil
}

func clampAll(v []float64, lo float64) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = math.Max(x, lo)
	}
	return out
}

func sum(v []float64) float64 {
	t := 0.0
	for _, x := range v {
		t += x
	}
	return t
}

// printable maps the line breaks and tabs a snapshot value may hold to what Typst prints: a CR
// or CRLF is a line break and a tab is a space. Every other character is kept as it is.
func printable(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.NewReplacer("\r", "\n", "\t", " ").Replace(s)
}

// exactNumber is the form of the integer and decimal values a published snapshot holds.
var exactNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// groupDigits inserts thousands separators into the integer part of a validated exact decimal
// string. It changes no digit.
func groupDigits(v string) string {
	sign, rest := "", v
	if strings.HasPrefix(rest, "-") {
		sign, rest = "-", rest[1:]
	}
	whole, frac, dot := strings.Cut(rest, ".")
	var b strings.Builder
	for i, ch := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	if dot {
		return sign + b.String() + "." + frac
	}
	return sign + b.String()
}

// Where a line may break. Typst follows the Unicode line breaking rules; this model knows only
// places where those rules always allow a break: between two ideographs that may start a line,
// and after spaces before a letter or digit that may start a line, unless the spaces follow
// opening punctuation. Typst may break at more places, never fewer, so text between two of
// these places (a run) is the only text that could overflow its box, and segment cuts a run
// that is wider than its box. Punctuation thus joins the run it touches: a line may not start
// with a closing mark or end with an opening one.

// ideograph reports a Han character that a line may start with (not 々 or 〻).
func ideograph(ch rune) bool {
	return unicode.Is(unicode.Han, ch) && ch != 0x3005 && ch != 0x303B
}

// startsWord reports a letter or digit that a line may start with: not a small kana, a
// prolonged sound mark or an iteration mark.
func startsWord(ch rune) bool {
	return (unicode.IsLetter(ch) || unicode.IsDigit(ch)) && !strings.ContainsRune(noStart, ch) && (ch < 0x31F0 || ch > 0x31FF)
}

const noStart = "々〻ゝゞヽヾーぁぃぅぇぉっゃゅょゎゕゖァィゥェォッャュョヮヵヶ"

// runs splits one line at the places where a line may certainly break.
func runs(line string, each func(run string) error) error {
	start := 0
	var prev, solid rune // the previous character and the previous one that is not a space
	for i, ch := range line {
		if i > 0 && ((ideograph(prev) && ideograph(ch)) || (prev == ' ' && solid != 0 && startsWord(ch) && !unicode.In(solid, unicode.Ps, unicode.Pi))) {
			if err := each(line[start:i]); err != nil {
				return err
			}
			start = i
		}
		prev = ch
		if ch != ' ' {
			solid = ch
		}
	}
	if start < len(line) {
		return each(line[start:])
	}
	return nil
}

// extent returns the widest line and the widest unbreakable run of s in points.
func (m *measurer) extent(s string, size float64, bold bool) (line, token float64, err error) {
	for _, l := range strings.Split(s, "\n") {
		w, err := m.width(l, size, bold)
		if err != nil {
			return 0, 0, err
		}
		line = math.Max(line, w)
		err = runs(l, func(text string) error {
			t, err := m.width(text, size, bold)
			token = math.Max(token, t)
			return err
		})
		if err != nil {
			return 0, 0, err
		}
	}
	return line, token, nil
}

// segment checks every character of s and returns it for a box of the given width: the string
// itself, or a list of strings and {"pieces": [...]} for runs wider than the box.
func (m *measurer) segment(s string, size float64, bold bool, width float64) (any, error) {
	var parts []any
	var plain strings.Builder
	split := false
	for i, l := range strings.Split(s, "\n") {
		if i > 0 {
			plain.WriteByte('\n')
		}
		err := runs(l, func(text string) error {
			w, err := m.width(text, size, bold)
			if err != nil {
				return err
			}
			if w*slack > width {
				if plain.Len() > 0 {
					parts = append(parts, plain.String())
					plain.Reset()
				}
				cut, err := m.cut(text, size, bold, width)
				if err != nil {
					return err
				}
				parts = append(parts, pieces{cut})
				split = true
				return nil
			}
			plain.WriteString(text)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if !split {
		return plain.String(), nil
	}
	if plain.Len() > 0 {
		parts = append(parts, plain.String())
	}
	return parts, nil
}

// cut splits a run into pieces no wider than width, never before a character that joins the
// one before it (a combining mark, a joiner or a variation selector).
func (m *measurer) cut(text string, size float64, bold bool, width float64) ([]string, error) {
	var out []string
	start, w := 0, 0.0
	var prev rune
	for i, ch := range text {
		e, err := m.em(ch, bold)
		if err != nil {
			return nil, err
		}
		adv := (e + gap(prev, ch)) * size * slack
		if i > start && w+adv > width && !joins(ch) {
			out = append(out, text[start:i])
			start, w, adv = i, 0, e*size*slack
		}
		w += adv
		prev = ch
	}
	return append(out, text[start:]), nil
}

func joins(ch rune) bool {
	return unicode.Is(unicode.M, ch) || ch == 0x200D || (ch >= 0xFE00 && ch <= 0xFE0F) || (ch >= 0xE0100 && ch <= 0xE01EF)
}
