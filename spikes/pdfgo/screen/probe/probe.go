// Package probe holds what both library probes share: flags, fonts, sample text and the XMP packet.
package probe

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	Title       = "应收账款对账单"
	Producer    = "idp-reporting pdf spike"
	RowsPerPage = 40
)

// Stamp is UTC because fpdf writes the PDF dates without a time zone; XMP must match them.
var Stamp = time.Date(2026, 10, 26, 16, 0, 0, 0, time.UTC)

var lines = []string{
	"按合同第3.2条约定结算货款，含运费与保险费。",
	"根据《补充协议》（编号BC-2026-017），逾期按日万分之五计收违约金。",
	"退货冲减：SKU-A12（加急）共12件，已签收“退货单”。",
	"预收款核销——2026年第三季度服务费，见附件1、附件2……",
	"汇率差额调整（USD→CNY），参考中间价7.1234。",
	"华东大区重点客户·上海浦东新区张江高科技园区第二事业部",
	"手续费",
}

type Options struct {
	Out, Fonts, ICC string
	Pages           int
}

func Flags() Options {
	var o Options
	flag.StringVar(&o.Out, "out", "probe.pdf", "output PDF")
	flag.StringVar(&o.Fonts, "fonts", "", "directory with NotoSansSC-Regular.ttf and NotoSansSC-Bold.ttf")
	flag.StringVar(&o.ICC, "icc", "", "sRGB ICC profile; adds the PDF/A-2b output intent and XMP where the library can")
	flag.IntVar(&o.Pages, "pages", 2, "pages of 40 rows")
	flag.Parse()
	return o
}

func Fonts(dir string) (regular, bold []byte) {
	var e error
	if regular, e = os.ReadFile(filepath.Join(dir, "NotoSansSC-Regular.ttf")); e != nil {
		log.Fatal(e)
	}
	if bold, e = os.ReadFile(filepath.Join(dir, "NotoSansSC-Bold.ttf")); e != nil {
		log.Fatal(e)
	}
	return regular, bold
}

func Line(n int) string   { return lines[n%len(lines)] }
func Amount(n int) string { return fmt.Sprintf("%d.%02d", n*7919%1000000, n%100) }

// XMP is the minimal PDF/A-2b identification with values mirroring the document information dictionary.
func XMP() string {
	date := Stamp.Format("2006-01-02T15:04:05")
	return `<?xpacket begin="` + string(rune(0xFEFF)) + `" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about="" xmlns:pdfaid="http://www.aiim.org/pdfa/ns/id/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:pdf="http://ns.adobe.com/pdf/1.3/">
   <pdfaid:part>2</pdfaid:part>
   <pdfaid:conformance>B</pdfaid:conformance>
   <dc:title><rdf:Alt><rdf:li xml:lang="x-default">` + Title + `</rdf:li></rdf:Alt></dc:title>
   <xmp:CreateDate>` + date + `</xmp:CreateDate>
   <xmp:ModifyDate>` + date + `</xmp:ModifyDate>
   <pdf:Producer>` + Producer + `</pdf:Producer>
  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`
}

// Report prints one JSON line: output size, wall time and this process's peak resident memory.
func Report(out string, started time.Time, extra map[string]any) {
	info, e := os.Stat(out)
	if e != nil {
		log.Fatal(e)
	}
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	extra["bytes"], extra["seconds"], extra["max_rss_kb"] = info.Size(), time.Since(started).Seconds(), ru.Maxrss
	_ = json.NewEncoder(os.Stdout).Encode(extra)
}
