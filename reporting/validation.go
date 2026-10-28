package reporting

import (
	"bytes"
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
	if err != nil || t == nil {
		return nil, ErrInvalid
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
func checkShape(v any, t reflect.Type) error {
	if t.Kind() == reflect.Pointer {
		return checkShape(v, t.Elem())
	}
	switch t.Kind() {
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
func Normalize(q QuerySpec) QuerySpec {
	q.Companies = slices.Clone(q.Companies)
	sort.Strings(q.Companies)
	if q.Filters == nil {
		q.Filters = []Filter{}
	}
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
func QueryHash(q QuerySpec) string { q = Normalize(q); q.Page = PageSpec{}; return Digest(q) }
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
	if q.Dataset != d.Ref || !reflect.DeepEqual(q.Metrics, d.Metrics) || !reflect.DeepEqual(q.Dimensions, d.Dimensions) || !reflect.DeepEqual(q.CurrencyPolicy, d.CurrencyPolicy) || q.Timezone != d.Timezone || len(q.Filters) != 0 || len(q.Sort) != 0 {
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
	return nil
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

func validCell(v string, c Column) bool {
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
func validateRows(rows [][]string, cols []Column) error {
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
func validateTotals(t map[string]string, q QuerySpec, cols []Column) error {
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
