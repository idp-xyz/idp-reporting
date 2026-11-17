package reporting

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DecodeStrict bounds the document, rejects duplicate keys, null, unknown or
// wrongly-cased field names, multiple roots and excessive nesting.
//
// A null is refused wherever it stands, with one exception (ADR 0028 decision 1, S26-A): where the value it is decoded into is a
// Cell, which is a JSON string or null. That is the one rule, held by the one type: a cell of a row, a total, a value of a group's
// key. Whether a cell may be without a value there is not the decoder's question: the validation of the rows, the totals and the key
// answers it, and until a column can declare that it is nullable (S26-B) it refuses every one.
func DecodeStrict(in io.Reader, out any) error {
	b, err := io.ReadAll(io.LimitReader(in, MaxResultBytes+1))
	if err != nil || len(b) > MaxResultBytes || !utf8.Valid(b) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, err := readJSON(d, 0)
	if err != nil {
		return ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return ErrInvalid
	}
	t := reflect.TypeOf(out)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct || checkShape(v, t.Elem()) != nil {
		return ErrInvalid
	}
	b, err = json.Marshal(v)
	if err != nil {
		return ErrInvalid
	}
	if err = json.Unmarshal(b, out); err != nil {
		return ErrInvalid
	}
	return nil
}
func readJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return nil, ErrInvalid
	}
	if t == nil {
		// A null: kept as itself, and refused by checkShape everywhere but where a Cell is read.
		return jsonNull{}, nil
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, e
				}
				key, ok := k.(string)
				if !ok {
					return nil, ErrInvalid
				}
				if _, seen := m[key]; seen {
					return nil, ErrInvalid
				}
				v, e := readJSON(d, depth+1)
				if e != nil {
					return nil, e
				}
				m[key] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, ErrInvalid
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, e := readJSON(d, depth+1)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, ErrInvalid
			}
			return a, nil
		}
		return nil, ErrInvalid
	}
	return t, nil
}

// jsonNull is a null of the document, as readJSON keeps it: it is written back as null for the decoding that follows the check.
type jsonNull struct{}

func (jsonNull) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

var cellType = reflect.TypeOf(Cell{})

// holdsNull says whether a value of the document holds a null anywhere.
func holdsNull(v any) bool {
	switch v := v.(type) {
	case jsonNull:
		return true
	case map[string]any:
		for _, value := range v {
			if holdsNull(value) {
				return true
			}
		}
	case []any:
		for _, value := range v {
			if holdsNull(value) {
				return true
			}
		}
	}
	return false
}

// checkShape holds a value of the document to the type it is decoded into: no field the type does not have, and no null but where
// the type is a Cell. A value whose type has no shape to walk (a text, a number, an interface) holds no null either.
func checkShape(v any, t reflect.Type) error {
	if t == cellType {
		switch v.(type) {
		case string, jsonNull:
			return nil
		}
		return ErrInvalid
	}
	if _, null := v.(jsonNull); null {
		return ErrInvalid
	}
	if t.Kind() == reflect.Pointer {
		return checkShape(v, t.Elem())
	}
	switch t.Kind() {
	case reflect.Map:
		// The values of a map were never walked (an unknown field inside one is ignored, as before); what is held is that they hold
		// no null, except that the values of a map of cells (a result's totals) are cells.
		if t.Elem() != cellType {
			if holdsNull(v) {
				return ErrInvalid
			}
			return nil
		}
		m, ok := v.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		for _, value := range m {
			if checkShape(value, cellType) != nil {
				return ErrInvalid
			}
		}
	default:
		if holdsNull(v) {
			return ErrInvalid
		}
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = f.Type
			}
		}
		for k, value := range m {
			ft, ok := fields[k]
			if !ok || checkShape(value, ft) != nil {
				return ErrInvalid
			}
		}
	case reflect.Slice:
		a, ok := v.([]any)
		if !ok {
			return ErrInvalid
		}
		for _, value := range a {
			if checkShape(value, t.Elem()) != nil {
				return ErrInvalid
			}
		}
	}
	return nil
}

