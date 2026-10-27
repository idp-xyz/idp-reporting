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
	Now         func() time.Time // creation time recorded in documents; default time.Now
}

// Writer is safe for concurrent use.
type Writer struct {
	cfg   Config
	fonts *fontSet
	ids   map[string]fileID
}

const (
	defaultTimeout = 15 * time.Second
	defaultMemory  = 2 << 30
	maxStderr      = 4 << 10
)

// New verifies the Typst binary against its checksum, loads the fonts and renders a probe
// document, so a misconfigured host fails at startup rather than at the first export.
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
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.WorkDir == "" {
		c.WorkDir = os.TempDir()
	}
	if c.Timeout < 0 || c.Timeout >= r.WorkTimeout || c.Jobs < 1 || c.Jobs > 64 || c.MemoryBytes < 256<<20 {
		return nil, errors.New("pdf: invalid timeout, memory or job limits")
	}
	if b, err := hex.DecodeString(c.TypstSHA256); err != nil || len(b) != sha256.Size || c.TypstSHA256 != strings.ToLower(c.TypstSHA256) {
		return nil, errors.New("pdf: the Typst checksum must be 64 lowercase hex digits")
	}
	var err error
	if c.Typst, err = filepath.EvalSymlinks(c.Typst); err != nil {
		return nil, fmt.Errorf("pdf: Typst binary: %w", err)
	}
	if c.FontDir, err = filepath.EvalSymlinks(c.FontDir); err != nil {
		return nil, fmt.Errorf("pdf: font directory: %w", err)
	}
	w := &Writer{cfg: c, ids: map[string]fileID{}}
	before, err := identity(c.Typst)
	if err != nil {
		return nil, err
	}
	sum, err := fileSHA256(c.Typst)
	if err != nil {
		return nil, err
	}
	if sum != c.TypstSHA256 {
		return nil, errors.New("pdf: the Typst binary does not match the pinned checksum")
	}
	fonts, ids, err := loadFonts(c.FontDir, c.Families)
	if err != nil {
		return nil, err
	}
	w.fonts, w.ids = fonts, ids
	w.ids[c.Typst] = before
	// The identities were taken before reading; the probe fails if anything changed since.
	if _, err := w.Write(context.Background(), probe); err != nil {
		return nil, fmt.Errorf("pdf: probe document: %w", err)
	}
	return w, nil
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
	out, err := w.render(ctx, dir, created)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		return nil, errors.New("pdf: Typst wrote no PDF")
	}
	return out, nil
}

// unchanged refuses to render when the binary, the font directory or a font file was replaced
// or modified after New checked them.
func (w *Writer) unchanged() error {
	for p, want := range w.ids {
		got, err := identity(p)
		if err != nil || got != want {
			return fmt.Errorf("pdf: %s changed after startup: %w", filepath.Base(p), r.ErrIntegrity)
		}
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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

// RenderError is a failed Typst run. Its message holds no values; Stderr, which may quote the
// document, is for tests and must not be logged.
type RenderError struct {
	Status string
	stderr []byte
}

func (e *RenderError) Error() string  { return "pdf: Typst failed: " + e.Status }
func (e *RenderError) Stderr() string { return string(e.stderr) }

// tail keeps the first bytes written to it, up to its capacity.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	if room := maxStderr - len(t.b); room > 0 {
		t.b = append(t.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

var _ r.WorkbookWriter = (*Writer)(nil)
