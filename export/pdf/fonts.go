package pdf

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

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

// fontExtensions are the files Typst loads from a font directory, in lower or upper case.
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
	listed   []listedFace // every face, as Typst must list it
}

// listedFace is one face as `typst fonts --variants` lists it.
type listedFace struct {
	family, path, style string
	weight              int
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
	var listed []listedFace
	files := 0
	total := 0
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if !e.Type().IsRegular() {
			return nil, nil, fmt.Errorf("pdf: font directory may hold only regular files: %s", e.Name())
		}
		ext := filepath.Ext(e.Name())
		if !fontExtensions[strings.ToLower(ext)] {
			continue
		}
		if ext != strings.ToLower(ext) && ext != strings.ToUpper(ext) {
			return nil, nil, fmt.Errorf("pdf: Typst does not load %s: use a lower or upper case extension", e.Name())
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
				listed = append(listed, listedFace{name, path, "Normal", 400})
			case style == "Bold" && fam.bold == nil:
				fam.bold = fc
				listed = append(listed, listedFace{name, path, "Normal", 700})
			default:
				return nil, nil, fmt.Errorf("pdf: %s: face %q %q is not a single Regular or Bold face", e.Name(), name, style)
			}
		}
	}
	if len(order) == 0 {
		order = slices.Sorted(slices.Values(found))
	}
	set := &fontSet{listed: listed}
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

// faceNames returns the family and style names from the name table: the typographic names when
// the font has them, else the legacy ones. Typst may name a family differently (it drops words
// such as "CN" or "Bold" from some family names), so New compares the result with Typst's own
// listing of the font directory.
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

// em returns the advance of ch in ems, or ErrUnsupported when no configured font maps ch or ch
// is a soft hyphen or a Hangul tone mark.
func (m *measurer) em(ch rune, bold bool) (float64, error) {
	switch ch {
	case 0x00AD:
		return 0, fmt.Errorf("pdf: a soft hyphen would print as a hyphen where Typst breaks the line: %w", r.ErrUnsupported)
	case 0x302E, 0x302F:
		// Hangul tone marks: Typst draws a dotted circle before each, which the data does not hold.
		return 0, fmt.Errorf("pdf: a Hangul tone mark would print with a dotted circle: %w", r.ErrUnsupported)
	}
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
	// In Chinese text Typst draws these dashes wider than their default glyphs.
	v = math.Max(v, wideDashes[ch])
	m.ems[k] = v
	return v, nil
}

// wideDashes are the least widths in ems Typst gives these dashes in Chinese text: an em dash
// one em, the two- and three-em dashes two and three. Measured against Typst over every
// character the pinned fonts cover; no other character is drawn wider than its advance.
var wideDashes = map[rune]float64{0x2014: 1, 0x2E3A: 2, 0x2E3B: 3}

// width returns the width in points of s set on one line, without kerning.
func (m *measurer) width(s string, size float64, bold bool) (float64, error) {
	w := 0.0
	var prev rune
	for _, ch := range s {
		e, err := m.em(ch, bold)
		if err != nil {
			return 0, err
		}
		w += e + gap(prev, ch)
		prev = ch
	}
	return w * size, nil
}

// gap returns the space in ems that Typst may put between two characters. Typst puts a quarter
// em between Chinese or Japanese text and its neighbours of other kinds (cjk-latin-spacing),
// including some symbols; this counts it next to anything but a space or another such
// character, which may be more than Typst adds, never less.
func gap(a, b rune) float64 {
	if a != 0 && a != ' ' && b != ' ' && cjk(a) != cjk(b) {
		return 0.25
	}
	return 0
}

// cjk reports Chinese and Japanese characters, including the prolonged sound marks and
// half-width katakana.
func cjk(ch rune) bool {
	return unicode.In(ch, unicode.Han, unicode.Hiragana, unicode.Katakana) || ch == 0x30FC || ch == 0xFF70 || (ch >= 0xFF66 && ch <= 0xFF9F)
}
