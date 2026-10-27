package pdf

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	r "idp.local/reporting/reporting"
)

// fontDir writes the given files into a new directory and returns it.
func fontDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadFontsReadsRegularAndBoldFaces(t *testing.T) {
	dir := fontDir(t, map[string][]byte{"Go-Regular.ttf": goregular.TTF, "Go-Bold.TTF": gobold.TTF, "Mono.ttf": gomono.TTF, "LICENSE": []byte("text")})
	set, ids, err := loadFonts(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(set.names(), []string{"Go", "Go Mono"}) || set.families[0].bold == nil || set.families[1].bold != nil {
		t.Fatalf("families %v", set.names())
	}
	if len(ids) != 4 { // the directory and three fonts; LICENSE is not a font
		t.Errorf("identities of %d paths", len(ids))
	}
	if set, _, err = loadFonts(dir, []string{"Go Mono", "Go"}); err != nil || !slices.Equal(set.names(), []string{"Go Mono", "Go"}) {
		t.Errorf("explicit order: %v %v", set, err)
	}
	m := newMeasurer(set)
	regular, err := m.em('W', false)
	if err != nil || regular <= 0 || regular > 1 {
		t.Errorf("advance of W: %v %v", regular, err)
	}
	if _, err := m.em('中', false); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("an ideograph the Go fonts lack: %v; want unsupported", err)
	}
}

func TestLoadFontsRefusesAmbiguousOrUnsafeDirectories(t *testing.T) {
	type setup func(t *testing.T) (dir string, order []string)
	only := func(files map[string][]byte, order ...string) setup {
		return func(t *testing.T) (string, []string) { return fontDir(t, files), order }
	}
	regular := map[string][]byte{"a.ttf": goregular.TTF}
	for name, prepare := range map[string]setup{
		"an italic face":                  only(map[string][]byte{"a.ttf": goregular.TTF, "b.ttf": goitalic.TTF}),
		"two Regular faces":               only(map[string][]byte{"a.ttf": goregular.TTF, "b.otf": goregular.TTF}),
		"a family without a Regular face": only(map[string][]byte{"b.ttf": gobold.TTF}),
		"an unknown family":               only(regular, "Noto Sans SC"),
		"a family listed twice":           only(regular, "Go", "Go"),
		"no fonts":                        only(map[string][]byte{"README": []byte("x")}),
		"not a font":                      only(map[string][]byte{"a.ttf": []byte("not a font")}),
		"a subdirectory": func(t *testing.T) (string, []string) {
			dir := fontDir(t, regular)
			if err := os.Mkdir(filepath.Join(dir, "more"), 0o755); err != nil {
				t.Fatal(err)
			}
			return dir, nil
		},
		"a symbolic link": func(t *testing.T) (string, []string) {
			dir := fontDir(t, regular)
			if err := os.Symlink(filepath.Join(dir, "a.ttf"), filepath.Join(dir, "b.ttf")); err != nil {
				t.Fatal(err)
			}
			return dir, nil
		},
		"a font others may write": func(t *testing.T) (string, []string) {
			dir := fontDir(t, regular)
			if err := os.Chmod(filepath.Join(dir, "a.ttf"), 0o666); err != nil {
				t.Fatal(err)
			}
			return dir, nil
		},
	} {
		dir, order := prepare(t)
		if _, _, err := loadFonts(dir, order); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNewChecksTheBinaryAndTheLimits(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "typst")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("#!/bin/sh\nexit 1\n"))
	good := hex.EncodeToString(sum[:])
	fonts := fontDir(t, map[string][]byte{"a.ttf": goregular.TTF, "b.ttf": gobold.TTF})
	for name, c := range map[string]Config{
		"a wrong checksum":   {Typst: bin, TypstSHA256: strings.Repeat("0", 64), FontDir: fonts},
		"an uppercase sum":   {Typst: bin, TypstSHA256: strings.ToUpper(good), FontDir: fonts},
		"a short sum":        {Typst: bin, TypstSHA256: good[:62], FontDir: fonts},
		"a missing binary":   {Typst: bin + "-missing", TypstSHA256: good, FontDir: fonts},
		"a missing font dir": {Typst: bin, TypstSHA256: good, FontDir: fonts + "-missing"},
		"the work timeout":   {Typst: bin, TypstSHA256: good, FontDir: fonts, Timeout: r.WorkTimeout},
		"a negative timeout": {Typst: bin, TypstSHA256: good, FontDir: fonts, Timeout: -1},
		"too many jobs":      {Typst: bin, TypstSHA256: good, FontDir: fonts, Jobs: 65},
		"too little memory":  {Typst: bin, TypstSHA256: good, FontDir: fonts, MemoryBytes: 1 << 20},
		"fonts without Han":  {Typst: bin, TypstSHA256: good, FontDir: fonts}, // the probe's Chinese is not covered
	} {
		if _, err := New(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := New(Config{Typst: bin, TypstSHA256: good, FontDir: fonts}); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("the probe with fonts lacking its characters: %v; want unsupported", err)
	}
	if err := os.Chmod(bin, 0o720); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Typst: bin, TypstSHA256: good, FontDir: fonts}); err == nil || errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a binary others may write: %v", err)
	}
}

func TestUnchangedDetectsModifiedOrReplacedFiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("one"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w := &Writer{ids: map[string]fileID{}}
	for _, p := range []string{dir, a, b} {
		id, err := identity(p)
		if err != nil {
			t.Fatal(err)
		}
		w.ids[p] = id
	}
	if err := w.unchanged(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, []byte("two!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.unchanged(); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a modified file: %v; want integrity failure", err)
	}
	w.ids[a], _ = identity(a)
	if err := os.WriteFile(b+".new", []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(b+".new", b); err != nil {
		t.Fatal(err)
	}
	if err := w.unchanged(); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a replaced file: %v; want integrity failure", err)
	}
}
