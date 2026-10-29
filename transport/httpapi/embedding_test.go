package httpapi_test

import (
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/embedding"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"strings"
	"testing"
)

func capabilitiesOf(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	w := request(h, "GET", "/api/reporting/v1/capabilities", nil, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var view map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &view); e != nil {
		t.Fatal(e)
	}
	return view
}

// The capabilities say whether embedding is configured for the caller's integrations, and never which origins.
func TestCapabilitiesSayWhetherEmbeddingIsConfiguredAndNothingMore(t *testing.T) {
	s, core := setup(t)
	auth, e := httpapi.DemoBearer(secret, contractsim.DemoPrincipal())
	if e != nil {
		t.Fatal(e)
	}
	if got := capabilitiesOf(t, core)["embedding"]; got != false {
		t.Fatalf("the core API alone says embedding=%v", got)
	}
	const origin = "https://app.brant.example"
	for name, c := range map[string]struct {
		origins map[string][]string
		want    bool
	}{
		"the caller's integration":    {map[string][]string{contractsim.FinanceIntegration: {origin}}, true},
		"another integration's":       {map[string][]string{contractsim.ShipmentIntegration: {origin}}, false},
		"one of the caller's and not": {map[string][]string{contractsim.ShipmentIntegration: {origin}, contractsim.FinanceIntegration: {origin}}, true},
		"none":                        {map[string][]string{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			config, e := embedding.New(c.origins, embedding.Options{})
			if e != nil {
				t.Fatal(e)
			}
			h, e := httpapi.WithEmbedding(core, s, auth, config)
			if e != nil {
				t.Fatal(e)
			}
			w := request(h, "GET", "/api/reporting/v1/capabilities", nil, true)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var view map[string]any
			if e := json.Unmarshal(w.Body.Bytes(), &view); e != nil {
				t.Fatal(e)
			}
			if view["embedding"] != c.want {
				t.Errorf("embedding = %v, want %v", view["embedding"], c.want)
			}
			if strings.Contains(w.Body.String(), "brant.example") || strings.Contains(w.Body.String(), "https://") {
				t.Errorf("the capabilities carry an origin: %s", w.Body.String())
			}
			// Everything else the core says is still said, by the one document.
			for key := range capabilitiesOf(t, core) {
				if _, ok := view[key]; !ok {
					t.Errorf("capabilities lost %q", key)
				}
			}
			// An unauthenticated caller learns nothing, as for every route.
			if w := request(h, "GET", "/api/reporting/v1/capabilities", nil, false); w.Code != 401 || strings.Contains(w.Body.String(), "embedding") {
				t.Errorf("anonymous: %d %s", w.Code, w.Body.String())
			}
			// The mount adds no other route: the rest of the API is the fallback's.
			if w := request(h, "GET", "/api/reporting/v1/session", nil, true); w.Code != 200 {
				t.Errorf("session through the mount: %d", w.Code)
			}
		})
	}
}

func TestWithEmbeddingRefusesMissingParts(t *testing.T) {
	s, core := setup(t)
	auth, _ := httpapi.DemoBearer(secret, contractsim.DemoPrincipal())
	config, _ := embedding.New(map[string][]string{}, embedding.Options{})
	for name, call := range map[string]func() (http.Handler, error){
		"no fallback":       func() (http.Handler, error) { return httpapi.WithEmbedding(nil, s, auth, config) },
		"no service":        func() (http.Handler, error) { return httpapi.WithEmbedding(core, nil, auth, config) },
		"no authentication": func() (http.Handler, error) { return httpapi.WithEmbedding(core, s, nil, config) },
		"no configuration":  func() (http.Handler, error) { return httpapi.WithEmbedding(core, s, auth, nil) },
	} {
		if h, e := call(); e == nil || h != nil || e != r.ErrInvalid {
			t.Errorf("%s: %v", name, e)
		}
	}
}
