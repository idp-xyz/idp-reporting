package doctemplates_test

import (
	"errors"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
	"strings"
	"testing"
)

// countedBackend counts the reads of an owner's document.
type countedBackend struct {
	privatedocs.Backend
	loads *int
}

func (b countedBackend) Load(owner r.Identity) ([]byte, error) {
	*b.loads++
	return b.Backend.Load(owner)
}

// The index gives Resolve's verdict on every kind of reference, for backgrounds and images, and reads each store once however many
// references it is asked about; an index that is never asked reads nothing.
func TestTheAssetIndexGivesResolvesVerdictsFromOneReadOfEachStore(t *testing.T) {
	var backgroundLoads, imageLoads int
	a, e := doctemplates.OpenAssetsBackend(countedBackend{privatedocs.NewMemoryBackend(), &backgroundLoads}, countedBackend{privatedocs.NewMemoryBackend(), &imageLoads})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	other := owner
	other.Subject = "another"
	binding := doctemplates.Binding{Integration: "finance", Dataset: r.Ref{ID: "d", Version: "v1"}}
	elsewhere := binding
	elsewhere.Dataset.Version = "v2"
	var backgrounds []doctemplates.BackgroundRef
	var images []doctemplates.ImageRef
	for range 3 {
		ref, e := a.Put(t.Context(), owner, binding, samplePNG(t, 210, 297))
		if e != nil {
			t.Fatal(e)
		}
		backgrounds = append(backgrounds, ref)
		summary, e := a.Images.Put(t.Context(), owner, binding, samplePNG(t, 40, 20))
		if e != nil {
			t.Fatal(e)
		}
		images = append(images, summary.Ref())
	}
	same := func(got, want error) bool {
		return (got == nil && want == nil) || (want != nil && errors.Is(got, want))
	}
	wrong, unknown, malformed := strings.Repeat("a", 64), strings.Repeat("0", 31)+"1", "not-an-id"
	type reference struct {
		name    string
		binding doctemplates.Binding
		id, sum string
		want    error
	}
	cases := func(id, sum string) []reference {
		return []reference{
			{"the owner's", binding, id, sum, nil},
			{"another digest", binding, id, wrong, r.ErrIntegrity},
			{"another binding", elsewhere, id, sum, r.ErrDenied},
			{"an unknown id", binding, unknown, sum, r.ErrNotFound},
			{"a malformed id", binding, malformed, sum, r.ErrInvalid},
			{"a malformed digest", binding, id, "xyz", r.ErrInvalid},
		}
	}
	for _, who := range []struct {
		name  string
		owner r.Identity
	}{{"the owner", owner}, {"another person", other}} {
		// Resolve's verdicts first (each reads its store), then the index's with the reads counted.
		type verdict struct {
			name string
			want error
			got  func(*doctemplates.AssetIndex) error
		}
		var verdicts []verdict
		for i := range backgrounds {
			for _, c := range cases(backgrounds[i].ID, backgrounds[i].SHA256) {
				ref := doctemplates.BackgroundRef{ID: c.id, SHA256: c.sum}
				_, want := a.Resolve(who.owner, c.binding, ref)
				if who.owner == owner && !same(want, c.want) {
					t.Fatalf("Resolve of a background, %s: %v", c.name, want)
				}
				verdicts = append(verdicts, verdict{"background, " + c.name, want, func(x *doctemplates.AssetIndex) error { return x.Background(c.binding, ref) }})
			}
			for _, c := range cases(images[i].ID, images[i].SHA256) {
				ref := doctemplates.ImageRef{ID: c.id, SHA256: c.sum}
				_, want := a.Images.Resolve(who.owner, c.binding, ref)
				if who.owner == owner && !same(want, c.want) {
					t.Fatalf("Resolve of an image, %s: %v", c.name, want)
				}
				verdicts = append(verdicts, verdict{"image, " + c.name, want, func(x *doctemplates.AssetIndex) error { return x.Image(c.binding, ref) }})
			}
		}
		backgroundLoads, imageLoads = 0, 0
		index := a.Index(who.owner)
		if backgroundLoads != 0 || imageLoads != 0 {
			t.Fatalf("%s: an index read %d and %d times before it was asked", who.name, backgroundLoads, imageLoads)
		}
		for _, v := range verdicts {
			if got := v.got(index); !same(got, v.want) {
				t.Errorf("%s: %s: the index says %v, Resolve %v", who.name, v.name, got, v.want)
			}
		}
		if backgroundLoads != 1 || imageLoads != 1 {
			t.Errorf("%s: %d verdicts read the backgrounds %d times and the images %d times", who.name, len(verdicts), backgroundLoads, imageLoads)
		}
	}
	// A malformed reference is refused without a read.
	backgroundLoads, imageLoads = 0, 0
	index := a.Index(owner)
	if e = index.Background(binding, doctemplates.BackgroundRef{ID: malformed, SHA256: wrong}); !errors.Is(e, r.ErrInvalid) || backgroundLoads != 0 {
		t.Fatalf("%v after %d reads", e, backgroundLoads)
	}
	if e = index.Image(binding, doctemplates.ImageRef{ID: malformed, SHA256: wrong}); !errors.Is(e, r.ErrInvalid) || imageLoads != 0 {
		t.Fatalf("%v after %d reads", e, imageLoads)
	}
}
