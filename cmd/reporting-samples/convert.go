package main

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/barcode"
)

// The converter (S06-B part 2, B6): one prototype template in, one sample of schema 2.1 and its record out. It is deterministic (no clock,
// no randomness, no map order), has one rule per node type (the functions node* below), and refuses to return a sample that
// doctemplates does not accept. Everything it drops or rewrites goes into the record.

const mmPerPt = 25.4 / 72

var exactNumberRe = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func r1(x float64) float64    { return math.Round(x*10) / 10 }
func ceil1(x float64) float64 { return math.Ceil(x*10-1e-9) / 10 }
func floor1(x float64) float64 {
	return math.Floor(x*10+1e-9) / 10
}

type slot struct {
	id     string
	label  string
	src    string // "d": a field of the dataset, "m": a document-level value of the prototype
	srcID  string
	scope  string
	metric bool
}

// cand is a node placed in a band of the sample: its box in the band's millimetres, after the fit.
type cand struct {
	n          *protoNode
	band       string
	x, y, w, h float64
}

// placed is an element of the sample with the node it came from.
type placed struct {
	item doctemplates.Item
	node string
}

type conv struct {
	pf       protoFile
	rec      *Record
	pre      doctemplates.Preset
	bw       float64
	form     bool
	table    *protoNode
	capScale float64
	scale    float64 // the factor the fixed form was fitted with (font sizes follow it)
	slots    []*slot
	slotBy   map[string]*slot
	taken    map[string]string // slot id -> "d" or "m"
	kept     []map[string]any  // the prototype rows the sample keeps
	bands    map[string][]placed
	height   map[string]float64
	group    []string
	// notes
	lineBreaks, controls int
}

// convert is the whole conversion of one template.
func convert(pf protoFile) (doctemplates.Sample, *Record, error) {
	// A fixed form is fitted by an estimate of how high its boxes become once the platform's minimums are applied; when the result is
	// refused (the bands leave too little for the table), the fit is tried again a little smaller.
	var sample doctemplates.Sample
	var rec *Record
	var err error
	for attempt := 0; attempt < 24; attempt++ {
		sample, rec, err = convertAt(pf, math.Pow(0.97, float64(attempt)))
		if err == nil || !strings.Contains(err.Error(), "doctemplates refuses") {
			return sample, rec, err
		}
	}
	return sample, rec, err
}

func convertAt(pf protoFile, capScale float64) (doctemplates.Sample, *Record, error) {
	c, s, err := convertAtWith(pf, capScale)
	if err != nil {
		return doctemplates.Sample{}, nil, err
	}
	return s, c.rec, nil
}

// convertAtWith is one attempt of the conversion, with the converter's state for the tests that look inside.
func convertAtWith(pf protoFile, capScale float64) (*conv, doctemplates.Sample, error) {
	c := &conv{pf: pf, slotBy: map[string]*slot{}, taken: map[string]string{}, bands: map[string][]placed{}, height: map[string]float64{}, scale: 1, capScale: capScale}
	c.rec = newRecord(pf.Doc.Kind, pf.Source.File, pf.Doc.Name, pf.Doc.DatasetID)
	d := pf.Doc
	c.rec.Counts.NodesIn = len(d.Nodes)
	c.rec.Page = fmt.Sprintf("%s %g×%g", d.Page.Preset, d.Page.Width, d.Page.Height)
	if err := c.choosePreset(); err != nil {
		return nil, doctemplates.Sample{}, err
	}
	c.bw = c.pre.BandWidth()
	// Nodes: hidden ones are not converted; a template has at most one visible table.
	var nodes []*protoNode
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.Hidden {
			c.rec.dropped("node_hidden", "隐藏的节点不转换", n.ID)
			continue
		}
		if len(n.extra) > 0 {
			c.rec.add("node_unknown", "原型节点的未转换属性："+strings.Join(n.extra, "、"), n.ID)
		}
		if n.Type == "table" {
			if c.table != nil {
				c.rec.dropped("detail_extra", "第二张明细表：平台的明细区段只放一张表", n.ID)
				continue
			}
			c.table = n
		}
		nodes = append(nodes, n)
	}
	c.form = c.table == nil
	c.noteDocument()
	c.selectRows()
	var err error
	if c.form {
		err = c.buildForm(nodes)
	} else {
		err = c.buildBanded(nodes)
	}
	if err != nil {
		return nil, doctemplates.Sample{}, err
	}
	sample, _, err := c.assemble()
	return c, sample, err
}

// choosePreset maps the prototype's paper to a preset (B6 gave A4 and the 4 x 6 inch label; the other sizes of the prototype's picker
// are mapped when their size is a preset's). A paper that is no preset (the WMS labels of 150 x 100 mm) is put at its true size on the
// least preset that holds it, and the record says so.
func (c *conv) choosePreset() error {
	p := c.pf.Doc.Page
	w, h := p.Width, p.Height
	same := func(pre doctemplates.Preset) bool {
		return math.Abs(pre.Width-w) < 0.05 && math.Abs(pre.Height-h) < 0.05
	}
	for _, pre := range doctemplates.Presets {
		if same(pre) {
			c.pre = pre
			c.rec.Preset = pre.ID
			c.rec.add("page_size", fmt.Sprintf("纸张 %s（%g×%g 毫米）即预设 %s", p.Preset, w, h, pre.ID))
			return nil
		}
	}
	// The least preset whose safe region holds the paper's margin box and the table the platform requires.
	need := doctemplates.MinDetailHeightTenths(8) // tenths of a millimetre
	for _, id := range []string{doctemplates.PresetLabel4x6in, doctemplates.PresetLabel100x150, doctemplates.PresetA5, doctemplates.PresetA4Landscape, doctemplates.PresetA4} {
		pre, _ := doctemplates.PresetByID(id)
		if w-p.Margins.Left-p.Margins.Right <= pre.BandWidth()+1e-9 && h-p.Margins.Top-p.Margins.Bottom+float64(need)/10 <= pre.Bottom-pre.Top+1e-9 {
			c.pre = pre
			c.rec.Preset = pre.ID
			c.rec.add("page_size", fmt.Sprintf("纸张 %s（%g×%g 毫米）没有对应的预设（自定义尺寸不在本阶段）：按原尺寸放在预设 %s 的左上角，不缩放，需要时由使用者另行裁切或换纸", p.Preset, w, h, pre.ID))
			return nil
		}
	}
	return fmt.Errorf("the paper %s (%g x %g mm) fits no preset", p.Preset, w, h)
}

