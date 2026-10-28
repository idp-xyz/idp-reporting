package doctemplates_test

import (
	"bytes"
	"context"
	"errors"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"testing"
)

func samplePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	im.Set(20, 40, color.NRGBA{R: 200, A: 255})
	var b bytes.Buffer
	if e := png.Encode(&b, im); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestPrivateBackgroundAssets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "assets")
	a, e := doctemplates.OpenAssets(dir)
	if e != nil {
		t.Fatal(e)
	}
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	binding := doctemplates.Binding{Integration: "finance", Dataset: r.Ref{ID: "d", Version: "v1"}}
	ref, e := a.Put(t.Context(), owner, binding, samplePNG(t, 210, 297))
	if e != nil {
		t.Fatal(e)
	}
	data, e := a.Resolve(owner, binding, ref)
	if e != nil || len(data) == 0 {
		t.Fatal(e)
	}
	other := owner
	other.Subject = "another"
	if _, e = a.Resolve(other, binding, ref); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("owner leak", e)
	}
	other = owner
	other.Tenant = "another"
	if _, e = a.Resolve(other, binding, ref); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("tenant leak", e)
	}
	other = owner
	other.Issuer = "another"
	if _, e = a.Resolve(other, binding, ref); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("issuer leak", e)
	}
	bad := ref
	bad.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, e = a.Resolve(owner, binding, bad); !errors.Is(e, r.ErrIntegrity) {
		t.Fatal("digest", e)
	}
	b := binding
	b.Integration = "other"
	if _, e = a.Resolve(owner, b, ref); !errors.Is(e, r.ErrDenied) {
		t.Fatal("binding", e)
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	a, e = doctemplates.OpenAssets(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	again, e := a.Resolve(owner, binding, ref)
	if e != nil || !bytes.Equal(data, again) {
		t.Fatal("reopen", e)
	}
	for i := 1; i < 8; i++ {
		if _, e = a.Put(t.Context(), owner, binding, samplePNG(t, 210, 297)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = a.Put(t.Context(), owner, binding, data); !errors.Is(e, r.ErrBudget) {
		t.Fatal("quota", e)
	}
	list, e := a.List(owner)
	if e != nil || len(list) != 8 {
		t.Fatal("list", e)
	}
}
func TestBackgroundPNGNormalizationAndBudgets(t *testing.T) {
	assets, e := doctemplates.OpenAssets(filepath.Join(t.TempDir(), "assets"))
	if e != nil {
		t.Fatal(e)
	}
	defer assets.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	binding := doctemplates.Binding{Integration: "finance", Dataset: r.Ref{ID: "d", Version: "v1"}}
	for _, data := range [][]byte{[]byte("%PDF-1.7"), samplePNG(t, 209, 297), samplePNG(t, 210, 2980), samplePNG(t, 300, 300), make([]byte, doctemplates.MaxBackgroundBytes+1), samplePNG(t, 210, 297)[:40]} {
		if _, e := assets.Put(t.Context(), owner, binding, data); e == nil {
			t.Fatal("accepted invalid image")
		}
	}
	raw := samplePNG(t, 210, 297)
	raw = append(raw, []byte("discard trailing metadata")...)
	ref, e := assets.Put(t.Context(), owner, binding, raw)
	if e != nil {
		t.Fatal(e)
	}
	normalized, e := assets.Resolve(owner, binding, ref)
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := png.DecodeConfig(bytes.NewReader(normalized))
	if e != nil || cfg.Width != 210 || cfg.Height != 297 || bytes.Contains(normalized, []byte("discard")) {
		t.Fatal("normalization", e)
	}
}

func TestBackgroundUploadRunsTheFitCheck(t *testing.T) {
	a, e := doctemplates.OpenAssets(filepath.Join(t.TempDir(), "assets"))
	if e != nil {
		t.Fatal(e)
	}
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	binding := doctemplates.Binding{Integration: "finance", Dataset: r.Ref{ID: "d", Version: "v1"}}
	checked := 0
	a.Fits = func(ctx context.Context, png []byte) error {
		checked++
		if ctx.Err() != nil {
			t.Error("the check got an ended context")
		}
		if !bytes.HasPrefix(png, []byte("\x89PNG")) {
			t.Error("the check did not get the normalized PNG")
		}
		return r.ErrBudget
	}
	// A background the renderer cannot use is refused with the renderer's error and not stored.
	if _, e = a.Put(t.Context(), owner, binding, samplePNG(t, 210, 297)); !errors.Is(e, r.ErrBudget) || checked != 1 {
		t.Fatalf("%v after %d checks", e, checked)
	}
	if all, e := a.List(owner); e != nil || len(all) != 0 {
		t.Fatalf("stored anyway: %v %v", all, e)
	}
	a.Fits = func(context.Context, []byte) error { checked++; return nil }
	if _, e = a.Put(t.Context(), owner, binding, samplePNG(t, 210, 297)); e != nil || checked != 2 {
		t.Fatalf("%v after %d checks", e, checked)
	}
	// An owner at the quota is refused before the renderer is asked.
	for range 7 {
		if _, e = a.Put(t.Context(), owner, binding, samplePNG(t, 210, 297)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = a.Put(t.Context(), owner, binding, samplePNG(t, 210, 297)); !errors.Is(e, r.ErrBudget) || checked != 9 {
		t.Fatalf("ninth background: %v after %d checks", e, checked)
	}
}
