package doctemplates

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"sync"

	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
)

// Images (ADR 0005, decision 6; S03-M) are the pictures a schema 2.1 template draws: private PNG or JPEG assets of the
// template's binding, stored per identity and dataset beside the backgrounds, immutable, and named by an id and the SHA-256 of
// their bytes. An image is content of the template, never data; it is not a signature or a seal.
const (
	// MaxImageBytes is the most a stored image weighs, as uploaded and as the platform keeps it.
	MaxImageBytes = 512 << 10
	// MaxImagePixels is the most pixels on a side.
	MaxImagePixels = 2000
	// MaxImagesPerDataset is the most images an identity keeps for one binding.
	MaxImagesPerDataset = 16

	FormatPNG  = "png"
	FormatJPEG = "jpeg"
)

// ImageRef names an image by id and digest, as a template holds it.
type ImageRef struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

// ImageSummary is what the listing and the upload say about an image. Its id and digest are those of its Ref.
type ImageSummary struct {
	ID      string  `json:"id"`
	SHA256  string  `json:"sha256"`
	Binding Binding `json:"binding"`
	Format  string  `json:"format"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
}

// Ref is the reference a template names the image by.
func (s ImageSummary) Ref() ImageRef { return ImageRef{ID: s.ID, SHA256: s.SHA256} }

type imageAsset struct {
	Binding Binding `json:"binding"`
	Format  string  `json:"format"`
	Data    []byte  `json:"data"`
	SHA256  string  `json:"sha256"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
}

func validImageRef(ref ImageRef) bool {
	b, e := hex.DecodeString(ref.SHA256)
	return privatedocs.ValidID(ref.ID) && e == nil && len(b) == 32 && hex.EncodeToString(b) == ref.SHA256
}

func (ref *ImageRef) UnmarshalJSON(data []byte) error {
	type wire ImageRef
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	if !validImageRef(ImageRef(value)) {
		return r.ErrInvalid
	}
	*ref = ImageRef(value)
	return nil
}

// Images is the private image store: the same single-writer store as the backgrounds, in a directory of its own.
type Images struct {
	mu    sync.Mutex
	store *privatedocs.Store[imageAsset]
	// Fits, when set, draws a normalized image with the renderer before it is stored, so that one the renderer could not
	// embed (or whose PDF would exceed the artifact limit) is refused at upload with the renderer's error, as a background is.
	// It runs outside the store's lock, with the request's context.
	Fits func(ctx context.Context, data []byte) error
}

// ImageInfo reads what the platform requires of an image from its bytes: the format by content (never by a name or a declared
// type), the pixels on a side and whether the picture is one the renderer takes. A PNG is any the decoder reads; a JPEG is a
// baseline or progressive one of one or three components (grey or YCbCr), not CMYK.
func ImageInfo(data []byte) (format string, w, h int, err error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return "", 0, 0, r.ErrBudget
	}
	var cfg image.Config
	switch {
	case bytes.HasPrefix(data, pngSignature):
		format = FormatPNG
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		format = FormatJPEG
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
		if err == nil && cfg.ColorModel != color.YCbCrModel && cfg.ColorModel != color.GrayModel {
			err = r.ErrInvalid // CMYK needs a colour profile PDF/A-2b would require to be declared
		}
	default:
		return "", 0, 0, r.ErrInvalid
	}
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxImagePixels || cfg.Height > MaxImagePixels {
		return "", 0, 0, r.ErrInvalid
	}
	return format, cfg.Width, cfg.Height, nil
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// normalizeImage decodes an upload completely and returns the bytes the platform keeps. A PNG is decoded and written again
// by the standard encoder, so that chunks the renderer has no use for (text, colour profiles, anything unexpected) are not
// kept; a JPEG keeps its entropy-coded data untouched, so that no pixel changes, and loses every segment but those that
// decide how it is read: the metadata (EXIF, XMP, ICC profiles, comments) is dropped, because an issued document would carry it.
func normalizeImage(data []byte) (out []byte, format string, w, h int, err error) {
	format, w, h, err = ImageInfo(data)
	if err != nil {
		return nil, "", 0, 0, err
	}
	switch format {
	case FormatPNG:
		im, e := png.Decode(bytes.NewReader(data))
		if e != nil {
			return nil, "", 0, 0, r.ErrInvalid
		}
		var buf bytes.Buffer
		if e = png.Encode(&buf, im); e != nil {
			return nil, "", 0, 0, e
		}
		out = buf.Bytes()
	default:
		if out, err = stripJPEG(data); err != nil {
			return nil, "", 0, 0, err
		}
		if _, e := jpeg.Decode(bytes.NewReader(out)); e != nil {
			return nil, "", 0, 0, r.ErrInvalid
		}
	}
	if len(out) > MaxImageBytes {
		return nil, "", 0, 0, r.ErrBudget
	}
	// What is kept must read back as what was checked.
	if f, ow, oh, e := ImageInfo(out); e != nil || f != format || ow != w || oh != h {
		return nil, "", 0, 0, r.ErrInvalid
	}
	return out, format, w, h, nil
}

