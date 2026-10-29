package console_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"idp.local/reporting/console"
	"idp.local/reporting/embedding"
)

const (
	hostA       = "http://127.0.0.1:7811"
	hostB       = "https://app.brant.example"
	otherOrigin = "https://evil.example"
	dashboard   = "0123456789abcdef0123456789abcdef"
	template    = "fedcba9876543210fedcba9876543210"
)

func embedHandler(t *testing.T, config *embedding.Config) http.Handler {
	t.Helper()
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte("private-api"))
	})
	h, err := console.HandlerWithEmbedding(api, config)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func embedConfig(t *testing.T) *embedding.Config {
	t.Helper()
	config, err := embedding.Parse([]string{"brant=" + hostA, "brant=" + hostB, "second=https://second.example"}, embedding.Options{LoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func ask(h http.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

// query builds a query string from key/value pairs with every value escaped, in the order given.
func query(pairs ...string) string {
	var parts []string
	for i := 0; i < len(pairs); i += 2 {
		parts = append(parts, url.QueryEscape(pairs[i])+"="+url.QueryEscape(pairs[i+1]))
	}
	return strings.Join(parts, "&")
}

func unframeable(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	if got := w.Header().Values("X-Frame-Options"); len(got) != 1 || got[0] != "DENY" {
		t.Errorf("%s: X-Frame-Options = %q", what, got)
	}
	got := w.Header().Values("Content-Security-Policy")
	if len(got) != 1 || !strings.Contains(got[0], "frame-ancestors 'none'") || strings.Count(got[0], "frame-ancestors") != 1 {
		t.Errorf("%s: Content-Security-Policy = %q", what, got)
	}
}

// An allowed embed answer carries the console's policy with one difference, the exact parent origin as the only frame-ancestors
// source, and no X-Frame-Options (which cannot name an origin).
func TestEmbedPageIsFramedByTheListedOriginOnly(t *testing.T) {
	h := embedHandler(t, embedConfig(t))
	for _, c := range []struct {
		name, path, integration, parent string
		resource                        []string
	}{
		{"report, loopback origin", "/embed/report", "brant", hostA, []string{"dataset", "receivables.aging@1.0.0"}},
		{"report, dataset without version", "/embed/report", "brant", hostA, []string{"dataset", "receivables.aging"}},
		{"report, https origin", "/embed/report", "brant", hostB, []string{"dataset", "receivables.aging"}},
		{"dashboard", "/embed/dashboard", "brant", hostB, []string{"dashboard", dashboard}},
		{"designer", "/embed/document-designer", "brant", hostA, []string{"template", template}},
		{"designer with a version", "/embed/document-designer", "brant", hostA, []string{"template", template, "version", "20"}},
		{"second integration", "/embed/dashboard", "second", "https://second.example", []string{"dashboard", dashboard}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := ask(h, "GET", c.path+"?"+query(append([]string{"integration", c.integration, "parent", c.parent}, c.resource...)...))
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			want := strings.Replace(contentSecurityPolicy, "frame-ancestors 'none'", "frame-ancestors "+c.parent, 1)
			if got := w.Header().Values("Content-Security-Policy"); len(got) != 1 || got[0] != want {
				t.Errorf("Content-Security-Policy = %q, want %q", got, want)
			}
			if got := w.Header().Values("X-Frame-Options"); len(got) != 0 {
				t.Errorf("X-Frame-Options = %q", got)
			}
			for name, want := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Content-Type": "text/html; charset=utf-8"} {
				if got := w.Header().Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			body := w.Body.String()
			if !strings.Contains(body, `src="/console/embed.js"`) || !strings.Contains(body, `src="/console/app.js"`) || strings.Contains(body, "access-token") || strings.Contains(body, `id="logout"`) {
				t.Error("not the embed page: it loads the console's scripts and embed.js and has no token form and no logout")
			}
			// No inline script, handler or style: the policy forbids them and the page must not need them.
			lower := strings.ToLower(body)
			for _, banned := range []string{"<script>", " onclick=", " onload=", " onerror=", "javascript:", " style=", "<style"} {
				if strings.Contains(lower, banned) {
					t.Errorf("embed page contains %q", banned)
				}
			}
			if strings.Count(body, "<script") != strings.Count(body, `<script src="/console/`) {
				t.Error("a script of the embed page is not one of the console's files")
			}
			// HEAD answers the same headers without the body.
			head := ask(h, "HEAD", c.path+"?"+query(append([]string{"integration", c.integration, "parent", c.parent}, c.resource...)...))
			if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Security-Policy") != want {
				t.Errorf("HEAD: %d %d %q", head.Code, head.Body.Len(), head.Header().Get("Content-Security-Policy"))
			}
		})
	}
}

// Whoever is not listed gets a 403 that cannot be framed by anyone: an unknown integration, an origin that is not on that integration's
// list (another integration's, a lookalike, a different scheme or port, a different case), a missing, empty, repeated or malformed parent.
func TestEmbedRefusesEveryoneElseWithAnUnframeableAnswer(t *testing.T) {
	h := embedHandler(t, embedConfig(t))
	resource := []string{"dashboard", dashboard}
	for name, target := range map[string]string{
		"an unknown integration":            "/embed/dashboard?" + query(append([]string{"integration", "unknown", "parent", hostA}, resource...)...),
		"an unlisted origin":                "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", otherOrigin}, resource...)...),
		"another integration's origin":      "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", "https://second.example"}, resource...)...),
		"the origin of the platform":        "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", "http://example.com"}, resource...)...),
		"a trailing slash":                  "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", hostB + "/"}, resource...)...),
		"a path":                            "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", hostB + "/app"}, resource...)...),
		"upper case":                        "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", "https://APP.brant.example"}, resource...)...),
		"another port":                      "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", "http://127.0.0.1:7812"}, resource...)...),
		"another scheme":                    "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", "http://app.brant.example"}, resource...)...),
		"a lookalike suffix":                "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", hostB + ".evil.example"}, resource...)...),
		"a lookalike prefix":                "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", "https://evil.example?" + hostB}, resource...)...),
		"null":                              "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", "null"}, resource...)...),
		"a wildcard":                        "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", "*"}, resource...)...),
		"two origins in one value":          "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", hostB + " " + otherOrigin}, resource...)...),
		"an origin and a directive":         "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", hostB + "; frame-ancestors *"}, resource...)...),
		"an empty parent":                   "/embed/dashboard?" + query(append([]string{"integration", "brant", "parent", ""}, resource...)...),
		"no parent":                         "/embed/dashboard?" + query(append([]string{"integration", "brant"}, resource...)...),
		"no integration":                    "/embed/dashboard?" + query(append([]string{"parent", hostA}, resource...)...),
		"no query at all":                   "/embed/dashboard",
		"the parent twice, both listed":     "/embed/dashboard?" + query("integration", "brant", "parent", hostA, "parent", hostB, "dashboard", dashboard),
		"the parent twice, one unlisted":    "/embed/dashboard?" + query("integration", "brant", "parent", hostA, "parent", otherOrigin, "dashboard", dashboard),
		"the integration twice":             "/embed/dashboard?" + query("integration", "brant", "integration", "second", "parent", hostA, "dashboard", dashboard),
		"a query that does not parse":       "/embed/dashboard?integration=brant&parent=%zz&dashboard=" + dashboard,
		"a semicolon in the query":          "/embed/dashboard?integration=brant;parent=" + url.QueryEscape(hostA) + "&dashboard=" + dashboard,
		"report, unlisted":                  "/embed/report?" + query("integration", "brant", "parent", otherOrigin, "dataset", "receivables.aging"),
		"designer, unlisted":                "/embed/document-designer?" + query("integration", "brant", "parent", otherOrigin, "template", template),
		"unlisted with a malformed dataset": "/embed/report?" + query("integration", "brant", "parent", otherOrigin, "dataset", "<x>"),
	} {
		t.Run(name, func(t *testing.T) {
			w := ask(h, "GET", target)
			if w.Code != 403 {
				t.Fatalf("status %d, want 403", w.Code)
			}
			unframeable(t, name, w)
			if body := w.Body.String(); strings.Contains(body, "<html") || strings.Contains(body, "script") {
				t.Errorf("a refusal carries a page: %q", body)
			}
		})
	}
}

