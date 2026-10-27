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
	"math/big"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed statement.typ
var statementTemplate []byte

type meta struct {
	Title    string `json:"title"`
	Company  string `json:"company"`
	Customer string `json:"customer"`
	Period   string `json:"period"`
	Snapshot string `json:"snapshot"`
	Template string `json:"template"`
	Draft    bool   `json:"draft"`
}

type row struct {
	Date   string `json:"date"`
	Doc    string `json:"doc"`
	Desc   string `json:"desc"`
	Amount string `json:"amount"`
	Cum    string `json:"cum"`
}

type statement struct {
	Meta       meta   `json:"meta"`
	Rows       []row  `json:"rows"`
	Total      string `json:"total"`
	TotalUpper string `json:"total_upper"`
}

var phrases = []string{
	"销售回款",
	"按合同第3.2条约定结算货款，含运费与保险费。",
	"根据《补充协议》（编号BC-2026-017），甲方应于收到发票后30日内付款；逾期未付的，按日万分之五计收违约金。",
	"退货冲减：SKU-A12（加急）共12件，质检不合格，已签收“退货单”。",
	"预收款核销——2026年第三季度服务费，见附件1、附件2……",
	"汇率差额调整（USD→CNY），参考中国人民银行中间价7.1234。",
	"手续费",
	"华东大区重点客户·上海浦东新区张江高科技园区某某医疗器械股份有限公司第二事业部月度对账差异，经双方确认后调整。",
}

var hostiles = []string{`#panic("injected")`, `]`, `[`, `$x^2$`, `<script>alert(1)</script>`, `@label`, `*粗体*`, `_斜体_`, `\`, `#read("/etc/passwd")`, `= 标题`, "```raw```"}

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
	first := flag.String("first-amount", "", "force the first row's amount (plain decimal string)")
	draft := flag.Bool("draft", false, "draft watermark")
	tmpl := flag.String("template", "", "use this template file instead of the embedded one (safety tests only)")
	allowNetwork := flag.Bool("allow-network", false, "let Typst reach the network; it downloads @preview packages on import")
	maxMemory := flag.Int("max-memory-mb", 0, "address-space limit for Typst through prlimit; 0 for none")
	flag.Parse()
	if *fonts == "" {
		return errors.New("-fonts is required so that Typst cannot fall back to other fonts")
	}

	doc, e := build(*rows, *seed, *hostile, *long, *first, *draft)
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
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"pdf": pdf, "bytes": info.Size(), "rows": len(doc.Rows),
		"typst_seconds": elapsed.Seconds(), "typst_max_rss_kb": maxRSS, "typst_output": strings.TrimSpace(string(output))})
}

// build makes a deterministic statement; every amount and running total is exact cents.
func build(n int, seed uint64, hostile, long bool, first string, draft bool) (statement, error) {
	rnd := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	doc := statement{Meta: meta{Title: "应收账款对账单", Company: "示例集团华东有限公司", Customer: "某某医疗器械股份有限公司",
		Period: "2026-09-01 至 2026-09-30", Snapshot: fmt.Sprintf("snap-%016x", seed), Template: "statement@spike-1", Draft: draft}}
	sum := new(big.Int)
	for i := 0; i < n; i++ {
		cents := big.NewInt(rnd.Int64N(55_000_000) - 5_000_000)
		if i == 0 && first != "" {
			c, e := parseCents(first)
			if e != nil {
				return doc, e
			}
			cents = c
		}
		desc := phrases[rnd.IntN(len(phrases))]
		for k := 2 + rnd.IntN(3); long && k > 0; k-- {
			desc += phrases[rnd.IntN(len(phrases))]
		}
		if hostile && i%7 == 3 {
			desc = hostiles[(i/7)%len(hostiles)] + " " + desc
		}
		sum.Add(sum, cents)
		doc.Rows = append(doc.Rows, row{Date: fmt.Sprintf("2026-09-%02d", 1+i%30), Doc: fmt.Sprintf("AR-%06d", i+1),
			Desc: desc, Amount: formatCents(cents), Cum: formatCents(sum)})
	}
	doc.Total = formatCents(sum)
	upper, e := rmbUpper(doc.Total)
	if e != nil {
		return doc, fmt.Errorf("total %s: %w", doc.Total, e)
	}
	doc.TotalUpper = upper
	return doc, nil
}

// parseCents accepts a plain decimal with at most two fraction digits and no exponent.
func parseCents(s string) (*big.Int, error) {
	neg := strings.HasPrefix(s, "-")
	whole, frac, _ := strings.Cut(strings.TrimPrefix(s, "-"), ".")
	if !allDigits(whole) || len(frac) > 2 || (frac != "" && !allDigits(frac)) {
		return nil, fmt.Errorf("not a plain two-decimal amount: %q", s)
	}
	v, _ := new(big.Int).SetString(whole+(frac + "00")[:2], 10)
	if neg {
		v.Neg(v)
	}
	return v, nil
}

func formatCents(v *big.Int) string {
	s := new(big.Int).Abs(v).String()
	if len(s) < 3 {
		s = strings.Repeat("0", 3-len(s)) + s
	}
	s = s[:len(s)-2] + "." + s[len(s)-2:]
	if v.Sign() < 0 {
		return "-" + s
	}
	return s
}