// Normalize is the canonical form of a query, the one that is hashed and stored: the companies sorted, the filters sorted by field,
// operator and value (ADR 0023: a conjunction has no order), the sort keys in their order (order is meaning), empty lists never nil.
func Normalize(q QuerySpec) QuerySpec {
	q.Companies = slices.Clone(q.Companies)
	sort.Strings(q.Companies)
	q.Filters = slices.Clone(q.Filters)
	slices.SortStableFunc(q.Filters, func(a, b Filter) int {
		return cmp.Or(strings.Compare(a.Field, b.Field), strings.Compare(a.Operator, b.Operator), strings.Compare(a.Value, b.Value))
	})
	if q.Filters == nil {
		q.Filters = []Filter{}
	}
	q.Sort = slices.Clone(q.Sort)
	if q.Sort == nil {
		q.Sort = []Sort{}
	}
	return q
}
func Digest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// QueryHash is the digest of the normalized query without its page: the companies, the dataset, the shape, the date, and (ADR 0023) the
// filters and the sort, so that a frozen snapshot's hash binds the narrowed scope as it binds the companies.
func QueryHash(q QuerySpec) string { q = Normalize(q); q.Page = PageSpec{}; return Digest(q) }

// ValidateQuery holds a query to a dataset: the query well formed (ErrInvalid); of the dataset's shape, and its filters and sort within
// what the dataset declares (ErrUnsupported, a combination the catalogue does not publish); the dataset itself one the platform can run,
// its columns and its declaration of filters and sorts well formed (ErrIntegrity).
func ValidateQuery(q QuerySpec, d Dataset) error {
	if q.SchemaVersion != "1.0" || q.Integration == "" || len(q.Integration) > 128 || len(q.Companies) == 0 || len(q.Companies) > 100 || q.Page.Limit < 1 || q.Page.Limit > 100 {
		return ErrInvalid
	}
	if _, e := time.Parse("2006-01-02", q.AsOf); e != nil {
		return ErrInvalid
	}
	if _, e := time.LoadLocation(q.Timezone); e != nil {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range q.Companies {
		if c == "" || len(c) > 128 || seen[c] {
			return ErrInvalid
		}
		seen[c] = true
	}
	if q.Dataset != d.Ref || !reflect.DeepEqual(q.Metrics, d.Metrics) || !reflect.DeepEqual(q.Dimensions, d.Dimensions) || !reflect.DeepEqual(q.CurrencyPolicy, d.CurrencyPolicy) || q.Timezone != d.Timezone {
		return ErrUnsupported
	}
	if len(d.Columns) != len(q.Metrics)+len(q.Dimensions) || len(d.Columns) == 0 || len(d.Columns) > 64 {
		return ErrIntegrity
	}
	seen = map[string]bool{}
	for i, c := range d.Columns {
		id := ""
		if i < len(q.Dimensions) {
			id = q.Dimensions[i]
		} else {
			id = q.Metrics[i-len(q.Dimensions)].ID
		}
		if c.ID != id || seen[c.ID] || c.Label == "" || !validColumn(c) {
			return ErrIntegrity
		}
		seen[c.ID] = true
	}
	if !validDeclaration(d) {
		return ErrIntegrity
	}
	return validateNarrowing(q, d)
}
func validColumn(c Column) bool {
	switch c.Type {
	case "string", "date", "boolean", "integer":
		return c.Scale == 0
	case "decimal":
		return c.Scale >= 0 && c.Scale <= 18
	}
	return false
}

var integerPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// validCell holds a cell to its column: it has a value, and the value is a text of the column's type (validText). A cell without a
// value is not valid in any column: no column can declare that it may be absent yet (ADR 0028 decision 1; S26-B adds the declaration).
func validCell(cell Cell, c Column) bool {
	v, ok := cell.Value()
	return ok && validText(v, c)
}

// validText holds a text to the column's type: what a cell that has a value holds, and what a filter's value is.
func validText(v string, c Column) bool {
	if !utf8.ValidString(v) || len(v) > 4096 {
		return false
	}
	for _, ch := range v {
		if (ch < 32 && ch != 9 && ch != 10 && ch != 13) || ch == 0xFFFE || ch == 0xFFFF {
			return false
		}
	}
	switch c.Type {
	case "string":
		return true
	case "integer":
		return len(v) <= 128 && integerPattern.MatchString(v)
	case "decimal":
		parts := strings.Split(v, ".")
		if c.Scale == 0 {
			return len(v) <= 128 && integerPattern.MatchString(v)
		}
		return len(v) <= 128 && len(parts) == 2 && len(parts[1]) == c.Scale && integerPattern.MatchString(parts[0]) && allDigits(parts[1])
	case "date":
		_, err := time.Parse("2006-01-02", v)
		return err == nil
	case "boolean":
		return v == "true" || v == "false"
	}
	return false
}
func allDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return s != ""
}
func validateRows(rows [][]Cell, cols []Column) error {
	for _, row := range rows {
		if len(row) != len(cols) {
			return ErrIntegrity
		}
		for i, v := range row {
			if !validCell(v, cols[i]) {
				return ErrIntegrity
			}
		}
	}
	return nil
}
func validateTotals(t map[string]Cell, q QuerySpec, cols []Column) error {
	if len(t) != len(q.Metrics) {
		return ErrIntegrity
	}
	for i, m := range q.Metrics {
		v, ok := t[m.ID]
		if !ok || !validCell(v, cols[len(q.Dimensions)+i]) {
			return ErrIntegrity
		}
	}
	return nil
}

