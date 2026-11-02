package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/dashboards"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Capacity guards over HTTP (S04-G, docs/S04G_SCOPE.md G3): the bound of every body, the requests in flight, and the queries in flight of an
// identity.

// fullStack mounts every route that reads a body: the core API with layouts, backgrounds and images, previews and checks, templates and issues,
// dashboards.
func fullStack(t *testing.T) http.Handler {
	t.Helper()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	auth, _ := httpapi.DemoBearer(secret, p)
	dir := t.TempDir()
	layoutStore, e := layouts.Open(filepath.Join(dir, "layouts"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { layoutStore.Close() })
	h, e := httpapi.Handler(s, auth, layoutStore)
	if e != nil {
		t.Fatal(e)
	}
	assets, e := doctemplates.OpenAssets(filepath.Join(dir, "assets"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { assets.Close() })
	renderer := &codedRenderer{}
	if h, e = httpapi.WithDocumentAssets(h, s, auth, assets); e != nil {
		t.Fatal(e)
	}
	if h, e = httpapi.WithDocumentPreviews(h, s, auth, renderer, assets); e != nil {
		t.Fatal(e)
	}
	templates, e := doctemplates.Open(filepath.Join(dir, "templates"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { templates.Close() })
	if h, e = httpapi.WithDocumentTemplates(h, s, auth, templates, renderer, assets); e != nil {
		t.Fatal(e)
	}
	dashboardStore, e := dashboards.Open(filepath.Join(dir, "dashboards"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { dashboardStore.Close() })
	if h, e = httpapi.WithDashboards(h, s, auth, dashboardStore); e != nil {
		t.Fatal(e)
	}
	return h
}

// padded is a JSON object of exactly n bytes that is well formed and has a field no route knows: parsed, it would be a 400; refused for its
// size, it is a 413, which is what shows that nothing was parsed.
func padded(n int) []byte {
	const head, tail = `{"pad":"`, `"}`
	return []byte(head + strings.Repeat("a", n-len(head)-len(tail)) + tail)
}

func TestEveryRouteThatReadsABodyRefusesAnOversizedOneBeforeParsingIt(t *testing.T) {
	h := fullStack(t)
	const base = "/api/reporting/v1"
	small, query, document := 4<<10, 64<<10, 1<<20
	for _, route := range []struct {
		path  string
		limit int
	}{
		{"/queries", query}, {"/snapshot-jobs", query}, {"/exports", small},
		{"/layouts", document}, {"/layouts/abc/release", small},
		{"/dashboards", document}, {"/dashboards/abc/release", small},
		{"/document-templates", document}, {"/document-templates/abc/release", small}, {"/document-templates/abc/previews", document},
		{"/issues", small}, {"/document-previews", document}, {"/document-checks", document},
		{"/document-backgrounds", document}, {"/document-images", document},
	} {
		for _, chunked := range []bool{false, true} {
			send := func(n int) *httptest.ResponseRecorder {
				q := httptest.NewRequest("POST", base+route.path, bytes.NewReader(padded(n)))
				if chunked {
					q.ContentLength = -1 // no Content-Length: the bound is held while reading
				}
				q.Header.Set("Content-Type", "application/json")
				q.Header.Set("Idempotency-Key", "k")
				q.Header.Set("Authorization", "Bearer "+secret)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, q)
				return w
			}
			var body map[string]string
			if w := send(route.limit + 1); w.Code != 413 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body["error"] != "REQUEST_TOO_LARGE" || len(body) != 1 {
				t.Errorf("%s (chunked=%t): one byte over the bound of %d: %d %.200s", route.path, chunked, route.limit, w.Code, w.Body.String())
			}
			// At the bound it is read and parsed: the unknown field makes it the 400 of any malformed request, not a 413.
			if w := send(route.limit); w.Code == 413 {
				t.Errorf("%s (chunked=%t): a body of exactly the bound %d was refused for its size", route.path, chunked, route.limit)
			}
		}
	}
}

// A cancel has no body: one byte is refused, and the bound it reads is one byte whatever is sent.
func TestCancelReadsNoBody(t *testing.T) {
	_, h := setup(t)
	w := request(h, "POST", "/api/reporting/v1/jobs/abc/cancel", bytes.Repeat([]byte("x"), 1<<20), true)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// --- requests in flight ---

type gatedHandler struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (g *gatedHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	g.calls.Add(1)
	switch req.URL.Path {
	case "/hold":
		g.entered <- struct{}{}
		select {
		case <-g.release:
		case <-req.Context().Done(): // the client went away
		}
	case "/panic":
		panic("a handler panicked")
	case "/fail":
		http.Error(w, "failed", 500)
		return
	}
	io.WriteString(w, "done")
}

func newGated() *gatedHandler {
	return &gatedHandler{entered: make(chan struct{}, 256), release: make(chan struct{})}
}

func hold(t *testing.T, h http.Handler, g *gatedHandler, path string) chan *httptest.ResponseRecorder {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		func() {
			defer func() { _ = recover() }()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		}()
		done <- w
	}()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the handler")
	}
	return done
}

func overloaded(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	var body map[string]string
	if w.Code != 503 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body) != 1 || body["error"] != "OVERLOADED" {
		t.Fatalf("want 503 OVERLOADED, got %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") != "1" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json; charset=utf-8" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers of the refusal: %v", w.Header())
	}
}

