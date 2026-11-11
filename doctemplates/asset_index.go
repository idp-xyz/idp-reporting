package doctemplates

import (
	r "idp.local/reporting/reporting"
)

// AssetIndex is what Resolve checks of an owner's backgrounds and images (the id, the binding, the digest), without their bytes, from
// one read of each store, made when it is first asked. A listing checks every template it shows against one index, where resolving each
// reference reads and checks the owner's whole store again. An index is for one request: it does not see an upload made after its read.
// It is not safe for concurrent use.
type AssetIndex struct {
	assets *Assets
	owner  r.Identity

	backgrounds     map[string]BackgroundSummary
	backgroundsRead bool
	backgroundsErr  error

	images     map[string]ImageSummary
	imagesRead bool
	imagesErr  error
}

// Index is the index of the owner's backgrounds and images. Nothing is read until a reference is checked.
func (a *Assets) Index(owner r.Identity) *AssetIndex { return &AssetIndex{assets: a, owner: owner} }

// Background is Resolve's verdict on a background reference of a template with the binding, without the bytes: an invalid reference,
// another owner's background (not found), another binding's (denied), another digest (an integrity failure), or the store's error.
func (x *AssetIndex) Background(binding Binding, ref BackgroundRef) error {
	if !validBackgroundRef(ref) {
		return r.ErrInvalid
	}
	if !x.backgroundsRead {
		x.backgroundsRead = true
		all, e := x.assets.List(x.owner)
		x.backgroundsErr = e
		x.backgrounds = make(map[string]BackgroundSummary, len(all))
		for _, v := range all {
			x.backgrounds[v.ID] = v
		}
	}
	if x.backgroundsErr != nil {
		return x.backgroundsErr
	}
	v, ok := x.backgrounds[ref.ID]
	switch {
	case !ok:
		return r.ErrNotFound
	case v.Binding != binding:
		return r.ErrDenied
	case v.SHA256 != ref.SHA256:
		return r.ErrIntegrity
	}
	return nil
}

// Image is Images.Resolve's verdict on an image reference of a template with the binding, without the bytes. The assets must hold an
// image store.
func (x *AssetIndex) Image(binding Binding, ref ImageRef) error {
	if !validImageRef(ref) {
		return r.ErrInvalid
	}
	if !x.imagesRead {
		x.imagesRead = true
		all, e := x.assets.Images.List(x.owner)
		x.imagesErr = e
		x.images = make(map[string]ImageSummary, len(all))
		for _, v := range all {
			x.images[v.ID] = v
		}
	}
	if x.imagesErr != nil {
		return x.imagesErr
	}
	v, ok := x.images[ref.ID]
	switch {
	case !ok:
		return r.ErrNotFound
	case v.Binding != binding:
		return r.ErrDenied
	case v.SHA256 != ref.SHA256:
		return r.ErrIntegrity
	}
	return nil
}
