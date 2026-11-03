package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"idp.local/reporting/doctemplates"
)

// maxBandGrowth is how much higher than the prototype's a band may be made, in millimetres, for the elements that have to move down to clear
// each other.
const maxBandGrowth = 12.0

// The prototype's bands and their place in the platform's.
var bandMap = []struct{ proto, name string }{
	{"pageHeader", doctemplates.BandPageHeader},
	{"reportHeader", doctemplates.BandDocumentHeader},
	{"reportFooter", doctemplates.BandDocumentFooter},
	{"pageFooter", doctemplates.BandPageFooter},
}

func (c *conv) protoBandHeight(proto string) float64 {
	b := c.pf.Doc.Bands
	switch proto {
	case "pageHeader":
		return b.PageHeader
	case "reportHeader":
		return b.ReportHeader
	case "reportFooter":
		return b.ReportFooter
	case "pageFooter":
		return b.PageFooter
	}
	return 0
}

func (c *conv) native() bool {
	p := c.pf.Doc.Page
	return math.Abs(c.pre.Width-p.Width) < 0.05 && math.Abs(c.pre.Height-p.Height) < 0.05
}

// buildBanded converts a template with a table: the prototype's bands keep their heights (page header and footer; the report header and
// footer become the document header and footer) and their nodes keep their places, the paper's x translated into the band's; the
// detail band is the table.
func (c *conv) buildBanded(nodes []*protoNode) error {
	ox := c.pre.Left
	if !c.native() {
		ox = c.pf.Doc.Page.Margins.Left
	}
	heights := map[string]float64{}
	for _, bm := range bandMap {
		h := c.protoBandHeight(bm.proto)
		if h < doctemplates.MinBandHeight {
			c.rec.add("band_height", fmt.Sprintf("区段 %s 高 %g 毫米，低于下限 5 毫米，提高到 5 毫米", bm.proto, h))
			h = doctemplates.MinBandHeight
		}
		heights[bm.name] = h
	}
	c.height = heights
	for _, n := range nodes {
		if n.Type == "table" {
			continue
		}
		name := ""
		for _, bm := range bandMap {
			if bm.proto == n.Band {
				name = bm.name
			}
		}
		if name == "" {
			c.rec.dropped("detail_extra", fmt.Sprintf("区段 %s 里的节点：平台的明细区段只放一张表", n.Band), n.ID)
			continue
		}
		cd := &cand{n: n, band: name, x: n.X - ox, y: n.Y, w: n.W, h: n.H}
		if !c.clip(cd, heights[name]) {
			continue
		}
		c.bands[name] = append(c.bands[name], c.nodeElements(cd)...)
	}
	for _, bm := range bandMap {
		var need float64
		c.bands[bm.name], need = c.resolveOverlaps(c.bands[bm.name], heights[bm.name], false, maxBandGrowth)
		if need > heights[bm.name]+1e-9 {
			c.rec.add("band_height", fmt.Sprintf("区段 %s 高 %g → %g 毫米：为让开互相重叠的文本，其中的元素需要往下挪出原来的区段", bm.proto, heights[bm.name], ceil1(need)))
			heights[bm.name] = ceil1(need)
		}
	}
	return c.buildTable(ox)
}

// clip puts a node's box inside its band; false when nothing of it is left.
func (c *conv) clip(cd *cand, bandH float64) bool {
	x0, y0, x1, y1 := cd.x, cd.y, cd.x+cd.w, cd.y+cd.h
	cd.x, cd.y = math.Max(0, x0), math.Max(0, y0)
	cd.w, cd.h = math.Min(c.bw, x1)-cd.x, math.Min(bandH, y1)-cd.y
	if math.Abs(cd.x-x0) > 0.05 || math.Abs(cd.y-y0) > 0.05 || math.Abs(cd.x+cd.w-x1) > 0.05 || math.Abs(cd.y+cd.h-y1) > 0.05 {
		c.rec.add("band_clipped", "节点超出所在区段（宽度按页面安全区、高度按区段高度），已缩到区段内", cd.n.ID)
	}
	if cd.w <= 0.05 || cd.h <= 0.05 {
		c.rec.dropped("band_clipped", "节点完全在区段之外，舍去", cd.n.ID)
		return false
	}
	cd.x, cd.y, cd.w, cd.h = r1(cd.x), r1(cd.y), floor1(cd.w), floor1(cd.h)
	return true
}

