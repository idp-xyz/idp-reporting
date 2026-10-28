// Package console serves the standalone local console over the existing API.
// Business authentication and authorization remain in the API and Service.
package console

import (
	"embed"
	"io/fs"
	"net/http"

	r "idp.local/reporting/reporting"
)

//go:embed assets/index.html assets/app.js assets/dashboard.js assets/documents.js assets/styles.css
var assets embed.FS

func Handler(api http.Handler) (http.Handler, error) {
	if api == nil {
		return nil, r.ErrInvalid
	}
	bundled, err := fs.Sub(assets, "assets")
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
		case "/console/", "/console/index.html", "/console/app.js", "/console/dashboard.js", "/console/documents.js", "/console/styles.css":
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "no-referrer")
			files.ServeHTTP(w, req)
		default:
			http.NotFound(w, req)
		}
	})
	return mux, nil
}
