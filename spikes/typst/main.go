// typst-spike renders a sample receivables statement through a pinned Typst CLI.
// It is an evaluation prototype: exact money stays in Go, Typst only lays out strings.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	sd "idp.local/reporting/spikes/statementdata"
)

//go:embed statement.typ
var statementTemplate []byte

func main() {
	if e := run(); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}

func run() error {
	rows := flag.Int("rows", 120, "statement rows")
	out := flag.String("out", "statement.pdf", "output PDF; the input data is written next to it as <out>.json")
	typst := flag.String("typst", "typst", "pinned Typst CLI binary")
	fonts := flag.String("fonts", "", "directory holding the only fonts Typst may use")
	standard := flag.String("standard", "a-2b", "PDF standard passed to Typst; empty for none")
	stamp := flag.Int64("timestamp", time.Date(2026, 10, 27, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)).Unix(), "fixed creation time (unix) for reproducible output")
	timeout := flag.Duration("timeout", 2*time.Minute, "kill Typst after this long")
	seed := flag.Uint64("seed", 7, "data seed")
	hostile := flag.Bool("hostile", false, "mix markup-like and script-like strings into descriptions")
	long := flag.Bool("long", false, "join several punctuation-dense phrases per description (line breaking stress)")
	tall := flag.Bool("tall-row", false, "make the second row's description taller than a page")
	first := flag.String("first-amount", "", "force the first row's amount (plain decimal string)")
	draft := flag.Bool("draft", false, "draft watermark")
	tmpl := flag.String("template", "", "use this template file instead of the embedded one (safety tests only)")
	allowNetwork := flag.Bool("allow-network", false, "let Typst reach the network; it downloads @preview packages on import")
	maxMemory := flag.Int("max-memory-mb", 0, "address-space limit for Typst through prlimit; 0 for none")
	flag.Parse()
	if *fonts == "" {
		return errors.New("-fonts is required so that Typst cannot fall back to other fonts")
	}

	doc, e := sd.Build(sd.Options{Rows: *rows, Seed: *seed, Hostile: *hostile, Long: *long, Tall: *tall, FirstAmount: *first, Draft: *draft})
	if e != nil {
		return e
	}
	data, e := json.MarshalIndent(doc, "", " ")
	if e != nil {
		return e
	}
	source := statementTemplate
	if *tmpl != "" {
		if source, e = os.ReadFile(*tmpl); e != nil {
			return e
		}
	}
	pdf, e := filepath.Abs(*out)
	if e != nil {
		return e
	}
	if e = os.WriteFile(pdf+".json", data, 0o644); e != nil {
		return e
	}

	// Typst sees only a private root holding the template and the data, an empty package
	// store, the given fonts and no proxy settings.
	work, e := os.MkdirTemp("", "typst-spike-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(work)
	for name, body := range map[string][]byte{"statement.typ": source, "data.json": data} {
		if e = os.WriteFile(filepath.Join(work, name), body, 0o600); e != nil {
			return e
		}
	}
	packages := filepath.Join(work, ".packages")
	args := []string{"compile", "--root", work, "--font-path", *fonts, "--ignore-system-fonts", "--ignore-embedded-fonts",
		"--package-path", packages, "--package-cache-path", packages, "--creation-timestamp", strconv.FormatInt(*stamp, 10)}
	if *standard != "" {
		args = append(args, "--pdf-standard", *standard)
	}
	args = append(args, filepath.Join(work, "statement.typ"), pdf)

	name := *typst
	if *maxMemory > 0 {
		args = append([]string{fmt.Sprintf("--as=%d", *maxMemory<<20), "--", *typst}, args...)
		name = "prlimit"
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + work}
	if !*allowNetwork {
		// Typst has no offline switch, so it runs in a new user and network namespace whose
		// only interface is a loopback device that is down.
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
			UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
			GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}}
	}
	started := time.Now()
	output, e := cmd.CombinedOutput()
	elapsed := time.Since(started)
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("typst killed after %s", *timeout)
	}
	if e != nil {
		return fmt.Errorf("typst failed: %w\n%s", e, output)
	}
	info, e := os.Stat(pdf)
	if e != nil {
		return e
	}
	usage, _ := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	var maxRSS int64
	if usage != nil {
		maxRSS = usage.Maxrss
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"engine": "typst", "pdf": pdf, "bytes": info.Size(), "rows": len(doc.Rows),
		"render_seconds": elapsed.Seconds(), "render_max_rss_kb": maxRSS, "engine_output": strings.TrimSpace(string(output))})
}
