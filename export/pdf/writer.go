// Package pdf renders a frozen snapshot as a PDF/A-2b table with a pinned Typst binary.
//
// Go prepares everything: it formats values without computing them, checks every printed
// character against the configured fonts and chooses the column widths. Typst only lays out
// data.json with the embedded template, in a sandbox: an empty environment, new user and network
// namespaces, an empty package store, only the configured fonts, a private root directory, a
// time limit and limits on memory, output size and core dumps. See docs/ADR_0001_PDF_ENGINE.md.
package pdf

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	r "idp.local/reporting/reporting"
)

//go:embed template.typ
var template []byte

// Config locates the pinned Typst binary and the fonts. Zero durations and sizes take defaults.
type Config struct {
	Typst       string           // path of the Typst binary
	TypstSHA256 string           // its expected SHA-256 in lowercase hex
	FontDir     string           // the only fonts Typst may use: regular files, Regular and Bold faces
	Families    []string         // optional family order; by default every family, sorted by name
	WorkDir     string           // optional parent of the private render directories
	Timeout     time.Duration    // per document; default 15 s, less than reporting.WorkTimeout
	MemoryBytes uint64           // address-space limit of Typst; default 2 GiB
	Jobs        int              // Typst worker threads; default 2
	Concurrency int              // Typst processes at once, exports and previews together; default 2. Previews take at most all but one of them, so the export worker always finds one
	Now         func() time.Time // creation time recorded in exports and previews, not in issues, which carry the product's issue time; default time.Now
}

// Writer is safe for concurrent use.
type Writer struct {
	cfg      Config
	exe      *os.File // the Typst binary, opened and checked once; every run executes this file
	exeID    fileID
	fonts    *fontSet
	ids      map[string]fileID // the font directory and font files
	slots    chan struct{}     // one entry per running Typst
	previews chan struct{}     // the part of slots previews may hold: all but one, at least one
}

const (
	defaultTimeout = 15 * time.Second
	defaultMemory  = 2 << 30
	maxOutput      = 64 << 10 // kept of Typst's standard output and error
)

// New verifies the Typst binary against its checksum, loads the fonts, checks that Typst sees
// exactly the same faces, and renders a probe document, so that a misconfigured host fails at
// startup rather than at the first export.
func New(c Config) (*Writer, error) {
	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	if c.MemoryBytes == 0 {
		c.MemoryBytes = defaultMemory
	}
	if c.Jobs == 0 {
		c.Jobs = 2
	}
	if c.Concurrency == 0 {
		c.Concurrency = 2
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.WorkDir == "" {
		c.WorkDir = os.TempDir()
	}
	if c.Timeout < 0 || c.Timeout >= r.WorkTimeout || c.Jobs < 1 || c.Jobs > 64 || c.Concurrency < 1 || c.Concurrency > 64 || c.MemoryBytes < 256<<20 {
		return nil, errors.New("pdf: invalid timeout, memory, job or concurrency limits")
	}
	if b, err := hex.DecodeString(c.TypstSHA256); err != nil || len(b) != sha256.Size || c.TypstSHA256 != strings.ToLower(c.TypstSHA256) {
		return nil, errors.New("pdf: the Typst checksum must be 64 lowercase hex digits")
	}
	if os.Geteuid() == 0 {
		return nil, errors.New("pdf: Typst must not run as root")
	}
	var err error
	if c.FontDir, err = filepath.EvalSymlinks(c.FontDir); err != nil {
		return nil, fmt.Errorf("pdf: font directory: %w", err)
	}
	if c.WorkDir, err = filepath.EvalSymlinks(c.WorkDir); err != nil {
		return nil, fmt.Errorf("pdf: work directory: %w", err)
	}
	if err := checkHost(c); err != nil {
		return nil, err
	}
	// Typst always runs from this descriptor, so a file put at the path later never runs;
	// writing to or unlinking the opened file is detected before each render.
	exe, err := os.Open(c.Typst)
	if err != nil {
		return nil, fmt.Errorf("pdf: Typst binary: %w", err)
	}
	w := &Writer{cfg: c, exe: exe, slots: make(chan struct{}, c.Concurrency), previews: make(chan struct{}, max(1, c.Concurrency-1))}
	ok := false
	defer func() {
		if !ok {
			exe.Close()
		}
	}()
	if w.exeID, err = openIdentity(exe); err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(exe, 0, w.exeID.size)); err != nil {
		return nil, err
	}
	if hex.EncodeToString(h.Sum(nil)) != c.TypstSHA256 {
		return nil, errors.New("pdf: the Typst binary does not match the pinned checksum")
	}
	// The identities of the binary and the fonts were taken before reading them; every render,
	// starting with the probe below, first checks that none changed since.
	if w.fonts, w.ids, err = loadFonts(c.FontDir, c.Families); err != nil {
		return nil, err
	}
	if _, err := build(probe, newMeasurer(w.fonts), c.Now()); err != nil {
		return nil, fmt.Errorf("pdf: probe document: %w", err)
	}
	if err := w.sameFaces(context.Background()); err != nil {
		return nil, err
	}
	if _, err := w.Write(context.Background(), probe); err != nil {
		return nil, fmt.Errorf("pdf: probe document: %w", err)
	}
	docProbe, docValues, docBackground := documentProbe()
	if _, err := w.Render(context.Background(), docProbe, docValues, docBackground, nil); err != nil {
		return nil, fmt.Errorf("pdf: probe document template: %w", err)
	}
	ok = true
	return w, nil
}