// ValidateSourcePage holds one page a remote source answered to the dataset it was read from: every row of the dataset's width, every cell
// of its column's type, and a total for each metric and nothing else (ErrIntegrity otherwise). An adapter that reaches its product over a
// network (adapters/httpsource, S19-A) calls it before it passes a page on, with the dataset of the product's own catalog; the service
// applies the same checks again to the columns the job froze. The dataset must be one ValidateQuery accepts.
func ValidateSourcePage(rows [][]Cell, totals map[string]Cell, d Dataset) error {
	if len(d.Columns) != len(d.Dimensions)+len(d.Metrics) {
		return ErrIntegrity
	}
	if e := validateRows(rows, d.Columns); e != nil {
		return e
	}
	return validateTotals(totals, QuerySpec{Metrics: d.Metrics, Dimensions: d.Dimensions}, d.Columns)
}

// ValidateCompanyList holds a company directory's answer to ADR 0015 (ErrIntegrity otherwise): at most MaxCompanies entries, each id and
// name 1..MaxCompanyBytes bytes of UTF-8 text without control characters and not blank (plainText, the rule of every text a person
// reads), and no id twice. The service applies it to every answer of an in-process adapter before it is passed on; an adapter that reaches
// its product over a network (adapters/httpsource, S19-B) applies it to the product's answer first, with the same rule. A nil list is
// the empty answer: nothing may be chosen.
func ValidateCompanyList(l CompanyList) error {
	if len(l.Companies) > MaxCompanies {
		return ErrIntegrity
	}
	seen := make(map[string]bool, len(l.Companies))
	for _, c := range l.Companies {
		if !plainText(c.ID, MaxCompanyBytes) || !plainText(c.Name, MaxCompanyBytes) || seen[c.ID] {
			return ErrIntegrity
		}
		seen[c.ID] = true
	}
	return nil
}

// plainText: 1..max bytes of valid UTF-8 a person reads on one line, not blank, without control characters (C0, DEL) or the two
// noncharacters U+FFFE and U+FFFF; a schedule's name, a company's id and name.
func plainText(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) || strings.TrimSpace(s) == "" {
		return false
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f || c == 0xFFFE || c == 0xFFFF {
			return false
		}
	}
	return true
}
func validKey(key string) bool {
	return len(key) > 0 && len(key) <= 128 && !strings.ContainsAny(key, "\r\n\x00")
}

// validID accepts the canonical form of a platform id only: 32 lowercase hexadecimal characters,
// which is what NewID makes. An uppercase spelling decodes to the same bytes but is another
// string, and the issue stamp's width guarantee (docs/S03H_SCOPE.md) assumes the lowercase form.
func validID(id string) bool {
	b, e := hex.DecodeString(id)
	return e == nil && len(b) == 16 && hex.EncodeToString(b) == id
}
func offset(s string) (int, error) {
	v, e := strconv.Atoi(s)
	if e != nil || v < 0 {
		return 0, ErrInvalid
	}
	return v, nil
}
