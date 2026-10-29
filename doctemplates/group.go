package doctemplates

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	r "idp.local/reporting/reporting"
)

// RowGroup is one document of a 2.0 template: the rows of the frozen snapshot that have the same
// exact values of the template's group fields (ADR 0004, decision 2).
type RowGroup struct {
	Key  []string // the exact frozen strings of the group fields, in the order of the template
	Rows []int    // the snapshot row indices, in the snapshot's order
}

// groupable is what Groups, GroupOf and BindGroup share: a valid 2.0 document bound to the
// dataset the snapshot is of. A 1.x document is bound by row.
func groupable(d Document, s r.Snapshot) error {
	if e := Validate(d); e != nil {
		return e
	}
	if d.Schema != SchemaBanded {
		return r.ErrInvalid
	}
	if d.Binding.Integration != s.Owner.Integration || d.Binding.Dataset != s.Query.Dataset {
		return r.ErrDenied
	}
	return nil
}

func columnIndex(s r.Snapshot) map[string]int {
	out := map[string]int{}
	for i, c := range s.Result.Columns {
		out[c.ID] = i
	}
	return out
}

// groupColumns returns the snapshot columns of the group fields: each must be a column of the
// snapshot (else it is denied, as any field the snapshot lacks) and a dimension, never a metric.
func groupColumns(d Document, s r.Snapshot) ([]int, error) {
	if d.Group == nil {
		return nil, nil
	}
	cols, dims := columnIndex(s), map[string]bool{}
	for _, f := range s.Query.Dimensions {
		dims[f] = true
	}
	out := make([]int, len(d.Group.Fields))
	for i, f := range d.Group.Fields {
		j, ok := cols[f]
		if !ok {
			return nil, r.ErrDenied
		}
		if !dims[f] {
			return nil, fmt.Errorf("doctemplates: a group field must be a dimension: %w", r.ErrUnsupported)
		}
		out[i] = j
	}
	return out, nil
}

// groupKey is the key of a row: the length of each value before it, so that no two different
// tuples of values make the same key.
func groupKey(row []string, cols []int) (string, []string, error) {
	var b strings.Builder
	parts := make([]string, len(cols))
	for i, j := range cols {
		if j >= len(row) {
			return "", nil, r.ErrIntegrity
		}
		parts[i] = row[j]
		b.WriteString(strconv.Itoa(len(row[j])))
		b.WriteByte(':')
		b.WriteString(row[j])
	}
	return b.String(), parts, nil
}

// Groups partitions the frozen rows of the snapshot by the exact strings of the group fields
// (the empty string is a key like any other), ordered by their first row; the rows of a group keep
// the snapshot's order. Without a group the whole snapshot is one group, with every row, or none
// when the snapshot has none. It refuses a field the snapshot lacks (denied) and a metric as a group
// field (unsupported).
func Groups(d Document, s r.Snapshot) ([]RowGroup, error) {
	if e := groupable(d, s); e != nil {
		return nil, e
	}
	cols, e := groupColumns(d, s)
	if e != nil {
		return nil, e
	}
	rows := s.Result.Rows
	if d.Group == nil {
		all := make([]int, len(rows))
		for i := range rows {
			all[i] = i
		}
		return []RowGroup{{Rows: all}}, nil
	}
	var out []*RowGroup
	byKey := map[string]*RowGroup{}
	for i, row := range rows {
		key, parts, e := groupKey(row, cols)
		if e != nil {
			return nil, e
		}
		g := byKey[key]
		if g == nil {
			g = &RowGroup{Key: parts}
			byKey[key] = g
			out = append(out, g)
		}
		g.Rows = append(g.Rows, i)
	}
	groups := make([]RowGroup, len(out))
	for i, g := range out {
		groups[i] = *g
	}
	return groups, nil
}

// GroupOf returns the group that contains the snapshot row; a row the snapshot lacks is invalid.
func GroupOf(d Document, s r.Snapshot, row int) (RowGroup, error) {
	groups, e := Groups(d, s)
	if e != nil {
		return RowGroup{}, e
	}
	if row < 0 || row >= len(s.Result.Rows) {
		return RowGroup{}, r.ErrInvalid
	}
	for _, g := range groups {
		for _, i := range g.Rows {
			if i == row {
				return g, nil
			}
		}
	}
	return RowGroup{}, r.ErrIntegrity
}