// Close releases the Typst binary. The Writer cannot render afterwards.
func (w *Writer) Close() error { return w.exe.Close() }

// bounded limits one render, waiting for a slot included, to the render timeout, so that a
// render never holds a request longer than that; the returned function turns the deadline
// into a budget error, unless the caller's own context ended first.
func (w *Writer) bounded(ctx context.Context) (context.Context, context.CancelFunc, func(error) error) {
	bounded, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	budget := func(err error) error {
		if err != nil && ctx.Err() == nil && bounded.Err() != nil {
			return fmt.Errorf("pdf: waiting for a Typst process and rendering took longer than %s: %w", w.cfg.Timeout, r.ErrBudget)
		}
		return err
	}
	return bounded, cancel, budget
}

// acquire takes one entry of slots or gives up when ctx ends: more renders than the slots
// would start more Typst processes than the host was sized for.
func acquire(ctx context.Context, slots chan struct{}) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func release(slots chan struct{}) { <-slots }

// sameFaces lists the fonts as Typst sees them and requires exactly the faces loadFonts found:
// the same family names, files, styles and weights. A family name Typst does not know would
// otherwise only be a warning, and the text would be laid out without any font.
func (w *Writer) sameFaces(ctx context.Context) error {
	dir, err := os.MkdirTemp(w.cfg.WorkDir, "reporting-pdf-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	out, err := w.run(ctx, dir, "fonts", "--font-path", w.cfg.FontDir, "--ignore-system-fonts", "--ignore-embedded-fonts", "--variants")
	if err != nil {
		return fmt.Errorf("pdf: list fonts: %w", err)
	}
	got, err := typstFaces(string(out))
	if err != nil {
		return err
	}
	key := func(f listedFace) string { return fmt.Sprintf("%q %q %q %d", f.family, f.path, f.style, f.weight) }
	want := make([]string, len(w.fonts.listed))
	for i, f := range w.fonts.listed {
		want[i] = key(f)
	}
	have := make([]string, len(got))
	for i, f := range got {
		have[i] = key(f)
	}
	slices.Sort(want)
	slices.Sort(have)
	if !slices.Equal(want, have) {
		return fmt.Errorf("pdf: Typst sees other font families or faces than the font check: %v, not %v", have, want)
	}
	return nil
}

// typstFaces parses `typst fonts --variants`: each family name on its own line, then for each
// face a line with its path and a line "Style: Normal, Weight: 400, Stretch: 100%".
func typstFaces(out string) ([]listedFace, error) {
	var faces []listedFace
	family := ""
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		item := strings.TrimLeft(line, " │")
		switch {
		case line == "":
		case !strings.HasPrefix(line, " "):
			family = line
		case strings.HasPrefix(item, "├ ") || strings.HasPrefix(item, "└ "):
			_, path, _ := strings.Cut(item, " ")
			faces = append(faces, listedFace{family: family, path: path})
		case strings.HasPrefix(item, "Style: ") && len(faces) > 0:
			f := &faces[len(faces)-1]
			for _, part := range strings.Split(item, ", ") {
				name, value, _ := strings.Cut(part, ": ")
				switch name {
				case "Style":
					f.style = value
				case "Weight":
					f.weight, _ = strconv.Atoi(value)
				}
			}
		default:
			return nil, errors.New("pdf: unexpected font listing from Typst")
		}
	}
	return faces, nil
}

