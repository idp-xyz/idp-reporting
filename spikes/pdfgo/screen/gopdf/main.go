// gopdf probe: the same page as the fpdf probe, drawn with github.com/signintech/gopdf, which has
// no XMP or output-intent API, so its PDF/A-2b result shows what would have to be added.
package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/signintech/gopdf"
	"idp.local/reporting/spikes/pdfgo/screen/probe"
)

func main() {
	opt := probe.Flags()
	started := time.Now()
	regular, bold := probe.Fonts(opt.Fonts)

	pdf := gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4, Unit: gopdf.UnitMM})
	must(pdf.AddTTFFontData("noto", regular))
	must(pdf.AddTTFFontDataWithOption("noto", bold, gopdf.TtfOption{Style: gopdf.Bold}))
	pdf.SetInfo(gopdf.PdfInfo{Title: probe.Title, Producer: probe.Producer, CreationDate: probe.Stamp})
	cell := func(x, y, w, h float64, text string, align int, border int) {
		pdf.SetXY(x, y)
		must(pdf.CellWithOption(&gopdf.Rect{W: w, H: h}, text, gopdf.CellOption{Align: align | gopdf.Middle, Border: border}))
	}
	for p := 1; p <= opt.Pages; p++ {
		pdf.AddPage()
		pdf.SetTextColor(225, 225, 225)
		must(pdf.SetFont("noto", "B", 60))
		pdf.Rotate(30, 105, 150)
		pdf.SetXY(80, 140)
		must(pdf.Text("草稿"))
		pdf.RotateReset()
		pdf.SetTextColor(0, 0, 0)
		must(pdf.SetFont("noto", "B", 14))
		cell(15, 12, 180, 8, probe.Title, gopdf.Center, 0)
		must(pdf.SetFont("noto", "B", 9))
		cell(15, 24, 12, 6, "序号", gopdf.Right, gopdf.AllBorders)
		cell(27, 24, 128, 6, "摘要", gopdf.Left, gopdf.AllBorders)
		cell(155, 24, 40, 6, "金额", gopdf.Right, gopdf.AllBorders)
		must(pdf.SetFont("noto", "", 9))
		for i := 0; i < probe.RowsPerPage; i++ {
			n := (p-1)*probe.RowsPerPage + i + 1
			y := 30 + float64(i)*6
			cell(15, y, 12, 6, fmt.Sprint(n), gopdf.Right, gopdf.AllBorders)
			cell(27, y, 128, 6, probe.Line(n), gopdf.Left, gopdf.AllBorders)
			cell(155, y, 40, 6, probe.Amount(n), gopdf.Right, gopdf.AllBorders)
		}
		cell(15, 282, 180, 6, fmt.Sprintf("第 %d 页 / 共 %d 页", p, opt.Pages), gopdf.Center, 0)
	}
	f, e := os.Create(opt.Out)
	if e != nil {
		log.Fatal(e)
	}
	must(pdf.Write(f))
	must(f.Close())
	width, e := pdf.MeasureTextWidth(probe.Title)
	must(e)
	probe.Report(opt.Out, started, map[string]any{"width_of_title_mm": width})
}

func must(e error) {
	if e != nil {
		log.Fatal(e)
	}
}