// buildTable: table -> the detail table. Columns keep field, label, width and align; repeatHeader, carry and subtotal are what the platform's
// table does (a repeated header, a carried sum and a total of its metric columns), and the numeric columns are metrics where they were summed;
// groupField -> the group fields; striped, sortField, negativeRed and headerColor are dropped.
func (c *conv) buildTable(ox float64) error {
	n := c.table
	x := math.Max(0, n.X-ox)
	w := math.Min(n.W, c.bw-x)
	if w < n.W-0.05 || x > n.X-ox+0.05 {
		c.rec.add("band_clipped", "表格超出页面安全区，已缩到区段内", n.ID)
	}
	c.noteStyle(n)
	if len(n.Columns) < 1 || len(n.Columns) > doctemplates.MaxTableColumns {
		return fmt.Errorf("the table has %d columns", len(n.Columns))
	}
	// Widths: scaled to the table's width, on the 0.1 grid, the last column taking the rest, none under 8 mm.
	sum := 0.0
	for _, col := range n.Columns {
		sum += col.Width
	}
	if sum <= 0 {
		return fmt.Errorf("the table's columns have no width")
	}
	if math.Abs(sum-n.W) > 0.05 {
		c.rec.add("table_option", fmt.Sprintf("列宽之和 %g 与表格宽 %g 不等，按比例调整", sum, n.W), n.ID)
	}
	widths := make([]float64, len(n.Columns))
	left := math.Round(w * 10)
	for i, col := range n.Columns {
		if i == len(n.Columns)-1 {
			widths[i] = left / 10
			break
		}
		t := math.Round(col.Width / sum * w * 10)
		widths[i] = t / 10
		left -= t
	}
	for i := range widths {
		if widths[i] < doctemplates.MinColumnWidth {
			need := doctemplates.MinColumnWidth - widths[i]
			widest := 0
			for j := range widths {
				if widths[j] > widths[widest] {
					widest = j
				}
			}
			if widths[widest]-need < doctemplates.MinColumnWidth {
				return fmt.Errorf("the columns do not fit in %g mm", w)
			}
			widths[i], widths[widest] = doctemplates.MinColumnWidth, r1(widths[widest]-need)
			c.rec.add("table_option", "列宽小于 8 毫米，提高到 8 毫米，从最宽的一列让出", n.ID)
		}
	}
	fs := c.fontSize(n, n.Style.Size)
	if fs > doctemplates.MaxTableFontSize {
		c.rec.add("font_rounded", fmt.Sprintf("表格字号 %g 磅高于上限 12 磅，降到 12 磅", n.Style.Size), n.ID)
		fs = doctemplates.MaxTableFontSize
	}
	if err := c.widenFirstColumn(widths, fs, n); err != nil {
		return err
	}
	totals := n.Subtotal || n.Carry
	item := doctemplates.Item{Type: doctemplates.TypeTable, X: r1(x), Width: r1(w), FontSize: fs}
	formats := map[string]bool{}
	for i, col := range n.Columns {
		f, ok := c.dsField(col.Field)
		if !ok {
			return fmt.Errorf("the table column %q is no field of the dataset", col.Field)
		}
		s := c.rowSlot(col.Field, doctemplates.ScopeRow)
		if (f.Type == "integer" || f.Type == "decimal") && f.ID != "unitPrice" && totals {
			s.metric = true
		}
		ic := doctemplates.ItemColumn{Field: s.id, Width: widths[i]}
		switch col.Align {
		case "left", "center", "right":
			ic.Align = col.Align
		}
		if col.Label != "" && col.Label != f.Label {
			ic.Label = col.Label
		} else if col.Label != "" {
			ic.Label = col.Label
		}
		if col.Format != "" && col.Format != "auto" && col.Format != "text" {
			formats[col.Format] = true
		}
		item.Columns = append(item.Columns, ic)
	}
	if len(formats) > 0 {
		var fm []string
		for k := range formats {
			fm = append(fm, k)
		}
		sort.Strings(fm)
		c.rec.add("format", "列的显示格式 "+strings.Join(fm, "、")+" 不转换，取值按原样输出", n.ID)
	}
	c.bands[doctemplates.BandDetail] = []placed{{item, n.ID}}
	// Options.
	if totals {
		c.rec.add("table_option", fmt.Sprintf("承前页 / 转下页累计（carry=%v）和本页加和（subtotal=%v）对应平台表格的承前、转下页和合计行；数值列（单价除外）标为度量列", n.Carry, n.Subtotal), n.ID)
	} else {
		c.rec.add("table_option", "原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和", n.ID)
	}
	if !n.RepeatHeader {
		c.rec.add("table_option", "repeatHeader 为否：平台的表格每页都重复表头，没有关闭的选项", n.ID)
	}
	if n.GroupField != "" {
		s := c.rowSlot(n.GroupField, doctemplates.ScopeDocument)
		s.scope = doctemplates.ScopeDocument
		c.group = []string{s.id}
	}
	if n.GroupTotals {
		c.rec.dropped("table_option", "groupTotals（分组小计）：平台的分组是一份文档一组，没有组内小计行", n.ID)
	}
	var dropped []string
	if n.Striped {
		dropped = append(dropped, "striped（隔行底色）")
	}
	if n.SortField != "" {
		dropped = append(dropped, "sortField="+n.SortField+"（排序）")
	}
	if n.NegativeRed {
		dropped = append(dropped, "negativeRed（负数红字）")
	}
	if n.HeaderColor != "" {
		dropped = append(dropped, "headerColor（表头底色）")
	}
	if n.RowsPerPage != 0 {
		dropped = append(dropped, fmt.Sprintf("rowsPerPage=%g（每页行数）", n.RowsPerPage))
	}
	if n.MinRowHeight != 0 || n.Padding != 0 {
		dropped = append(dropped, "minRowHeight、padding（行高与内边距，平台的行高由字号定）")
	}
	if n.Y != 0 {
		dropped = append(dropped, fmt.Sprintf("y=%g（表格顶部在区段里的位置，平台的表格紧跟上方的区段）", n.Y))
	}
	if len(dropped) > 0 {
		c.rec.dropped("table_option", "不转换："+strings.Join(dropped, "、"), n.ID)
	}
	return nil
}

