package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/embedding"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/transport/httpapi"
)

// S05-A: the production server shows its own profile label and nothing else changes; production_approved is false whatever the label says.
func TestWithProfileShowsTheLabelAndNeverApproves(t *testing.T) {
	s, base := setup(t)
	p := contractsim.DemoPrincipal()
	auth, _ := httpapi.DemoBearer(secret, p)
	embed, err := embedding.New(map[string][]string{contractsim.FinanceIntegration: {"https://app.example"}}, embedding.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for name, config := range map[string]*embedding.Config{"without embedding": nil, "with embedding": embed} {
		h, err := httpapi.WithProfile(base, s, auth, "staging-eu 1", config)
		if err != nil {
			t.Fatal(err)
		}
		w := request(h, "GET", "/api/reporting/v1/capabilities", nil, true)
		var caps struct {
			Profile   string `json:"profile"`
			Approved  bool   `json:"production_approved"`
			Embedding bool   `json:"embedding"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &caps) != nil || caps.Profile != "staging-eu 1" || caps.Approved || caps.Embedding != (config != nil) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		if w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", name, w.Header())
		}
		// Unauthenticated, and every other route, as before.
		if w := request(h, "GET", "/api/reporting/v1/capabilities", nil, false); w.Code != 401 {
			t.Errorf("%s: %d", name, w.Code)
		}
		if w := request(h, "GET", "/api/reporting/v1/session", nil, true); w.Code != 200 {
			t.Errorf("%s: session %d", name, w.Code)
		}
	}
	// The core API alone still says what it always said.
	w := request(base, "GET", "/api/reporting/v1/capabilities", nil, true)
	if !strings.Contains(w.Body.String(), `"profile":"S01-B-contract-simulators"`) || !strings.Contains(w.Body.String(), `"production_approved":false`) {
		t.Errorf("%s", w.Body)
	}
	for _, label := range []string{"", strings.Repeat("a", 65), " a", "a ", "a\nb", "é", "a\x7f"} {
		if httpapi.ValidProfileLabel(label) {
			t.Errorf("%q is a valid label", label)
		}
		if h, err := httpapi.WithProfile(base, s, auth, label, nil); !errors.Is(err, r.ErrInvalid) || h != nil {
			t.Errorf("%q: %v", label, err)
		}
	}
	if h, err := httpapi.WithProfile(nil, s, auth, "x", nil); !errors.Is(err, r.ErrInvalid) || h != nil {
		t.Error(err)
	}
}

// The gauge returns to zero when a handler panics, returns an error status or finishes: a slot is never kept.
func TestMaxInFlightGaugeCountsHeldSlots(t *testing.T) {
	var gauge func() int64
	seen := int64(-1)
	inner := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = gauge()
		if req.URL.Path == "/panic" {
			panic(http.ErrAbortHandler)
		}
	})
	h, g, err := httpapi.WithMaxInFlightGauge(inner, 3)
	if err != nil {
		t.Fatal(err)
	}
	gauge = g
	for _, path := range []string{"/ok", "/panic"} {
		func() {
			defer func() { _ = recover() }()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
		}()
		if seen != 1 || g() != 0 {
			t.Errorf("%s: %d inside, %d after", path, seen, g())
		}
	}
	if _, _, err := httpapi.WithMaxInFlightGauge(inner, 4097); !errors.Is(err, r.ErrInvalid) {
		t.Error(err)
	}
}

// The core API still says contract-simulator; WithDataClass overrides it for every answer, and accepts only the two classes.
func TestDataClassDefaultsToSimulatorAndWithDataClassOverrides(t *testing.T) {
	_, base := setup(t)
	if got := request(base, "GET", "/api/reporting/v1/session", nil, false).Header().Values("X-Reporting-Data-Class"); len(got) != 1 || got[0] != "contract-simulator" {
		t.Fatalf("%q", got)
	}
	h, err := httpapi.WithDataClass(base, httpapi.DataClassBusiness)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/reporting/v1/session", "/api/reporting/v1/nope"} {
		if got := request(h, "GET", path, nil, true).Header().Values("X-Reporting-Data-Class"); len(got) != 1 || got[0] != "business-data" {
			t.Errorf("%s: %q", path, got)
		}
	}
	for _, bad := range []string{"", "other"} {
		if _, err := httpapi.WithDataClass(base, bad); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