// stripJPEG copies the segments of a JPEG that decide how its scans are read (the quantization and Huffman tables, the frame
// header, restart interval, JFIF and Adobe markers) and the scans, and drops the other application segments and comments. A
// stream that is not a plain sequence of segments ending in EOI, or whose frame is lossless, arithmetic-coded or hierarchical, is
// refused.
func stripJPEG(data []byte) ([]byte, error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, r.ErrInvalid
	}
	out := []byte{0xFF, 0xD8}
	i := 2
	for i < len(data) {
		if data[i] != 0xFF {
			return nil, r.ErrInvalid
		}
		for i < len(data) && data[i] == 0xFF { // fill bytes
			i++
		}
		if i >= len(data) {
			return nil, r.ErrInvalid
		}
		marker := data[i]
		i++
		switch {
		case marker == 0xD9: // EOI: whatever follows is not part of the image
			return append(out, 0xFF, 0xD9), nil
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD8):
			return nil, r.ErrInvalid // a marker without a segment outside a scan
		}
		if i+2 > len(data) {
			return nil, r.ErrInvalid
		}
		length := int(data[i])<<8 | int(data[i+1])
		if length < 2 || i+length > len(data) {
			return nil, r.ErrInvalid
		}
		segment := data[i : i+length]
		i += length
		switch {
		case marker == 0xC0 || marker == 0xC1 || marker == 0xC2 || marker == 0xC4 || marker == 0xDB || marker == 0xDD || marker == 0xE0 || marker == 0xEE || marker == 0xDA:
			out = append(append(out, 0xFF, marker), segment...)
		case (marker >= 0xC3 && marker <= 0xCF) || marker == 0xDC || marker == 0xDE || marker == 0xDF:
			return nil, r.ErrInvalid // lossless, hierarchical or arithmetic coding, or a marker nothing here uses
		}
		if marker == 0xDA { // the entropy-coded data of the scan runs to the next marker that is not a restart
			start := i
			for i+1 < len(data) && !(data[i] == 0xFF && data[i+1] != 0x00 && !(data[i+1] >= 0xD0 && data[i+1] <= 0xD7) && data[i+1] != 0xFF) {
				i++
			}
			if i+1 >= len(data) {
				return nil, r.ErrInvalid
			}
			out = append(out, data[start:i]...)
		}
	}
	return nil, r.ErrInvalid
}

func OpenImages(dir string) (*Images, error) {
	store, e := privatedocs.Open(dir, validateImage, func(a imageAsset) string {
		return a.Binding.Integration + "\x00" + a.Binding.Dataset.ID + "\x00" + a.Binding.Dataset.Version
	})
	if e != nil {
		return nil, e
	}
	return &Images{store: store}, nil
}

