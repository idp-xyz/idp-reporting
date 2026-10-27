// fpdf probe: CJK TrueType subsetting, determinism and the PDF/A-2b hooks of codeberg.org/go-pdf/fpdf.
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/go-pdf/fpdf"
	"idp.local/reporting/spikes/pdfgo/screen/probe"
)

func main() {
	opt := probe.Flags()
	started := time.Now()
	regular, bold := probe.Fonts(opt.Fonts)

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddUTF8FontFromBytes("noto", "", regular)
	pdf.AddUTF8FontFromBytes("noto", "B", bold)
	pdf.SetCreationDate(probe.Stamp)
	pdf.SetModificationDate(probe.Stamp)
	pdf.SetCatalogSort(true)
	pdf.SetProducer(probe.Producer, true)
	pdf.SetTitle(probe.Title, true)
	pdf.SetLang("zh-CN")
	if opt.ICC != "" {
		profile, e := os.ReadFile(opt.ICC)
		if e != nil {
			log.Fatal(e)
		}
		pdf.AddOutputIntent(fpdf.OutputIntentType{SubtypeIdent: fpdf.OutputIntent_GTS_PDFA1,
			OutputConditionIdentifier: "sRGB IEC61966-2.1", Info: "sRGB IEC61966-2.1", ICCProfile: profile})
		pdf.SetXmpMetadata([]byte(probe.XMP()))
	}
	pdf.SetAutoPageBreak(false, 0)
	for p := 1; p <= opt.Pages; p++ {
		pdf.AddPage()
		pdf.SetTextColor(225, 225, 225)
		pdf.SetFont("noto", "B", 60)
		pdf.TransformBegin()
		pdf.TransformRotate(30, 105, 150)
		pdf.Text(80, 160, "草稿")
		pdf.TransformEnd()
		pdf.SetTextColor(0, 0, 0)
		pdf.SetFont("noto", "B", 14)
		pdf.SetXY(15, 12)
		pdf.CellFormat(180, 8, probe.Title, "", 1, "C", false, 0, "")
		pdf.SetFont("noto", "B", 9)
		pdf.SetXY(15, 24)
		pdf.CellFormat(12, 6, "序号", "1", 0, "R", false, 0, "")
		pdf.CellFormat(128, 6, "摘要", "1", 0, "L", false, 0, "")
		pdf.CellFormat(40, 6, "金额", "1", 1, "R", false, 0, "")
		pdf.SetFont("noto", "", 9)
		for i := 0; i < probe.RowsPerPage; i++ {
			n := (p-1)*probe.RowsPerPage + i + 1
			pdf.SetX(15)
			pdf.CellFormat(12, 6, fmt.Sprint(n), "1", 0, "R", false, 0, "")
			pdf.CellFormat(128, 6, probe.Line(n), "1", 0, "L", false, 0, "")
			pdf.CellFormat(40, 6, probe.Amount(n), "1", 1, "R", false, 0, "")
		}
		pdf.SetXY(15, 282)
		pdf.CellFormat(180, 6, fmt.Sprintf("第 %d 页 / 共 %d 页", p, opt.Pages), "", 0, "C", false, 0, "")
	}
	f, e := os.Create(filepath.Clean(opt.Out))
	if e != nil {
		log.Fatal(e)
	}
	if e = pdf.OutputAndClose(f); e != nil {
		log.Fatal(e)
	}
	probe.Report(opt.Out, started, map[string]any{"width_of_title_mm": pdf.GetStringWidth(probe.Title)})
}
