package httpapi

import (
	"context"
	"net/http"

	r "idp.local/reporting/reporting"
)

// AdminConfiguration is what an administrator reads of the configuration in force (S18-B, docs/S18_SCOPE.md, ADR 0016 decision 1): the product
// connections and the bindings of the administrator's own tenant, the digests and times that say which files are in force, and whether the
// scheduled reports, their webhook and the browser sign-in are configured. It is read-only: nothing writes a connection or a binding through
// the platform. It never carries a secret, an adapter's options, a key or the webhook's path and query (a receiver may put a token there):
// of the webhook only its host.
type AdminConfiguration struct {
	Tenant            string            `json:"tenant"`
	Version           string            `json:"version"`
	StartedAt         string            `json:"started_at"`
	Connections       []AdminConnection `json:"connections"`
	ConnectionsSHA256 string            `json:"connections_sha256"`
	Bindings          []AdminBinding    `json:"bindings"`
	BindingsSHA256    string            `json:"bindings_sha256"`
	BindingsLoadedAt  string            `json:"bindings_loaded_at"`
	Schedules         AdminSchedules    `json:"schedules"`
	SignIn            bool              `json:"sign_in"`
}

// AdminConnection is one product connection of the tenant: the product, the source product's tenant and the adapter's name.
type AdminConnection struct {
	Integration  string `json:"integration_id"`
	SourceTenant string `json:"source_tenant"`
	Adapter      string `json:"adapter"`
}

// AdminBinding is one binding of the tenant: who (the token's subject and client), which products and which roles (null: the default roles).
type AdminBinding struct {
	Subject      string   `json:"subject"`
	ClientID     string   `json:"client_id"`
	Integrations []string `json:"integrations"`
	Roles        []string `json:"roles"`
}

// AdminSchedules says whether the scheduled reports run and where their ends are delivered.
type AdminSchedules struct {
	Enabled     bool   `json:"enabled"`
	PerIdentity int    `json:"per_identity,omitempty"`
	Webhook     bool   `json:"webhook"`
	WebhookHost string `json:"webhook_host,omitempty"`
}

// AdminView answers the configuration in force for an administrator's tenant. The host builds it from what it holds (the server from its
// configuration and the bindings in force; the demo from its own principals).
type AdminView func(ctx context.Context, tenant string) (AdminConfiguration, error)

// WithAdministration mounts GET /api/reporting/v1/admin/configuration over next. RequiredRole holds every /admin/ route to the administrator
// role, so a person without it is refused (403 AUTH_ROLE_DENIED) before the view is asked.
func WithAdministration(next http.Handler, authenticate Authenticate, view AdminView) (http.Handler, error) {
	if next == nil || authenticate == nil || view == nil {
		return nil, r.ErrInvalid
	}
	mux := http.NewServeMux()
	mux.Handle("/", next)
	mux.HandleFunc("GET /api/reporting/v1/admin/configuration", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		if len(req.URL.Query()) != 0 {
			return r.ErrInvalid
		}
		v, e := view(req.Context(), p.Tenant)
		if e != nil {
			return e
		}
		if v.Tenant != p.Tenant {
			return r.ErrIntegrity
		}
		if v.Connections == nil {
			v.Connections = []AdminConnection{}
		}
		if v.Bindings == nil {
			v.Bindings = []AdminBinding{}
		}
		for i := range v.Bindings {
			if v.Bindings[i].Integrations == nil {
				v.Bindings[i].Integrations = []string{}
			}
		}
		return send(w, 200, v)
	}))
	return secured(mux), nil
}
