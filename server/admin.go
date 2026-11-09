package server

import (
	"context"
	"net/url"
	"slices"
	"time"

	"idp.local/reporting/transport/httpapi"
)

// adminView is the administrator's read-only view of the configuration in force (S18-B, docs/S18_SCOPE.md): the connections and bindings of the
// administrator's tenant, the digests of what is in force (the same values as the start and reload log lines), and the scheduled reports' and
// sign-in's state. No adapter option, secret, key or the webhook's path is in it.
func (s *Server) adminView(_ context.Context, tenant string) (httpapi.AdminConfiguration, error) {
	s.reloadMu.Lock()
	bindings, digest, loaded := s.issuer.bindings, s.issuer.digest, s.bindingsLoadedAt
	s.reloadMu.Unlock()
	c := &s.cfg
	v := httpapi.AdminConfiguration{
		Tenant:            tenant,
		Version:           s.version,
		StartedAt:         s.startedAt.Format(time.RFC3339),
		ConnectionsSHA256: connectionsDigest(c.Connections),
		BindingsSHA256:    digest,
		BindingsLoadedAt:  loaded.Format(time.RFC3339),
		SignIn:            c.SignIn != nil,
	}
	for _, x := range c.Connections {
		if x.Tenant == tenant {
			v.Connections = append(v.Connections, httpapi.AdminConnection{Integration: x.Integration, SourceTenant: x.SourceTenant, Adapter: x.Adapter})
		}
	}
	for _, b := range bindings {
		if b.Tenant == tenant {
			v.Bindings = append(v.Bindings, httpapi.AdminBinding{Subject: b.Subject, ClientID: b.ClientID, Integrations: slices.Clone(b.Integrations), Roles: slices.Clone(b.Roles)})
		}
	}
	if sc := c.Schedules; sc != nil && s.schedules != nil {
		v.Schedules.Enabled = true
		v.Schedules.PerIdentity = s.schedules.Limit()
		if sc.Webhook != nil && s.schedules.Delivers() {
			v.Schedules.Webhook = true
			if u, err := url.Parse(sc.Webhook.URL); err == nil {
				v.Schedules.WebhookHost = u.Host
			}
		}
	}
	return v, nil
}
