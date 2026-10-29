// Package console serves the standalone local console over the existing API, and the embed pages that show the same views in
// a product's frame (ADR 0006). Business authentication and authorization remain in the API and Service.
package console

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"idp.local/reporting/embedding"
	r "idp.local/reporting/reporting"
)

//go:embed assets/index.html assets/embed.html assets/app.js assets/dashboard.js assets/documents.js assets/canvas.js assets/embed.js assets/styles.css assets/embed.css
var assets embed.FS

// policy is the console's Content-Security-Policy with the given frame-ancestors source list: 'none' for every page of the
// console and for every answer that is not an allowed embed page, one exact origin for an allowed embed page. Nothing else
// differs: no inline script or style, no new source.
func policy(frameAncestors string) string {
	return "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors " + frameAncestors + "; object-src 'none'"
}

// Handler serves the console and the API. The embed entry points answer 403 to everyone: no embedding is configured.
func Handler(api http.Handler) (http.Handler, error) {
	return HandlerWithEmbedding(api, nil)
}

// HandlerWithEmbedding serves the console, the API and the embed entry points /embed/report, /embed/dashboard and
// /embed/document-designer, which answer only a request whose integration and parent origin are on the given configuration (nil
// allows nothing). Every answer is unframeable (X-Frame-Options: DENY and frame-ancestors 'none') except the embed page of an
// allowed origin, which is framed by that one origin and no other.
func HandlerWithEmbedding(api http.Handler, embed *embedding.Config) (http.Handler, error) {
	if api == nil {
		return nil, r.ErrInvalid
	}
	bundled, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil, err
	}
	page, err := fs.ReadFile(bundled, "embed.html")
	if err != nil {
		return nil, err
	}
	files := http.StripPrefix("/console/", http.FileServer(http.FS(bundled)))
	mux := http.NewServeMux()
	mux.Handle("/api/reporting/", api)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/console/", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /console/", func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/console/", "/console/index.html", "/console/app.js", "/console/dashboard.js", "/console/documents.js", "/console/canvas.js", "/console/embed.js", "/console/styles.css", "/console/embed.css":
			w.Header().Set("Content-Security-Policy", policy("'none'"))
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "no-referrer")
			files.ServeHTTP(w, req)
		default:
			http.NotFound(w, req)
		}
	})
	for route, view := range map[string]embedView{"/embed/report": reportView, "/embed/dashboard": dashboardView, "/embed/document-designer": designerView} {
		mux.HandleFunc("GET "+route, serveEmbed(view, embed, page))
	}
	mux.HandleFunc("/embed/", http.NotFound)
	// Unframeable is the default of every answer, the API's, the redirect, a 404 and a 405 included; only serveEmbed lifts it, for
	// one origin, on the one answer that is an embed page.
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		mux.ServeHTTP(w, req)
	}), nil
}

// embedView is what an embed entry point asks of its query besides the integration and the parent origin: the keys that must be
// present, the keys that may be, and the form of each value. Anything else is a 400.
type embedView struct {
	required []string
	optional []string
}

var (
	reportView    = embedView{required: []string{"dataset"}}
	dashboardView = embedView{required: []string{"dashboard"}}
	designerView  = embedView{required: []string{"template"}, optional: []string{"version"}}

	// An identifier of the platform: 32 lower-case hexadecimal digits.
	embedID = regexp.MustCompile(`^[a-f0-9]{32}$`)
	// A dataset is its identifier, optionally with its version as the console writes it (id@version).
	embedDataset = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}(@[A-Za-z0-9][A-Za-z0-9._-]{0,31})?$`)
	// A template version is a whole number from 1 to 20 (the number of versions the store keeps).
	embedVersion = regexp.MustCompile(`^([1-9]|1[0-9]|20)$`)
)

func (v embedView) valid(values url.Values) bool {
	count := 0
	check := func(key string) bool {
		list, present := values[key]
		if !present {
			return false
		}
		count++
		if len(list) != 1 {
			return false
		}
		switch key {
		case "dataset":
			return embedDataset.MatchString(list[0])
		case "dashboard", "template":
			return embedID.MatchString(list[0])
		case "version":
			return embedVersion.MatchString(list[0])
		}
		return false
	}
	for _, key := range v.required {
		if !check(key) {
			return false
		}
	}
	for _, key := range v.optional {
		if _, present := values[key]; present && !check(key) {
			return false
		}
	}
	// integration and parent, which were checked, and the view's own keys: nothing else.
	return len(values) == 2+count
}

// serveEmbed answers one embed entry point. The order is the point: first the origin (the only thing that decides who may frame),
// then the shape of the resource. A refusal of either is a plain-text answer that stays unframeable.
func serveEmbed(view embedView, embed *embedding.Config, page []byte) http.HandlerFunc {
	refuse := func(w http.ResponseWriter, status int) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(http.StatusText(status) + "\n"))
	}
	return func(w http.ResponseWriter, req *http.Request) {
		values, err := url.ParseQuery(req.URL.RawQuery)
		integration, parent := values["integration"], values["parent"]
		if err != nil || len(integration) != 1 || len(parent) != 1 || !embed.Allows(integration[0], parent[0]) {
			// An unknown integration, an origin that is not on its list, a missing or repeated parameter: the same answer.
			refuse(w, http.StatusForbidden)
			return
		}
		if !view.valid(values) {
			refuse(w, http.StatusBadRequest)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", policy(parent[0]))
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// X-Frame-Options cannot name an origin, and a browser that honours it would refuse the frame it is meant to allow.
		h.Del("X-Frame-Options")
		http.ServeContent(w, req, "embed.html", time.Time{}, bytes.NewReader(page))
	}
}
