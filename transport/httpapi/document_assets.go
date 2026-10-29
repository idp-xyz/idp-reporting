package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"net/http"
	"strconv"
)

// WithDocumentAssets enables bounded PNG ingestion; raw assets are never public. The private images of schema 2.1 templates are
// mounted with them when the assets hold an image store (document-images: an upload, the listing and the owner-only content).
func WithDocumentAssets(fallback http.Handler, s *r.Service, auth Authenticate, assets *doctemplates.Assets) (http.Handler, error) {
	if fallback == nil || s == nil || auth == nil || assets == nil {
		return nil, r.ErrInvalid
	}
	mux := http.NewServeMux()
	mux.Handle("/", fallback)
	if assets.Images != nil {
		mountDocumentImages(mux, s, auth, assets.Images)
	}
	const base = "/api/reporting/v1/document-backgrounds"
	mux.HandleFunc("POST "+base, authenticated(auth, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var input struct {
			Binding doctemplates.Binding `json:"binding"`
			PNG     string               `json:"png"`
		}
		if e := decode(req, &input); e != nil {
			return e
		}
		if _, e := describeDocumentBinding(req, s, p, input.Binding); e != nil {
			return e
		}
		if len(input.PNG) > base64.StdEncoding.EncodedLen(doctemplates.MaxBackgroundBytes) {
			return r.ErrBudget
		}
		data, e := base64.StdEncoding.Strict().DecodeString(input.PNG)
		if e != nil {
			return r.ErrInvalid
		}
		ref, e := assets.Put(req.Context(), p.Identity, input.Binding, data)
		if e != nil {
			return e
		}
		return send(w, 201, ref)
	}))
	mux.HandleFunc("GET "+base, authenticated(auth, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		all, e := assets.List(p.Identity)
		if e != nil {
			return e
		}
		out := []doctemplates.BackgroundSummary{}
		for _, v := range all {
			if _, e = describeDocumentBinding(req, s, p, v.Binding); e != nil {
				if errors.Is(e, r.ErrDenied) || errors.Is(e, r.ErrNotFound) {
					continue
				}
				return e
			}
			out = append(out, v)
		}
		return send(w, 200, out)
	}))
	return secured(mux), nil
}

func resolveDocumentBackground(p r.Principal, d doctemplates.Document, assets *doctemplates.Assets) ([]byte, error) {
	if d.Background == nil {
		return nil, nil
	}
	if assets == nil {
		// A template naming a background on a deployment without a background store.
		return nil, r.ErrUnsupported
	}
	return assets.Resolve(p.Identity, d.Binding, *d.Background)
}

// resolveDocumentImages returns the bytes of each image element of a 2.1 template by element id, for the caller and the template's
// binding: another caller's image is not found, another binding's is denied, a digest that is not the stored one is an
// integrity failure, and a deployment without an image store cannot draw one.
func resolveDocumentImages(p r.Principal, d doctemplates.Document, assets *doctemplates.Assets) (map[string][]byte, error) {
	var out map[string][]byte
	for _, bi := range d.Items() {
		if bi.Item.Type != doctemplates.TypeImage || bi.Item.Image == nil {
			continue
		}
		if assets == nil || assets.Images == nil {
			return nil, r.ErrUnsupported
		}
		data, e := assets.Images.Resolve(p.Identity, d.Binding, *bi.Item.Image)
		if e != nil {
			return nil, e
		}
		if out == nil {
			out = map[string][]byte{}
		}
		out[bi.Item.ID] = data
	}
	return out, nil
}

// mountDocumentImages mounts the image routes (ADR 0005, S03-M) beside the background routes: an upload (PNG or JPEG, base64, the
// format read from the content), the caller's listing and the content of one image, to its owner only.
func mountDocumentImages(mux *http.ServeMux, s *r.Service, auth Authenticate, images *doctemplates.Images) {
	const base = "/api/reporting/v1/document-images"
	mux.HandleFunc("POST "+base, authenticated(auth, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var input struct {
			Binding doctemplates.Binding `json:"binding"`
			Image   string               `json:"image"`
		}
		if e := decode(req, &input); e != nil {
			return e
		}
		if _, e := describeDocumentBinding(req, s, p, input.Binding); e != nil {
			return e
		}
		if len(input.Image) > base64.StdEncoding.EncodedLen(doctemplates.MaxImageBytes) {
			return r.ErrBudget
		}
		data, e := base64.StdEncoding.Strict().DecodeString(input.Image)
		if e != nil {
			return r.ErrInvalid
		}
		summary, e := images.Put(req.Context(), p.Identity, input.Binding, data)
		if e != nil {
			return e
		}
		return send(w, 201, summary)
	}))
	mux.HandleFunc("GET "+base, authenticated(auth, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		all, e := images.List(p.Identity)
		if e != nil {
			return e
		}
		out := []doctemplates.ImageSummary{}
		for _, v := range all {
			if _, e = describeDocumentBinding(req, s, p, v.Binding); e != nil {
				if errors.Is(e, r.ErrDenied) || errors.Is(e, r.ErrNotFound) {
					continue
				}
				return e
			}
			out = append(out, v)
		}
		return send(w, 200, out)
	}))
	// The bytes of one image, for the console's canvas: the caller's own image only (another caller's is not found), and only while
	// the caller may still read its dataset. It is served as the image it is, with its digest (never cached or sniffed: the wrapper's).
	mux.HandleFunc("GET "+base+"/{id}/content", authenticated(auth, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		summary, data, e := images.Content(p.Identity, req.PathValue("id"))
		if e != nil {
			return e
		}
		if _, e = describeDocumentBinding(req, s, p, summary.Binding); e != nil {
			return e
		}
		media := "image/png"
		if summary.Format == doctemplates.FormatJPEG {
			media = "image/jpeg"
		}
		digest := sha256.Sum256(data)
		w.Header().Set("Content-Type", media)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Content-Disposition", `inline; filename="image.`+map[string]string{"image/png": "png", "image/jpeg": "jpg"}[media]+`"`)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
		w.Header().Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
		w.WriteHeader(200)
		if _, e = w.Write(data); e != nil {
			panic(http.ErrAbortHandler)
		}
		return nil
	}))
}
