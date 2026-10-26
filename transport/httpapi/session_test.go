package httpapi_test

import (
	"encoding/json"
	"testing"
)

func TestSessionReturnsOnlyAuthenticatedIdentityAndConnections(t *testing.T) {
	_, h := setup(t)
	w := request(h, "GET", "/api/reporting/v1/session", nil, true)
	if w.Code != 200 {
		t.Fatalf("session status=%d", w.Code)
	}
	var value struct {
		Identity     struct{ Tenant, Issuer, Subject string }
		Integrations []string
	}
	if json.Unmarshal(w.Body.Bytes(), &value) != nil || value.Identity.Tenant != "platform_demo" || value.Identity.Subject != "user_a" || len(value.Integrations) != 1 || value.Integrations[0] != "brant-contract-demo" {
		t.Fatal("session did not use trusted identity")
	}
	var fields map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &fields)
	if len(fields) != 2 {
		t.Fatal("unexpected session disclosure")
	}
	if request(h, "GET", "/api/reporting/v1/session", nil, false).Code != 401 {
		t.Fatal("anonymous session allowed")
	}
}
