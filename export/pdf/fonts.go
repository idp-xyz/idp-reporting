package pdf

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	r "idp.local/reporting/reporting"
)

const (
	maxFontFiles     = 16
	maxFontFileBytes = 64 << 20
	maxFontBytes     = 160 << 20
)

// fontExtensions are the files Typst loads from a font directory.
var fontExtensions = map[string]bool{".ttf": true, ".otf": true, ".ttc": true, ".otc": true}

type face struct {
	font *sfnt.Font
	upem float64
}

// family holds the faces Typst can pick for one family name: weight 400 picks Regular and
// weight 700 picks Bold, or Regular when the family has no Bold.
type family struct {
	name          string
	regular, bold *face
}

func (f family) face(bold bool) *face {
	if bold && f.bold != nil {
		return f.bold
	}
	return f.regular
}

type fontSet struct {
	families []family
}

// names returns the family names in the order Typst tries them.
func (s *fontSet) names() []string {
	out := make([]string, len(s.families))
	for i, f := range s.families {
		out[i] = f.name
	}
	return out
}

// loadFonts parses every font file in dir. Typst is given the same directory and nothing else,
// so the fonts measured and checked here are exactly the fonts it may use. The directory must
// hold only regular files and only Regular and Bold faces, so that Typst's choice of face for
// each character is the one checked here.
//
// It returns the identity of the directory and of each font file, taken before reading them
// and confirmed after, so that a later change is detected before each render.
func loadFonts(dir string, order []string) (*fontSet, map[string]fileID, error) {
	ids := map[string]fileID{}
	dirID, err := identity(dir)
	if err != nil {
		return nil, nil, err
	}
	ids[dir] = dirID
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("pdf: font directory: %w", err)
	}
	byName := map[string]*family{}
	var found []string
	files := 0
	total := 0
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if !e.Type().IsRegular() {
			return nil, nil, fmt.Errorf("pdf: font directory may hold only regular files: %s", e.Name())
		}
		if !fontExtensions[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		if files++; files > maxFontFiles {
			return nil, nil, fmt.Errorf("pdf: more than %d font files", maxFontFiles)
		}
		info, err := e.Info()
		if err != nil {
			return nil, nil, err
		}
		if info.Size() > maxFontFileBytes || total+int(info.Size()) > maxFontBytes {
			return nil, nil, fmt.Errorf("pdf: font files are too large")
		}
		before, err := identity(path)
		if err != nil {
			return nil, nil, err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		if after, err := identity(path); err != nil || after != before || int64(len(b)) != info.Size() {
			return nil, nil, fmt.Errorf("pdf: %s changed while it was read", e.Name())
		}
		ids[path] = before
		total += len(b)
		c, err := sfnt.ParseCollection(b)
		if err != nil {
			return nil, nil, fmt.Errorf("pdf: %s: %w", e.Name(), err)
		}
		for i := range c.NumFonts() {
			f, err := c.Font(i)
			if err != nil {
				return nil, nil, fmt.Errorf("pdf: %s: %w", e.Name(), err)
			}
			name, style, err := faceNames(f)
			if err != nil {
				return nil, nil, fmt.Errorf("pdf: %s: %w", e.Name(), err)
			}
			fam := byName[name]
			if fam == nil {
				fam = &family{name: name}
				byName[name] = fam
				found = append(found, name)
			}
			fc := &face{font: f, upem: float64(f.UnitsPerEm())}
			switch {
			case style == "Regular" && fam.regular == nil:
				fam.regular = fc
			case style == "Bold" && fam.bold == nil:
				fam.bold = fc
			default:
				return nil, nil, fmt.Errorf("pdf: %s: face %q %q is not a single Regular or Bold face", e.Name(), name, style)
			}
		}
	}
	if len(order) == 0 {
		order = slices.Sorted(slices.Values(found))
	}
	set := &fontSet{}
	for _, name := range order {
		fam := byName[name]
		if fam == nil || fam.regular == nil || slices.ContainsFunc(set.families, func(f family) bool { return f.name == name }) {
			return nil, nil, fmt.Errorf("pdf: font family %q has no Regular face in the font directory or is listed twice", name)
		}
		set.families = append(set.families, *fam)
	}
	if len(set.families) == 0 {
		return nil, nil, errors.New("pdf: the font directory holds no fonts")
	}
	if after, err := identity(dir); err != nil || after != dirID {
		return nil, nil, errors.New("pdf: the font directory changed while it was read")
	}
	return set, ids, nil
}

// faceNames returns the family and style names Typst uses: the typographic names when the font
// has them, else the legacy ones.
func faceNames(f *sfnt.Font) (string, string, error) {
	var b sfnt.Buffer
	name, err := f.Name(&b, sfnt.NameIDTypographicFamily)
	style, err2 := f.Name(&b, sfnt.NameIDTypographicSubfamily)
	if err != nil || err2 != nil || name == "" {
		if name, err = f.Name(&b, sfnt.NameIDFamily); err != nil {
			return "", "", err
		}
		if style, err = f.Name(&b, sfnt.NameIDSubfamily); err != nil {
			return "", "", err
		}
	}
	if name == "" {
		return "", "", errors.New("font has no family name")
	}
	return name, style, nil
}

// advance returns the advance in ems of ch in the face Typst uses for it, and false when no
// configured font maps ch.
type advance func(ch rune, bold bool) (float64, bool, error)

// lookup finds the face Typst uses for each character: Typst tries the families in order and
// takes the first whose face of the wanted weight maps the character.
func (s *fontSet) lookup() advance {
	var buf sfnt.Buffer
	return func(ch rune, bold bool) (float64, bool, error) {
		for _, fam := range s.families {
			fc := fam.face(bold)
			x, err := fc.font.GlyphIndex(&buf, ch)
			if err != nil {
				return 0, false, err
			}
			if x == 0 {
				continue
			}
			adv, err := fc.font.GlyphAdvance(&buf, x, fixed.I(int(fc.upem)), font.HintingNone)
			if err != nil {
				return 0, false, err
			}
			return float64(adv) / 64 / fc.upem, true, nil
		}
		return 0, false, nil
	}
}

type glyphKey struct {
	r    rune
	bold bool
}

// measurer measures text for one document. A character that no configured font maps would be
// dropped or drawn as a blank box by Typst, so it is refused. Not safe for concurrent use.
type measurer struct {
	families []string
	advance  advance
	ems      map[glyphKey]float64
}

func newMeasurer(f *fontSet) *measurer {
	return &measurer{families: f.names(), advance: f.lookup(), ems: map[glyphKey]float64{}}
}

// em returns the advance of ch in ems, or ErrUnsupported when no configured font maps ch.
func (m *measurer) em(ch rune, bold bool) (float64, error) {
	k := glyphKey{ch, bold}
	if v, ok := m.ems[k]; ok {
		return v, nil
	}
	v, ok, err := m.advance(ch, bold)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("pdf: a character is not covered by the configured fonts: %w", r.ErrUnsupported)
	}
	m.ems[k] = v
	return v, nil
}

// width returns the width in points of s set on one line, without kerning.
func (m *measurer) width(s string, size float64, bold bool) (float64, error) {
	w := 0.0
	for _, ch := range s {
		e, err := m.em(ch, bold)
		if err != nil {
			return 0, err
		}
		w += e
	}
	return w * size, nil
}