// BindGroup gives the values of the text elements of a 2.0 document, by element id, and the table's
// data, for one group of an already authorized frozen snapshot, never caller values: a literal as it
// is, a page text with its placeholders still in it (the layout fills them once the pages are known),
// a field from the rows of the group, a group sum as the exact decimal sum of the rows (bound exactly;
// the layout prints it with grouped digits, as the table prints its totals, see GroupDigits). The group
// must be one of the snapshot's, whole. A field that is not a group field must hold the same value
// in every row, else the document is refused with INCONSISTENT_GROUP: picking one row's value would
// be a silent decision. A sum of a value that is not a decimal is an integrity failure.
func BindGroup(d Document, s r.Snapshot, g RowGroup) (map[string]string, *TableData, error) {
	if e := groupable(d, s); e != nil {
		return nil, nil, e
	}
	if e := wholeGroup(d, s, g); e != nil {
		return nil, nil, e
	}
	cols, metrics := columnIndex(s), map[string]bool{}
	for _, m := range s.Query.Metrics {
		metrics[m.ID] = true
	}
	groupPos := map[string]int{}
	if d.Group != nil {
		for i, f := range d.Group.Fields {
			groupPos[f] = i
		}
	}
	values := map[string]string{}
	for _, bi := range d.Items() {
		it := bi.Item
		if it.Type != TypeText {
			continue
		}
		text := it.Text
		switch it.Source {
		case SourceField:
			j, ok := cols[it.Field]
			if !ok {
				return nil, nil, r.ErrDenied
			}
			if i, isGroup := groupPos[it.Field]; isGroup {
				text = g.Key[i]
				break
			}
			for n, i := range g.Rows {
				row := s.Result.Rows[i]
				if j >= len(row) {
					return nil, nil, r.ErrIntegrity
				}
				if n == 0 {
					text = row[j]
				} else if row[j] != text {
					return nil, nil, Refuse(r.ErrUnsupported, Detail{Reason: ReasonInconsistentGroup, Band: bi.Band, Element: it.ID, Column: it.Field}.At(i))
				}
			}
			if len(g.Rows) == 0 {
				text = ""
			}
		case SourceGroupSum:
			j, ok := cols[it.Field]
			if !ok {
				return nil, nil, r.ErrDenied
			}
			// Only a metric is summed, as only a metric has a total in 1.x; a metric that is no number is no sum.
			if !metrics[it.Field] {
				return nil, nil, r.ErrDenied
			}
			if c := s.Result.Columns[j]; c.Type != "decimal" && c.Type != "integer" {
				return nil, nil, fmt.Errorf("doctemplates: a group sum needs a numeric metric: %w", r.ErrUnsupported)
			}
			sum := newDecimalSum(s.Result.Columns[j].Scale)
			for _, i := range g.Rows {
				row := s.Result.Rows[i]
				if j >= len(row) {
					return nil, nil, r.ErrIntegrity
				}
				if e := sum.add(row[j]); e != nil {
					return nil, nil, e
				}
			}
			text = sum.String()
		}
		// A value the box cannot print as it is: one line takes no control and at most 256
		// characters, a wrapped value up to 1024 and its line breaks.
		limit := MaxTextRunes
		if it.Wrap {
			limit = MaxWrapTextRunes
		}
		if !printableBound(text, it.Wrap) {
			reason := ReasonNotCovered
			if utf8.ValidString(text) && utf8.RuneCountInString(text) > limit {
				reason = ReasonDoesNotFit
			}
			return nil, nil, Refuse(r.ErrUnsupported, Detail{Reason: reason, Band: bi.Band, Element: it.ID})
		}
		values[it.ID] = text
	}
	table, e := bindGroupTable(*d.TableItem(), s, g.Rows, metrics)
	if e != nil {
		return nil, nil, e
	}
	return values, table, nil
}