// An origin that is listed with a request that is not one: the shape of the resource is wrong, so 400, still unframeable.
func TestEmbedRefusesMalformedResourcesWithoutFraming(t *testing.T) {
	h := embedHandler(t, embedConfig(t))
	base := []string{"integration", "brant", "parent", hostA}
	for name, c := range map[string]struct {
		path string
		more []string
	}{
		"report without a dataset":         {"/embed/report", nil},
		"report with a dashboard":          {"/embed/report", []string{"dashboard", dashboard}},
		"report with an empty dataset":     {"/embed/report", []string{"dataset", ""}},
		"report with markup":               {"/embed/report", []string{"dataset", "<img src=x onerror=alert(1)>"}},
		"report with a path":               {"/embed/report", []string{"dataset", "../../etc/passwd"}},
		"report with a very long dataset":  {"/embed/report", []string{"dataset", strings.Repeat("a", 129)}},
		"report with a version":            {"/embed/report", []string{"dataset", "receivables.aging", "version", "1"}},
		"report with an extra key":         {"/embed/report", []string{"dataset", "receivables.aging", "company", "company_demo"}},
		"report with the dataset twice":    {"/embed/report", []string{"dataset", "a", "dataset", "b"}},
		"dashboard without an id":          {"/embed/dashboard", nil},
		"dashboard with a short id":        {"/embed/dashboard", []string{"dashboard", "abc"}},
		"dashboard with an upper-case id":  {"/embed/dashboard", []string{"dashboard", strings.ToUpper(dashboard)}},
		"dashboard with a template":        {"/embed/dashboard", []string{"template", template}},
		"dashboard with a version":         {"/embed/dashboard", []string{"dashboard", dashboard, "version", "1"}},
		"designer without a template":      {"/embed/document-designer", nil},
		"designer with a dashboard":        {"/embed/document-designer", []string{"dashboard", dashboard}},
		"designer with version zero":       {"/embed/document-designer", []string{"template", template, "version", "0"}},
		"designer with version 21":         {"/embed/document-designer", []string{"template", template, "version", "21"}},
		"designer with a signed version":   {"/embed/document-designer", []string{"template", template, "version", "+1"}},
		"designer with a padded version":   {"/embed/document-designer", []string{"template", template, "version", "01"}},
		"designer with an empty version":   {"/embed/document-designer", []string{"template", template, "version", ""}},
		"designer with a text version":     {"/embed/document-designer", []string{"template", template, "version", "latest"}},
		"designer with the version twice":  {"/embed/document-designer", []string{"template", template, "version", "1", "version", "2"}},
		"designer with a token parameter":  {"/embed/document-designer", []string{"template", template, "token", "secret"}},
		"dashboard with a token parameter": {"/embed/dashboard", []string{"dashboard", dashboard, "access_token", "secret"}},
	} {
		t.Run(name, func(t *testing.T) {
			w := ask(h, "GET", c.path+"?"+query(append(append([]string{}, base...), c.more...)...))
			if w.Code != 400 {
				t.Fatalf("status %d, want 400", w.Code)
			}
			unframeable(t, name, w)
		})
	}
}

