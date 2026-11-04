package main

import (
	"fmt"
	"sort"
	"strings"
)

// Entry is one thing the converter did to a template that the template's author should know: a drop, a substitution, a change of a number.
// Entries with the same code and detail are one entry that lists every node it concerns.
type Entry struct {
	Code   string   `json:"code"`
	Detail string   `json:"detail"`
	Nodes  []string `json:"nodes,omitempty"`
}

// Counts are the numbers of a template before and after.
type Counts struct {
	NodesIn     int `json:"nodes_in"`
	ElementsOut int `json:"elements_out"`
	Dropped     int `json:"dropped"`
	Substituted int `json:"substituted"`
}

// Record is samples/<kind>.record.json: what the converter did to one template of the prototype.
type Record struct {
	Kind      string  `json:"kind"`
	File      string  `json:"file"`
	Name      string  `json:"name"`
	Dataset   string  `json:"dataset"`
	Page      string  `json:"page"`
	Preset    string  `json:"preset"`
	Counts    Counts  `json:"counts"`
	Entries   []Entry `json:"entries"`
	byKey     map[string]int
	dropNodes map[string]bool
	subNodes  map[string]bool
}

func newRecord(kind, file, name, dataset string) *Record {
	return &Record{Kind: kind, File: file, Name: name, Dataset: dataset, byKey: map[string]int{}, dropNodes: map[string]bool{}, subNodes: map[string]bool{}}
}

// add notes something; nodes are the ids of the prototype's nodes it concerns (none for a note about the template).
func (r *Record) add(code, detail string, nodes ...string) {
	key := code + "\x00" + detail
	i, ok := r.byKey[key]
	if !ok {
		i = len(r.Entries)
		r.byKey[key] = i
		r.Entries = append(r.Entries, Entry{Code: code, Detail: detail})
	}
	for _, n := range nodes {
		dup := false
		for _, m := range r.Entries[i].Nodes {
			if m == n {
				dup = true
			}
		}
		if !dup {
			r.Entries[i].Nodes = append(r.Entries[i].Nodes, n)
		}
	}
}

// dropped notes that a node (or a property of it that matters) has no counterpart in the sample.
func (r *Record) dropped(code, detail string, nodes ...string) {
	for _, n := range nodes {
		r.dropNodes[n] = true
	}
	r.add(code, detail, nodes...)
}

// substituted notes a node that the sample draws as something else.
func (r *Record) substituted(code, detail string, nodes ...string) {
	for _, n := range nodes {
		r.subNodes[n] = true
	}
	r.add(code, detail, nodes...)
}

// seal fixes the counts. A node is dropped when it was reported dropped and none of its elements is in the sample; it is substituted when
// it was reported so and some of its elements are.
func (r *Record) seal(produced map[string]bool) {
	r.Counts.Dropped, r.Counts.Substituted = 0, 0
	for n := range r.dropNodes {
		if !produced[n] {
			r.Counts.Dropped++
		}
	}
	for n := range r.subNodes {
		if produced[n] {
			r.Counts.Substituted++
		}
	}
	sort.SliceStable(r.Entries, func(i, j int) bool { return r.Entries[i].Code < r.Entries[j].Code })
}

// legend explains the codes in docs/SAMPLES.md.
var legend = [][2]string{
	{"band_clipped", "节点超出所在区段，已缩到区段内"},
	{"band_height", "区段高度低于下限 5 毫米而提高，或为让开互相重叠的文本而略加高（至多 12 毫米）"},
	{"barcode_symbology", "条码类型换成 Code 128 / 二维码类型换成 QR"},
	{"box_grown", "文本框小于平台下限（宽 10、高 5 毫米，高度不小于字号所需），已放大"},
	{"detail_extra", "明细区段里表格之外的节点（平台的明细区段只放一张表）"},
	{"element_overlap", "与先出现的文本 / 码 / 图框重叠，已调整位置或舍去"},
	{"field_label", "字段节点的标签，另出一个文本（上下或左右）或舍去"},
	{"font_raised", "字号低于 8 磅，提高到 8 磅"},
	{"font_rounded", "字号取整磅"},
	{"font_scaled", "整版缩放后字号随之缩小"},
	{"form_fit", "固定版式转成表单（没有明细带）：整版放在页眉区段，放不进安全区时才等比缩小"},
	{"line_break", "样例取值里的换行换成「 / 」（样例取值不含控制字符）"},
	{"node_hidden", "隐藏的节点不转换"},
	{"node_unknown", "原型节点的未知属性"},
	{"not_numeric", "字段取值不是精确数字，样例里按文本处理"},
	{"overprint", "套打：只印变量，背景留空"},
	{"page_background", "原型的底图 / 透明度 / 版式参考图"},
	{"page_size", "纸张尺寸与平台预设的对应"},
	{"parameters", "原型的查询参数（平台的查询面板就是参数）"},
	{"rows_kept", "样例保留的行"},
	{"signature", "签章与签名属于后续阶段，转成方框加文字"},
	{"slot_collision", "单据级字段与明细字段同名，单据级字段改名"},
	{"style_dropped", "样式里平台没有的属性（颜色、斜体、字距、行距）"},
	{"summary", "汇总卡片转成各数值列的合计"},
	{"table_option", "明细表的选项：平台版式已固定，或平台没有对应"},
	{"watermark", "水印不转换"},
	{"aggregate", "计算节点：求和转合计，其他转带标签的文字"},
	{"image", "图片转成方框加名称"},
	{"page_number", "页码节点转成页脚文字和页码文字"},
	{"fit_risk", "按字体估算取值可能放不下，以渲染结果为准"},
	{"group_field", "分组字段：平台按分组字段拆成多份文档，样例只留第一组的行"},
	{"format", "字段的显示格式（日期、数值、币种）由平台按取值原样输出"},
}

func legendOf(code string) string {
	for _, l := range legend {
		if l[0] == code {
			return l[1]
		}
	}
	return ""
}

// markdown renders the per-template section of docs/SAMPLES.md.
func (r *Record) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "### `%s` · %s\n\n", r.Kind, r.Name)
	fmt.Fprintf(&b, "原型数据集 `%s`，纸张 %s → `%s`；节点 %d → 元素 %d（舍去 %d 个节点，改写 %d 个）。\n\n", r.Dataset, r.Page, r.Preset, r.Counts.NodesIn, r.Counts.ElementsOut, r.Counts.Dropped, r.Counts.Substituted)
	if len(r.Entries) == 0 {
		b.WriteString("无舍去或改写。\n\n")
		return b.String()
	}
	for _, e := range r.Entries {
		nodes := ""
		if len(e.Nodes) > 0 {
			shown := e.Nodes
			more := ""
			if len(shown) > 6 {
				more = fmt.Sprintf(" 等 %d 个", len(e.Nodes))
				shown = shown[:6]
			}
			nodes = "（" + strings.Join(shown, "、") + more + "）"
		}
		fmt.Fprintf(&b, "- `%s`：%s%s\n", e.Code, e.Detail, nodes)
	}
	b.WriteString("\n")
	return b.String()
}
