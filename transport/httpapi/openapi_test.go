package httpapi_test

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/dashboards"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/embedding"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/identity/signin"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// openAPIPath is the API description this test holds to the handlers.
const openAPIPath = "../../docs/openapi.json"

// apiPrefix is the part of the description this package binds; the rest (/healthz, /readyz) is the production server's and is held by
// server/openapi_test.go.
const apiPrefix = "/api/reporting/"

// openAPIMethods are the operation keys of an OpenAPI path item.
var openAPIMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// TestOpenAPIMatchesTheBoundRoutes holds docs/openapi.json to the routes the handlers of this package bind. The candidates are read from
// the package's source: every pattern given to bind, HandleFunc or Handle, with the constants and the parameters it is built from resolved
// through the calls that pass them (a pattern the reader cannot resolve fails the test, so that a new way of binding a route is not
// missed). Each candidate and each documented operation is then asked of the API composed with every mount, as the production server and
// the demo compose it, with an authenticator that records the pattern the request reached and refuses it. A pattern is bound when a
// request for it reaches exactly it. The test fails when a bound pattern is not documented and when a documented operation is not bound.
func TestOpenAPIMatchesTheBoundRoutes(t *testing.T) {
	documented := openAPIOperations(t, openAPIPath)
	candidates := sourcePatterns(t, ".")
	handler, reached := composedForProbing(t)
	probe := func(pattern string) bool {
		method, path, _ := strings.Cut(pattern, " ")
		*reached = ""
		req := httptest.NewRequest(method, probePath(path), nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)
		// An unauthenticated route (S15-A's sign-in) never reaches the authenticator; the mux that matched it wrote its pattern on the request.
		return *reached == pattern || (*reached == "" && req.Pattern == pattern)
	}
	bound := map[string]bool{}
	for _, pattern := range candidates {
		if probe(pattern) {
			bound[pattern] = true
		}
	}
	for pattern := range bound {
		if !documented[pattern] {
			t.Errorf("%s is bound but docs/openapi.json does not document it", pattern)
		}
	}
	api := 0
	for pattern := range documented {
		_, path, _ := strings.Cut(pattern, " ")
		if !strings.HasPrefix(path, apiPrefix) {
			continue
		}
		api++
		if !probe(pattern) {
			t.Errorf("docs/openapi.json documents %s, which no handler binds", pattern)
		} else if !bound[pattern] {
			t.Errorf("%s is bound but the source reader did not find it: a pattern built in a way it cannot follow", pattern)
		}
	}
	if len(bound) == 0 || api == 0 {
		t.Fatal("no route found", len(bound), api)
	}
	t.Logf("%d bound routes, %d documented API operations, %d source candidates", len(bound), api, len(candidates))
}

// probePath is a request path for a pattern's path: each wildcard is an identifier of the right shape.
func probePath(path string) string {
	return regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "0123456789abcdef0123456789abcdef")
}

