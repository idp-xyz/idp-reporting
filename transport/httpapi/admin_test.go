package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	r "idp.local/reporting/reporting"
	"idp.local/reporting/transport/httpapi"
)

// S18-B: the administrator's view answers an administrator of the tenant and nobody else; it is asked for the caller's tenant; a view that
// answers another tenant is refused; a query is refused; empty lists are [] and a binding without products has [].
func TestTheAdministratorsViewIsTheAdministratorsAlone(t *testing.T) {
	admin := r.Principal{Identity: r.Identity{Tenant: "tenant_x", Issuer: "urn:test", Subject: "ada"}, Integrations: []string{"finance"}, Roles: []string{r.RoleViewer, r.RoleAdministrator}}
	viewer := r.Principal{Identity: r.Identity{Tenant: "tenant_x", Issuer: "urn:test", Subject: "vic"}, Integrations: []string{"finance"}}
	people := map[string]r.Principal{"Bearer admin": admin, "Bearer viewer": viewer}
	authenticate := func(req *http.Request) (r.Principal, error) {
		if p, ok := people[req.Header.Get("Authorization")]; ok {
			return p, nil
		}
		return r.Principal{}, r.ErrDenied
	}
	asked, answer := "", "tenant_x"
	view := func(_ context.Context, tenant string) (httpapi.AdminConfiguration, error) {
		asked = tenant
		return httpapi.AdminConfiguration{Tenant: answer, Bindings: []httpapi.AdminBinding{{Subject: "vic", ClientID: "c"}}}, nil
	}
	if _, e := httpapi.WithAdministration(nil, authenticate, view); e == nil {
		t.Fatal("a nil handler was accepted")
	}
	h, e := httpapi.WithAdministration(http.NotFoundHandler(), authenticate, view)
	if e != nil {
		t.Fatal(e)
	}
	call := func(token, target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("Authorization", token)
		h.ServeHTTP(w, req)
		return w
	}
	const route = "/api/reporting/v1/admin/configuration"
	w := call("Bearer viewer", route)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "AUTH_ROLE_DENIED") || asked != "" {
		t.Fatalf("a viewer: %d %s (view asked for %q)", w.Code, w.Body, asked)
	}
	if w := call("Bearer nobody", route); w.Code != 401 {
		t.Fatalf("no identity: %d", w.Code)
	}
	w = call("Bearer admin", route)
	if w.Code != 200 || asked != "tenant_x" {
		t.Fatalf("the administrator: %d %s (asked %q)", w.Code, w.Body, asked)
	}
	var got map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &got); e != nil {
		t.Fatal(e)
	}
	if c, ok := got["connections"].([]any); !ok || len(c) != 0 {
		t.Fatalf("connections: %v", got["connections"])
	}
	if b := got["bindings"].([]any)[0].(map[string]any); b["integrations"] == nil || b["roles"] != nil {
		t.Fatalf("binding: %v", b)
	}
	if w := call("Bearer admin", route+"?tenant=tenant_y"); w.Code != 400 {
		t.Fatalf("a query: %d", w.Code)
	}
	answer = "tenant_y"
	if w := call("Bearer admin", route); w.Code != 409 || !strings.Contains(w.Body.String(), "INTEGRITY_FAILURE") || strings.Contains(w.Body.String(), "tenant_y") {
		t.Fatalf("a view of another tenant: %d %s", w.Code, w.Body)
	}
}