// ---- fixed forms ----

type frame struct {
	n              *protoNode
	x0, y0, x1, y1 float64
}

// buildForm converts a template without a table (the prototype's fixed forms: waybill, bill of lading, parcel labels, WMS labels) into a
// form: a 2.x template without a detail band (S06-B), whose page header holds the whole form at its natural height. Every node is placed by
// its paper coordinates. Where the form lies inside the preset's safe region as it is on the paper, it keeps its place; otherwise its
// bounding box is put at the safe region's top-left corner and, only where it is bigger than the region, scaled uniformly (the type with
// it). The sample has no group fields: a form is one document per row, as the prototype's forms are one form per record.
func (c *conv) buildForm(nodes []*protoNode) error {
	var fr []frame
	for _, n := range nodes {
		if n.Band != "detail" {
			return fmt.Errorf("the form has a node %s outside the detail band", n.ID)
		}
		fr = append(fr, frame{n, n.X, n.Y, n.X + n.W, n.Y + n.H})
	}
	if len(fr) == 0 {
		return fmt.Errorf("the form has no node")
	}
	x0, y0, x1, y1 := fr[0].x0, fr[0].y0, fr[0].x1, fr[0].y1
	for _, f := range fr {
		x0, y0, x1, y1 = math.Min(x0, f.x0), math.Min(y0, f.y0), math.Max(x1, f.x1), math.Max(y1, f.y1)
	}
	W, H := x1-x0, y1-y0
	safeH := c.pre.Bottom - c.pre.Top
	left, top := c.pre.Left, c.pre.Top
	inside := c.native() && c.capScale >= 1 && x0 >= left-1e-9 && x1 <= c.pre.Width-left+1e-9 && y0 >= top-1e-9 && y1 <= c.pre.Bottom+1e-9
	s := 1.0
	fit := "原样放在纸上的位置（外框在页面安全区内）"
	ox, oy := left, top // the paper coordinates that are the band's origin
	if !inside {
		s = math.Floor(math.Min(math.Min(1, c.bw/W), math.Min(1, safeH/H))*c.capScale*1000) / 1000
		if s <= 0.3 {
			return fmt.Errorf("the form would need a scale of %.2f", s)
		}
		ox, oy = x0-0, y0
		fit = "外框左上角对齐安全区左上角"
		if s < 1 {
			fit += fmt.Sprintf("，整体等比缩小到 %.3f（字号随之取整，不低于 8 磅）", s)
		} else {
			fit += "，不缩放"
		}
	}
	c.scale = s
	var cands []*cand
	for _, f := range fr {
		cd := &cand{n: f.n, band: doctemplates.BandPageHeader}
		if inside {
			cd.x, cd.y, cd.w, cd.h = f.n.X-ox, f.n.Y-oy, f.n.W, f.n.H
		} else {
			cd.x, cd.y, cd.w, cd.h = (f.n.X-ox)*s, (f.n.Y-oy)*s, f.n.W*s, f.n.H*s
		}
		cd.x, cd.y, cd.w, cd.h = r1(cd.x), r1(cd.y), floor1(cd.w), floor1(cd.h)
		if cd.x+cd.w > c.bw {
			cd.w = floor1(c.bw - cd.x)
		}
		cands = append(cands, cd)
	}
	c.rec.add("form_fit", fmt.Sprintf("固定版式转成表单（没有明细带、没有明细表）：整版放在页眉区段，高度取最低元素的底边；外框 %g×%g 毫米；%s", r1(W), r1(H), fit))
	for _, cd := range cands {
		c.bands[doctemplates.BandPageHeader] = append(c.bands[doctemplates.BandPageHeader], c.nodeElements(cd)...)
	}
	c.bands[doctemplates.BandPageHeader], _ = c.resolveOverlaps(c.bands[doctemplates.BandPageHeader], math.Inf(1), true, 0)
	bottom := 0.0
	for _, p := range c.bands[doctemplates.BandPageHeader] {
		_, _, _, b := itemBox(p.item)
		bottom = math.Max(bottom, b)
	}
	c.height = map[string]float64{doctemplates.BandPageHeader: math.Max(doctemplates.MinBandHeight, ceil1(bottom))}
	return nil
}

