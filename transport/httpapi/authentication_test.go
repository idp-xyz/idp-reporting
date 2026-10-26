package httpapi_test

import (
	"net/http/httptest"
	"testing"
)

func TestHTTPAuthenticationFailureChallengesWithoutLeakingCredentials(t *testing.T) {
	_, h := setup(t)
	w := request(h, "GET", "/api/reporting/v1/capabilities", nil, false)
	if w.Code != 401 || w.Header().Get("WWW-Authenticate") != `Bearer realm="reporting"` || w.Body.String() != "{\"error\":\"AUTH_SCOPE_DENIED\"}\n" {
		t.Fatalf("status=%d challenge=%q body=%s", w.Code, w.Header().Get("WWW-Authenticate"), w.Body.String())
	}
	req := httptest.NewRequest("GET", "https://reporting.example.invalid/api/reporting/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer invalid-private-credential")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 || w.Header().Get("WWW-Authenticate") != `Bearer realm="reporting", error="invalid_token"` || w.Body.String() != "{\"error\":\"AUTH_SCOPE_DENIED\"}\n" {
		t.Fatal("invalid credential challenge or redaction failed")
	}
}
