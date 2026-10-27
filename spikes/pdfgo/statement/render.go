package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"codeberg.org/go-pdf/fpdf"
	sd "idp.local/reporting/spikes/statementdata"
)

// Geometry in points, close to the Typst template: A4, 9pt text, 4pt by 3.5pt cell insets.
const (
	pageW, pageH = 595.28, 841.89
	marginX      = 42.52 // 15mm
	marginTop    = 90.71 // 32mm
	marginBottom = 73.70 // 26mm
	fontSize     = 9.0
	lineH        = 12.5
	padX, padY   = 4.0, 3.5
	baseline     = 9.4 // from the top of a line box to the text baseline
	producer     = "idp-reporting fpdf spike"
)

var (
	headers      = []string{"序号", "日期", "单据号", "摘要", "金额"}
	rightAligned = []bool{true, false, false, false, true}
)

type renderOptions struct {
	regular, bold, icc []byte
	pdfA               bool
	created            time.Time // UTC: fpdf writes PDF dates without a zone and the XMP must match
}

// render lays out and draws the statement and returns the PDF bytes and the page count.
func render(doc sd.Statement, o renderOptions) ([]byte, int, error) {
	pdf := fpdf.New("P", "pt", "A4", "")
	pdf.AddUTF8FontFromBytes("noto", "", o.regular)
	pdf.AddUTF8FontFromBytes("noto", "B", o.bold)
	keywords := doc.Meta.Snapshot + ", " + doc.Meta.Template
	pdf.SetCreationDate(o.created)
	pdf.SetModificationDate(o.created)
	pdf.SetCatalogSort(true)
	pdf.SetTitle(doc.Meta.Title, true)
	pdf.SetAuthor(doc.Meta.Company, true)
	pdf.SetKeywords(keywords, true)
	pdf.SetProducer(producer, true)
	pdf.SetLang("zh-CN")
	if o.pdfA {
		pdf.AddOutputIntent(fpdf.OutputIntentType{SubtypeIdent: fpdf.OutputIntent_GTS_PDFA1,
			OutputConditionIdentifier: "sRGB IEC61966-2.1", Info: "sRGB IEC61966-2.1", ICCProfile: o.icc})
		pdf.SetXmpMetadata([]byte(xmp(doc.Meta.Title, doc.Meta.Company, keywords, o.created)))
	}
	pdf.SetAutoPageBreak(false, 0)
	pdf.SetDrawColor(150, 150, 150)
	pdf.SetLineWidth(0.4)
	pdf.SetFont("noto", "", fontSize)
	if e := pdf.Error(); e != nil {
		return nil, 0, e
	}

	cache := map[string]float64{}
	measure := func(s string) float64 { // regular 9pt; widths are additive because fpdf does not kern
		w, ok := cache[s]
		if !ok {
			w = pdf.GetStringWidth(s)
			cache[s] = w
		}
		return w
	}
	// Fixed-format columns fit their widest value; the amount column is capped so that an extreme
	// amount wraps instead of squeezing the description, which takes the remaining width.
	content := pageW - 2*marginX
	cols := make([]float64, len(headers))
	pdf.SetFont("noto", "B", fontSize)
	for c, h := range headers {
		cols[c] = pdf.GetStringWidth(h)
	}
	pdf.SetFont("noto", "", fontSize)
	cols[0] = max(cols[0], measure(fmt.Sprint(len(doc.Rows))))
	for _, r := range doc.Rows {
		cols[1], cols[2], cols[4] = max(cols[1], measure(r.Date)), max(cols[2], measure(r.Doc)), max(cols[4], measure(r.Amount))
	}
	for c := range cols {
		cols[c] += 2 * padX
	}
	cols[4] = min(cols[4], 110)
	cols[3] = content - (cols[0] + cols[1] + cols[2] + cols[4])
	rows, cums := make([][][]string, len(doc.Rows)), make([]string, len(doc.Rows))
	for i, r := range doc.Rows {
		texts := []string{fmt.Sprint(i + 1), r.Date, r.Doc, r.Desc, r.Amount}
		rows[i] = make([][]string, len(texts))
		for c, t := range texts {
			rows[i][c] = wrap(t, cols[c]-2*padX, measure)
		}
		cums[i] = r.Cum
	}
	upper := wrap("合计（人民币大写）："+doc.TotalUpper, content, measure)
	headH := lineH + 2*padY
	totalsH := float64(1+len(upper))*lineH + 8
	pages := paginate(rows, cums, lineH, padY, pageH-marginTop-marginBottom-headH, totalsH)

	text := func(x, y float64, s string) { pdf.Text(x, y, s) }
	right := func(x, y float64, s string) { pdf.Text(x-pdf.GetStringWidth(s), y, s) }
	cells := func(lines [][]string, y, h float64) {
		x := marginX
		for c, w := range cols {
			pdf.Rect(x, y, w, h, "D")
			for i, l := range lines[c] {
				ty := y + padY + float64(i)*lineH + baseline
				if rightAligned[c] {
					right(x+w-padX, ty, l)
				} else {
					text(x+padX, ty, l)
				}
			}
			x += w
		}
	}
	for k, p := range pages {
		pdf.AddPage()
		if doc.Meta.Draft {
			pdf.SetTextColor(225, 225, 225)
			pdf.SetFont("noto", "B", 72)
			pdf.TransformBegin()
			pdf.TransformRotate(30, pageW/2, pageH/2)
			text(pageW/2-pdf.GetStringWidth("草稿")/2, pageH/2+24, "草稿")
			pdf.TransformEnd()
			pdf.SetTextColor(0, 0, 0)
		}
		pdf.SetFont("noto", "B", 14)
		text((pageW-pdf.GetStringWidth(doc.Meta.Title))/2, 40, doc.Meta.Title)
		pdf.SetFont("noto", "", fontSize)
		text(marginX, 60, "单位："+doc.Meta.Company)
		right(pageW-marginX, 60, "客户："+doc.Meta.Customer)
		text(marginX, 76, "期间："+doc.Meta.Period)
		if p.carriedIn != "" {
			right(pageW-marginX, 76, "承前页累计："+p.carriedIn)
		}
		pdf.SetFont("noto", "B", fontSize)
		head := make([][]string, len(headers))
		for c, h := range headers {
			head[c] = []string{h}
		}
		cells(head, marginTop, headH)
		pdf.SetFont("noto", "", fontSize)
		y := marginTop + headH
		for _, f := range p.fragments {
			n := 1
			for _, l := range f.lines {
				n = max(n, len(l))
			}
			h := float64(n)*lineH + 2*padY
			cells(f.lines, y, h)
			y += h
		}
		if p.totals {
			right(pageW-marginX, y+8+baseline, "合计："+doc.Total)
			for i, l := range upper {
				text(marginX, y+8+float64(i+1)*lineH+baseline, l)
			}
		}
		footer := pageH - marginBottom + 16
		if k < len(pages)-1 && p.carriedOut != "" {
			right(pageW-marginX, footer, "过次页累计："+p.carriedOut)
		}
		numbering := fmt.Sprintf("第 %d 页 / 共 %d 页", k+1, len(pages))
		text((pageW-pdf.GetStringWidth(numbering))/2, footer+16, numbering)
		pdf.SetFont("noto", "", 7)
		pdf.SetTextColor(100, 100, 100)
		text(marginX, footer+16, "快照 "+doc.Meta.Snapshot)
		right(pageW-marginX, footer+16, "模板 "+doc.Meta.Template)
		pdf.SetTextColor(0, 0, 0)
		pdf.SetFont("noto", "", fontSize)
	}
	var buf bytes.Buffer
	if e := pdf.Output(&buf); e != nil {
		return nil, 0, e
	}
	if !o.pdfA {
		return buf.Bytes(), len(pages), nil
	}
	out, e := fixTrailer(buf.Bytes(), 1)
	return out, len(pages), e
}

