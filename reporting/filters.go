package reporting

import (
	"encoding/json"
	"math/big"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Published filters and sorts (S20-A, docs/ADR_0023_PUBLISHED_FILTERS_AND_SORTS.md). The catalogue declares, per dataset version, which
// columns a query may narrow by and order by and with which operators (Dataset.Filters, Dataset.Sorts); a query's filters and sort are
// validated against that declaration and nothing else is accepted (ErrUnsupported, 422). The platform never evaluates a filter or a sort:
// it has no rows to do it on. The source applies them in its own trusted transaction, binding the values as parameters, and its totals
// are the narrowed scope's.

// The operators a declaration may publish, the closed set of ADR 0023.
const (
	OpEq       = "eq"
	OpNe       = "ne"
	OpLt       = "lt"
	OpLe       = "le"
	OpGt       = "gt"
	OpGe       = "ge"
	OpIn       = "in"
	OpBetween  = "between"
	OpPrefix   = "prefix"
	OpContains = "contains"
)

// FilterOperators is the closed set of operators, in the order of the ADR. `prefix` and `contains` fit `string` columns only; `lt le gt ge
// between` fit `date`, `integer`, `decimal` and `string`; a `boolean` column takes `eq` and `ne` only; `in` fits every type but `boolean`.
var FilterOperators = []string{OpEq, OpNe, OpLt, OpLe, OpGt, OpGe, OpIn, OpBetween, OpPrefix, OpContains}

// SortDirections is the closed set of sort directions.
var SortDirections = []string{"asc", "desc"}

// The bounds of a declaration and of a query's filters and sort (ADR 0023 decisions 1 and 2).
const (
	MaxDeclaredFilters  = 16  // filter specs of a dataset version
	MaxDeclaredSorts    = 8   // sort specs of a dataset version
	MaxFilterValues     = 64  // closed values of one filter spec
	MaxQueryFilters     = 16  // filters of one query
	MaxQuerySortKeys    = 3   // sort keys of one query
	MaxInValues         = 32  // values of one `in` filter
	MaxFilterValueChars = 128 // characters of one filter value (a string value, a closed value; integers and decimals are bounded the same)
	// MaxFilterValueBytes bounds the text of a filter's Value before it is decoded: the 32 values of an `in`, each of 128 characters as
	// JSON escapes them at most (six bytes a character), with their quotes and commas.
	MaxFilterValueBytes = MaxInValues*(MaxFilterValueChars*6+3) + 2
)

// OperatorFits says whether an operator may be declared for a column of the type (ADR 0023 decision 1).
func OperatorFits(op, columnType string) bool {
	switch op {
	case OpEq, OpNe:
		return columnType == "boolean" || OperatorFits(OpIn, columnType)
	case OpLt, OpLe, OpGt, OpGe, OpBetween, OpIn:
		return columnType == "date" || columnType == "integer" || columnType == "decimal" || columnType == "string"
	case OpPrefix, OpContains:
		return columnType == "string"
	}
	return false
}

// validDeclaration is the dataset's own rule for its filters and sorts: at most 16 and 8 of them, each on a column of the dataset and a
// column at most once in each list, operators a non-empty set of the closed set that fit the column's type, directions a non-empty set of
// asc and desc, closed values at most 64, distinct and each well formed for the column. A declaration that breaks it is a catalogue the
// platform cannot run, like a malformed column (ErrIntegrity in ValidateQuery).
func validDeclaration(d Dataset) bool {
	if len(d.Filters) > MaxDeclaredFilters || len(d.Sorts) > MaxDeclaredSorts {
		return false
	}
	columns := columnsByID(d)
	seen := map[string]bool{}
	for _, f := range d.Filters {
		c, ok := columns[f.Field]
		if !ok || seen[f.Field] || len(f.Operators) == 0 || len(f.Values) > MaxFilterValues {
			return false
		}
		seen[f.Field] = true
		ops := map[string]bool{}
		for _, op := range f.Operators {
			if ops[op] || !OperatorFits(op, c.Type) {
				return false
			}
			ops[op] = true
		}
		values := map[string]bool{}
		for _, v := range f.Values {
			if values[v] || !ValidFilterValue(v, c) {
				return false
			}
			values[v] = true
		}
	}
	seen = map[string]bool{}
	for _, s := range d.Sorts {
		if _, ok := columns[s.Field]; !ok || seen[s.Field] || len(s.Directions) == 0 {
			return false
		}
		seen[s.Field] = true
		directions := map[string]bool{}
		for _, dir := range s.Directions {
			if directions[dir] || !slices.Contains(SortDirections, dir) {
				return false
			}
			directions[dir] = true
		}
	}
	return true
}

func columnsByID(d Dataset) map[string]Column {
	out := map[string]Column{}
	for _, c := range d.Columns {
		out[c.ID] = c
	}
	return out
}

// validateNarrowing holds a query's filters and sort to the dataset's declaration (ADR 0023 decision 2): at most 16 filters and 3 sort
// keys; every filter on a declared field with a declared operator, its values well formed for the column (ValidFilterValue; an `in` or
// `between` carries them as FilterValues reads them), from the closed list when the field has one and the operator is `eq`, `ne` or `in`,
// a `between`'s two in ascending order; a required field filtered exactly once; every sort key on a declared field in a declared
// direction, no field twice. Anything else is ErrUnsupported: a combination the catalogue does not publish. The declaration itself has
// been checked (validDeclaration) before this is reached.
func validateNarrowing(q QuerySpec, d Dataset) error {
	if len(q.Filters) > MaxQueryFilters || len(q.Sort) > MaxQuerySortKeys {
		return ErrUnsupported
	}
	columns := columnsByID(d)
	specs := map[string]FilterSpec{}
	for _, f := range d.Filters {
		specs[f.Field] = f
	}
	count := map[string]int{}
	for _, f := range q.Filters {
		spec, ok := specs[f.Field]
		if !ok || !slices.Contains(spec.Operators, f.Operator) {
			return ErrUnsupported
		}
		count[f.Field]++
		values, e := FilterValues(f)
		if e != nil {
			return e
		}
		c := columns[f.Field]
		closed := len(spec.Values) > 0 && (f.Operator == OpEq || f.Operator == OpNe || f.Operator == OpIn)
		for _, v := range values {
			if !ValidFilterValue(v, c) || closed && !slices.Contains(spec.Values, v) {
				return ErrUnsupported
			}
		}
		if f.Operator == OpBetween && CompareValues(c, values[0], values[1]) > 0 {
			return ErrUnsupported
		}
	}
	for _, spec := range d.Filters {
		if spec.Required && count[spec.Field] != 1 {
			return ErrUnsupported
		}
	}
	sorts := map[string]SortSpec{}
	for _, s := range d.Sorts {
		sorts[s.Field] = s
	}
	seen := map[string]bool{}
	for _, s := range q.Sort {
		spec, ok := sorts[s.Field]
		if !ok || seen[s.Field] || !slices.Contains(spec.Directions, s.Direction) {
			return ErrUnsupported
		}
		seen[s.Field] = true
	}
	return nil
}

// FilterValues is the values a filter carries: the one value of a scalar operator, or, for `in` and `between`, the strings of the JSON
// array of strings in Value (`["a","b"]`, ADR 0023 decision 2): 1..32 of them for `in`, exactly 2 for `between`. Anything else in the
// Value of those two operators (not an array, not strings, a null, too many or too few) is ErrUnsupported. The adapters read a filter's
// values with it, so that they decode the one form the platform validated.
func FilterValues(f Filter) ([]string, error) {
	if f.Operator != OpIn && f.Operator != OpBetween {
		return []string{f.Value}, nil
	}
	if len(f.Value) > MaxFilterValueBytes || !json.Valid([]byte(f.Value)) {
		return nil, ErrUnsupported
	}
	var raw []json.RawMessage
	if json.Unmarshal([]byte(f.Value), &raw) != nil || raw == nil {
		return nil, ErrUnsupported
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		var s string
		if len(item) == 0 || item[0] != '"' || json.Unmarshal(item, &s) != nil {
			return nil, ErrUnsupported
		}
		out = append(out, s)
	}
	if f.Operator == OpIn && (len(out) < 1 || len(out) > MaxInValues) || f.Operator == OpBetween && len(out) != 2 {
		return nil, ErrUnsupported
	}
	return out, nil
}

// ValidFilterValue says whether one value is well formed for a column (ADR 0023 decision 2): a `date` as YYYY-MM-DD; an `integer` or a
// `decimal` as the exact string the column uses (the integer pattern, exactly the column's scale), at most 128 characters; a `boolean` as
// true or false; a `string` of 1..128 characters without a control character. The same rule holds a declaration's closed values.
func ValidFilterValue(v string, c Column) bool {
	switch c.Type {
	case "string":
		if !utf8.ValidString(v) {
			return false
		}
		n := 0
		for _, ch := range v {
			n++
			if unicode.IsControl(ch) || ch == 0xFFFE || ch == 0xFFFF {
				return false
			}
		}
		return n >= 1 && n <= MaxFilterValueChars
	case "integer", "decimal", "date", "boolean":
		return validCell(v, c)
	}
	return false
}

// CompareValues orders two well-formed values of a column: an `integer` or a `decimal` by its exact value (never through a float); a
// `date`, a `string` and a `boolean` by their text (YYYY-MM-DD orders as its text does, and false before true). The core checks a
// `between`'s order with it; an adapter that applies filters and sorts in memory (the contract simulator) compares with it, so that the
// two agree. A number that is not of the column's form is compared as text.
func CompareValues(c Column, a, b string) int {
	if c.Type == "integer" || c.Type == "decimal" {
		// The column's values have the same number of digits after the point (validCell), so the texts without the point compare as integers.
		x, okX := new(big.Int).SetString(strings.ReplaceAll(a, ".", ""), 10)
		y, okY := new(big.Int).SetString(strings.ReplaceAll(b, ".", ""), 10)
		if okX && okY {
			return x.Cmp(y)
		}
	}
	return strings.Compare(a, b)
}
