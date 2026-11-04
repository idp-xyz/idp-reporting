// Command reporting-samples converts the prototype's template library into the platform's sample pack (S06-B part 2).
//
//	reporting-samples convert --in doctemplates/samples/prototype --out doctemplates/samples [--docs docs/SAMPLES.md] [--check]
//
// The input is what scripts/prototype_templates.mjs extracted; the output is one <kind>.json (a sample) and one <kind>.record.json (what the
// conversion dropped or rewrote) per template, and docs/SAMPLES.md. The conversion is deterministic: with --check nothing is written and the
// command fails when a file differs from what it would write.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"idp.local/reporting/doctemplates"
)

func marshalSample(s doctemplates.Sample) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func marshalRecord(r *Record) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// output is everything a run writes: path (relative to the output directory, or the docs path) to content.
type output struct {
	samples map[string][]byte // by file name in the out directory
	docs    []byte
}

// run converts a directory of extraction files.
func run(in string) (output, error) {
	files, err := loadDir(in)
	if err != nil {
		return output{}, err
	}
	out := output{samples: map[string][]byte{}}
	var records []*Record
	var failures []string
	for _, pf := range files {
		sample, rec, err := convert(pf)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", pf.Source.File, err))
			continue
		}
		data, err := marshalSample(sample)
		if err != nil {
			return output{}, err
		}
		rdata, err := marshalRecord(rec)
		if err != nil {
			return output{}, err
		}
		out.samples[sample.Kind+".json"] = data
		out.samples[sample.Kind+".record.json"] = rdata
		records = append(records, rec)
	}
	if len(failures) > 0 {
		return output{}, fmt.Errorf("%d templates cannot be converted:\n%s", len(failures), strings.Join(failures, "\n"))
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Kind < records[j].Kind })
	out.docs = []byte(samplesDoc(records, files))
	return out, nil
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "convert" {
		fmt.Fprintln(os.Stderr, "usage: reporting-samples convert --in DIR --out DIR [--docs FILE] [--check]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("convert", flag.ExitOnError)
	in := fs.String("in", "", "directory of the extraction files")
	outDir := fs.String("out", "", "directory of the samples")
	docs := fs.String("docs", "", "file of the per-template record (docs/SAMPLES.md)")
	check := fs.Bool("check", false, "write nothing; fail when a file differs")
	fs.Parse(os.Args[2:])
	if *in == "" || *outDir == "" {
		fs.Usage()
		os.Exit(2)
	}
	out, err := run(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reporting-samples:", err)
		os.Exit(1)
	}
	failed := false
	put := func(path string, data []byte) {
		if *check {
			have, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(have, data) {
				fmt.Fprintln(os.Stderr, "differs:", path)
				failed = true
			}
			return
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "reporting-samples:", err)
			os.Exit(1)
		}
	}
	var names []string
	for name := range out.samples {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		put(filepath.Join(*outDir, name), out.samples[name])
	}
	if *docs != "" {
		put(*docs, out.docs)
	}
	if failed {
		os.Exit(1)
	}
	fmt.Printf("%d samples\n", len(out.samples)/2)
}
