package doctemplates

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
	"image/png"
	"math"
	"sync"
)

const MaxBackgroundBytes = 512 << 10

type BackgroundRef struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}
type BackgroundSummary struct {
	BackgroundRef
	Binding Binding `json:"binding"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
}
type backgroundAsset struct {
	Binding Binding `json:"binding"`
	PNG     []byte  `json:"png"`
	SHA256  string  `json:"sha256"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
}

// Assets exposes immutable, private PNGs over the existing single-writer store.
type Assets struct {
	mu    sync.Mutex
	store *privatedocs.Store[backgroundAsset]
	// Fits, when set, checks a normalized background with the renderer before it is stored,
	// so that one the previews could not use (for instance, one whose PDF would exceed the
	// artifact limit) is refused at upload with the renderer's error.
	Fits func(png []byte) error
}

func validBinding(b Binding) bool {
	return b.Integration != "" && len(b.Integration) <= 128 && b.Dataset.ID != "" && len(b.Dataset.ID) <= 128 && b.Dataset.Version != "" && len(b.Dataset.Version) <= 128
}
func validBackgroundRef(ref BackgroundRef) bool {
	b, e := hex.DecodeString(ref.SHA256)
	return privatedocs.ValidID(ref.ID) && e == nil && len(b) == 32 && hex.EncodeToString(b) == ref.SHA256
}
func backgroundSize(w, h int) bool {
	return w >= 210 && w <= 1240 && h >= 297 && h <= 1754 && math.Abs(float64(w)/float64(h)-210.0/297) < 0.005
}
func normalizePNG(data []byte) ([]byte, int, int, error) {
	if len(data) == 0 || len(data) > MaxBackgroundBytes {
		return nil, 0, 0, r.ErrBudget
	}
	cfg, e := png.DecodeConfig(bytes.NewReader(data))
	if e != nil || !backgroundSize(cfg.Width, cfg.Height) {
		return nil, 0, 0, r.ErrInvalid
	}
	im, e := png.Decode(bytes.NewReader(data))
	if e != nil {
		return nil, 0, 0, r.ErrInvalid
	}
	var out bytes.Buffer
	if e = png.Encode(&out, im); e != nil {
		return nil, 0, 0, e
	}
	if out.Len() > MaxBackgroundBytes {
		return nil, 0, 0, r.ErrBudget
	}
	return out.Bytes(), cfg.Width, cfg.Height, nil
}
func validateAsset(a backgroundAsset) error {
	if !validBinding(a.Binding) || len(a.PNG) == 0 || len(a.PNG) > MaxBackgroundBytes {
		return r.ErrInvalid
	}
	cfg, e := png.DecodeConfig(bytes.NewReader(a.PNG))
	hash := sha256.Sum256(a.PNG)
	if e != nil || !backgroundSize(a.Width, a.Height) || cfg.Width != a.Width || cfg.Height != a.Height || hex.EncodeToString(hash[:]) != a.SHA256 {
		return r.ErrIntegrity
	}
	return nil
}
func OpenAssets(dir string) (*Assets, error) {
	store, e := privatedocs.Open(dir, validateAsset, func(a backgroundAsset) string {
		return a.Binding.Integration + "\x00" + a.Binding.Dataset.ID + "\x00" + a.Binding.Dataset.Version
	})
	if e != nil {
		return nil, e
	}
	return &Assets{store: store}, nil
}
func (a *Assets) Close() error { a.mu.Lock(); defer a.mu.Unlock(); return a.store.Close() }
func (a *Assets) Put(owner r.Identity, binding Binding, data []byte) (BackgroundRef, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !owner.Valid() {
		return BackgroundRef{}, r.ErrDenied
	}
	if !validBinding(binding) {
		return BackgroundRef{}, r.ErrInvalid
	}
	normalized, w, h, e := normalizePNG(data)
	if e != nil {
		return BackgroundRef{}, e
	}
	if a.Fits != nil {
		if e = a.Fits(normalized); e != nil {
			return BackgroundRef{}, e
		}
	}
	all, e := a.store.List(owner)
	if e != nil {
		return BackgroundRef{}, e
	}
	if len(all) >= 8 {
		return BackgroundRef{}, r.ErrBudget
	}
	hash := sha256.Sum256(normalized)
	digest := hex.EncodeToString(hash[:])
	entry, e := a.store.Save(owner, "", 0, backgroundAsset{Binding: binding, PNG: normalized, SHA256: digest, Width: w, Height: h})
	if e != nil {
		return BackgroundRef{}, e
	}
	return BackgroundRef{ID: entry.ID, SHA256: digest}, nil
}
func (a *Assets) List(owner r.Identity) ([]BackgroundSummary, error) {
	all, e := a.store.List(owner)
	if e != nil {
		return nil, e
	}
	out := []BackgroundSummary{}
	for _, entry := range all {
		v := entry.Layout
		out = append(out, BackgroundSummary{BackgroundRef: BackgroundRef{ID: entry.ID, SHA256: v.SHA256}, Binding: v.Binding, Width: v.Width, Height: v.Height})
	}
	return out, nil
}
func (a *Assets) Resolve(owner r.Identity, binding Binding, ref BackgroundRef) ([]byte, error) {
	if !validBackgroundRef(ref) {
		return nil, r.ErrInvalid
	}
	entry, e := a.store.Get(owner, ref.ID, 1)
	if e != nil {
		return nil, e
	}
	v := entry.Layout
	if v.Binding != binding {
		return nil, r.ErrDenied
	}
	if v.SHA256 != ref.SHA256 {
		return nil, r.ErrIntegrity
	}
	return append([]byte{}, v.PNG...), nil
}
