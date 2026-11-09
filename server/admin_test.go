package server

import (
	"encoding/json"
	"strings"
	"testing"

	"idp.local/reporting/delivery/webhook"
)

// S18-B in the production server: GET /admin/configuration is refused to a person with the default roles (recorded in their audit), answers an
// administrator the tenant's connections and bindings with the digests of the start line, follows a reload, and says of the webhook its host
// alone: never its path or query, the signing secret, or an adapter's options.
func TestTheServerShowsTheConfigurationInForceToAnAdministrator(t *testing.T) {
	w := newWorld(t)
	w.cfg.Schedules = &SchedulesConfig{PerIdentity: 7, Webhook: &WebhookConfig{URL: "https://hooks.example.com/reporting/path-secret?key=query-secret"}}
	w.env = map[string]string{webhook.SecretEnv: testWebhookSecret}
	s := startWorld(t, w)
	start := s.log.events(t, "start")[0]
	token := mint(t, w.keys[0], claims{})
	const route = "/api/reporting/v1/admin/configuration"
	if code, _, body := s.do(t, "GET", route, token, nil); code != 403 || !strings.Contains(string(body), "AUTH_ROLE_DENIED") {
		t.Fatalf("the default roles: %d %s", code, body)
	}
	if code, _, body := s.do(t, "GET", "/api/reporting/v1/audit", token, nil); code != 200 || !strings.Contains(string(body), `"role.refused"`) {
		t.Fatalf("the refusal is not in the audit: %d %s", code, body)
	}
	bindings := `[{"subject":"` + testSubject + `","client_id":"` + testClient + `","tenant":"` + testTenant + `","integrations":["` + w.finance + `"],"roles":["viewer","administrator"]},` +
		`{"subject":"colleague","client_id":"` + testClient + `","tenant":"` + testTenant + `","integrations":["` + w.finance + `"]}]`
	replaceFile(t, w.binds, []byte(bindings), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	code, _, body := s.do(t, "GET", route, token, nil)
	if code != 200 {
		t.Fatalf("the administrator: %d %s", code, body)
	}
	for _, secret := range []string{"path-secret", "query-secret", testWebhookSecret, `"options"`} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("the view carries %q: %s", secret, body)
		}
	}
	var v struct {
		Tenant      string `json:"tenant"`
		Connections []struct {
			Integration, SourceTenant, Adapter string
		} `json:"connections"`
		ConnectionsSHA256 string `json:"connections_sha256"`
		Bindings          []struct {
			Subject      string   `json:"subject"`
			Integrations []string `json:"integrations"`
			Roles        []string `json:"roles"`
		} `json:"bindings"`
		BindingsSHA256 string `json:"bindings_sha256"`
		Schedules      struct {
			Enabled     bool   `json:"enabled"`
			PerIdentity int    `json:"per_identity"`
			Webhook     bool   `json:"webhook"`
			WebhookHost string `json:"webhook_host"`
		} `json:"schedules"`
		SignIn bool `json:"sign_in"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	if v.Tenant != testTenant || len(v.Connections) == 0 || v.ConnectionsSHA256 != start["connections_sha256"] {
		t.Fatalf("connections: %s (start %v)", body, start["connections_sha256"])
	}
	if len(v.Bindings) != 2 || strings.Join(v.Bindings[0].Roles, ",") != "viewer,administrator" || v.Bindings[1].Roles != nil || v.BindingsSHA256 == start["bindings_sha256"] {
		t.Fatalf("bindings after the reload: %s", body)
	}
	if reload := s.log.events(t, "reload"); v.BindingsSHA256 != reload[len(reload)-1]["bindings_sha256"] {
		t.Fatalf("the digest is not the reload's: %s", body)
	}
	if !v.Schedules.Enabled || v.Schedules.PerIdentity != 7 || !v.Schedules.Webhook || v.Schedules.WebhookHost != "hooks.example.com" || v.SignIn {
		t.Fatalf("schedules: %s", body)
	}
}
