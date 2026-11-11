package httpsource_test

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"idp.local/reporting/adapters/httpsource/protocol"
	r "idp.local/reporting/reporting"
)

// The Go type of each schema of docs/source-openapi.json.
var schemaTypes = map[string]reflect.Type{
	"Context": reflect.TypeFor[protocol.Context](), "Ref": reflect.TypeFor[r.Ref](), "Column": reflect.TypeFor[r.Column](), "Dataset": reflect.TypeFor[r.Dataset](),
	"Filter": reflect.TypeFor[r.Filter](), "Sort": reflect.TypeFor[r.Sort](), "PageSpec": reflect.TypeFor[r.PageSpec](), "QuerySpec": reflect.TypeFor[r.QuerySpec](),
	"Read": reflect.TypeFor[protocol.Read](), "CatalogRequest": reflect.TypeFor[protocol.CatalogRequest](), "CatalogResponse": reflect.TypeFor[protocol.CatalogResponse](),
	"Company": reflect.TypeFor[r.Company](), "CompaniesRequest": reflect.TypeFor[protocol.CompaniesRequest](), "CompaniesResponse": reflect.TypeFor[protocol.CompaniesResponse](),
	"PrepareRequest": reflect.TypeFor[protocol.PrepareRequest](), "PrepareResponse": reflect.TypeFor[protocol.PrepareResponse](),
	"PageRequest": reflect.TypeFor[protocol.PageRequest](), "PageResponse": reflect.TypeFor[protocol.PageResponse](),
	"AuthorizeRequest": reflect.TypeFor[protocol.AuthorizeRequest](), "AuthorizeResponse": reflect.TypeFor[protocol.AuthorizeResponse](),
	"Group": reflect.TypeFor[protocol.Group](), "Intent": reflect.TypeFor[protocol.Intent](), "PrepareIssueRequest": reflect.TypeFor[protocol.PrepareIssueRequest](),
	"Reservation": reflect.TypeFor[protocol.Reservation](), "PrepareIssueResponse": reflect.TypeFor[protocol.PrepareIssueResponse](),
	"Confirmation": reflect.TypeFor[protocol.Confirmation](), "ConfirmRequest": reflect.TypeFor[protocol.ConfirmRequest](), "Receipt": reflect.TypeFor[protocol.Receipt](),
	"ConfirmResponse": reflect.TypeFor[protocol.ConfirmResponse](), "VoidRequest": reflect.TypeFor[protocol.VoidRequest](), "VoidResponse": reflect.TypeFor[protocol.VoidResponse](),
	"Error": reflect.TypeFor[protocol.ErrorBody](),
}

type schema struct {
	Ref                  string            `json:"$ref"`
	Type                 string            `json:"type"`
	Properties           map[string]schema `json:"properties"`
	Required             []string          `json:"required"`
	Items                *schema           `json:"items"`
	AdditionalProperties any               `json:"additionalProperties"`
	Enum                 []string          `json:"enum"`
}

func nameOf(ref string) string { return strings.TrimPrefix(ref, "#/components/schemas/") }

// field checks one property's schema against the Go field's type.
func field(t *testing.T, where string, s schema, ft reflect.Type) {
	t.Helper()
	if ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	switch ft.Kind() {
	case reflect.String:
		if s.Type != "string" {
			t.Errorf("%s: %q, want string", where, s.Type)
		}
	case reflect.Int:
		if s.Type != "integer" {
			t.Errorf("%s: %q, want integer", where, s.Type)
		}
	case reflect.Bool:
		if s.Type != "boolean" {
			t.Errorf("%s: %q, want boolean", where, s.Type)
		}
	case reflect.Slice:
		if s.Type != "array" || s.Items == nil {
			t.Errorf("%s: %q, want array", where, s.Type)
			return
		}
		field(t, where+"[]", *s.Items, ft.Elem())
	case reflect.Map:
		if s.Type != "object" || !reflect.DeepEqual(s.AdditionalProperties, map[string]any{"type": "string"}) {
			t.Errorf("%s: want an object of strings", where)
		}
	case reflect.Struct:
		if schemaTypes[nameOf(s.Ref)] != ft {
			t.Errorf("%s: $ref %q, want the schema of %v", where, s.Ref, ft)
		}
	default:
		t.Errorf("%s: a Go type the test does not know: %v", where, ft)
	}
}