func validateImage(a imageAsset) error {
	if !validBinding(a.Binding) || len(a.Data) == 0 || len(a.Data) > MaxImageBytes {
		return r.ErrInvalid
	}
	format, w, h, e := ImageInfo(a.Data)
	hash := sha256.Sum256(a.Data)
	if e != nil || format != a.Format || w != a.Width || h != a.Height || hex.EncodeToString(hash[:]) != a.SHA256 {
		return r.ErrIntegrity
	}
	return nil
}

func (i *Images) Close() error { i.mu.Lock(); defer i.mu.Unlock(); return i.store.Close() }

// Put checks, normalizes and stores an upload for the owner and the binding, and returns what the platform kept. The format is
// read from the content. At most MaxImagesPerDataset images are kept for an identity and a binding; one more is a budget error.
func (i *Images) Put(ctx context.Context, owner r.Identity, binding Binding, data []byte) (ImageSummary, error) {
	if !owner.Valid() {
		return ImageSummary{}, r.ErrDenied
	}
	if !validBinding(binding) {
		return ImageSummary{}, r.ErrInvalid
	}
	normalized, format, w, h, e := normalizeImage(data)
	if e != nil {
		return ImageSummary{}, e
	}
	// The quota is checked before the renderer is asked, and again before saving.
	if e = i.quota(owner, binding); e != nil {
		return ImageSummary{}, e
	}
	if i.Fits != nil {
		if e = i.Fits(ctx, normalized); e != nil {
			return ImageSummary{}, e
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if e = i.quota(owner, binding); e != nil {
		return ImageSummary{}, e
	}
	hash := sha256.Sum256(normalized)
	digest := hex.EncodeToString(hash[:])
	entry, e := i.store.Save(owner, "", 0, imageAsset{Binding: binding, Format: format, Data: normalized, SHA256: digest, Width: w, Height: h})
	if e != nil {
		return ImageSummary{}, e
	}
	return ImageSummary{ID: entry.ID, SHA256: digest, Binding: binding, Format: format, Width: w, Height: h}, nil
}

func (i *Images) quota(owner r.Identity, binding Binding) error {
	all, e := i.store.List(owner)
	if e != nil {
		return e
	}
	n := 0
	for _, entry := range all {
		if entry.Layout.Binding == binding {
			n++
		}
	}
	if n >= MaxImagesPerDataset {
		return r.ErrBudget
	}
	return nil
}

// List returns the owner's images, oldest first. It is the caller's to drop the ones whose binding the caller may no longer read.
func (i *Images) List(owner r.Identity) ([]ImageSummary, error) {
	all, e := i.store.List(owner)
	if e != nil {
		return nil, e
	}
	out := []ImageSummary{}
	for _, entry := range all {
		v := entry.Layout
		out = append(out, ImageSummary{ID: entry.ID, SHA256: v.SHA256, Binding: v.Binding, Format: v.Format, Width: v.Width, Height: v.Height})
	}
	return out, nil
}

// Resolve returns the bytes of the image a template names, for the owner and the binding of the template, checked against the
// digest it names: another owner's image is not found, another binding's is denied, another digest is an integrity failure.
func (i *Images) Resolve(owner r.Identity, binding Binding, ref ImageRef) ([]byte, error) {
	if !validImageRef(ref) {
		return nil, r.ErrInvalid
	}
	entry, e := i.store.Get(owner, ref.ID, 1)
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
	return append([]byte{}, v.Data...), nil
}

// Content returns an image of the owner by id: its summary and bytes, for the console's canvas. Another owner's image is not found.
func (i *Images) Content(owner r.Identity, id string) (ImageSummary, []byte, error) {
	if !privatedocs.ValidID(id) {
		return ImageSummary{}, nil, r.ErrInvalid
	}
	entry, e := i.store.Get(owner, id, 1)
	if e != nil {
		return ImageSummary{}, nil, e
	}
	v := entry.Layout
	return ImageSummary{ID: entry.ID, SHA256: v.SHA256, Binding: v.Binding, Format: v.Format, Width: v.Width, Height: v.Height}, append([]byte{}, v.Data...), nil
}
