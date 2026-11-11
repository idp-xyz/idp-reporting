package httpapi

import (
	"context"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"net/http"
)

// A listing (of layouts, dashboards, templates, backgrounds, images) authorizes every entry it shows, and the entries have much in
// common: the source's catalog of their integration, and, for templates, the owner's backgrounds and images. A listing asks each of
// these once and answers every entry from it; before, it asked the source for the catalog once per entry (with the http adapter, a
// request to the product each) and read the owner's whole background or image store once per reference. What is shared is the answer of
// one moment to one principal, which is what each entry would have been given; nothing of it outlives the request, so the next
// request asks the source again, as every read does.

type listingKey struct{}

// listingShare is what one listing request has asked so far. It is used by the one goroutine that serves the request.
type listingShare struct {
	catalogs map[string]catalogAnswer
	indexes  map[r.Identity]*doctemplates.AssetIndex
}

type catalogAnswer struct {
	datasets []r.Dataset
	err      error
}

// listing marks the request as a listing: the authorizations made with the request it returns share their catalog answers and their
// reads of the owner's assets.
func listing(req *http.Request) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), listingKey{}, &listingShare{catalogs: map[string]catalogAnswer{}, indexes: map[r.Identity]*doctemplates.AssetIndex{}}))
}

func shareOf(req *http.Request) *listingShare {
	share, _ := req.Context().Value(listingKey{}).(*listingShare)
	return share
}

// catalogOf is the source's catalog of an integration for the principal: Service.Catalog, asked once per integration inside a listing.
// A refusal is shared like an answer: the entries of an integration the principal may not read are all refused by the one refusal.
func catalogOf(req *http.Request, s *r.Service, p r.Principal, integration string) ([]r.Dataset, error) {
	share := shareOf(req)
	if share == nil {
		return s.Catalog(req.Context(), p, integration)
	}
	if answer, ok := share.catalogs[integration]; ok {
		return answer.datasets, answer.err
	}
	datasets, e := s.Catalog(req.Context(), p, integration)
	share.catalogs[integration] = catalogAnswer{datasets, e}
	return datasets, e
}

// checkDocumentAssets is the verdict of resolveDocumentBackground and resolveDocumentImages on a template, for an authorization, which
// needs no bytes. Inside a listing the references are checked against one index of the caller's assets (doctemplates.AssetIndex: the
// same verdicts, each store read once); elsewhere they are resolved as before.
func checkDocumentAssets(req *http.Request, p r.Principal, d doctemplates.Document, assets *doctemplates.Assets) error {
	share := shareOf(req)
	if share == nil {
		if _, e := resolveDocumentBackground(p, d, assets); e != nil {
			return e
		}
		_, e := resolveDocumentImages(p, d, assets)
		return e
	}
	index := func() *doctemplates.AssetIndex {
		if share.indexes[p.Identity] == nil {
			share.indexes[p.Identity] = assets.Index(p.Identity)
		}
		return share.indexes[p.Identity]
	}
	if ref := d.BackgroundRefOf(); ref != nil {
		if assets == nil {
			return r.ErrUnsupported
		}
		if e := index().Background(d.Binding, *ref); e != nil {
			return e
		}
	}
	for _, bi := range d.Items() {
		if bi.Item.Type != doctemplates.TypeImage || bi.Item.Image == nil {
			continue
		}
		if assets == nil || assets.Images == nil {
			return r.ErrUnsupported
		}
		if e := index().Image(d.Binding, *bi.Item.Image); e != nil {
			return e
		}
	}
	return nil
}