// composedForProbing is the API with every mount and every optional part each mount has (a layout store, an image store, a renderer that
// can check), in the order the production server composes them. Every request is refused by its authenticator, which first records the
// pattern the request reached, in the string the second result points to; nothing is read or written.
func composedForProbing(t *testing.T) (http.Handler, *string) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	source := contractsim.Financial()
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: source}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	reached := new(string)
	auth := func(req *http.Request) (r.Principal, error) {
		*reached = req.Pattern
		return r.Principal{}, r.ErrDenied
	}
	dir := t.TempDir()
	layoutStore, e := layouts.Open(filepath.Join(dir, "layouts"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { layoutStore.Close() })
	boards, e := dashboards.Open(filepath.Join(dir, "boards"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { boards.Close() })
	assets, e := doctemplates.OpenAssets(filepath.Join(dir, "assets"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { assets.Close() })
	if assets.Images == nil {
		t.Fatal("the assets have no image store: the image routes would not be mounted")
	}
	templates, e := doctemplates.Open(filepath.Join(dir, "templates"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { templates.Close() })
	config, e := embedding.New(map[string][]string{contractsim.FinanceIntegration: {"https://app.brant.example"}}, embedding.Options{})
	if e != nil {
		t.Fatal(e)
	}
	renderer := headerRenderer{}
	h, e := httpapi.Handler(service, auth, layoutStore)
	if e != nil {
		t.Fatal(e)
	}
	for _, mount := range []func(http.Handler) (http.Handler, error){
		func(next http.Handler) (http.Handler, error) {
			return httpapi.WithDashboards(next, service, auth, boards)
		},
		func(next http.Handler) (http.Handler, error) {
			return httpapi.WithDocumentAssets(next, service, auth, assets)
		},
		func(next http.Handler) (http.Handler, error) {
			return httpapi.WithDocumentPreviews(next, service, auth, renderer, assets)
		},
		func(next http.Handler) (http.Handler, error) {
			return httpapi.WithDocumentTemplates(next, service, auth, templates, renderer, assets)
		},
		func(next http.Handler) (http.Handler, error) {
			return httpapi.WithEmbedding(next, service, auth, config)
		},
		func(next http.Handler) (http.Handler, error) {
			schedules, e := r.NewSchedules(service, r.ScheduleOptions{Store: memory.New(time.Now), Resolve: func(r.Identity) (r.Principal, bool) { return r.Principal{}, false }})
			if e != nil {
				return nil, e
			}
			return httpapi.WithSchedules(next, service, auth, schedules)
		},
		func(next http.Handler) (http.Handler, error) {
			return httpapi.WithAdministration(next, auth, func(context.Context, string) (httpapi.AdminConfiguration, error) {
				return httpapi.AdminConfiguration{}, nil
			})
		},
		func(next http.Handler) (http.Handler, error) {
			return httpapi.WithProfile(next, service, auth, "probe", config)
		},
		func(next http.Handler) (http.Handler, error) {
			return httpapi.WithSignIn(next, probeSignIn(t), auth)
		},
	} {
		if h, e = mount(h); e != nil {
			t.Fatal(e)
		}
	}
	return h, reached
}

// probeSignIn is a sign-in client that is never asked to exchange anything.
func probeSignIn(t *testing.T) *signin.Client {
	t.Helper()
	c, e := signin.New(signin.Config{AuthorizationEndpoint: "https://idp.example/authorize", TokenEndpoint: "https://idp.example/token", ClientID: "reporting-console", RedirectURI: "https://reporting.example/console/", Scope: "openid"})
	if e != nil {
		t.Fatal(e)
	}
	return c
}

// openAPIOperations reads an OpenAPI description and answers its operations as ServeMux patterns ("GET /path/{id}"). It fails the test
// when the file is not JSON, is not OpenAPI 3.1, has a $ref that does not resolve, or has an operation whose path parameters are not
// the wildcards of its path.
func openAPIOperations(t *testing.T, name string) map[string]bool {
	t.Helper()
	raw, e := os.ReadFile(name)
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]any
	if e = json.Unmarshal(raw, &doc); e != nil {
		t.Fatal(name, e)
	}
	if v, _ := doc["openapi"].(string); !strings.HasPrefix(v, "3.1.") {
		t.Fatalf("%s: openapi %q, want 3.1.x", name, v)
	}
	checkReferences(t, doc, doc)
	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatal(name, "has no paths")
	}
	wildcard := regexp.MustCompile(`\{([^}]+)\}`)
	out := map[string]bool{}
	for path, v := range paths {
		item, ok := v.(map[string]any)
		if !ok {
			t.Fatal(path, "is not a path item")
		}
		var want []string
		for _, m := range wildcard.FindAllStringSubmatch(path, -1) {
			want = append(want, m[1])
		}
		slices.Sort(want)
		for key, v := range item {
			if !slices.Contains(openAPIMethods, key) {
				t.Errorf("%s: %q is not an operation; this description keeps its parameters on the operations", path, key)
				continue
			}
			operation, _ := v.(map[string]any)
			if _, ok := operation["responses"].(map[string]any); !ok {
				t.Errorf("%s %s has no responses", key, path)
			}
			var got []string
			list, _ := operation["parameters"].([]any)
			for _, p := range list {
				p := resolve(t, doc, p)
				if p["in"] == "path" {
					got = append(got, p["name"].(string))
					if p["required"] != true {
						t.Errorf("%s %s: path parameter %v is not required", key, path, p["name"])
					}
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("%s %s: path parameters %v, want %v", key, path, got, want)
			}
			out[strings.ToUpper(key)+" "+path] = true
		}
	}
	return out
}

// checkReferences fails the test for each $ref under v that does not name an object of doc.
func checkReferences(t *testing.T, doc map[string]any, v any) {
	t.Helper()
	switch v := v.(type) {
	case map[string]any:
		if _, ok := v["$ref"]; ok {
			resolve(t, doc, v)
		}
		for _, child := range v {
			checkReferences(t, doc, child)
		}
	case []any:
		for _, child := range v {
			checkReferences(t, doc, child)
		}
	}
}

// resolve answers the object v is, following a local $ref.
func resolve(t *testing.T, doc map[string]any, v any) map[string]any {
	t.Helper()
	m, _ := v.(map[string]any)
	target, ok := m["$ref"].(string)
	if !ok {
		return m
	}
	if !strings.HasPrefix(target, "#/") {
		t.Errorf("$ref %q is not local", target)
		return nil
	}
	var at any = doc
	for _, part := range strings.Split(strings.TrimPrefix(target, "#/"), "/") {
		node, _ := at.(map[string]any)
		if at = node[part]; at == nil {
			t.Errorf("$ref %q does not resolve", target)
			return nil
		}
	}
	out, _ := at.(map[string]any)
	return out
}

// sourcePatterns reads the non-test Go files of dir and answers every ServeMux pattern with a method that they bind: the first argument of
// each call of HandleFunc or Handle, and of each local function that passes its first parameter on to them (as Handler's bind does). A
// pattern is a string expression of literals, constants and parameters joined by +; a parameter takes every value the calls of its
// function pass it. An argument the reader cannot evaluate, and a pattern without a method other than the fallback "/", fail the test.
func sourcePatterns(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	names, e := filepath.Glob(filepath.Join(dir, "*.go"))
	if e != nil {
		t.Fatal(e)
	}
	src := &source{t: t, consts: map[string]string{}, funcs: map[string]*ast.FuncDecl{}}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, e := parser.ParseFile(fset, name, nil, 0)
		if e != nil {
			t.Fatal(e)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					src.funcs[d.Name.Name] = d
				}
				src.decls = append(src.decls, d)
			case *ast.GenDecl:
				collectConsts(d, src.consts)
			}
		}
	}
	seen := map[string]bool{}
	for _, fn := range src.decls {
		for _, pattern := range src.patternsOf(fn) {
			if !strings.Contains(pattern, " ") {
				if pattern != "/" {
					t.Errorf("%s binds %q, a pattern without a method", fn.Name.Name, pattern)
				}
				continue
			}
			seen[pattern] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// source is the package as sourcePatterns reads it: its string constants, its functions by name and every function declaration.
type source struct {
	t      *testing.T
	consts map[string]string
	funcs  map[string]*ast.FuncDecl
	decls  []*ast.FuncDecl
}

// collectConsts adds the string constants of a declaration to into.
func collectConsts(d *ast.GenDecl, into map[string]string) {
	if d.Tok != token.CONST {
		return
	}
	for _, spec := range d.Specs {
		v := spec.(*ast.ValueSpec)
		for i, name := range v.Names {
			if i < len(v.Values) {
				if lit, ok := v.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, e := strconv.Unquote(lit.Value); e == nil {
						into[name.Name] = s
					}
				}
			}
		}
	}
}

// patternsOf answers the patterns fn binds.
func (s *source) patternsOf(fn *ast.FuncDecl) []string {
	if fn.Body == nil {
		return nil
	}
	local := map[string]string{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if d, ok := n.(*ast.GenDecl); ok {
			collectConsts(d, local)
		}
		return true
	})
	// A closure that passes its first parameter on as a pattern (bind) is a binder: its calls bind routes, its own call does not.
	binders := maps.Clone(muxMethods)
	forwarded := map[*ast.CallExpr]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lit, ok := assign.Rhs[0].(*ast.FuncLit)
		name, named := assign.Lhs[0].(*ast.Ident)
		if !ok || !named || len(lit.Type.Params.List) == 0 || len(lit.Type.Params.List[0].Names) == 0 {
			return true
		}
		first := lit.Type.Params.List[0].Names[0].Name
		ast.Inspect(lit.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && muxMethods[callName(call)] && len(call.Args) > 0 {
				if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == first {
					binders[name.Name] = true
					forwarded[call] = true
				}
			}
			return true
		})
		return true
	})
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || forwarded[call] || !binders[callName(call)] {
			return true
		}
		if len(call.Args) == 0 {
			s.t.Errorf("%s: a call of %s without a pattern", fn.Name.Name, callName(call))
			return true
		}
		values, ok := s.eval(fn, local, call.Args[0], 0)
		if !ok {
			s.t.Errorf("%s: the pattern of a call of %s cannot be read", fn.Name.Name, callName(call))
			return true
		}
		out = append(out, values...)
		return true
	})
	return out
}

