package console_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"idp.local/reporting/console"
)

const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'"

func TestConsoleServesOnlyBundledAssetsAndPreservesAPI(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte("private-api"))
	})
	h, err := console.Handler(api)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path    string
		status  int
		content string
	}{
		{"/", 303, ""}, {"/console/", 200, "IDP Reporting"}, {"/console/app.js", 200, "/api/reporting/v1"}, {"/console/styles.css", 200, "--ink"}, {"/console/dashboard.js", 200, "createReportingDashboard"}, {"/console/documents.js", 200, "createReportingDocuments"}, {"/console/canvas.js", 200, "createReportingCanvas"},
		{"/console/private.json", 404, ""}, {"/console/assets/", 404, ""}, {"/api/reporting/v1/session", 401, "private-api"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.content) {
				t.Fatalf("path=%s status=%d", tc.path, w.Code)
			}
			if tc.status == 200 && (w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") || w.Header().Get("X-Content-Type-Options") != "nosniff") {
				t.Fatal("missing static protection")
			}
			// Every served file, the canvas script included, carries exactly the same headers: the policy
			// allows scripts and styles from the console's own origin only, so no inline style or script.
			if tc.status == 200 {
				for name, want := range map[string]string{"Content-Security-Policy": contentSecurityPolicy, "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer"} {
					if got := w.Header().Get(name); got != want {
						t.Fatalf("%s = %q", name, got)
					}
				}
			}
		})
	}
	if _, err := console.Handler(nil); err == nil {
		t.Fatal("nil API accepted")
	}
}
