// pdfgo-statement renders the sample statement with a self-built layout on codeberg.org/go-pdf/fpdf.
// Money arrives as exact strings from statementdata; the layout only places them.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"

	sd "idp.local/reporting/spikes/statementdata"
)

func main() {
	if e := run(); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}

func run() error {
	rows := flag.Int("rows", 120, "statement rows")
	out := flag.String("out", "statement.pdf", "output PDF; the input data is written next to it as <out>.json")
	fonts := flag.String("fonts", "", "directory with NotoSansSC-Regular.ttf and NotoSansSC-Bold.ttf (TrueType outlines)")
	icc := flag.String("icc", "", "sRGB ICC profile for the PDF/A-2b output intent")
	standard := flag.String("standard", "a-2b", "a-2b, or empty for a plain PDF")
	stamp := flag.Int64("timestamp", time.Date(2026, 10, 27, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)).Unix(), "fixed creation time (unix) for reproducible output")
	seed := flag.Uint64("seed", 7, "data seed")
	hostile := flag.Bool("hostile", false, "mix markup-like and script-like strings into descriptions")
	long := flag.Bool("long", false, "join several punctuation-dense phrases per description (line breaking stress)")
	tall := flag.Bool("tall-row", false, "make the second row's description taller than a page")
	first := flag.String("first-amount", "", "force the first row's amount (plain decimal string)")
	draft := flag.Bool("draft", false, "draft watermark")
	flag.Parse()
	if *standard != "" && *standard != "a-2b" {
		return errors.New("-standard must be a-2b or empty")
	}
	if *standard == "a-2b" && *icc == "" {
		return errors.New("-icc is required for PDF/A-2b")
	}

	doc, e := sd.Build(sd.Options{Rows: *rows, Seed: *seed, Hostile: *hostile, Long: *long, Tall: *tall, FirstAmount: *first, Draft: *draft})
	if e != nil {
		return e
	}
	data, e := json.MarshalIndent(doc, "", " ")
	if e != nil {
		return e
	}
	pdf, e := filepath.Abs(*out)
	if e != nil {
		return e
	}
	if e = os.WriteFile(pdf+".json", data, 0o644); e != nil {
		return e
	}
	o := renderOptions{pdfA: *standard == "a-2b", created: time.Unix(*stamp, 0).UTC()}
	if o.regular, e = os.ReadFile(filepath.Join(*fonts, "NotoSansSC-Regular.ttf")); e != nil {
		return e
	}
	if o.bold, e = os.ReadFile(filepath.Join(*fonts, "NotoSansSC-Bold.ttf")); e != nil {
		return e
	}
	if o.pdfA {
		if o.icc, e = os.ReadFile(*icc); e != nil {
			return e
		}
	}
	started := time.Now()
	body, pages, e := render(doc, o)
	if e != nil {
		return e
	}
	elapsed := time.Since(started)
	if e = os.WriteFile(pdf, body, 0o644); e != nil {
		return e
	}
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"engine": "fpdf", "pdf": pdf, "bytes": len(body), "rows": len(doc.Rows),
		"pages": pages, "render_seconds": elapsed.Seconds(), "render_max_rss_kb": ru.Maxrss, "engine_output": ""})
}