// probe covers the template's paths: a dimension, an exact decimal metric with its total, and
// Chinese and Latin text.
var probe = r.Snapshot{
	ID:       "probe",
	Query:    r.QuerySpec{Dataset: r.Ref{ID: "probe", Version: "1"}, Companies: []string{"c"}, Dimensions: []string{"d"}, Metrics: []r.Ref{{ID: "m", Version: "1"}}, AsOf: "2026-01-01", Timezone: "UTC"},
	Prepared: r.PreparedRead{Watermark: "probe"},
	Result: r.Result{
		Columns:  []r.Column{{ID: "d", Label: "项目", Type: "string"}, {ID: "m", Label: "金额", Type: "decimal", Scale: 2, Currency: "CNY"}},
		Rows:     [][]string{{"中文 Latin 0123456789", "-1234567.89"}},
		Totals:   map[string]string{"m": "-1234567.89"},
		Evidence: r.Evidence{DataClass: "contract-simulator"},
	},
}

// Write renders one snapshot. Characters the fonts do not cover and snapshots too wide for a
// page are unsupported; time and size limits are budget errors. Errors never carry values.
func (w *Writer) Write(ctx context.Context, s r.Snapshot) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(s.Result.Rows) > r.MaxRows {
		return nil, r.ErrBudget
	}
	created := w.cfg.Now()
	d, err := build(s, newMeasurer(w.fonts), created)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	bounded, cancel, budget := w.bounded(ctx)
	defer cancel()
	if err := acquire(bounded, w.slots); err != nil {
		return nil, budget(err)
	}
	defer release(w.slots)
	if err := w.unchanged(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(w.cfg.WorkDir, "reporting-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for name, b := range map[string][]byte{"main.typ": template, "data.json": data} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return nil, err
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "packages"), 0o700); err != nil {
		return nil, err
	}
	out, err := w.render(bounded, dir, created)
	if err != nil {
		return nil, budget(err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		return nil, errors.New("pdf: Typst wrote no PDF")
	}
	return out, nil
}

// unchanged refuses to render when the binary was modified, or the font directory or a font file
// was replaced or modified, after New checked them.
func (w *Writer) unchanged() error {
	if got, err := openIdentity(w.exe); err != nil || got != w.exeID {
		return fmt.Errorf("pdf: the Typst binary changed after startup: %w", r.ErrIntegrity)
	}
	for p, want := range w.ids {
		got, err := identity(p)
		if err != nil || got != want {
			return fmt.Errorf("pdf: %s changed after startup: %w", filepath.Base(p), r.ErrIntegrity)
		}
	}
	return nil
}

// readBounded reads a rendered file, refusing one above the artifact limit.
func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, r.MaxArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > r.MaxArtifactBytes {
		return nil, r.ErrBudget
	}
	return b, nil
}

// RenderError is a failed Typst run. Its message holds no values. Typst's output, which may
// quote the document, is kept for tests only.
type RenderError struct {
	Status string
	stderr []byte
}

func (e *RenderError) Error() string  { return "pdf: Typst failed: " + e.Status }
func (e *RenderError) output() string { return string(e.stderr) }

// head keeps the first maxOutput bytes written to it.
type head struct{ b []byte }

func (t *head) Write(p []byte) (int, error) {
	if room := maxOutput - len(t.b); room > 0 {
		t.b = append(t.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

var _ r.WorkbookWriter = (*Writer)(nil)