func TestMaxInFlightRefusesTheRequestBeyondItAndFreesTheSlotWhenOneFinishes(t *testing.T) {
	g := newGated()
	h, e := httpapi.WithMaxInFlight(g, 2)
	if e != nil {
		t.Fatal(e)
	}
	one, two := hold(t, h, g, "/hold"), hold(t, h, g, "/hold")
	before := g.calls.Load()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/hold", nil))
	overloaded(t, w)
	if g.calls.Load() != before {
		t.Fatal("a refused request reached the handler")
	}
	g.release <- struct{}{}
	<-one
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/ok", nil))
	if w.Code != 200 || w.Body.String() != "done" {
		t.Fatalf("the slot was not given back when a request finished: %d %s", w.Code, w.Body.String())
	}
	g.release <- struct{}{}
	<-two
}

func TestMaxInFlightCountsTheSessionAndTheCapabilitiesAndDoesNoWorkWhenRefusing(t *testing.T) {
	s, api := setup(t)
	_ = s
	g := newGated()
	var authentications atomic.Int32
	counting := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/hold" {
				g.ServeHTTP(w, req)
				return
			}
			authentications.Add(1) // everything else reaches the API, which authenticates it
			next.ServeHTTP(w, req)
		})
	}
	h, e := httpapi.WithMaxInFlight(counting(api), 1)
	if e != nil {
		t.Fatal(e)
	}
	held := hold(t, h, g, "/hold")
	for _, path := range []string{"/api/reporting/v1/session", "/api/reporting/v1/capabilities", "/api/reporting/v1/catalog?integration_id=x"} {
		w := request(h, "GET", path, nil, true)
		overloaded(t, w)
	}
	if authentications.Load() != 0 {
		t.Fatal("a refused request was worked on")
	}
	g.release <- struct{}{}
	<-held
	if w := request(h, "GET", "/api/reporting/v1/session", nil, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestMaxInFlightSlotIsGivenBackOnEveryWayOut(t *testing.T) {
	g := newGated()
	h, _ := httpapi.WithMaxInFlight(g, 1)
	ok := func(when string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/ok", nil))
		if w.Code != 200 {
			t.Fatalf("%s: the slot was not given back: %d %s", when, w.Code, w.Body.String())
		}
	}
	// An error answer.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/fail", nil))
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
	ok("after an error")
	// A panic (the server recovers it; here the caller does).
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the handler's panic was swallowed")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/panic", nil))
	}()
	ok("after a panic")
	// A client that goes away, over a real connection: the handler sees its context end and returns.
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	gone := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/hold", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		gone <- err
	}()
	<-g.entered
	resp, err := http.Get(server.URL + "/ok") // the one slot is held by the client that is about to go
	if err != nil || resp.StatusCode != 503 {
		t.Fatalf("%v %v", resp, err)
	}
	resp.Body.Close()
	cancel()
	if e := <-gone; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(server.URL + "/ok")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the slot of a client that went away was never given back")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMaxInFlightBoundsAndDefault(t *testing.T) {
	for _, n := range []int{-1, 4097} {
		if h, e := httpapi.WithMaxInFlight(newGated(), n); !errors.Is(e, r.ErrInvalid) || h != nil {
			t.Errorf("%d: %v", n, e)
		}
	}
	if h, e := httpapi.WithMaxInFlight(nil, 1); !errors.Is(e, r.ErrInvalid) || h != nil {
		t.Error("a nil handler was accepted")
	}
	for _, n := range []int{1, 4096} {
		if _, e := httpapi.WithMaxInFlight(newGated(), n); e != nil {
			t.Errorf("%d: %v", n, e)
		}
	}
	// Zero is 128: the 128 requests are served together and the 129th is refused.
	g := newGated()
	h, e := httpapi.WithMaxInFlight(g, 0)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for range httpapi.DefaultMaxInFlight {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/hold", nil))
		}()
	}
	for range httpapi.DefaultMaxInFlight {
		select {
		case <-g.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the default admitted fewer than 128")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/ok", nil))
	overloaded(t, w)
	close(g.release)
	wg.Wait()
}