// noteDocument records what the document level of the prototype has that the sample does not.
func (c *conv) noteDocument() {
	d := c.pf.Doc
	if len(d.Parameters) > 0 {
		var names []string
		for _, p := range d.Parameters {
			names = append(names, p.Label)
		}
		c.rec.add("parameters", fmt.Sprintf("%d 个查询参数（%s）不转换：平台的查询面板就是参数", len(d.Parameters), strings.Join(names, "、")))
	}
	if d.Page.FormGuide != "" {
		c.rec.add("page_background", fmt.Sprintf("版式参考图 %q 是原型自己画的图形，不是位图，不转换", d.Page.FormGuide))
	}
	if d.Page.Background != "" {
		c.rec.add("page_background", "底图不转换（样例不带底图）")
	}
	if d.Page.Opacity != 0 && (d.Page.Background != "" || d.Page.FormGuide != "") {
		c.rec.add("page_background", fmt.Sprintf("底图透明度 %g 不转换", d.Page.Opacity))
	}
}

// selectRows decides the prototype rows the sample keeps: all of a table's (at most 50, and only the first group when the table is
// grouped), the first of a fixed form (one record, one form).
func (c *conv) selectRows() {
	rows := c.pf.Rows
	if c.form {
		if len(rows) > 1 {
			c.rec.add("rows_kept", fmt.Sprintf("原型有 %d 行样例数据，每行一张表单；样例只留第一行，其余 %d 行舍去", len(rows), len(rows)-1))
		}
		if len(rows) > 1 {
			rows = rows[:1]
		}
		c.kept = rows
		return
	}
	if g := c.table.GroupField; g != "" {
		var first string
		var keep []map[string]any
		for i, row := range rows {
			key := rowValue(row[g])
			if key == "" {
				key = "未分类"
			}
			if i == 0 {
				first = key
			}
			if key == first {
				keep = append(keep, row)
			}
		}
		c.rec.dropped("group_field", fmt.Sprintf("按字段 %s 分组：平台把分组字段拆成多份文档，样例只留第一组的 %d 行（共 %d 行），分组字段作单据级字段", g, len(keep), len(rows)), c.table.ID)
		rows = keep
	}
	if len(rows) > doctemplates.MaxSampleRows {
		c.rec.add("rows_kept", fmt.Sprintf("原型有 %d 行样例数据，样例至多 %d 行，只留前 %d 行", len(rows), doctemplates.MaxSampleRows, doctemplates.MaxSampleRows))
		rows = rows[:doctemplates.MaxSampleRows]
	} else {
		c.rec.add("rows_kept", fmt.Sprintf("保留全部 %d 行样例数据", len(rows)))
	}
	c.kept = rows
}

// ---- slots ----

func (c *conv) dsField(id string) (protoField, bool) {
	for _, f := range c.pf.Dataset.Fields {
		if f.ID == id {
			return f, true
		}
	}
	return protoField{}, false
}

// rowSlot is the slot of a dataset field, created on first use. scope is the scope the first use gives it.
func (c *conv) rowSlot(id, scope string) *slot {
	if s, ok := c.slotBy["d:"+id]; ok {
		if scope == doctemplates.ScopeRow && s.scope == doctemplates.ScopeDocument {
			s.scope = doctemplates.ScopeRow
		}
		return s
	}
	f, _ := c.dsField(id)
	label := f.Label
	if label == "" {
		label = id
	}
	sid := id
	if c.taken[sid] == "m" {
		sid = "row_" + id
		c.rec.add("slot_collision", fmt.Sprintf("单据级字段与明细字段同名 %s：明细字段改名为 %s", id, sid))
	}
	s := &slot{id: sid, label: label, src: "d", srcID: id, scope: scope}
	c.slotBy["d:"+id], c.taken[sid] = s, "d"
	c.slots = append(c.slots, s)
	return s
}

// metaSlot is the slot of a document-level value (the prototype's scope "meta").
func (c *conv) metaSlot(id, label string) *slot {
	if s, ok := c.slotBy["m:"+id]; ok {
		return s
	}
	if label == "" {
		label = id
	}
	sid := id
	if c.taken[sid] == "d" {
		sid = "meta_" + id
		c.rec.add("slot_collision", fmt.Sprintf("单据级字段与明细字段同名 %s：单据级字段改名为 %s", id, sid))
	}
	s := &slot{id: sid, label: label, src: "m", srcID: id, scope: doctemplates.ScopeDocument}
	c.slotBy["m:"+id], c.taken[sid] = s, "m"
	c.slots = append(c.slots, s)
	return s
}

// value is the text a slot has in the first kept row.
func (c *conv) value(s *slot) string {
	if len(c.kept) == 0 {
		return ""
	}
	return c.valueIn(s, c.kept[0])
}

func (c *conv) valueIn(s *slot, row map[string]any) string {
	v := ""
	if s.src == "m" {
		v = c.pf.Meta[s.srcID]
	} else {
		v = rowValue(row[s.srcID])
	}
	return c.cleanValue(v)
}