// ---- boxes and overlaps ----

func boxedType(t string) bool {
	return t == doctemplates.TypeText || t == doctemplates.TypeBarcode || t == doctemplates.TypeQRCode || t == doctemplates.TypeImage
}

// itemBox is the box of an element in its band: left, top, right, bottom.
func itemBox(it doctemplates.Item) (x0, y0, x1, y1 float64) {
	switch it.Type {
	case doctemplates.TypeLine:
		return math.Min(it.X1, it.X2), math.Min(it.Y1, it.Y2), math.Max(it.X1, it.X2), math.Max(it.Y1, it.Y2)
	case doctemplates.TypeQRCode:
		return it.X, it.Y, it.X + it.Size, it.Y + it.Size
	case doctemplates.TypeTable:
		return it.X, 0, it.X + it.Width, 0
	}
	return it.X, it.Y, it.X + it.Width, it.Y + it.Height
}

func tenth(x float64) int { return int(math.Round(x * 10)) }

// overlapAmounts returns how far two boxes overlap in x and y (zero or negative when they do not).
func overlapAmounts(a, b doctemplates.Item) (dx, dy float64) {
	ax0, ay0, ax1, ay1 := itemBox(a)
	bx0, by0, bx1, by1 := itemBox(b)
	dx = math.Min(float64(tenth(ax1)), float64(tenth(bx1))) - math.Max(float64(tenth(ax0)), float64(tenth(bx0)))
	dy = math.Min(float64(tenth(ay1)), float64(tenth(by1))) - math.Max(float64(tenth(ay0)), float64(tenth(by0)))
	return dx / 10, dy / 10
}

