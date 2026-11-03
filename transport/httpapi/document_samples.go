package httpapi

import (
	"net/http"

	"idp.local/reporting/doctemplates"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
)

// sampleSummary is a sample as the list answers it: what the template centre shows on a card, with no rows and no document.
type sampleSummary struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Domain      string `json:"domain"`
	Flow        string `json:"flow"`
	Description string `json:"description"`
	Preset      string `json:"preset"`
	Schema      string `json:"schema_version"`
	Elements    int    `json:"elements"`
	Fields      int    `json:"fields"`
}

// sampleView is one sample in full: the document with its field slots and the rows its preview is made from.
type sampleView struct {
	sampleSummary
	FieldSlots []doctemplates.SampleField `json:"field_slots"`
	Rows       []map[string]string        `json:"rows"`
	Document   doctemplates.Document      `json:"document"`
}

func summarize(s doctemplates.Sample) sampleSummary {
	return sampleSummary{Kind: s.Kind, Name: s.Name, Domain: s.Domain, Flow: s.Flow, Description: s.Description, Preset: s.Preset(), Schema: s.Document.Schema, Elements: s.ElementCount(), Fields: len(s.Fields)}
}

// mountSamples mounts the sample pack (S06-B) on a mux of its own (WithDocumentTemplates): the samples are readable by any signed-in
// identity and never written. A preview renders a sample's own rows, marked as every preview is, with no dataset and no authorization
// beyond the identity; a copy binds the sample to the caller's dataset by a mapping, validates the result exactly as a save does (the same
// authorization, the same store, so the private store's cap applies) and answers the saved template.
func mountSamples(mux *http.ServeMux, base string, authenticate Authenticate, pack []doctemplates.Sample, s *r.Service, store *doctemplates.Store, renderer DocumentRenderer, assets *doctemplates.Assets,
	authorize func(*http.Request, r.Principal, doctemplates.Document) error, describeBinding func(*http.Request, r.Principal, doctemplates.Binding) (r.Dataset, error)) {
	find := func(req *http.Request) (doctemplates.Sample, error) {
		for _, sample := range pack {
			if sample.Kind == req.PathValue("kind") {
				return sample, nil
			}
		}
		return doctemplates.Sample{}, r.ErrNotFound
	}
	mux.HandleFunc("GET "+base+"/samples", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		out := make([]sampleSummary, 0, len(pack))
		for _, sample := range pack {
			out = append(out, summarize(sample))
		}
		return send(w, 200, out)
	}))
	mux.HandleFunc("GET "+base+"/samples/{kind}", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		sample, e := find(req)
		if e != nil {
			return e
		}
		return send(w, 200, sampleView{sampleSummary: summarize(sample), FieldSlots: sample.Fields, Rows: sample.Rows, Document: sample.Document})
	}))
	if renderer != nil {
		mux.HandleFunc("POST "+base+"/samples/{kind}/previews", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
			sample, e := find(req)
			if e != nil {
				return e
			}
			// No body, or {}: a preview of a sample has nothing to choose.
			if req.ContentLength != 0 {
				var input struct{}
				if e = decode(w, req, &input, bodySmall); e != nil {
					return e
				}
			}
			values, table, e := sample.BindPreview()
			if e != nil {
				return e
			}
			var pdf []byte
			if sample.Document.Coded() {
				rr, ok := renderer.(ResolvedRenderer)
				if !ok {
					return r.ErrUnsupported
				}
				pdf, e = rr.RenderWith(req.Context(), sample.Document, values, nil, table, doctemplates.Resolved{})
			} else {
				pdf, e = renderer.Render(req.Context(), sample.Document, values, nil, table)
			}
			if e != nil {
				return e
			}
			if len(pdf) > r.MaxArtifactBytes || len(pdf) < 8 || string(pdf[:5]) != "%PDF-" {
				return r.ErrIntegrity
			}
			w.Header().Set("X-Sample-Kind", sample.Kind)
			return sendPreviewPDF(w, pdf)
		}))
	}
	mux.HandleFunc("POST "+base+"/samples/{kind}/copy", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		sample, e := find(req)
		if e != nil {
			return e
		}
		var input struct {
			Dataset *struct {
				Integration string `json:"integration_id"`
				ID          string `json:"id"`
				Version     string `json:"version"`
			} `json:"dataset"`
			Name    string                               `json:"name"`
			Mapping map[string]doctemplates.FieldMapping `json:"mapping"`
		}
		if e = decode(w, req, &input, bodyDocument); e != nil {
			return e
		}
		if input.Dataset == nil || input.Mapping == nil || len(input.Mapping) > doctemplates.MaxSampleFields {
			return r.ErrInvalid
		}
		binding := doctemplates.Binding{Integration: input.Dataset.Integration, Dataset: r.Ref{ID: input.Dataset.ID, Version: input.Dataset.Version}}
		// The dataset must be one the caller may read, as for a saved template.
		if _, e = describeBinding(req, p, binding); e != nil {
			return e
		}
		name := input.Name
		if name == "" {
			name = sample.Name
		}
		for _, m := range input.Mapping {
			if (m.Column != nil && (*m.Column == "" || len(*m.Column) > 128)) || (m.Literal != nil && len([]rune(*m.Literal)) > doctemplates.MaxWrapTextRunes) {
				return r.ErrInvalid
			}
		}
		doc, e := sample.Bind(binding, name, input.Mapping)
		if e != nil {
			return e
		}
		// From here the save path: the template is authorized for the dataset as a saved one is (a column the dataset lacks is denied, a
		// group field that is a metric unsupported), then stored as version 1 of a new template, which the private store's cap bounds.
		if e = authorize(req, p, doc); e != nil {
			return e
		}
		entry, e := store.Save(p.Identity, "", 0, doc)
		if e != nil {
			return e
		}
		// The copy starts in its sample's domain and flow in the template centre; the template is saved whether or not this is.
		_, _ = store.SetCatalog(p.Identity, entry.ID, 0, privatedocs.Catalog{Domain: sample.Domain, Flow: sample.Flow, Collections: []string{}})
		return send(w, 201, map[string]any{"id": entry.ID, "version": entry.Version, "config": entry.Layout})
	}))
}