// --- queries in flight over HTTP ---

type heldSource struct {
	*contractsim.Simulator
	entered chan struct{}
	release chan struct{}
	hold    atomic.Bool
	reached atomic.Int32
}

func (a *heldSource) PrepareRead(ctx context.Context, sc r.SourceContext, q r.QuerySpec, key string) (r.PreparedRead, error) {
	a.reached.Add(1)
	if a.hold.Load() {
		a.entered <- struct{}{}
		select {
		case <-a.release:
		case <-ctx.Done():
			return r.PreparedRead{}, ctx.Err()
		}
	}
	return a.Simulator.PrepareRead(ctx, sc, q, key)
}

func TestQueriesInFlightAre429WithRetryAfterOneAndNothingRuns(t *testing.T) {
	p := contractsim.DemoPrincipal()
	sim := contractsim.Financial()
	sim.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	a := &heldSource{Simulator: sim, entered: make(chan struct{}, 8), release: make(chan struct{})}
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}, Quotas: r.Quotas{QueriesInFlight: 1}})
	if e != nil {
		t.Fatal(e)
	}
	auth, _ := httpapi.DemoBearer(secret, p)
	h, e := httpapi.Handler(s, auth)
	if e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(contractsim.FinancialQuery())
	// A snapshot to read rows of, before anything is held.
	job := requestWithKey(h, "POST", "/api/reporting/v1/snapshot-jobs", body, "k")
	if job.Code != 202 {
		t.Fatal(job.Code, job.Body.String())
	}
	if _, e := s.RunOne(context.Background()); e != nil {
		t.Fatal(e)
	}
	var view r.JobView
	json.Unmarshal(requestWithKey(h, "GET", job.Header().Get("Location"), nil, "k").Body.Bytes(), &view)
	a.hold.Store(true)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- request(h, "POST", "/api/reporting/v1/queries", body, true) }()
	<-a.entered
	reached := a.reached.Load()
	check := func(w *httptest.ResponseRecorder) {
		t.Helper()
		var got map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &got); e != nil || w.Code != 429 || len(got) != 2 || got["error"] != "QUOTA_EXCEEDED" || got["quota"] != "queries_in_flight" {
			t.Fatalf("want 429 queries_in_flight, got %d %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Retry-After") != "1" || w.Header().Get("Location") != "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("headers: %v", w.Header())
		}
	}
	check(request(h, "POST", "/api/reporting/v1/queries", body, true))
	check(request(h, "GET", "/api/reporting/v1/snapshots/"+view.SnapshotID+"/rows?limit=1", nil, true))
	if a.reached.Load() != reached {
		t.Fatal("a refused read reached the source")
	}
	// The other routes are not bounded by it.
	if w := request(h, "GET", "/api/reporting/v1/capabilities", nil, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	a.release <- struct{}{}
	if w := <-first; w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a.hold.Store(false)
	if w := request(h, "GET", "/api/reporting/v1/snapshots/"+view.SnapshotID+"/rows?limit=1", nil, true); w.Code != 200 {
		t.Fatal("the slot was not given back:", w.Code, w.Body.String())
	}
}