func overlapping(a, b doctemplates.Item) bool {
	dx, dy := overlapAmounts(a, b)
	return dx > 0 && dy > 0
}

// resolveOverlaps keeps the texts, codes and images of a band apart, as the platform requires (decoration may lie under them). An element that
// overlaps an earlier one is moved down or narrowed by the little it overlaps, or the earlier one is made lower or narrower, and when that is
// not enough it is left out, with a record. open says that the band may grow downwards (a fixed form's bands are as high as their lowest element).
func (c *conv) resolveOverlaps(list []placed, bandH float64, open bool, grow float64) ([]placed, float64) {
	var kept []placed
	need := 0.0
	for _, e := range list {
		if !open {
			// Inside the band: an element whose box (grown to the platform's minimum) reaches past the band's bottom is moved up, or left out.
			if _, _, _, y1 := itemBox(e.item); y1 > bandH+1e-9 && e.item.Type != doctemplates.TypeTable {
				over := y1 - bandH
				if _, y0, _, _ := itemBox(e.item); y0-over >= 0 && e.item.Type != doctemplates.TypeLine {
					e.item.Y = r1(e.item.Y - over)
					c.rec.add("band_clipped", "元素放大后超出区段底边，上移", e.node)
				} else {
					c.rec.dropped("band_clipped", "元素放大后超出区段底边且无处可移，舍去", e.node)
					continue
				}
			}
		}
		if !boxedType(e.item.Type) {
			kept = append(kept, e)
			continue
		}
		drop := false
		for iter := 0; iter < 8; iter++ {
			hit := -1
			for i := range kept {
				if boxedType(kept[i].item.Type) && overlapping(e.item, kept[i].item) {
					hit = i
					break
				}
			}
			if hit < 0 {
				break
			}
			p := &kept[hit]
			dx, dy := overlapAmounts(e.item, p.item)
			ex0, ey0, ex1, _ := itemBox(e.item)
			px0, py0, _, py1 := itemBox(p.item)
			_, _ = ex1, py1
			moved, ok := c.freeSlot(e.item, kept, bandH, open)
			if !ok && !open && grow > 0 {
				// No place inside the band: the band may be a little higher, which the caller records.
				if moved, ok = c.freeSlot(e.item, kept, bandH+grow, false); ok {
					_, y0, _, y1 := itemBox(e.item)
					need = math.Max(need, moved+(y1-y0))
				}
			}
			switch {
			case ok && e.item.Type != doctemplates.TypeQRCode:
				c.rec.add("element_overlap", fmt.Sprintf("与先出现的元素重叠，纵向移到空处（%+.1f 毫米）", moved-e.item.Y), e.node)
				e.item.Y = moved
			case dy <= 3.0 && ey0 >= py0 && p.item.Type == doctemplates.TypeText && p.item.Height-dy-0.1 >= minTextHeight(p.item.FontSize):
				p.item.Height = r1(p.item.Height - dy - 0.1)
				c.rec.add("element_overlap", fmt.Sprintf("与先出现的元素重叠 %.1f 毫米，先出现的元素压低让开", dy), e.node)
			case dx <= 3.0 && ex0 >= px0 && e.item.Type == doctemplates.TypeText && e.item.Width-dx-0.1 >= 10:
				e.item.X = r1(e.item.X + dx + 0.1)
				e.item.Width = r1(e.item.Width - dx - 0.1)
				c.rec.add("element_overlap", fmt.Sprintf("与先出现的元素重叠 %.1f 毫米，右移并收窄让开", dx), e.node)
			case dx <= 3.0 && ex0 >= px0 && p.item.Type == doctemplates.TypeText && p.item.Width-dx-0.1 >= 10:
				p.item.Width = r1(p.item.Width - dx - 0.1)
				c.rec.add("element_overlap", fmt.Sprintf("与先出现的元素重叠 %.1f 毫米，先出现的元素收窄让开", dx), e.node)
			default:
				drop = true
			}
			if drop {
				break
			}
		}
		if !drop {
			for i := range kept {
				if boxedType(kept[i].item.Type) && overlapping(e.item, kept[i].item) {
					drop = true
				}
			}
		}
		if drop {
			c.rec.dropped("element_overlap", "与先出现的元素重叠太多，舍去（平台的文本、码、图框不能互相压住）", e.node)
			continue
		}
		kept = append(kept, e)
	}
	return kept, need
}

