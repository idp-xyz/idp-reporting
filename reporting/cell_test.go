package reporting_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// The cell (ADR 0028 decision 1, docs/S26A_SCOPE.md). A value of a row, of a total or of a group's key is a type of its own, which is
// its text or no value. The tests of this file hold the slice to its one promise: a cell that has a value is, wherever it is encoded,
// digested or compared, the string it was, and a cell without a value gets in nowhere, because no column can declare that it may be
// absent yet.

// cellTexts is what a cell may hold, and what an encoder treats specially.
var cellTexts = []string{
	"", "a", "customer_a", "-1234567.89", "2026-11-13", "true",
	"中文 Latin 0123456789", "emoji 🙂", strings.Repeat("长", 1365),
	`a quote " and a backslash \ and a slash /`, "a tab\tand a newline\nand a return\r", "controls \x01\x08\x0c\x1f and DEL \x7f",
	// Each of the characters a JSON string escapes by itself, so that no other character of the text decides how it is written: a
	// backslash alone, a backslash before a letter that would make an escape of it (the text is the two characters, not a newline),
	// a quote alone; and the HTML characters both in a text that is otherwise written as it is and in one that is not.
	`a backslash \ alone`, `C:\new\table`, `a quote " alone`, "html <b>bold</b> & more", `html <b>"bold"</b> & more`,
	"the separators \u2028 and \u2029", "not UTF-8 \xff\xfe", "half a rune \xe4\xb8",
	"null", `"null"`, "[]", "{}", " leading and trailing ",
}