// muxMethods are the methods of http.ServeMux that bind a pattern.
var muxMethods = map[string]bool{"HandleFunc": true, "Handle": true}

// callName is the name a call is of: the function, or the method without its receiver; "" for anything else.
func callName(call *ast.CallExpr) string {
	fun := call.Fun
	switch f := fun.(type) {
	case *ast.IndexExpr:
		fun = f.X
	case *ast.IndexListExpr:
		fun = f.X
	}
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// eval answers the values a string expression of fn can have.
func (s *source) eval(fn *ast.FuncDecl, local map[string]string, x ast.Expr, depth int) ([]string, bool) {
	if depth > 8 {
		return nil, false
	}
	switch x := x.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return nil, false
		}
		v, e := strconv.Unquote(x.Value)
		return []string{v}, e == nil
	case *ast.ParenExpr:
		return s.eval(fn, local, x.X, depth)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return nil, false
		}
		left, ok := s.eval(fn, local, x.X, depth)
		if !ok {
			return nil, false
		}
		right, ok := s.eval(fn, local, x.Y, depth)
		if !ok {
			return nil, false
		}
		var out []string
		for _, l := range left {
			for _, r := range right {
				out = append(out, l+r)
			}
		}
		return out, true
	case *ast.Ident:
		if v, ok := local[x.Name]; ok {
			return []string{v}, true
		}
		if v, ok := s.consts[x.Name]; ok {
			return []string{v}, true
		}
		return s.paramValues(fn, x.Name, depth+1)
	}
	return nil, false
}

// paramValues answers the values the calls of fn pass for its parameter name.
func (s *source) paramValues(fn *ast.FuncDecl, name string, depth int) ([]string, bool) {
	index, i := -1, 0
	for _, field := range fn.Type.Params.List {
		for _, n := range field.Names {
			if n.Name == name {
				index = i
			}
			i++
		}
	}
	if index < 0 || s.funcs[fn.Name.Name] != fn {
		return nil, false
	}
	var out []string
	for _, caller := range s.decls {
		if caller.Body == nil {
			continue
		}
		local := map[string]string{}
		ast.Inspect(caller.Body, func(n ast.Node) bool {
			if d, ok := n.(*ast.GenDecl); ok {
				collectConsts(d, local)
			}
			return true
		})
		failed := false
		ast.Inspect(caller.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || callName(call) != fn.Name.Name || len(call.Args) <= index {
				return true
			}
			if _, method := call.Fun.(*ast.SelectorExpr); method {
				return true
			}
			values, ok := s.eval(caller, local, call.Args[index], depth)
			if !ok {
				failed = true
				return false
			}
			out = append(out, values...)
			return true
		})
		if failed {
			return nil, false
		}
	}
	return out, len(out) > 0
}
