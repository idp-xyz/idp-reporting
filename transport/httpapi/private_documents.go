package httpapi

import (
	"errors"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
	"net/http"
	"strconv"
	"time"
)

// mountPrivateDocuments shares immutable metadata/version/release HTTP behavior.
//
// With catalogue (the document templates, S06-B) the listing carries each template's catalogue metadata, reading a version records
// when it was opened, and GET and PUT /{id}/catalog read and replace the metadata.
func mountPrivateDocuments[T any](mux *http.ServeMux, base string, authenticate Authenticate, store *privatedocs.Store[T], authorize func(*http.Request, r.Principal, T) error, authorizeBinding func(*http.Request, r.Principal, T) error, binding func(T) any, summary func(T) map[string]any, catalogue ...bool) {
	mountSpacedDocuments(mux, base, ownSpace, authenticate, store, authorize, authorizeBinding, binding, summary, catalogue...)
}

// docSpace is whose designs a mount serves (S12-C): the caller's own, or the tenant's shared space, where every version records who saved it
// and a publication is another person's approval.
type docSpace struct {
	owner func(r.Identity) r.Identity
	team  bool
}

var (
	ownSpace  = docSpace{owner: func(i r.Identity) r.Identity { return i }}
	teamSpace = docSpace{owner: r.TeamOf, team: true}
)

// actor is the caller's mark in the space: its digest in the shared space, nothing in its own.
func (d docSpace) actor(p r.Principal) string {
	if d.team {
		return r.ActorDigest(p.Identity)
	}
	return ""
}