// encoded is v as an encoder set one way or the other writes it.
func encoded(t *testing.T, v any, escapeHTML, indent bool) string {
	t.Helper()
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(escapeHTML)
	if indent {
		e.SetIndent("", " ")
	}
	if err := e.Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestACellIsEncodedAsItsStringWas: a cell that has a value is written as the JSON string it was, byte for byte, alone, in a row, in
// rows, as a total and inside a result, whichever way the encoder is set (it escapes the three HTML characters or it does not, it
// indents or it does not); json.Marshal agrees with the encoder; and what was written is read back as the same texts.
func TestACellIsEncodedAsItsStringWas(t *testing.T) {
	type before struct {
		Rows   [][]string        `json:"rows"`
		Totals map[string]string `json:"totals"`
		Key    []string          `json:"key"`
	}
	type after struct {
		Rows   [][]r.Cell        `json:"rows"`
		Totals map[string]r.Cell `json:"totals"`
		Key    []r.Cell          `json:"key"`
	}
	old := before{Rows: [][]string{cellTexts, {}, {"one"}}, Totals: map[string]string{}, Key: cellTexts[:3]}
	for i, s := range cellTexts {
		old.Totals[string(rune('a'+i))] = s
	}
	now := after{Rows: r.CellRows(old.Rows), Totals: r.CellTotals(old.Totals), Key: r.Cells(old.Key...)}
	for _, escape := range []bool{true, false} {
		for _, indent := range []bool{false, true} {
			for _, s := range cellTexts {
				if a, b := encoded(t, r.Text(s), escape, indent), encoded(t, s, escape, indent); a != b {
					t.Errorf("escape %v, indent %v: the cell of %q is %s, the string was %s", escape, indent, s, a, b)
				}
			}
			if a, b := encoded(t, now, escape, indent), encoded(t, old, escape, indent); a != b {
				t.Errorf("escape %v, indent %v: the rows, the totals and the key are\n%s\nand were\n%s", escape, indent, a, b)
			}
		}
	}
	a, _ := json.Marshal(now)
	b, _ := json.Marshal(old)
	if !bytes.Equal(a, b) {
		t.Errorf("json.Marshal: %s, was %s", a, b)
	}
	// Read back: the texts the strings are read back as (a text that is not UTF-8 comes back with the replacement character, as it
	// did), and no cell without a value.
	var texts before
	var cells after
	if e := json.Unmarshal(a, &texts); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal(a, &cells); e != nil {
		t.Fatal(e)
	}
	for i, row := range cells.Rows {
		if !slices.Equal(r.Texts(row), texts.Rows[i]) {
			t.Errorf("row %d is read back as %q, the strings as %q", i, r.Texts(row), texts.Rows[i])
		}
		for _, c := range row {
			if c.IsNull() {
				t.Error("a cell that was written with a value is read back without one")
			}
		}
	}
	if len(cells.Rows) != 3 || len(cells.Rows[1]) != 0 || cells.Rows[1] == nil {
		t.Errorf("an empty row is read back as %v", cells.Rows[1])
	}
	for k, v := range texts.Totals {
		if cells.Totals[k] != r.Text(v) {
			t.Errorf("the total %s is read back as %v, the string as %q", k, cells.Totals[k], v)
		}
	}
}

// TestTheDigestsOfRowsAreWhatTheyWere: the digest of a row and of a list of rows is the digest of the strings, and the literal a
// product not written in Go computes (the text and its SHA-256 were written out by hand, not by this code).
func TestTheDigestsOfRowsAreWhatTheyWere(t *testing.T) {
	rows := [][]string{cellTexts, {"customer_a", "current", "10.00"}, {}}
	if a, b := r.RowsDigest(r.CellRows(rows)), r.Digest(rows); a != b {
		t.Errorf("the digest of the rows is %s, of the strings %s", a, b)
	}
	for _, row := range rows {
		if a, b := r.Digest(r.Cells(row...)), r.Digest(row); a != b {
			t.Errorf("the digest of the row %q is %s, of the strings %s", row, a, b)
		}
	}
	if a, b := r.RowsDigest(nil), r.Digest([][]string{}); a != b {
		t.Errorf("the digest of no rows is %s, of the empty list %s", a, b)
	}
	// [["customer_a","current","10.00"],["\u003cb\u003e \u0026","中","-0.24"]]
	const literal = "6ead4b308b7621562747e166d1eccf8300b3749c862e89edd6dce6b21f4ae493"
	if got := r.RowsDigest(r.CellRows([][]string{{"customer_a", "current", "10.00"}, {"<b> &", "中", "-0.24"}})); got != literal {
		t.Errorf("the digest of the two rows is %s, the literal is %s", got, literal)
	}
	// A cell without a value is part of a digest as null (ADR 0028): it is not the digest of the empty text. No such row is stored
	// yet; the encoding is fixed here so that S26-B changes no digest when it lets one in.
	holed := [][]r.Cell{{r.Text("a"), r.NoValue()}}
	if b, _ := json.Marshal(holed); string(b) != `[["a",null]]` {
		t.Errorf("a cell without a value is written as %s", b)
	}
	if r.RowsDigest(holed) == r.RowsDigest([][]r.Cell{{r.Text("a"), r.Text("")}}) {
		t.Error("the digest of a row with a cell without a value is the digest of the row with the empty text")
	}
}

// TestACellIsATextOrNoValue: what a cell answers about itself, what it is read from, and the conversions.
func TestACellIsATextOrNoValue(t *testing.T) {
	var zero r.Cell
	if zero != r.Text("") || zero.IsNull() {
		t.Error("the zero cell is not the empty text")
	}
	if v, ok := r.Text("x").Value(); v != "x" || !ok || r.Text("x").Text() != "x" || r.Text("x").IsNull() {
		t.Error("a cell with a value")
	}
	if v, e := r.Text("").Required(); v != "" || e != nil {
		t.Error("the empty text is a value:", e)
	}
	none := r.NoValue()
	if v, ok := none.Value(); v != "" || ok || !none.IsNull() || none.Text() != "" || none == r.Text("") {
		t.Error("a cell without a value")
	}
	if _, e := none.Required(); !errors.Is(e, r.ErrIntegrity) {
		t.Error("a cell without a value is required and the answer is", e)
	}
	// Formatted, a cell is what JSON writes for it, in a row and inside a result alike: never the bare text, so that a cell printed
	// where its text was meant is seen, and an empty text is told from no value.
	if got := fmt.Sprint(r.Text("a"), r.Text(""), none); got != `"a" "" null` {
		t.Errorf("three cells are formatted as %s", got)
	}
	if got := fmt.Sprintf("%v %s", [][]r.Cell{{r.Text(`a "b"`), none}}, r.Text("<中>")); got != `[["a \"b\"" null]] "<中>"` {
		t.Errorf("rows and a cell are formatted as %s", got)
	}
	if got := fmt.Sprintf("%+v", r.Result{Rows: [][]r.Cell{{none}}, Totals: map[string]r.Cell{"m": r.Text("1")}}); !strings.Contains(got, `Rows:[[null]] Totals:map[m:"1"]`) {
		t.Errorf("a result is formatted as %s", got)
	}
	// Read: a JSON string or null, and nothing else.
	for in, want := range map[string]r.Cell{`"x"`: r.Text("x"), `""`: r.Text(""), `"\u4e2d\"\\"`: r.Text(`中"\`), ` "x" `: r.Text("x"), `null`: r.NoValue(), ` null `: r.NoValue(), `"null"`: r.Text("null")} {
		var c r.Cell
		if e := json.Unmarshal([]byte(in), &c); e != nil || c != want {
			t.Errorf("%s is read as %+v %v", in, c, e)
		}
	}
	for _, in := range []string{`1`, `1.5`, `true`, `false`, `[]`, `["x"]`, `{}`, `{"text":"x"}`} {
		var c r.Cell
		if e := json.Unmarshal([]byte(in), &c); e == nil {
			t.Errorf("%s was read as a cell: %+v", in, c)
		}
		var row []r.Cell
		if e := json.Unmarshal([]byte(`[`+in+`]`), &row); e == nil {
			t.Errorf("[%s] was read as a row", in)
		}
	}
	// Read as the string was: whatever JSON text a string is read from, a cell is read from as the same text: an escape of each
	// kind by itself, a pair of surrogates and half of one, the HTML characters, and bytes that are not UTF-8, which both read as
	// the replacement character.
	for _, in := range []string{
		`"x"`, `""`, `"\u4e2d"`, `"a\\b"`, `"a\nb"`, `"C:\\new"`, `"\"q\""`, `"\u4e2d\"\\"`, `"\/"`, `"\ud83d\ude42"`, `"\ud83d"`, `"<b> & \u2028"`,
		"\"raw \xff\xfe bytes\"", "\"half a rune \xe4\xb8\"", "\"中文 🙂 \u2028\"", ` "x" `, "\t\r\n\"x\"\n",
	} {
		var s string
		var c r.Cell
		if es, ec := json.Unmarshal([]byte(in), &s), json.Unmarshal([]byte(in), &c); es != nil || ec != nil || c != r.Text(s) {
			t.Errorf("%q is read as the string %q (%v) and as the cell %+v (%v)", in, s, es, c, ec)
		}
	}
	// Called by itself with what is no JSON value, it refuses rather than reads a part of it: a string that is not closed, a quote
	// or a control character that is not escaped, an escape that is cut, two values, a word that is not null, and white space that
	// is not JSON's.
	for _, in := range []string{``, ` `, `"`, `"a`, `a"`, `"a"b"`, `"a\"`, `"a\`, `"a" "b"`, "\"a\nb\"", "\"a\x00b\"", `nul`, `nulll`, `Null`, "\u00a0null", "\u00a0\"x\"", `null null`} {
		var c r.Cell
		if e := c.UnmarshalJSON([]byte(in)); !errors.Is(e, r.ErrInvalid) {
			t.Errorf("%q was read as a cell: %+v %v", in, c, e)
		}
	}
	for in, want := range map[string]r.Cell{` null `: r.NoValue(), "\n\"x\"\t": r.Text("x"), `"a\tb"`: r.Text("a\tb")} {
		var c r.Cell
		if e := c.UnmarshalJSON([]byte(in)); e != nil || c != want {
			t.Errorf("%q is read by itself as %+v %v", in, c, e)
		}
	}
	// The conversions keep nil and empty apart, and every cell they make has a value.
	if r.CellRows(nil) != nil || r.CellTotals(nil) != nil || r.Cells() == nil || len(r.Cells()) != 0 {
		t.Error("nil and empty")
	}
	if rows := r.CellRows([][]string{{"a", ""}, {}}); len(rows) != 2 || rows[0][0] != r.Text("a") || rows[0][1] != r.Text("") || rows[1] == nil || len(rows[1]) != 0 {
		t.Errorf("%+v", rows)
	}
	if got := r.Texts([]r.Cell{r.Text("a"), r.NoValue(), r.Text("")}); !slices.Equal(got, []string{"a", "", ""}) {
		t.Errorf("%q", got)
	}
	if got, e := r.RequiredTexts(r.Cells("a", "")); e != nil || !slices.Equal(got, []string{"a", ""}) {
		t.Error(got, e)
	}
	if _, e := r.RequiredTexts([]r.Cell{r.Text("a"), r.NoValue()}); !errors.Is(e, r.ErrIntegrity) {
		t.Error(e)
	}
	if got, e := r.RequiredRows(nil); e != nil || got == nil || len(got) != 0 {
		t.Error("no rows are an empty list:", got, e)
	}
	if _, e := r.RequiredRows([][]r.Cell{r.Cells("a"), {r.NoValue()}}); !errors.Is(e, r.ErrIntegrity) {
		t.Error(e)
	}
	if got, e := r.RequiredTotals(nil); e != nil || got == nil || len(got) != 0 {
		t.Error("no totals are an empty map:", got, e)
	}
	if got, e := r.RequiredTotals(map[string]r.Cell{"m": r.Text("1")}); e != nil || got["m"] != "1" {
		t.Error(got, e)
	}
	if _, e := r.RequiredTotals(map[string]r.Cell{"m": r.NoValue()}); !errors.Is(e, r.ErrIntegrity) {
		t.Error(e)
	}
}

// TestTheStrictDecoderTakesANullOnlyWhereACellIs: the one exception to "no null" is the one type. A null is read where the value it
// is decoded into is a cell (a cell of a row, a value of a map of cells, a cell of a key, a cell by itself) and refused wherever else
// it stands: in place of a text, a number, a list, a map, an object, inside a list of texts or a map of texts, inside a value the
// shape does not describe, in place of the list of cells itself, and at the root. A pointer to a cell is not a cell: a null there
// would be no pointer, which is not a cell without a value, and it is refused. And a cell is a string or null: a number is not one.
func TestTheStrictDecoderTakesANullOnlyWhereACellIs(t *testing.T) {
	type inner struct {
		Name string   `json:"name"`
		Row  []r.Cell `json:"row"`
	}
	type doc struct {
		Name   string            `json:"name"`
		Count  int               `json:"count"`
		One    r.Cell            `json:"one"`
		Opt    *r.Cell           `json:"opt"`
		Row    []r.Cell          `json:"row"`
		Rows   [][]r.Cell        `json:"rows"`
		Totals map[string]r.Cell `json:"totals"`
		Texts  []string          `json:"texts"`
		Labels map[string]string `json:"labels"`
		Inner  *inner            `json:"inner"`
		Items  []inner           `json:"items"`
		Any    any               `json:"any"`
	}
	taken := map[string]func(doc) bool{
		`{"one":null}`: func(d doc) bool { return d.One.IsNull() },
		`{"one":"x"}`:  func(d doc) bool { return d.One == r.Text("x") },
		`{"opt":"x"}`:  func(d doc) bool { return d.Opt != nil && *d.Opt == r.Text("x") },
		`{"row":[null,"a",null]}`: func(d doc) bool {
			return len(d.Row) == 3 && d.Row[0].IsNull() && d.Row[1] == r.Text("a") && d.Row[2].IsNull()
		},
		`{"rows":[["a",null],[],[null]]}`:          func(d doc) bool { return len(d.Rows) == 3 && d.Rows[0][1].IsNull() && d.Rows[2][0].IsNull() },
		`{"totals":{"m":null,"n":"1"}}`:            func(d doc) bool { return d.Totals["m"].IsNull() && d.Totals["n"] == r.Text("1") },
		`{"inner":{"name":"x","row":[null]}}`:      func(d doc) bool { return d.Inner != nil && d.Inner.Row[0].IsNull() },
		`{"items":[{"row":[null]},{"row":["a"]}]}`: func(d doc) bool { return d.Items[0].Row[0].IsNull() && d.Items[1].Row[0] == r.Text("a") },
		`{"name":"null","texts":["null"]}`:         func(d doc) bool { return d.Name == "null" && d.Texts[0] == "null" },
	}
	for in, check := range taken {
		var d doc
		if e := r.DecodeStrict(strings.NewReader(in), &d); e != nil || !check(d) {
			t.Errorf("%s: %+v %v", in, d, e)
		}
	}
	for _, in := range []string{
		`null`,
		`{"name":null}`, `{"count":null}`, `{"texts":null}`, `{"labels":null}`, `{"inner":null}`, `{"items":null}`, `{"any":null}`,
		`{"row":null}`, `{"rows":null}`, `{"rows":[null]}`, `{"totals":null}`, `{"opt":null}`, `{"opt":1}`,
		`{"texts":["a",null]}`, `{"labels":{"a":null}}`, `{"inner":{"name":null}}`, `{"items":[null]}`, `{"items":[{"name":null}]}`,
		`{"any":[1,null]}`, `{"any":{"a":{"b":null}}}`,
		`{"one":1}`, `{"one":true}`, `{"one":["x"]}`, `{"one":{}}`, `{"row":[1]}`, `{"row":[["a"]]}`, `{"rows":[[1]]}`, `{"totals":{"m":1}}`, `{"totals":["a"]}`,
		`{"row":[null],"unknown":1}`, `{"row":[null],"row":[null]}`,
	} {
		var d doc
		if e := r.DecodeStrict(strings.NewReader(in), &d); !errors.Is(e, r.ErrInvalid) {
			t.Errorf("%s was decoded: %+v %v", in, d, e)
		}
	}
	// The types the platform decodes strictly today hold no cell, so a null in them is refused where it always was: a query.
	for _, in := range []string{`{"company_ids":[null]}`, `{"filters":[{"field":"a","operator":"eq","value":null}]}`, `{"dataset":{"id":null}}`} {
		var q r.QuerySpec
		if e := r.DecodeStrict(strings.NewReader(in), &q); !errors.Is(e, r.ErrInvalid) {
			t.Errorf("%s was decoded as a query: %v", in, e)
		}
	}
}

// holed is an adapter that answers the pages of another with something changed.
type holed struct {
	r.Adapter
	change func(*r.SourcePage)
}

func (h holed) ReadPage(ctx context.Context, sc r.SourceContext, p r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	page, e := h.Adapter.ReadPage(ctx, sc, p, cursor, limit)
	if e == nil {
		// The page is the simulator's own: change a copy.
		page.Rows = append([][]r.Cell{}, page.Rows...)
		for i := range page.Rows {
			page.Rows[i] = append([]r.Cell{}, page.Rows[i]...)
		}
		totals := map[string]r.Cell{}
		for k, v := range page.Totals {
			totals[k] = v
		}
		page.Totals = totals
		h.change(&page)
	}
	return page, e
}

// TestACellWithoutAValueGetsInNowhere: no column can declare that it may be absent, so a cell without a value is refused wherever a
// value enters the platform, as a value that is not of its column's type always was: in a source's page (a cell of any column, a
// total), by the service that reads it for a query and by the worker that freezes it; in what a job publishes; in a group's key.
func TestACellWithoutAValueGetsInNowhere(t *testing.T) {
	ctx := context.Background()
	d := contractsim.Financial()
	p := contractsim.DemoPrincipal()
	owner := contractsim.DemoOwner(p, contractsim.FinanceIntegration)
	d.Grant(owner, contractsim.DefaultGrant())
	sc := r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}
	catalog, e := d.DescribeCatalog(ctx, sc)
	if e != nil || len(catalog) == 0 {
		t.Fatal(e)
	}
	q := contractsim.FinancialQuery()
	var dataset r.Dataset
	for _, c := range catalog {
		if c.Ref == q.Dataset {
			dataset = c
		}
	}
	prepared, e := d.PrepareRead(ctx, sc, q, r.QueryHash(q))
	if e != nil {
		t.Fatal(e)
	}
	page, e := d.ReadPage(ctx, sc, prepared, "", 100)
	if e != nil || len(page.Rows) == 0 || r.ValidateSourcePage(page.Rows, page.Totals, dataset) != nil {
		t.Fatal(e, len(page.Rows))
	}
	// The page: a cell without a value in each column in turn, and each total.
	for j := range dataset.Columns {
		rows := append([][]r.Cell{}, page.Rows...)
		rows[0] = append([]r.Cell{}, rows[0]...)
		rows[0][j] = r.NoValue()
		if e := r.ValidateSourcePage(rows, page.Totals, dataset); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("a %s cell without a value in a page: %v", dataset.Columns[j].Type, e)
		}
	}
	for _, m := range dataset.Metrics {
		totals := map[string]r.Cell{}
		for k, v := range page.Totals {
			totals[k] = v
		}
		totals[m.ID] = r.NoValue()
		if e := r.ValidateSourcePage(page.Rows, totals, dataset); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("the total of %s without a value in a page: %v", m.ID, e)
		}
	}
	// The service: the query and the snapshot job of a source that answers one.
	for name, change := range map[string]func(*r.SourcePage){
		"a cell":  func(page *r.SourcePage) { page.Rows[len(page.Rows)-1][0] = r.NoValue() },
		"a total": func(page *r.SourcePage) { page.Totals[dataset.Metrics[0].ID] = r.NoValue() },
	} {
		repo := memory.New(time.Now)
		s, err := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: holed{d, change}}}, CursorKey: []byte("01234567890123456789012345678901")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Query(ctx, p, q); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("%s without a value, the query: %v", name, err)
		}
		job, err := s.SubmitSnapshot(ctx, p, q, "holed "+name)
		if err != nil {
			t.Fatal(err)
		}
		if ran, err := s.RunOne(ctx); !ran || !errors.Is(err, r.ErrIntegrity) {
			t.Fatal(ran, err)
		}
		if got, err := s.Job(ctx, p, job.ID); err != nil || got.State != "failed" || got.ErrorCode != r.ErrIntegrity.Error() || got.SnapshotID != "" {
			t.Errorf("%s without a value, the snapshot job: %+v %v", name, got, err)
		}
	}
	// What a job publishes: the store holds a snapshot to its job's columns, and a cell without a value is of no column.
	columns := dataset.Columns
	result := r.Result{Columns: columns, Rows: page.Rows, Totals: page.Totals, Completeness: "complete", Issues: []string{}, Evidence: r.Evidence{QueryHash: prepared.QueryHash, SourceRef: prepared.Ref, Watermark: prepared.Watermark, DataClass: r.DataClassSimulator}}
	job := r.Job{ID: r.NewID(), Owner: owner, Kind: "snapshot", State: "running", Query: q, Prepared: prepared, Columns: columns}
	snapshot := func(res r.Result) r.Publication {
		return r.Publication{Snapshot: &r.Snapshot{ID: r.NewID(), Owner: owner, Query: q, Prepared: prepared, Result: res, ExpiresAt: prepared.ExpiresAt, SHA256: r.Digest(res)}}
	}
	if e := r.ValidatePublication(job, snapshot(result)); e != nil {
		t.Fatal("the snapshot as the source answered it is not one the store takes:", e)
	}
	withCell := result
	withCell.Rows = append([][]r.Cell{}, result.Rows...)
	withCell.Rows[0] = append([]r.Cell{}, result.Rows[0]...)
	withCell.Rows[0][len(columns)-1] = r.NoValue()
	if e := r.ValidatePublication(job, snapshot(withCell)); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("a snapshot with a cell without a value is published: %v", e)
	}
	withTotal := result
	withTotal.Totals = map[string]r.Cell{dataset.Metrics[0].ID: r.NoValue()}
	if e := r.ValidatePublication(job, snapshot(withTotal)); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("a snapshot with a total without a value is published: %v", e)
	}
	// A group's key.
	snap := *snapshot(result).Snapshot
	field := q.Dimensions[0]
	key := []r.Cell{page.Rows[0][0]}
	var rows []int
	for i, row := range page.Rows {
		if row[0] == key[0] {
			rows = append(rows, i)
		}
	}
	g, e := r.NewIssueGroup(snap, []string{field}, key, rows)
	if e != nil || !r.ValidGroupShape(g) {
		t.Fatal("the group of the first row's key:", e)
	}
	none := g.Clone()
	none.Key = []r.Cell{r.NoValue()}
	if r.ValidGroupShape(none) {
		t.Error("a key that holds a cell without a value is well formed")
	}
	if e := r.VerifyGroup(snap, none); !errors.Is(e, r.ErrInvalid) {
		t.Errorf("a group whose key holds a cell without a value: %v", e)
	}
	if _, e := r.NewIssueGroup(snap, []string{field}, []r.Cell{r.NoValue()}, rows); !errors.Is(e, r.ErrInvalid) {
		t.Errorf("a new group whose key holds a cell without a value: %v", e)
	}
}