// Without any embedding configuration the entry points exist and refuse everyone; the handler built by Handler is that one.
func TestEmbedWithoutConfigurationRefusesEveryone(t *testing.T) {
	for name, h := range map[string]http.Handler{"nil": embedHandler(t, nil), "empty": embedHandler(t, &embedding.Config{})} {
		for _, path := range []string{"/embed/report", "/embed/dashboard", "/embed/document-designer"} {
			w := ask(h, "GET", path+"?"+query("integration", "brant", "parent", hostA, "dataset", "receivables.aging", "dashboard", dashboard))
			if w.Code != 403 {
				t.Errorf("%s %s: %d", name, path, w.Code)
			}
			unframeable(t, name+" "+path, w)
		}
	}
	h, err := console.Handler(http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	w := ask(h, "GET", "/embed/report?"+query("integration", "brant", "parent", hostA, "dataset", "x"))
	if w.Code != 403 {
		t.Errorf("Handler: %d", w.Code)
	}
	if _, err := console.HandlerWithEmbedding(nil, embedConfig(t)); err == nil {
		t.Error("nil API accepted")
	}
}

// Every answer the handler gives that is not an allowed embed page keeps frame-ancestors 'none' and X-Frame-Options: DENY: the console and
// each of its files, the redirect, a missing file, the embed route's other paths and methods, the API's answers, an unknown path.
func TestEveryOtherAnswerStaysUnframeable(t *testing.T) {
	h := embedHandler(t, embedConfig(t))
	allowed := "/embed/report?" + query("integration", "brant", "parent", hostA, "dataset", "receivables.aging")
	for _, c := range []struct{ method, path string }{
		{"GET", "/"}, {"GET", "/console/"}, {"GET", "/console/index.html"}, {"GET", "/console/app.js"}, {"GET", "/console/dashboard.js"}, {"GET", "/console/documents.js"},
		{"GET", "/console/canvas.js"}, {"GET", "/console/embed.js"}, {"GET", "/console/styles.css"}, {"GET", "/console/embed.css"}, {"GET", "/console/embed.html"},
		{"GET", "/console/private.json"}, {"GET", "/console/assets/"}, {"GET", "/api/reporting/v1/session"}, {"POST", "/api/reporting/v1/queries"}, {"GET", "/nope"},
		{"GET", "/embed/"}, {"GET", "/embed/other"}, {"GET", "/embed/report/"}, {"GET", "/embed/report/x"}, {"POST", "/embed/report"}, {"POST", allowed},
		{"PUT", "/embed/dashboard"}, {"DELETE", "/embed/document-designer"}, {"OPTIONS", "/embed/report"}, {"GET", "/embed"}, {"GET", "//embed/report"},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			w := ask(h, c.method, c.path)
			if w.Code == 200 && strings.HasPrefix(c.path, "/embed") {
				t.Fatalf("answered 200: %s", w.Body.String())
			}
			unframeable(t, c.method+" "+c.path, w)
			if strings.HasPrefix(c.path, "/console/") && w.Code == 200 {
				if got := w.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
					t.Errorf("Content-Security-Policy = %q", got)
				}
			}
		})
	}
}

// The console page itself, and the embed page of an allowed origin, differ in the policy by the frame-ancestors source and nothing else.
func TestEmbedPolicyDiffersOnlyInFrameAncestors(t *testing.T) {
	h := embedHandler(t, embedConfig(t))
	page := ask(h, "GET", "/embed/report?"+query("integration", "brant", "parent", hostB, "dataset", "receivables.aging"))
	consolePage := ask(h, "GET", "/console/")
	a, b := page.Header().Get("Content-Security-Policy"), consolePage.Header().Get("Content-Security-Policy")
	if strings.Replace(a, "frame-ancestors "+hostB, "frame-ancestors 'none'", 1) != b || b != contentSecurityPolicy {
		t.Errorf("embed %q console %q", a, b)
	}
	for _, banned := range []string{"unsafe-inline", "unsafe-eval", "*", "http:", "blob:"} {
		if strings.Contains(a, banned) {
			t.Errorf("the embed policy contains %q", banned)
		}
	}
	if !strings.Contains(a, "frame-ancestors "+hostB+";") || strings.Count(a, hostB) != 1 {
		t.Errorf("the parent origin is not the one frame-ancestors source: %q", a)
	}
}