// mountSpacedDocuments is mountPrivateDocuments for a space.
func mountSpacedDocuments[T any](mux *http.ServeMux, base string, space docSpace, authenticate Authenticate, store *privatedocs.Store[T], authorize func(*http.Request, r.Principal, T) error, authorizeBinding func(*http.Request, r.Principal, T) error, binding func(T) any, summary func(T) map[string]any, catalogue ...bool) {
	withCatalog := len(catalogue) > 0 && catalogue[0]
	mux.HandleFunc("GET "+base, authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		all, e := store.List(space.owner(p.Identity))
		if e != nil {
			return e
		}
		var catalogs map[string]privatedocs.Catalog
		if withCatalog {
			if catalogs, e = store.Catalogs(space.owner(p.Identity)); e != nil {
				return e
			}
		}
		out := []map[string]any{}
		for _, entry := range all {
			if e = authorize(req, p, entry.Layout); e != nil {
				if errors.Is(e, r.ErrDenied) || errors.Is(e, r.ErrNotFound) {
					continue
				}
				return e
			}
			item := map[string]any{"id": entry.ID, "version": entry.Version, "binding": binding(entry.Layout)}
			if summary != nil {
				// What a card shows (S06-B part 3), read from the latest version without opening it (opening records last_opened).
				for k, v := range summary(entry.Layout) {
					item[k] = v
				}
				if state, e := store.Release(space.owner(p.Identity), entry.ID); e == nil {
					item["published_version"] = state.PublishedVersion
				}
			}
			if withCatalog {
				item["catalog"] = catalogView(catalogs[entry.ID])
			}
			out = append(out, item)
		}
		return send(w, 200, out)
	}))
	mux.HandleFunc("GET "+base+"/{id}", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		q := req.URL.Query()
		if len(q) != 1 || len(q["version"]) != 1 {
			return r.ErrInvalid
		}
		version, e := strconv.Atoi(q.Get("version"))
		if e != nil {
			return r.ErrInvalid
		}
		entry, e := store.Get(space.owner(p.Identity), req.PathValue("id"), version)
		if e != nil {
			return e
		}
		if e = authorize(req, p, entry.Layout); e != nil {
			return e
		}
		if withCatalog {
			// Opening a version records the time, for the template centre's "recent" list; a failure to record it does not fail the read.
			_, _, _ = store.Opened(space.owner(p.Identity), entry.ID, entry.Version, time.Now())
		}
		out := map[string]any{"id": entry.ID, "version": entry.Version, "config": entry.Layout}
		if space.team {
			// S12-C: whether the caller saved this version (who did is never answered): a publisher cannot approve its own.
			out["saved_by_me"] = entry.SavedBy == space.actor(p)
		}
		return send(w, 200, out)
	}))
	if withCatalog {
		mux.HandleFunc("GET "+base+"/{id}/catalog", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
			c, e := store.Catalog(space.owner(p.Identity), req.PathValue("id"))
			if e != nil {
				return e
			}
			return send(w, 200, catalogView(c))
		}))
		mux.HandleFunc("PUT "+base+"/{id}/catalog", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
			var input struct {
				Domain      *string   `json:"domain"`
				Flow        *string   `json:"flow"`
				Favourite   *bool     `json:"favourite"`
				Archived    *bool     `json:"archived"`
				Collections *[]string `json:"collections"`
				Expected    *int      `json:"expected_revision"`
			}
			if e := decode(w, req, &input, bodySmall); e != nil {
				return e
			}
			if input.Domain == nil || input.Flow == nil || input.Favourite == nil || input.Archived == nil || input.Collections == nil || input.Expected == nil {
				return r.ErrInvalid
			}
			c, e := store.SetCatalog(space.owner(p.Identity), req.PathValue("id"), *input.Expected, privatedocs.Catalog{Domain: *input.Domain, Flow: *input.Flow, Favourite: *input.Favourite, Archived: *input.Archived, Collections: *input.Collections})
			if e != nil {
				return e
			}
			return send(w, 200, catalogView(c))
		}))
	}
	// History/withdrawal authorize the fixed binding; publish/read authorize actual target fields.
	mux.HandleFunc("GET "+base+"/{id}/release", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		entry, e := store.Get(space.owner(p.Identity), req.PathValue("id"), 1)
		if e != nil {
			return e
		}
		if e = authorizeBinding(req, p, entry.Layout); e != nil {
			return e
		}
		state, e := store.Release(space.owner(p.Identity), entry.ID)
		if e != nil {
			return e
		}
		return send(w, 200, state)
	}))
	mux.HandleFunc("GET "+base+"/{id}/published", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		entry, e := store.Published(space.owner(p.Identity), req.PathValue("id"))
		if e != nil {
			return e
		}
		if e = authorize(req, p, entry.Layout); e != nil {
			return e
		}
		return send(w, 200, map[string]any{"id": entry.ID, "version": entry.Version, "config": entry.Layout})
	}))
	mux.HandleFunc("POST "+base+"/{id}/release", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var input struct {
			Action   string `json:"action"`
			Version  int    `json:"version"`
			Expected int    `json:"expected_revision"`
		}
		if e := decode(w, req, &input, bodySmall); e != nil {
			return e
		}
		if input.Action != "publish" && input.Action != "withdraw" {
			return r.ErrInvalid
		}
		target := 1
		if input.Action == "publish" {
			target = input.Version
		}
		entry, e := store.Get(space.owner(p.Identity), req.PathValue("id"), target)
		if e != nil {
			return e
		}
		if input.Action == "publish" {
			if e = authorize(req, p, entry.Layout); e != nil {
				return e
			}
		} else {
			if e = authorizeBinding(req, p, entry.Layout); e != nil {
				return e
			}
		}
		state, e := store.PublishAs(space.owner(space.owner(p.Identity)), space.actor(p), entry.ID, input.Action, input.Version, input.Expected)
		if e != nil {
			return e
		}
		return send(w, 200, state)
	}))
	mux.HandleFunc("POST "+base, authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var input struct {
			ID       string `json:"id"`
			Expected int    `json:"expected_version"`
			Config   T      `json:"config"`
		}
		if e := decode(w, req, &input, bodyDocument); e != nil {
			return e
		}
		if e := authorize(req, p, input.Config); e != nil {
			return e
		}
		entry, e := store.SaveAs(space.owner(space.owner(p.Identity)), space.actor(p), input.ID, input.Expected, input.Config)
		if e != nil {
			return e
		}
		return send(w, 201, map[string]any{"id": entry.ID, "version": entry.Version, "config": entry.Layout})
	}))

}

// catalogView is a template's catalogue metadata as the routes answer it: every key present, last_opened empty until the template is opened.
func catalogView(c privatedocs.Catalog) map[string]any {
	if c.Collections == nil {
		c.Collections = []string{}
	}
	return map[string]any{"domain": c.Domain, "flow": c.Flow, "favourite": c.Favourite, "archived": c.Archived, "collections": c.Collections, "last_opened": c.LastOpened, "revision": c.Revision}
}