// cleanValue makes a value one a sample row may hold: no control character. A line break becomes " / ".
func (c *conv) cleanValue(v string) string {
	if !strings.ContainsFunc(v, func(r rune) bool { return r < 32 || r == 0x7F }) {
		return v
	}
	v = strings.ReplaceAll(strings.ReplaceAll(v, "\r\n", "\n"), "\r", "\n")
	if strings.Contains(v, "\n") {
		c.lineBreaks++
	}
	v = strings.ReplaceAll(v, "\n", " / ")
	var b strings.Builder
	for _, r := range v {
		if r < 32 || r == 0x7F {
			c.controls++
			b.WriteRune(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---- text metrics (an estimate; the render of the pack is the check) ----

// emWidth estimates the width of a text in em: ideographs and full-width signs one em, digits and capitals a little over half, the rest less.
func emWidth(s string, bold bool) float64 {
	w := 0.0
	for _, r := range s {
		switch {
		case r >= 0x2E80 || (r >= 0x2000 && r <= 0x206F && r != 0x2019 && r != 0x2018):
			w += 1.0
		case r >= '0' && r <= '9':
			w += 0.58
		case r >= 'A' && r <= 'Z':
			w += 0.68
		case r >= 'a' && r <= 'z':
			w += 0.56
		case r == ' ':
			w += 0.27
		case r < 0x80:
			w += 0.5
		default:
			w += 0.8
		}
	}
	if bold {
		w *= 1.08
	}
	return w
}

// textLines estimates the lines a value takes in a box of the given width (mm) at a size (pt), breaking where the renderer does (a run
// wider than the line is cut): the value's own line breaks are lines.
func textLines(v string, widthMM float64, fs int, bold bool) int {
	avail := widthMM - 2*mmPerPt - 0.3
	if avail <= 0 {
		return 99
	}
	total := 0
	for _, para := range strings.Split(v, "\n") {
		w := emWidth(para, bold) * float64(fs) * mmPerPt
		n := int(math.Ceil(w/avail - 1e-9))
		if n < 1 {
			n = 1
		}
		total += n
	}
	return total
}

// lineCapacity is the number of lines a text box of the height (mm) holds at the size, as export/pdf counts them (1.46 font sizes for the
// first line, 1.5 for each next one).
func lineCapacity(heightMM float64, fs int) int {
	room := heightMM/mmPerPt/float64(fs) - 1.46
	return max(1, 1+int(math.Floor(room/1.5+1e-9)))
}

// minTextHeight is the least height of a text box at the size: the platform's 5 mm and the height of one line of the size.
func minTextHeight(fs int) float64 {
	return math.Max(5, ceil1(float64(fs)*400/72/10)) // tenths(h)*72 >= fs*400
}

// heightForLines is the height that holds n lines.
func heightForLines(n, fs int) float64 {
	return ceil1((1.46 + 1.5*float64(n-1)) * float64(fs) * mmPerPt)
}

// ---- fonts ----

// fontSize turns the size of a node (points, any number) into the platform's whole points, scaled by the fit of a fixed form.
func (c *conv) fontSize(n *protoNode, base float64) int {
	scaled := base * c.scale
	fs := int(math.Floor(scaled + 0.5))
	final := min(max(fs, 8), 28)
	switch {
	case c.scale < 1:
		if float64(final) != base {
			c.rec.add("font_scaled", fmt.Sprintf("整版按 %.3f 缩放：字号 %g 磅 → %d 磅（取整，范围 8～28 磅）", c.scale, base, final), n.ID)
		}
	case fs < 8:
		c.rec.add("font_raised", fmt.Sprintf("字号 %g 磅低于下限 8 磅，提高到 8 磅", base), n.ID)
	case fs > 28:
		c.rec.add("font_rounded", fmt.Sprintf("字号 %g 磅高于上限 28 磅，降到 28 磅", base), n.ID)
	case math.Abs(base-float64(fs)) > 1e-9:
		c.rec.add("font_rounded", fmt.Sprintf("字号 %g 磅取整为 %d 磅", base, fs), n.ID)
	}
	return final
}

func alignOf(a string) string {
	switch a {
	case "center", "right":
		return a
	}
	return "left"
}

// noteStyle records the style properties the platform's elements do not have.
func (c *conv) noteStyle(n *protoNode) {
	s := n.Style
	switch n.Type {
	case "line", "rect", "image", "signature", "summary", "barcode", "qr", "pageNumber", "text", "field", "aggregate", "watermark", "table":
	default:
		return
	}
	var parts []string
	if s.Color != "" {
		parts = append(parts, "颜色")
	}
	if s.Italic {
		parts = append(parts, "斜体")
	}
	if s.LetterSpace != 0 {
		parts = append(parts, "字距")
	}
	if s.LineHeight != 0 && s.LineHeight != 1.4 && n.Type != "line" && n.Type != "rect" {
		parts = append(parts, "行距")
	}
	if len(parts) > 0 {
		c.rec.add("style_dropped", "样式里平台没有的属性不转换："+strings.Join(parts, "、"), n.ID)
	}
}

// ---- text elements ----

// textSpec describes a text before the minimums of the platform are applied.
type textSpec struct {
	node         *protoNode
	source       string // literal, field, group_sum, page
	text, field  string
	sample       string // the value the box will hold, for the estimates
	x, y, w, h   float64
	fs           int
	align        string
	bold         bool
	bandH        float64
	forceWrap    bool
	keepHeight   bool // do not grow the height (the caller has laid out around it)
	noteOverfull bool
}

// text builds a text element from a spec: it raises the box to the platform's minimum, decides wrap, and shrinks the type when the value
// would not fit on the lines the box holds. It returns the element and records what it changed.
func (c *conv) text(sp textSpec) doctemplates.Item {
	n := sp.node
	fs := sp.fs
	w, h := sp.w, sp.h
	if w < 10 {
		c.rec.add("box_grown", "文本框宽度小于 10 毫米，放大到 10 毫米", n.ID)
		w = 10
	}
	lines := textLines(sp.sample, w, fs, sp.bold)
	// Lines the box holds at the size; shrink the type (to 8) before growing the box.
	for lines > lineCapacity(math.Max(h, minTextHeight(fs)), fs) && fs > 8 && h < heightForLines(lines, fs) && !sp.keepHeight {
		// try a smaller size when it brings the lines within the capacity of the box as it is
		smaller := fs - 1
		l2 := textLines(sp.sample, w, smaller, sp.bold)
		if l2 <= lineCapacity(math.Max(h, minTextHeight(smaller)), smaller) {
			fs, lines = smaller, l2
			c.rec.add("font_scaled", "取值放不下，字号缩小到能放下为止（不低于 8 磅）", n.ID)
			break
		}
		fs = smaller
		lines = l2
		c.rec.add("font_scaled", "取值放不下，字号缩小到能放下为止（不低于 8 磅）", n.ID)
	}
	need := minTextHeight(fs)
	if lines > 1 {
		need = math.Max(need, heightForLines(lines, fs))
	}
	if h < need && !sp.keepHeight {
		if h < minTextHeight(fs) || lines > lineCapacity(h, fs) {
			c.rec.add("box_grown", "文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大", n.ID)
		}
		h = need
	}
	if h < minTextHeight(fs) {
		h = minTextHeight(fs)
	}
	wrap := sp.forceWrap || lines > 1 || strings.Contains(sp.text, "\n") || lineCapacity(h, fs) >= 2 && sp.source != "page"
	if sp.source == "page" {
		wrap = false
	}
	if lines > lineCapacity(h, fs) {
		c.rec.add("fit_risk", fmt.Sprintf("按字体估算取值 %q 在 %g×%g 毫米的框里需要 %d 行，框只放得下 %d 行", clip(sp.sample, 24), r1(w), r1(h), lines, lineCapacity(h, fs)), n.ID)
	}
	it := doctemplates.Item{Type: doctemplates.TypeText, Source: sp.source, Text: sp.text, Field: sp.field, Wrap: wrap, X: r1(sp.x), Y: r1(sp.y), Width: r1(w), Height: r1(h), FontSize: fs, Align: sp.align, Bold: sp.bold}
	if sp.source == doctemplates.SourceField || sp.source == doctemplates.SourceGroupSum {
		it.Text = ""
	} else {
		it.Field = ""
	}
	return it
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// ---- the rules, one per node type ----
//
// Each rule takes the candidate (its node and its box in a band of the sample, already fitted) and returns the elements it makes.

func (c *conv) nodeElements(cd *cand) []placed {
	n := cd.n
	c.noteStyle(n)
	switch n.Type {
	case "text":
		return c.nodeText(cd)
	case "field":
		return c.nodeField(cd)
	case "line":
		return c.nodeLine(cd)
	case "rect":
		return c.nodeRect(cd)
	case "image":
		return c.nodeImage(cd)
	case "signature":
		return c.nodeSignature(cd)
	case "summary":
		return c.nodeSummary(cd)
	case "aggregate":
		return c.nodeAggregate(cd)
	case "barcode":
		return c.nodeBarcode(cd)
	case "qr":
		return c.nodeQR(cd)
	case "pageNumber":
		return c.nodePageNumber(cd)
	case "watermark":
		c.rec.dropped("watermark", "水印不转换（平台的元素里没有水印）", n.ID)
		return nil
	}
	c.rec.dropped("node_unknown", "未知的节点类型 "+n.Type+" 不转换", n.ID)
	return nil
}

// nodeText: text -> text (size, bold, align, wrap from the style; colours dropped).
func (c *conv) nodeText(cd *cand) []placed {
	n := cd.n
	it := c.text(textSpec{node: n, source: doctemplates.SourceLiteral, text: n.Text, sample: n.Text, x: cd.x, y: cd.y, w: cd.w, h: cd.h, fs: c.fontSize(n, n.Style.Size), align: alignOf(n.Style.Align), bold: n.Style.Bold})
	return []placed{{it, n.ID}}
}

// labelled lays a field's label and value out in the box of the node. A label above the value when the box is tall enough for both; the
// label to the left when it is wide enough; otherwise no label (the record says so). It returns the label's element (or none) and the
// box left for the value.
func (c *conv) labelled(cd *cand, label string) (lab *doctemplates.Item, x, y, w, h float64) {
	n := cd.n
	x, y, w, h = cd.x, cd.y, cd.w, cd.h
	if label == "" {
		return nil, x, y, w, h
	}
	vfs := c.fontSize(n, n.Style.Size)
	switch {
	case h >= 5+math.Max(5, minTextHeight(vfs)):
		it := c.text(textSpec{node: n, source: doctemplates.SourceLiteral, text: label, sample: label, x: x, y: y, w: w, h: 5, fs: 8, align: alignOf(n.Style.Align), keepHeight: true})
		c.rec.add("field_label", "字段的标签在取值上方另出一个 8 磅文本", n.ID)
		return &it, x, y + 5, w, h - 5
	case w >= 24:
		lw := math.Min(math.Max(10, floor1(emWidth(label, false)*8*mmPerPt+2*mmPerPt+1)), floor1(w*0.5))
		it := c.text(textSpec{node: n, source: doctemplates.SourceLiteral, text: label, sample: label, x: x, y: y, w: lw, h: h, fs: 8, align: "left", keepHeight: true})
		c.rec.add("field_label", "字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）", n.ID)
		return &it, x + lw, y, w - lw, h
	}
	c.rec.dropped("field_label", "字段的标签舍去（框太小，放不下标签和取值）", n.ID)
	return nil, x, y, w, h
}

// nodeField: field scope meta -> a document-scope slot; scope row outside the table -> a row slot (of the first row); the label becomes a
// text beside or above it.
func (c *conv) nodeField(cd *cand) []placed {
	n := cd.n
	var s *slot
	switch n.Scope {
	case "meta":
		label := n.Label
		if label == "" {
			label = n.Name
		}
		s = c.metaSlot(n.Field, label)
	case "row", "":
		s = c.rowSlot(n.Field, doctemplates.ScopeRow)
	default:
		c.rec.dropped("node_unknown", fmt.Sprintf("字段来源 %q（参数）不转换", n.Scope), n.ID)
		return nil
	}
	if n.Format != "" && n.Format != "text" {
		c.rec.add("format", fmt.Sprintf("显示格式 %q 不转换，取值按原样输出", n.Format), n.ID)
	}
	var out []placed
	label := n.Label
	lab, x, y, w, h := c.labelled(cd, label)
	if lab != nil {
		out = append(out, placed{*lab, n.ID})
	}
	it := c.text(textSpec{node: n, source: doctemplates.SourceField, field: s.id, sample: c.value(s), x: x, y: y, w: w, h: h, fs: c.fontSize(n, n.Style.Size), align: alignOf(n.Style.Align), bold: n.Style.Bold})
	return append(out, placed{it, n.ID})
}

func (c *conv) lineStroke(thickness float64) float64 {
	pt := thickness / mmPerPt
	return math.Min(2, math.Max(0.25, math.Round(pt*4)/4))
}

// nodeLine: line -> line along the longer side of the node's box, as thick as the shorter.
func (c *conv) nodeLine(cd *cand) []placed {
	n := cd.n
	it := doctemplates.Item{Type: doctemplates.TypeLine}
	if cd.w >= cd.h {
		it.X1, it.X2 = r1(cd.x), r1(cd.x+cd.w)
		it.Y1 = r1(cd.y + cd.h/2)
		it.Y2 = it.Y1
		it.Stroke = c.lineStroke(cd.h)
	} else {
		it.Y1, it.Y2 = r1(cd.y), r1(cd.y+cd.h)
		it.X1 = r1(cd.x + cd.w/2)
		it.X2 = it.X1
		it.Stroke = c.lineStroke(cd.w)
	}
	return []placed{{it, n.ID}}
}

// nodeRect: rect -> rect, the border a 0.75 pt line and a fill other than none the lightest grey.
func (c *conv) nodeRect(cd *cand) []placed {
	n := cd.n
	fill := "none"
	if n.Fill != "" && n.Fill != "none" && n.Fill != "transparent" {
		fill = "grey1"
		c.rec.substituted("style_dropped", "矩形的填充色换成最浅的灰（grey1）", n.ID)
	}
	return []placed{{doctemplates.Item{Type: doctemplates.TypeRect, X: r1(cd.x), Y: r1(cd.y), Width: r1(cd.w), Height: r1(cd.h), Stroke: 0.75, Fill: fill}, n.ID}}
}

// nodeImage: image -> a rectangle with the node's name (the prototype's images are empty; the platform's image slot is a private image,
// which a sample does not carry).
func (c *conv) nodeImage(cd *cand) []placed {
	n := cd.n
	c.rec.substituted("image", "图片节点转成方框加名称：原型的图片为空，只显示内置的标识图形，平台的样例不带私有图片", n.ID)
	out := []placed{{doctemplates.Item{Type: doctemplates.TypeRect, X: r1(cd.x), Y: r1(cd.y), Width: r1(cd.w), Height: r1(cd.h), Stroke: 0.75, Fill: "none"}, n.ID}}
	if n.Name != "" && cd.w >= 10 && cd.h >= 5 {
		fs := c.fontSize(n, 9)
		it := c.text(textSpec{node: n, source: doctemplates.SourceLiteral, text: n.Name, sample: n.Name, x: cd.x, y: cd.y + math.Max(0, (cd.h-minTextHeight(fs))/2), w: cd.w, h: math.Min(cd.h, minTextHeight(fs)), fs: fs, align: "center"})
		out = append(out, placed{it, n.ID})
	}
	return out
}

// nodeSignature: signature -> a rectangle plus the literal; the signature itself belongs to a later stage.
func (c *conv) nodeSignature(cd *cand) []placed {
	n := cd.n
	c.rec.substituted("signature", "签章与签名属于后续阶段：转成方框加原文字", n.ID)
	out := []placed{{doctemplates.Item{Type: doctemplates.TypeRect, X: r1(cd.x), Y: r1(cd.y), Width: r1(cd.w), Height: r1(cd.h), Stroke: 0.5, Fill: "none"}, n.ID}}
	if n.Text != "" {
		it := c.text(textSpec{node: n, source: doctemplates.SourceLiteral, text: n.Text, sample: n.Text, x: cd.x, y: cd.y, w: cd.w, h: cd.h, fs: c.fontSize(n, n.Style.Size), align: alignOf(n.Style.Align), bold: n.Style.Bold})
		out = append(out, placed{it, n.ID})
	}
	return out
}

// numericFields are the fields the prototype's summary and carry-forward lines sum: the numeric fields but the unit price.
func (c *conv) numericFields() []protoField {
	var out []protoField
	for _, f := range c.pf.Dataset.Fields {
		if (f.Type == "integer" || f.Type == "decimal") && f.ID != "unitPrice" {
			out = append(out, f)
		}
	}
	return out
}

// nodeSummary: summary -> a group sum of each numeric column it shows (up to three), each under its label. The ledger's third figure, the
// difference of two, is no sum of one column: a literal label stands there and the record says so.
func (c *conv) nodeSummary(cd *cand) []placed {
	n := cd.n
	fields := c.numericFields()
	if len(fields) > 3 {
		fields = fields[:3]
	}
	type fig struct {
		label string
		f     *protoField
	}
	var figs []fig
	if c.pf.Doc.DatasetID == "ledger" {
		get := func(id string) *protoField {
			if f, ok := c.dsField(id); ok {
				return &f
			}
			return nil
		}
		figs = []fig{{"本期应收 · 示例合计", get("debit")}, {"本期已收 · 示例合计", get("credit")}, {"本期差额 · 示例合计", nil}}
	} else {
		for i := range fields {
			f := fields[i]
			figs = append(figs, fig{f.Label + " · 范围合计", &f})
		}
	}
	if len(figs) == 0 || cd.h < 10 {
		c.rec.dropped("summary", "汇总卡片没有数值列可合计或框太矮，不转换", n.ID)
		return nil
	}
	c.rec.substituted("summary", fmt.Sprintf("汇总卡片转成 %d 栏：标签文字加合计（group_sum）", len(figs)), n.ID)
	cw := floor1(cd.w / float64(len(figs)))
	var out []placed
	for i, fg := range figs {
		x := cd.x + float64(i)*cw
		lab := c.text(textSpec{node: n, source: doctemplates.SourceLiteral, text: fg.label, sample: fg.label, x: x, y: cd.y, w: cw - 1, h: 5, fs: 8, align: "left", keepHeight: true})
		out = append(out, placed{lab, n.ID})
		if fg.f == nil {
			c.rec.dropped("summary", "「本期差额」是两列合计之差，平台的合计只对单列求和：只留标签，不出数值", n.ID)
			continue
		}
		s := c.rowSlot(fg.f.ID, doctemplates.ScopeRow)
		s.metric = true
		val := c.text(textSpec{node: n, source: doctemplates.SourceGroupSum, field: s.id, sample: "9,999,999.99", x: x, y: cd.y + 5, w: cw - 1, h: math.Min(cd.h-5, 8), fs: 12, align: "left", bold: true})
		out = append(out, placed{val, n.ID})
	}
	return out
}

// nodeAggregate: aggregate -> group_sum of the named column when it is a sum over all rows of a numeric column, else a literal with the
// label (the record says what was lost).
func (c *conv) nodeAggregate(cd *cand) []placed {
	n := cd.n
	label := n.Label
	if label == "" {
		label = n.Name
	}
	f, ok := c.dsField(n.Field)
	if n.Method == "sum" && n.Scope == "all" && ok && (f.Type == "integer" || f.Type == "decimal") {
		s := c.rowSlot(f.ID, doctemplates.ScopeRow)
		s.metric = true
		c.rec.substituted("aggregate", fmt.Sprintf("合计（sum，全部明细）转成 group_sum(%s)", s.id), n.ID)
		var out []placed
		lab, x, y, w, h := c.labelled(&cand{n: n, band: cd.band, x: cd.x, y: cd.y, w: cd.w, h: cd.h}, label)
		if lab != nil {
			out = append(out, placed{*lab, n.ID})
		}
		it := c.text(textSpec{node: n, source: doctemplates.SourceGroupSum, field: s.id, sample: "9,999,999.99", x: x, y: y, w: w, h: h, fs: c.fontSize(n, n.Style.Size), align: alignOf(n.Style.Align), bold: n.Style.Bold})
		return append(out, placed{it, n.ID})
	}
	c.rec.substituted("aggregate", fmt.Sprintf("计算节点（%s，范围 %s，字段 %s）平台没有对应，转成带标签的文字", n.Method, n.Scope, n.Field), n.ID)
	it := c.text(textSpec{node: n, source: doctemplates.SourceLiteral, text: label, sample: label, x: cd.x, y: cd.y, w: cd.w, h: cd.h, fs: c.fontSize(n, n.Style.Size), align: alignOf(n.Style.Align), bold: n.Style.Bold})
	return []placed{{it, n.ID}}
}

// codeSlot is the slot a barcode or QR node takes its value from; a node with a fixed text has a literal.
func (c *conv) codeValue(n *protoNode) (source, field, text, sample string) {
	if n.Scope == "row" || n.Scope == "meta" || n.Field != "" {
		var s *slot
		if n.Scope == "meta" {
			s = c.metaSlot(n.Field, n.Name)
		} else {
			s = c.rowSlot(n.Field, doctemplates.ScopeRow)
		}
		return doctemplates.SourceField, s.id, "", c.value(s)
	}
	return doctemplates.SourceLiteral, "", n.Text, n.Text
}

// nodeBarcode: barcode -> code128 with the value of the field slot; showText -> human readable. The platform draws the bars at least as
// high as 15 % of the symbol's width (and 5 mm), so a box that is wide for its height is made narrower, centred in the box it had.
func (c *conv) nodeBarcode(cd *cand) []placed {
	n := cd.n
	if n.CodeType != "code128" && n.CodeType != "" {
		c.rec.substituted("barcode_symbology", fmt.Sprintf("条码类型 %s 换成 Code 128", n.CodeType), n.ID)
	}
	src, field, text, sample := c.codeValue(n)
	x, w, h := cd.x, math.Max(cd.w, doctemplates.MinCodeBox), math.Max(cd.h, doctemplates.MinBarHeight)
	fs := 0
	zone := 0.0
	if n.ShowText {
		fs = c.fontSize(n, math.Max(n.Style.Size, 8))
		zone = (1 + 1.46*float64(fs)) * mmPerPt // humanGap and one line of the type
	}
	modules, err := barcode.Code128Modules(sample)
	if err != nil {
		c.rec.add("fit_risk", fmt.Sprintf("条码取值 %q 不是 Code 128 能编的值：%v", clip(sample, 24), err), n.ID)
	} else {
		total := float64(modules + 2*barcode.Code128Quiet)
		minW := math.Ceil(total*2.5) / 10
		barH := h - zone
		if barH < 5.2 {
			h = ceil1(zone + 5.2)
			barH = h - zone
		}
		maxW := floor1(barH * total / (0.15 * float64(modules) * 1.01))
		nw := math.Min(w, maxW)
		if nw < minW {
			nw = minW
			h = ceil1(zone + math.Max(5.2, 0.15*float64(modules)*nw/total*1.01))
		}
		nw = math.Max(nw, minW)
		if nw < w-0.05 || h > math.Max(cd.h, doctemplates.MinBarHeight)+0.05 {
			c.rec.add("box_grown", fmt.Sprintf("条码框按平台的条高下限调整：宽 %g → %g 毫米（居中），高 %g → %g 毫米", r1(cd.w), r1(nw), r1(cd.h), r1(h)), n.ID)
		}
		x, w = x+(w-nw)/2, nw
	}
	it := doctemplates.Item{Type: doctemplates.TypeBarcode, Source: src, Field: field, Text: text, X: r1(x), Y: r1(cd.y), Width: r1(w), Height: r1(h), Symbology: doctemplates.SymbologyCode128, HumanReadable: n.ShowText, FontSize: fs}
	return []placed{{it, n.ID}}
}

// nodeQR: qr -> qrcode (error correction M) with the value of the field slot. The prototype's qr node can be another matrix code (PDF417);
// the platform has the QR code only, a square whose modules are at least 0.4 mm.
func (c *conv) nodeQR(cd *cand) []placed {
	n := cd.n
	if n.CodeType != "qr" && n.CodeType != "" {
		c.rec.substituted("barcode_symbology", fmt.Sprintf("二维码类型 %s 换成 QR 码（纠错级别 M）：PDF417 是宽扁的矩形，QR 码是正方形，边长取框的短边，不小于模块 0.4 毫米所需的大小", n.CodeType), n.ID)
	}
	src, field, text, sample := c.codeValue(n)
	size := math.Max(math.Min(cd.w, cd.h), doctemplates.MinCodeBox)
	if dim, err := barcode.QRDimension(len([]byte(sample)), "M"); err == nil {
		if need := math.Ceil(0.4*float64(dim+2*barcode.QRQuiet)*10+1) / 10; size < need {
			c.rec.add("box_grown", fmt.Sprintf("二维码边长 %g → %g 毫米（模块不小于 0.4 毫米）", r1(size), r1(need)), n.ID)
			size = need
		}
	} else {
		c.rec.add("fit_risk", fmt.Sprintf("二维码取值 %q 编不了：%v", clip(sample, 24), err), n.ID)
	}
	it := doctemplates.Item{Type: doctemplates.TypeQRCode, Source: src, Field: field, Text: text, X: r1(cd.x), Y: r1(cd.y), Size: r1(size), ECC: "M"}
	return []placed{{it, n.ID}}
}

var placeholderRe = regexp.MustCompile(`\{[^{}]*\}`)

// nodePageNumber: pageNumber -> the node's text (left) and a page text with {page} / {pages} from the pattern (right).
func (c *conv) nodePageNumber(cd *cand) []placed {
	n := cd.n
	c.rec.substituted("page_number", "页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）", n.ID)
	pattern := n.Pattern
	if pattern == "" {
		pattern = "第 {page} / {pages} 页"
	}
	clean := placeholderRe.ReplaceAllStringFunc(pattern, func(p string) string {
		if p == "{page}" || p == "{pages}" {
			return p
		}
		c.rec.add("page_number", fmt.Sprintf("页码样式里的 %s 平台没有，去掉", p), n.ID)
		return ""
	})
	fs := c.fontSize(n, n.Style.Size)
	var out []placed
	leftW := floor1(cd.w * 0.55)
	if n.Text == "" || cd.w < 22 {
		leftW = 0
	}
	if n.Text != "" && leftW >= 10 {
		it := c.text(textSpec{node: n, source: doctemplates.SourceLiteral, text: n.Text, sample: n.Text, x: cd.x, y: cd.y, w: leftW - 1, h: cd.h, fs: fs, align: "left"})
		out = append(out, placed{it, n.ID})
	}
	px, pw := cd.x+leftW, cd.w-leftW
	if !strings.Contains(clean, "{page}") && !strings.Contains(clean, "{pages}") {
		c.rec.add("page_number", "页码样式里没有 {page} / {pages}，只留文字", n.ID)
		return out
	}
	it := c.text(textSpec{node: n, source: doctemplates.SourcePage, text: clean, sample: strings.NewReplacer("{page}", "99", "{pages}", "99").Replace(clean), x: px, y: cd.y, w: pw, h: cd.h, fs: fs, align: "right"})
	return append(out, placed{it, n.ID})
}

// ---- assembling the sample ----

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + 32)
		} else if r == '-' {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fractionDigits(v string) int {
	if i := strings.IndexByte(v, '.'); i >= 0 {
		return len(v) - i - 1
	}
	return 0
}

// assemble puts the bands, the slots and the rows together into the sample and has doctemplates decide whether it is one.
func (c *conv) assemble() (doctemplates.Sample, *Record, error) {
	d := c.pf.Doc
	doc := doctemplates.Document{Schema: doctemplates.SchemaCoded, Kind: "document", Binding: doctemplates.SampleBinding}
	doc.Page = &doctemplates.Page{Preset: c.pre.ID}
	if d.Page.Mode == "variables" || d.Page.OffsetX != 0 || d.Page.OffsetY != 0 {
		variables := d.Page.Mode == "variables"
		doc.Page.Print = &doctemplates.PagePrint{OffsetX: d.Page.OffsetX, OffsetY: d.Page.OffsetY, VariablesOnly: variables}
		if variables {
			c.rec.add("overprint", "套打（mode=variables）转成 page.print.variables_only：只印变量，文字、线、框不印；偏移取原型的偏移")
			c.rec.add("overprint", "背景留空：原型的预印版面是自己画的图形，不是位图，平台的 page.print.background 需要一张 PNG")
		} else {
			c.rec.add("overprint", fmt.Sprintf("整页打印的偏移 (%g, %g) 毫米转成 page.print 的偏移", d.Page.OffsetX, d.Page.OffsetY))
		}
	}
	if len(c.group) > 0 {
		doc.Group = &doctemplates.GroupSpec{Fields: c.group}
	}
	next := 0
	number := func(items []placed) []doctemplates.Item {
		out := make([]doctemplates.Item, 0, len(items))
		for _, p := range items {
			next++
			it := p.item
			it.ID = fmt.Sprintf("e%d", next)
			out = append(out, it)
		}
		return out
	}
	band := func(name string) *doctemplates.Band {
		items := c.bands[name]
		h, ok := c.height[name]
		if !ok {
			return nil
		}
		return &doctemplates.Band{Height: h, Elements: number(items)}
	}
	bands := &doctemplates.Bands{}
	bands.PageHeader = band(doctemplates.BandPageHeader)
	bands.DocumentHeader = band(doctemplates.BandDocumentHeader)
	if len(c.bands[doctemplates.BandDetail]) > 0 {
		bands.Detail = &doctemplates.DetailBand{Elements: number(c.bands[doctemplates.BandDetail])}
	}
	bands.DocumentFooter = band(doctemplates.BandDocumentFooter)
	bands.PageFooter = band(doctemplates.BandPageFooter)
	doc.Bands = bands
	// Slots and rows.
	var fields []doctemplates.SampleField
	var rows []map[string]string
	for range c.kept {
		rows = append(rows, map[string]string{})
	}
	for _, s := range c.slots {
		f := doctemplates.SampleField{ID: s.id, Label: s.label, Scope: s.scope, Type: "text"}
		var values []string
		for i, row := range c.kept {
			v := c.valueIn(s, row)
			rows[i][s.id] = v
			values = append(values, v)
		}
		if s.src == "d" {
			pf, _ := c.dsField(s.srcID)
			switch pf.Type {
			case "date":
				f.Type = "date"
			case "integer", "decimal":
				ok := true
				digits := 0
				for _, v := range values {
					if !exactNumberRe.MatchString(v) {
						ok = false
					}
					digits = max(digits, fractionDigits(v))
				}
				if ok && digits <= 6 {
					f.Type = pf.Type
					if pf.Type == "integer" && digits > 0 {
						f.Type = "decimal"
					}
					if f.Type == "decimal" {
						f.Scale = max(pf.Scale, digits)
					}
				} else {
					c.rec.add("not_numeric", fmt.Sprintf("字段 %s（%s）的取值不全是精确数字（有空值或非数字），样例里按文本处理", s.srcID, pf.Type))
				}
			}
		}
		if s.metric {
			if (f.Type != "integer" && f.Type != "decimal") || f.Scope != doctemplates.ScopeRow {
				return doctemplates.Sample{}, nil, fmt.Errorf("slot %s is summed but is no numeric row field", s.id)
			}
			f.Metric = true
		}
		fields = append(fields, f)
	}
	if c.lineBreaks > 0 || c.controls > 0 {
		c.rec.add("line_break", fmt.Sprintf("样例取值里的 %d 处换行换成「 / 」，%d 处其他控制字符换成空格（样例行不含控制字符）", c.lineBreaks, c.controls))
	}
	// Document-scope slots have one value in all rows: the form's single row, the table's header values.
	desc := c.pf.Dataset.Description
	if desc == "" {
		desc = "原型模板「" + c.pf.Doc.Name + "」"
	}
	sample := doctemplates.Sample{Kind: snake(c.pf.Source.File), Name: d.Name, Domain: c.pf.Catalog.DomainLabel, Flow: c.pf.Catalog.FlowLabel, Description: desc, Fields: fields, Rows: rows, Document: doc}
	c.rec.Kind = sample.Kind
	c.rec.Counts.ElementsOut = sample.ElementCount()
	produced := map[string]bool{}
	for _, items := range c.bands {
		for _, p := range items {
			produced[p.node] = true
		}
	}
	c.rec.seal(produced)
	data, err := marshalSample(sample)
	if err != nil {
		return doctemplates.Sample{}, nil, err
	}
	if _, err := doctemplates.DecodeSample(data); err != nil {
		return doctemplates.Sample{}, nil, fmt.Errorf("%s: doctemplates refuses the sample: %v%s", sample.Kind, err, c.locate(sample))
	}
	return sample, c.rec, nil
}

// locate names the elements that are invalid by themselves, for the message of a refusal (doctemplates says only "invalid").
func (c *conv) locate(s doctemplates.Sample) string {
	var bad []string
	d := s.Document
	for _, bi := range d.Items() {
		if bi.Item.Type == doctemplates.TypeTable {
			continue
		}
		probe := d
		probe.Bands = &doctemplates.Bands{Detail: d.Bands.Detail}
		one := &doctemplates.Band{Height: 5, Elements: []doctemplates.Item{bi.Item}}
		switch bi.Band {
		case doctemplates.BandPageHeader:
			one.Height = d.Bands.PageHeader.Height
			probe.Bands.PageHeader = one
		case doctemplates.BandDocumentHeader:
			one.Height = d.Bands.DocumentHeader.Height
			probe.Bands.DocumentHeader = one
		case doctemplates.BandDocumentFooter:
			one.Height = d.Bands.DocumentFooter.Height
			probe.Bands.DocumentFooter = one
		case doctemplates.BandPageFooter:
			one.Height = d.Bands.PageFooter.Height
			probe.Bands.PageFooter = one
		}
		if doctemplates.Validate(probe) != nil {
			bad = append(bad, fmt.Sprintf("%s in %s: %+v", bi.Item.ID, bi.Band, bi.Item))
		}
	}
	heights := ""
	for _, b := range []struct {
		n string
		b *doctemplates.Band
	}{{"ph", d.Bands.PageHeader}, {"dh", d.Bands.DocumentHeader}, {"df", d.Bands.DocumentFooter}, {"pf", d.Bands.PageFooter}} {
		if b.b != nil {
			heights += fmt.Sprintf(" %s=%g(%d)", b.n, b.b.Height, len(b.b.Elements))
		}
	}
	if len(bad) == 0 {
		return " [" + heights + " ]" + " (no single element is invalid: the bands' heights, the element count or the table)"
	}
	return "\n  invalid elements:\n  " + strings.Join(bad, "\n  ")
}