// TestTheOpenAPIDescriptionIsTheProtocol holds docs/source-openapi.json to the bodies of package protocol, which are what the adapter
// sends and accepts: the eight routes (companies since revision 1, S19-B), each POST, its body and answer, the statuses of its error
// codes, every schema's properties (the Go type's JSON fields, with their types) and required fields (those without omitempty), no
// additional property, and the closed set of error codes.
func TestTheOpenAPIDescriptionIsTheProtocol(t *testing.T) {
	b, err := os.ReadFile("../../docs/source-openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]struct {
			RequestBody struct {
				Content map[string]struct{ Schema schema } `json:"content"`
			} `json:"requestBody"`
			Responses map[string]struct {
				Content map[string]struct{ Schema schema } `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc.OpenAPI != "3.1.0" {
		t.Fatal(doc.OpenAPI, err)
	}
	routes := map[string][2]string{
		protocol.PathCatalog: {"CatalogRequest", "CatalogResponse"}, protocol.PathCompanies: {"CompaniesRequest", "CompaniesResponse"}, protocol.PathReads: {"PrepareRequest", "PrepareResponse"},
		protocol.PathPages: {"PageRequest", "PageResponse"}, protocol.PathAuthorize: {"AuthorizeRequest", "AuthorizeResponse"},
		protocol.PathPrepare: {"PrepareIssueRequest", "PrepareIssueResponse"}, protocol.PathConfirm: {"ConfirmRequest", "ConfirmResponse"},
		protocol.PathVoid: {"VoidRequest", "VoidResponse"},
	}
	if len(doc.Paths) != len(routes) {
		t.Fatalf("%d paths, want %d", len(doc.Paths), len(routes))
	}
	for path, bodies := range routes {
		ops, ok := doc.Paths[path]
		if !ok || len(ops) != 1 {
			t.Fatalf("%s: %v", path, ops)
		}
		op, ok := ops["post"]
		if !ok {
			t.Fatalf("%s is not POST", path)
		}
		if got := nameOf(op.RequestBody.Content["application/json"].Schema.Ref); got != bodies[0] {
			t.Errorf("%s: body %q, want %q", path, got, bodies[0])
		}
		if got := nameOf(op.Responses["200"].Content["application/json"].Schema.Ref); got != bodies[1] {
			t.Errorf("%s: answer %q, want %q", path, got, bodies[1])
		}
		issue := strings.Contains(path, "/issues/")
		want := []string{"200", "401"}
		for _, c := range protocol.Codes {
			if (issue || !c.IssueOnly) && !slices.Contains(want, itoa(c.Status)) {
				want = append(want, itoa(c.Status))
			}
		}
		var got []string
		for st, resp := range op.Responses {
			got = append(got, st)
			if st != "200" && st != "401" && nameOf(resp.Content["application/json"].Schema.Ref) != "Error" {
				t.Errorf("%s %s: not the error body", path, st)
			}
		}
		sort.Strings(got)
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: statuses %v, want %v", path, got, want)
		}
	}
	if len(doc.Components.Schemas) != len(schemaTypes) {
		t.Errorf("%d schemas, want %d", len(doc.Components.Schemas), len(schemaTypes))
	}
	for name, typ := range schemaTypes {
		s, ok := doc.Components.Schemas[name]
		if !ok {
			t.Errorf("no schema %s", name)
			continue
		}
		if s.Type != "object" || s.AdditionalProperties != false {
			t.Errorf("%s: not a closed object", name)
		}
		var props, required []string
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			props = append(props, tag[0])
			if !slices.Contains(tag[1:], "omitempty") {
				required = append(required, tag[0])
			}
			p, ok := s.Properties[tag[0]]
			if !ok {
				t.Errorf("%s has no property %s", name, tag[0])
				continue
			}
			field(t, name+"."+tag[0], p, f.Type)
		}
		if len(s.Properties) != len(props) {
			t.Errorf("%s: %d properties, want %v", name, len(s.Properties), props)
		}
		got := slices.Clone(s.Required)
		sort.Strings(got)
		sort.Strings(required)
		if !slices.Equal(got, required) {
			t.Errorf("%s: required %v, want %v", name, got, required)
		}
	}
	var codes []string
	for _, c := range protocol.Codes {
		codes = append(codes, c.Name)
	}
	enum := doc.Components.Schemas["Error"].Properties["code"].Enum
	sort.Strings(codes)
	sort.Strings(enum)
	if !slices.Equal(codes, enum) {
		t.Errorf("the error codes %v, want %v", enum, codes)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