// freeSlot looks for the nearest place along the band, at most 12 mm from where the element is, where its box overlaps none of the kept
// boxes: just under or just over one of them.
func (c *conv) freeSlot(e doctemplates.Item, kept []placed, bandH float64, open bool) (float64, bool) {
	_, y0, _, y1 := itemBox(e)
	h := y1 - y0
	cands := []float64{0}
	for _, q := range kept {
		if !boxedType(q.item.Type) {
			continue
		}
		_, qy0, _, qy1 := itemBox(q.item)
		cands = append(cands, r1(qy1+0.1), r1(qy0-h-0.1))
	}
	sort.SliceStable(cands, func(i, j int) bool { return math.Abs(cands[i]-y0) < math.Abs(cands[j]-y0) })
	for _, y := range cands {
		if y < 0 || math.Abs(y-y0) > 12 || (!open && y+h > bandH+1e-9) {
			continue
		}
		probe := e
		probe.Y = y
		clear := true
		for _, q := range kept {
			if boxedType(q.item.Type) && overlapping(probe, q.item) {
				clear = false
				break
			}
		}
		if clear {
			return y, true
		}
	}
	return 0, false
}

// carryLabels are the labels the platform's table sets in the first column of the carried rows and the total row, at the widest page number.
var carryLabels = []string{"承前（第 100 页）", "过次（转第 100 页）", "合计"}

// tableInsetPt is the inset of a table cell, each side, in points (export/pdf).
const tableInsetPt = 4.0

// widenFirstColumn gives the first column the width the platform's carried rows need for their labels, taking it from the other columns in
// proportion to what they have above the 8 mm least width.
func (c *conv) widenFirstColumn(widths []float64, fs int, n *protoNode) error {
	em := 0.0
	for _, l := range carryLabels {
		em = math.Max(em, emWidth(l, true))
	}
	need := math.Ceil((em*float64(fs)*1.04+2*tableInsetPt)*mmPerPt*2) / 2
	if widths[0] >= need {
		return nil
	}
	want := need - widths[0]
	excess := 0.0
	for _, w := range widths[1:] {
		excess += w - doctemplates.MinColumnWidth
	}
	if excess < want {
		return fmt.Errorf("the first column needs %.1f mm for the carried rows' labels and the others cannot give it", need)
	}
	left := math.Round(want * 10)
	for j := 1; j < len(widths); j++ {
		take := math.Round(want * 10 * (widths[j] - doctemplates.MinColumnWidth) / excess)
		if j == len(widths)-1 || take > left {
			take = math.Min(left, math.Round((widths[j]-doctemplates.MinColumnWidth)*10))
		}
		widths[j] = r1(widths[j] - take/10)
		left -= take
	}
	if left > 0 {
		return fmt.Errorf("the first column's width could not be taken from the others")
	}
	c.rec.add("table_option", fmt.Sprintf("首列 %g → %g 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出", widths[0], need), n.ID)
	widths[0] = need
	return nil
}
