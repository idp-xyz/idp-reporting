package console_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"idp.local/reporting/console"
)

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
		{"/", 303, ""}, {"/console/", 200, "IDP Reporting"}, {"/console/app.js", 200, "/api/reporting/v1"}, {"/console/styles.css", 200, "--ink"}, {"/console/dashboard.js", 200, "createReportingDashboard"},
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
		})
	}
	if _, err := console.Handler(nil); err == nil {
		t.Fatal("nil API accepted")
	}
}