var (
	trailerRoot = regexp.MustCompile(`/Root (\d+) 0 R`)
	trailerInfo = regexp.MustCompile(`/Info (\d+) 0 R`)
)

// fixTrailer repairs two fpdf v0.12.0 trailer defects. fpdf assumes the information dictionary
// is the object just before the catalog, but it writes the output intent profiles between them,
// so /Info points at a profile; and it writes no /ID, which PDF/A requires. The trailer follows
// the cross-reference table, so editing it moves no object offset. The corrected /Info target is
// verified to be the information dictionary; the identifier is derived from the bytes.
func fixTrailer(pdf []byte, outputIntents int) ([]byte, error) {
	i := bytes.LastIndex(pdf, []byte("trailer"))
	j := bytes.LastIndex(pdf, []byte("startxref"))
	if i < 0 || j < i {
		return nil, errors.New("no trailer")
	}
	trailer := pdf[i:j]
	root, info := trailerRoot.FindSubmatch(trailer), trailerInfo.FindSubmatch(trailer)
	if root == nil || info == nil {
		return nil, errors.New("trailer without /Root or /Info")
	}
	n, _ := strconv.Atoi(string(root[1]))
	want := n - 1 - outputIntents
	start := bytes.Index(pdf, []byte(fmt.Sprintf("\n%d 0 obj", want)))
	if start < 0 {
		return nil, fmt.Errorf("information dictionary object %d not found", want)
	}
	if end := bytes.Index(pdf[start:], []byte("endobj")); end < 0 || !bytes.Contains(pdf[start:start+end], []byte("/Producer")) {
		return nil, fmt.Errorf("object %d is not the information dictionary", want)
	}
	sum := sha256.Sum256(pdf)
	id := fmt.Sprintf("<%X>", sum[:16])
	fixed := bytes.Replace(trailer, info[0], []byte(fmt.Sprintf("/Info %d 0 R", want)), 1)
	fixed = bytes.Replace(fixed, []byte("<<"), []byte("<<\n/ID ["+id+" "+id+"]"), 1)
	return slices.Concat(pdf[:i], fixed, pdf[j:]), nil
}

// xmp is the PDF/A-2b identification, mirroring the document information dictionary.
func xmp(title, author, keywords string, created time.Time) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	date := created.UTC().Format("2006-01-02T15:04:05")
	return `<?xpacket begin="` + string(rune(0xFEFF)) + `" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about="" xmlns:pdfaid="http://www.aiim.org/pdfa/ns/id/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:pdf="http://ns.adobe.com/pdf/1.3/">
   <pdfaid:part>2</pdfaid:part>
   <pdfaid:conformance>B</pdfaid:conformance>
   <dc:title><rdf:Alt><rdf:li xml:lang="x-default">` + esc(title) + `</rdf:li></rdf:Alt></dc:title>
   <dc:creator><rdf:Seq><rdf:li>` + esc(author) + `</rdf:li></rdf:Seq></dc:creator>
   <pdf:Keywords>` + esc(keywords) + `</pdf:Keywords>
   <pdf:Producer>` + producer + `</pdf:Producer>
   <xmp:CreateDate>` + date + `</xmp:CreateDate>
   <xmp:ModifyDate>` + date + `</xmp:ModifyDate>
  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`
}