// printableBound reports whether a bound value can be printed in a text element: valid UTF-8,
// within the length limit, without controls. A wrapped value may hold line breaks (a CR, a CRLF or an
// LF) and tabs, which the layout turns into line breaks and spaces, as it does in table cells.
func printableBound(text string, wrap bool) bool {
	limit := MaxTextRunes
	if wrap {
		limit = MaxWrapTextRunes
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > limit {
		return false
	}
	for _, c := range text {
		if c < 32 || c == 0x7F {
			if wrap && (c == '\n' || c == '\r' || c == '\t') {
				continue
			}
			return false
		}
	}
	return true
}

// wholeGroup checks that g is a group of the snapshot as Groups makes it: rows in range and in
// order, each with the group's key, and every row of the snapshot with that key in it.
func wholeGroup(d Document, s r.Snapshot, g RowGroup) error {
	cols, e := groupColumns(d, s)
	if e != nil {
		return e
	}
	rows := s.Result.Rows
	if len(g.Key) != len(cols) {
		return r.ErrInvalid
	}
	prev := -1
	for _, i := range g.Rows {
		if i <= prev || i >= len(rows) {
			return r.ErrInvalid
		}
		prev = i
		for k, j := range cols {
			if j >= len(rows[i]) {
				return r.ErrIntegrity
			}
			if rows[i][j] != g.Key[k] {
				return r.ErrInvalid
			}
		}
	}
	count := 0
	for _, row := range rows {
		match := true
		for k, j := range cols {
			if j >= len(row) {
				return r.ErrIntegrity
			}
			if row[j] != g.Key[k] {
				match = false
				break
			}
		}
		if match {
			count++
		}
	}
	// Without a group the group is every row, none included; with one it has at least one.
	if count != len(g.Rows) || (d.Group != nil && count == 0) {
		return r.ErrInvalid
	}
	return nil
}

// bindGroupTable takes the table's columns and the group's rows from the snapshot: a field the
// snapshot lacks is a denial, a cell that is not valid UTF-8 an integrity failure. Cells wrap, so any
// text is accepted here. The snapshot's totals are not carried: a group's sums are the layout's.
func bindGroupTable(t Item, s r.Snapshot, rows []int, metrics map[string]bool) (*TableData, error) {
	index := columnIndex(s)
	out := &TableData{Totals: map[string]string{}, Index: append([]int{}, rows...)}
	picked := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		j, ok := index[c.Field]
		if !ok {
			return nil, r.ErrDenied
		}
		picked[i] = j
		out.Columns = append(out.Columns, s.Result.Columns[j])
		out.Metric = append(out.Metric, metrics[c.Field])
	}
	out.Rows = make([][]string, len(rows))
	for n, i := range rows {
		row := s.Result.Rows[i]
		out.Rows[n] = make([]string, len(picked))
		for k, j := range picked {
			if j >= len(row) || !utf8.ValidString(row[j]) {
				return nil, r.ErrIntegrity
			}
			out.Rows[n][k] = row[j]
		}
	}
	return out, nil
}

// exactNumber is the form of the integer and decimal values a published snapshot holds.
var exactNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// decimalSum accumulates exact decimal strings, as the table's totals do (export/pdf column): the sum
// is an integer scaled to the longest fraction seen or the column's declared scale, so no digit is ever
// rounded.
type decimalSum struct {
	scale int // the column's declared scale
	frac  int // the longest fraction seen; sum is scaled by 10^frac
	sum   big.Int
}

func newDecimalSum(scale int) *decimalSum { return &decimalSum{scale: scale} }

func (c *decimalSum) add(v string) error {
	if !exactNumber.MatchString(v) {
		return r.ErrIntegrity
	}
	_, f, _ := strings.Cut(v, ".")
	if len(f) > c.frac {
		c.sum.Mul(&c.sum, pow10(len(f)-c.frac))
		c.frac = len(f)
	}
	c.sum.Add(&c.sum, scaledInt(v, c.frac))
	return nil
}

func pow10(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }

// scaledInt is v as an integer with frac fraction digits.
func scaledInt(v string, frac int) *big.Int {
	whole, f, _ := strings.Cut(v, ".")
	f += strings.Repeat("0", frac-len(f))
	n, _ := new(big.Int).SetString(whole+f, 10)
	return n
}

// String is the sum with the column's fraction digits: the longest fraction seen or the scale.
func (c *decimalSum) String() string {
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

// GroupDigits inserts thousands separators into the integer part of an exact decimal string and changes
// no digit: how the table prints its amounts, carried sums and totals, and so how a group_sum text prints
// its sum (export/pdf groupDigits is the same rule). The value must be an exact decimal (exactNumber);
// anything else is returned as it is.
func GroupDigits(v string) string {
	if !exactNumber.MatchString(v) {
		return v
	}
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

// SumDecimals is the exact decimal sum of the values, printed with the longest fraction among them or
// the declared scale: the rule of the table's totals and of a group_sum element. A value that is not
// an exact decimal is an integrity failure. It sums nothing for presentation but the platform's own.
func SumDecimals(values []string, scale int) (string, error) {
	sum := newDecimalSum(scale)
	for _, v := range values {
		if e := sum.add(v); e != nil {
			return "", e
		}
	}
	return sum.String(), nil
}
