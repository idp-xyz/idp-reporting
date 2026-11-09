package server

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestOpenAPIMatchesTheHealthRoutes holds the part of docs/openapi.json outside /api/reporting/ to the routes healthHandlers binds on the
// API listener (transport/httpapi/openapi_test.go holds the rest). The candidates are the patterns with a method that healthHandlers passes
// to Handle or HandleFunc, read from http.go; each is asked of a mux the handlers are mounted on, and is bound when a request for it
// reaches exactly it. The metrics listener (GET /metrics) is another, loopback-only listener and is not described.
func TestOpenAPIMatchesTheHealthRoutes(t *testing.T) {
	raw, err := os.ReadFile("../docs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var documented []string
	for path, item := range doc.Paths {
		if strings.HasPrefix(path, "/api/reporting/") {
			continue
		}
		for method := range item {
			documented = append(documented, strings.ToUpper(method)+" "+path)
		}
	}
	slices.Sort(documented)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "http.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var candidates []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "healthHandlers" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("healthHandlers binds a pattern that is not a literal")
				return true
			}
			if pattern, err := strconv.Unquote(lit.Value); err == nil && strings.Contains(pattern, " ") {
				candidates = append(candidates, pattern)
			}
			return true
		})
	}
	top := http.NewServeMux()
	(&Server{}).healthHandlers(top)
	var bound []string
	for _, pattern := range candidates {
		method, path, _ := strings.Cut(pattern, " ")
		if _, reached := top.Handler(httptest.NewRequest(method, path, nil)); reached == pattern {
			bound = append(bound, pattern)
		}
	}
	slices.Sort(bound)
	if len(bound) == 0 || !slices.Equal(bound, documented) {
		t.Fatalf("healthHandlers binds %v; docs/openapi.json documents %v outside /api/reporting/", bound, documented)
	}
}
