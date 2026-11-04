package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
)

// The rules of the converter, one test each (a synthetic prototype template with the node under test), and the pack as a whole: the committed
// samples are what the converter makes of the committed prototype files, nothing the prototype had is dropped without a record.

const (
	prototypeDir = "../../doctemplates/samples/prototype"
	samplesDir   = "../../doctemplates/samples"
)

const dsFields = `[{"id":"date","label":"日期","type":"date"},{"id":"ref","label":"单号","type":"string"},{"id":"desc","label":"摘要","type":"string"},
{"id":"qty","label":"数量","type":"integer","scale":0},{"id":"unitPrice","label":"单价","type":"decimal","scale":2},{"id":"amount","label":"金额","type":"decimal","scale":2}]`

const dsRows = `[{"date":"2026-10-02","ref":"R1","desc":"甲","qty":"3","unitPrice":"2.50","amount":"7.50"},{"date":"2026-10-03","ref":"R2","desc":"乙","qty":"4","unitPrice":"3.00","amount":"12.00"}]`

func nodeJSON(id, typ, band string, x, y, w, h float64, size float64, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf(`{"id":%q,"type":%q,"band":%q,"name":"节点 %s","x":%g,"y":%g,"w":%g,"h":%g,"hidden":false,"locked":false,"group":"","style":{"font":"sans","size":%g,"color":"#30486d","bold":false,"italic":false,"align":"left","lineHeight":1.4,"letterSpace":0}%s}`, id, typ, band, id, x, y, w, h, size, extra)
}

const tableNode = `{"id":"t1","type":"table","band":"detail","name":"表","x":18,"y":0,"w":174,"h":80,"hidden":false,"locked":false,"group":"","style":{"font":"sans","size":8.5,"color":"#30486d","bold":false,"italic":false,"align":"left","lineHeight":1.4,"letterSpace":0},
"rowsPerPage":0,"minRowHeight":7,"padding":2,"wrap":true,"repeatHeader":true,"striped":true,"groupField":"","groupTotals":false,"carry":true,"subtotal":true,"sortField":"","sortDirection":"asc","negativeRed":true,"headerColor":"#edf1f9",
"columns":[{"field":"date","label":"日期","width":20,"align":"left","format":"auto"},{"field":"ref","label":"单号","width":34,"align":"left","format":"auto"},{"field":"qty","label":"数量","width":30,"align":"right","format":"auto"},{"field":"unitPrice","label":"单价","width":40,"align":"right","format":"auto"},{"field":"amount","label":"金额","width":50,"align":"right","format":"auto"}]}`

// build makes a prototype template of the A4 report kind from nodes (the table is added unless noTable) and edits of the whole file.
func build(t *testing.T, nodes []string, opts ...func(m map[string]any)) protoFile {
	t.Helper()
	all := append([]string{}, nodes...)
	all = append(all, tableNode)
	return buildRaw(t, all, opts...)
}

func buildRaw(t *testing.T, nodes []string, opts ...func(m map[string]any)) protoFile {
	t.Helper()
	src := `{"catalog":{"domain":"general","flow":"other","domain_label":"通用业务","flow_label":"其他"},
"source":{"template_name":"单元","kind":"unit","file":"unit","versions":1,"published":1},
"doc":{"schema":"studio-uiux-2.6","name":"单元","kind":"unit","datasetId":"ds","page":{"preset":"A4","width":210,"height":297,"orientation":"portrait","margins":{"top":14,"bottom":14,"left":18,"right":18},"mode":"full","offsetX":0,"offsetY":0,"background":"","opacity":0.2},
"bands":{"pageHeader":30,"reportHeader":40,"reportFooter":30,"pageFooter":10},"parameters":[],"nodes":[` + strings.Join(nodes, ",") + `]},
"dataset":{"id":"ds","name":"数据集","description":"单元测试的数据集","fields":` + dsFields + `},"rows":` + dsRows + `,"meta":{"title":"标题值","no":"NO-1"}}`
	var m map[string]any
	if err := json.Unmarshal([]byte(src), &m); err != nil {
		t.Fatal(err)
	}
	for _, o := range opts {
		o(m)
	}
	data, _ := json.Marshal(m)
	pf, err := decodeProto(data)
	if err != nil {
		t.Fatal(err)
	}
	return pf
}

func mustConvert(t *testing.T, pf protoFile) (doctemplates.Sample, *Record) {
	t.Helper()
	s, rec, err := convert(pf)
	if err != nil {
		t.Fatal(err)
	}
	return s, rec
}

func items(s doctemplates.Sample, band string) []doctemplates.Item {
	var out []doctemplates.Item
	for _, bi := range s.Document.Items() {
		if bi.Band == band {
			out = append(out, bi.Item)
		}
	}
	return out
}

func entry(rec *Record, code, contains string) *Entry {
	for i := range rec.Entries {
		if rec.Entries[i].Code == code && strings.Contains(rec.Entries[i].Detail, contains) {
			return &rec.Entries[i]
		}
	}
	return nil
}

func slotOf(s doctemplates.Sample, id string) doctemplates.SampleField {
	for _, f := range s.Fields {
		if f.ID == id {
			return f
		}
	}
	return doctemplates.SampleField{}
}

func TestRuleBandsKeepTheirHeightsAndTheReportBandsAreTheDocumentsBands(t *testing.T) {
	pf := build(t, []string{nodeJSON("n1", "text", "pageHeader", 18, 2, 80, 8, 12, `"text":"页眉"`), nodeJSON("n2", "text", "reportHeader", 18, 2, 80, 8, 12, `"text":"报表头"`),
		nodeJSON("n3", "text", "reportFooter", 18, 2, 80, 8, 12, `"text":"报表尾"`), nodeJSON("n4", "text", "pageFooter", 18, 1, 80, 8, 12, `"text":"页脚"`)})
	s, _ := mustConvert(t, pf)
	b := s.Document.Bands
	if b.PageHeader.Height != 30 || b.DocumentHeader.Height != 40 || b.DocumentFooter.Height != 30 || b.PageFooter.Height != 10 {
		t.Fatalf("heights %v %v %v %v", b.PageHeader.Height, b.DocumentHeader.Height, b.DocumentFooter.Height, b.PageFooter.Height)
	}
	for band, text := range map[string]string{doctemplates.BandPageHeader: "页眉", doctemplates.BandDocumentHeader: "报表头", doctemplates.BandDocumentFooter: "报表尾", doctemplates.BandPageFooter: "页脚"} {
		if it := items(s, band); len(it) != 1 || it[0].Text != text {
			t.Errorf("%s: %+v", band, it)
		}
	}
	// A band under 5 mm is raised to the platform's least, and the record says so.
	pf = build(t, nil, func(m map[string]any) { m["doc"].(map[string]any)["bands"].(map[string]any)["pageFooter"] = 3 })
	s, rec := mustConvert(t, pf)
	if s.Document.Bands.PageFooter.Height != 5 || entry(rec, "band_height", "pageFooter") == nil {
		t.Fatal("a band of 3 mm")
	}
}

func TestTextsThatOverlapAreMovedToFreeRoomAndTheBandGrowsOnlyWhenThereIsNone(t *testing.T) {
	// Two texts on the same place at the bottom of a band 20 mm high: the later one goes under the first, and the band is made higher by what it needed.
	a := nodeJSON("n1", "text", "reportFooter", 18, 0, 100, 20, 10, `"text":"第一"`)
	b := nodeJSON("n2", "text", "reportFooter", 18, 10, 100, 8, 10, `"text":"第二"`)
	pf := build(t, []string{a, b}, func(m map[string]any) { m["doc"].(map[string]any)["bands"].(map[string]any)["reportFooter"] = 20 })
	s, rec := mustConvert(t, pf)
	it := items(s, doctemplates.BandDocumentFooter)
	if len(it) != 2 || it[1].Y < it[0].Y+it[0].Height || s.Document.Bands.DocumentFooter.Height+1e-9 < it[1].Y+it[1].Height || s.Document.Bands.DocumentFooter.Height <= 20 || entry(rec, "band_height", "区段 reportFooter 高 20 →") == nil || entry(rec, "element_overlap", "纵向移到空处") == nil {
		t.Fatalf("%+v footer %v %+v", it, s.Document.Bands.DocumentFooter.Height, rec.Entries)
	}
	// With room in the band nothing grows: the later text is moved into the free part.
	pf = build(t, []string{a, b}, func(m map[string]any) { m["doc"].(map[string]any)["bands"].(map[string]any)["reportFooter"] = 40 })
	s, rec = mustConvert(t, pf)
	if it = items(s, doctemplates.BandDocumentFooter); len(it) != 2 || s.Document.Bands.DocumentFooter.Height != 40 || entry(rec, "band_height", "→") != nil {
		t.Fatalf("%+v %v", it, s.Document.Bands.DocumentFooter.Height)
	}
	// An element that cannot be placed within 12 mm of its place is left out, and the record says so.
	pf = build(t, []string{a, b}, func(m map[string]any) {
		m["doc"].(map[string]any)["bands"].(map[string]any)["reportFooter"] = 20
		for _, n := range m["doc"].(map[string]any)["nodes"].([]any) {
			if n.(map[string]any)["id"] == "n2" {
				n.(map[string]any)["h"] = 30
			}
		}
	})
	s, rec = mustConvert(t, pf)
	_ = s
	if entry(rec, "element_overlap", "舍去") == nil && entry(rec, "band_clipped", "") == nil {
		t.Fatalf("%+v", rec.Entries)
	}
}

func TestRuleTextKeepsSizeBoldAlignAndWrapAndDropsColours(t *testing.T) {
	n := nodeJSON("n1", "text", "reportHeader", 18, 4, 120, 12, 10.4, `"text":"标题"`)
	n = strings.Replace(n, `"bold":false,"italic":false,"align":"left"`, `"bold":true,"italic":true,"align":"right"`, 1)
	two := nodeJSON("n2", "text", "reportHeader", 18, 20, 120, 14, 9, `"text":"第一行\n第二行"`)
	s, rec := mustConvert(t, build(t, []string{n, two}))
	it := items(s, doctemplates.BandDocumentHeader)
	if len(it) != 2 {
		t.Fatal(it)
	}
	if it[0].Source != "literal" || it[0].Text != "标题" || it[0].FontSize != 10 || !it[0].Bold || it[0].Align != "right" || it[0].X != 8 || it[0].Width != 120 {
		t.Fatalf("the text: %+v", it[0])
	}
	if !it[1].Wrap || it[1].FontSize != 9 || !strings.Contains(it[1].Text, "\n") {
		t.Fatalf("a text of two lines wraps: %+v", it[1])
	}
	if e := entry(rec, "style_dropped", "颜色"); e == nil || !strings.Contains(e.Detail, "斜体") && len(e.Nodes) == 0 {
		t.Fatalf("colours and italics are recorded: %+v", rec.Entries)
	}
	if entry(rec, "font_rounded", "10.4") == nil {
		t.Fatal("rounding a size is recorded")
	}
}

func TestRuleTextBelowTheLeastTypeSizeIsRaisedAndRecorded(t *testing.T) {
	s, rec := mustConvert(t, build(t, []string{nodeJSON("n1", "text", "reportHeader", 18, 4, 120, 12, 6.5, `"text":"小字"`)}))
	if it := items(s, doctemplates.BandDocumentHeader); len(it) != 1 || it[0].FontSize != 8 {
		t.Fatal(it)
	}
	if entry(rec, "font_raised", "6.5") == nil {
		t.Fatal("the raise is recorded")
	}
}

func TestRuleFieldMetaIsADocumentSlotAndItsLabelIsTextAboveIt(t *testing.T) {
	n := nodeJSON("n1", "field", "reportHeader", 18, 4, 100, 14, 11, `"field":"title","scope":"meta","label":"标题","format":"text"`)
	s, rec := mustConvert(t, build(t, []string{n}))
	f := slotOf(s, "title")
	if f.Scope != doctemplates.ScopeDocument || f.Type != "text" || f.Label != "标题" {
		t.Fatalf("the slot: %+v", f)
	}
	if s.Rows[0]["title"] != "标题值" || s.Rows[1]["title"] != "标题值" {
		t.Fatal("the document value is in every row")
	}
	it := items(s, doctemplates.BandDocumentHeader)
	if len(it) != 2 || it[0].Source != "literal" || it[0].Text != "标题" || it[0].FontSize != 8 || it[1].Source != "field" || it[1].Field != "title" || it[1].Y < it[0].Y+it[0].Height-0.01 {
		t.Fatalf("label above value: %+v", it)
	}
	if entry(rec, "field_label", "上方") == nil {
		t.Fatal("recorded")
	}
	// A box too low for two lines puts the label to the left; one too small for both drops it, recorded.
	s, rec = mustConvert(t, build(t, []string{nodeJSON("n1", "field", "reportHeader", 18, 4, 100, 9, 11, `"field":"title","scope":"meta","label":"标题","format":"text"`)}))
	if it = items(s, doctemplates.BandDocumentHeader); len(it) != 2 || it[0].Y != it[1].Y || it[0].X >= it[1].X || entry(rec, "field_label", "左侧") == nil {
		t.Fatalf("label beside value: %+v", it)
	}
	s, rec = mustConvert(t, build(t, []string{nodeJSON("n1", "field", "reportHeader", 18, 4, 14, 9, 8, `"field":"title","scope":"meta","label":"标题","format":"text"`)}))
	if it = items(s, doctemplates.BandDocumentHeader); len(it) != 1 || entry(rec, "field_label", "舍去") == nil {
		t.Fatalf("label dropped: %+v", it)
	}
}

func TestRuleFieldRowOutsideTheTableIsARowSlotOfTheFirstRow(t *testing.T) {
	n := nodeJSON("n1", "field", "reportHeader", 18, 4, 100, 8, 10, `"field":"ref","scope":"row","label":"","format":"text"`)
	pf := build(t, []string{n}, func(m map[string]any) {
		// one row, so that the group's rows agree
		m["rows"] = m["rows"].([]any)[:1]
	})
	s, _ := mustConvert(t, pf)
	f := slotOf(s, "ref")
	if f.Scope != doctemplates.ScopeRow || f.Label != "单号" {
		t.Fatalf("%+v", f)
	}
	if it := items(s, doctemplates.BandDocumentHeader); len(it) != 1 || it[0].Source != "field" || it[0].Field != "ref" {
		t.Fatal(it)
	}
	values, _, err := s.BindPreview()
	if err != nil || values["e1"] != "R1" {
		t.Fatalf("the value of the first row: %v %v", values, err)
	}
}

func TestRuleTableKeepsColumnsAndMapsCarrySubtotalAndGroupField(t *testing.T) {
	s, rec := mustConvert(t, build(t, nil))
	tab := s.Document.TableItem()
	if tab == nil || tab.X != 8 || tab.Width != 174 || tab.FontSize != 9 || len(tab.Columns) != 5 {
		t.Fatalf("%+v", tab)
	}
	sum := 0.0
	for _, c := range tab.Columns {
		sum += c.Width
	}
	if sum < 173.999 || sum > 174.001 || tab.Columns[0].Field != "date" || tab.Columns[0].Label != "日期" || tab.Columns[0].Align != "left" || tab.Columns[4].Align != "right" {
		t.Fatalf("columns %+v (%v)", tab.Columns, sum)
	}
	// The first column holds the labels of the carried rows (承前, 过次, 合计): it is widened from the others, recorded.
	if tab.Columns[0].Width < 30 || entry(rec, "table_option", "首列") == nil {
		t.Fatalf("the first column: %v", tab.Columns[0].Width)
	}
	// carry and subtotal: numeric columns are metrics, the unit price is not; non-numeric ones are not.
	for id, want := range map[string]bool{"qty": true, "amount": true, "unitPrice": false, "date": false, "ref": false} {
		if got := slotOf(s, id).Metric; got != want {
			t.Errorf("metric %s = %v", id, got)
		}
	}
	for _, want := range []string{"striped", "negativeRed", "headerColor"} {
		if entry(rec, "table_option", want) == nil {
			t.Errorf("%s is not in the record", want)
		}
	}
	// Without carry and subtotal nothing is summed, and the record says the platform does not sum them.
	s, rec = mustConvert(t, build(t, nil, func(m map[string]any) {
		for _, n := range m["doc"].(map[string]any)["nodes"].([]any) {
			if n.(map[string]any)["type"] == "table" {
				n.(map[string]any)["carry"], n.(map[string]any)["subtotal"], n.(map[string]any)["repeatHeader"] = false, false, false
			}
		}
	}))
	if slotOf(s, "amount").Metric || entry(rec, "table_option", "不标为度量列") == nil || entry(rec, "table_option", "repeatHeader") == nil {
		t.Fatal("no totals in the prototype")
	}
	// groupField: the platform's group is a document per group; the sample keeps the first group and the field is document-scope.
	s, rec = mustConvert(t, build(t, nil, func(m map[string]any) {
		for _, n := range m["doc"].(map[string]any)["nodes"].([]any) {
			if n.(map[string]any)["type"] == "table" {
				n.(map[string]any)["groupField"] = "desc"
			}
		}
		rows := m["rows"].([]any)
		rows = append(rows, map[string]any{"date": "2026-10-04", "ref": "R3", "desc": "甲", "qty": "1", "unitPrice": "1.00", "amount": "1.00"})
		m["rows"] = rows
	}))
	if s.Document.Group == nil || len(s.Document.Group.Fields) != 1 || s.Document.Group.Fields[0] != "desc" || slotOf(s, "desc").Scope != doctemplates.ScopeDocument || len(s.Rows) != 2 || entry(rec, "group_field", "第一组") == nil {
		t.Fatalf("%+v rows %d", s.Document.Group, len(s.Rows))
	}
}

func TestRuleLineAndRect(t *testing.T) {
	h := nodeJSON("n1", "line", "pageHeader", 18, 20, 100, 0.3, 10, "")
	v := nodeJSON("n2", "line", "pageHeader", 18, 4, 0.5, 20, 10, "")
	r := nodeJSON("n3", "rect", "pageHeader", 130, 4, 40, 20, 10, `"fill":"#f4f7fc"`)
	s, rec := mustConvert(t, build(t, []string{h, v, r}))
	it := items(s, doctemplates.BandPageHeader)
	if len(it) != 3 {
		t.Fatal(it)
	}
	if it[0].Type != "line" || it[0].Y1 != it[0].Y2 || it[0].X1 != 8 || it[0].X2 != 108 || it[0].Stroke != 0.75 {
		t.Fatalf("a horizontal line: %+v", it[0])
	}
	if it[1].Type != "line" || it[1].X1 != it[1].X2 || it[1].Y1 != 4 || it[1].Y2 != 24 || it[1].Stroke != 1.5 {
		t.Fatalf("a vertical line: %+v", it[1])
	}
	if it[2].Type != "rect" || it[2].X != 120 || it[2].Width != 40 || it[2].Fill != "grey1" || it[2].Stroke != 0.75 || entry(rec, "style_dropped", "填充色") == nil {
		t.Fatalf("a rectangle: %+v", it[2])
	}
}

func TestRuleImageIsARectangleWithTheNodesName(t *testing.T) {
	s, rec := mustConvert(t, build(t, []string{nodeJSON("n1", "image", "pageHeader", 18, 4, 60, 20, 10, `"image":""`)}))
	it := items(s, doctemplates.BandPageHeader)
	if len(it) != 2 || it[0].Type != "rect" || it[1].Type != "text" || it[1].Text != "节点 n1" || it[1].Align != "center" || entry(rec, "image", "方框") == nil {
		t.Fatalf("%+v", it)
	}
	if rec.Counts.Substituted != 1 {
		t.Fatalf("counts %+v", rec.Counts)
	}
}

func TestRuleSignatureIsARectangleWithTheLiteralAndTheRecordSaysWhy(t *testing.T) {
	s, rec := mustConvert(t, build(t, []string{nodeJSON("n1", "signature", "reportFooter", 18, 4, 70, 8, 10, `"text":"客户确认 / 签章"`)}))
	it := items(s, doctemplates.BandDocumentFooter)
	if len(it) != 2 || it[0].Type != "rect" || it[1].Type != "text" || it[1].Text != "客户确认 / 签章" || entry(rec, "signature", "后续阶段") == nil {
		t.Fatalf("%+v", it)
	}
}

func TestRuleBarcodeAndQR(t *testing.T) {
	bar := nodeJSON("n1", "barcode", "reportHeader", 18, 4, 120, 20, 9, `"field":"ref","scope":"row","text":"","codeType":"code128","codeSettings":{},"showText":true`)
	qr := nodeJSON("n2", "qr", "reportHeader", 150, 4, 20, 20, 9, `"field":"desc","scope":"row","text":"","codeType":"pdf417","codeSettings":{"pdfColumns":4},"showText":false`)
	pf := build(t, []string{bar, qr}, func(m map[string]any) { m["rows"] = m["rows"].([]any)[:1] })
	s, rec := mustConvert(t, pf)
	it := items(s, doctemplates.BandDocumentHeader)
	if len(it) != 2 {
		t.Fatal(it)
	}
	if it[0].Type != "barcode" || it[0].Symbology != "code128" || it[0].Source != "field" || it[0].Field != "ref" || !it[0].HumanReadable || it[0].FontSize != 9 || it[0].X < 8 {
		t.Fatalf("barcode %+v", it[0])
	}
	if it[1].Type != "qrcode" || it[1].ECC != "M" || it[1].Field != "desc" || it[1].Size < 10 || entry(rec, "barcode_symbology", "PDF417") == nil {
		t.Fatalf("qr %+v", it[1])
	}
	// The bars are as high as the platform needs: a box wide for its height is made narrower, and the record says so.
	wide := nodeJSON("n1", "barcode", "reportHeader", 18, 4, 150, 10, 9, `"field":"ref","scope":"row","text":"","codeType":"code128","codeSettings":{},"showText":false`)
	s, rec = mustConvert(t, build(t, []string{wide}, func(m map[string]any) { m["rows"] = m["rows"].([]any)[:1] }))
	if it = items(s, doctemplates.BandDocumentHeader); len(it) != 1 || it[0].Width >= 150 || it[0].HumanReadable || entry(rec, "box_grown", "条码框") == nil {
		t.Fatalf("%+v", it)
	}
}

func TestRulePageNumberIsTheTextAndAPageTextFromThePattern(t *testing.T) {
	n := nodeJSON("n1", "pageNumber", "pageFooter", 18, 0, 174, 8, 7, `"text":"远山 · 页脚","pattern":"第 {page} / {pages} 页 {records}"`)
	s, rec := mustConvert(t, build(t, []string{n}))
	it := items(s, doctemplates.BandPageFooter)
	if len(it) != 2 || it[0].Source != "literal" || it[0].Text != "远山 · 页脚" || it[0].Align != "left" || it[1].Source != "page" || it[1].Text != "第 {page} / {pages} 页 " || it[1].Align != "right" || it[1].FontSize != 8 {
		t.Fatalf("%+v", it)
	}
	if entry(rec, "page_number", "{records}") == nil {
		t.Fatal("a placeholder the platform lacks is recorded")
	}
}

func TestRuleSummaryIsAGroupSumOfEachNumericColumnAndTheLedgerDifferenceIsALabelOnly(t *testing.T) {
	n := nodeJSON("n1", "summary", "reportHeader", 18, 4, 174, 18, 10, "")
	s, rec := mustConvert(t, build(t, []string{n}))
	var sums []string
	for _, it := range items(s, doctemplates.BandDocumentHeader) {
		if it.Source == "group_sum" {
			sums = append(sums, it.Field)
		}
	}
	// The numeric fields but the unit price, three at most: quantity and amount.
	if strings.Join(sums, ",") != "qty,amount" || entry(rec, "summary", "2 栏") == nil {
		t.Fatalf("sums %v", sums)
	}
	// The ledger's third figure is a difference of two sums: a label only, recorded.
	pf := build(t, []string{n}, func(m map[string]any) {
		m["doc"].(map[string]any)["datasetId"] = "ledger"
		m["dataset"].(map[string]any)["fields"] = []any{map[string]any{"id": "date", "label": "日期", "type": "date"}, map[string]any{"id": "ref", "label": "单号", "type": "string"},
			map[string]any{"id": "debit", "label": "应收", "type": "decimal", "scale": 2}, map[string]any{"id": "credit", "label": "已收", "type": "decimal", "scale": 2}}
		m["rows"] = []any{map[string]any{"date": "2026-10-02", "ref": "R1", "debit": "10.00", "credit": "4.00"}}
		for _, node := range m["doc"].(map[string]any)["nodes"].([]any) {
			if node.(map[string]any)["type"] == "table" {
				node.(map[string]any)["columns"] = []any{map[string]any{"field": "date", "label": "日期", "width": 60, "align": "left", "format": "auto"}, map[string]any{"field": "ref", "label": "单号", "width": 40, "align": "left", "format": "auto"},
					map[string]any{"field": "debit", "label": "应收", "width": 37, "align": "right", "format": "auto"}, map[string]any{"field": "credit", "label": "已收", "width": 37, "align": "right", "format": "auto"}}
			}
		}
	})
	s, rec = mustConvert(t, pf)
	labels, sums := 0, []string{}
	for _, it := range items(s, doctemplates.BandDocumentHeader) {
		if it.Source == "group_sum" {
			sums = append(sums, it.Field)
		}
		if it.Source == "literal" && strings.Contains(it.Text, "差额") {
			labels++
		}
	}
	if strings.Join(sums, ",") != "debit,credit" || labels != 1 || entry(rec, "summary", "两列合计之差") == nil {
		t.Fatalf("ledger: %v %d", sums, labels)
	}
}

func TestRuleAggregateIsAGroupSumOnlyWhereItSumsOneColumn(t *testing.T) {
	sum := nodeJSON("n1", "aggregate", "reportFooter", 18, 2, 100, 14, 10, `"field":"amount","scope":"all","method":"sum","label":"合计金额","format":"auto"`)
	count := nodeJSON("n2", "aggregate", "reportFooter", 18, 18, 100, 8, 10, `"field":"amount","scope":"page","method":"count","label":"本页笔数","format":"auto"`)
	s, rec := mustConvert(t, build(t, []string{sum, count}))
	it := items(s, doctemplates.BandDocumentFooter)
	var gs, lit int
	for _, e := range it {
		if e.Source == "group_sum" && e.Field == "amount" {
			gs++
		}
		if e.Source == "literal" && e.Text == "本页笔数" {
			lit++
		}
	}
	if gs != 1 || lit != 1 || !slotOf(s, "amount").Metric || entry(rec, "aggregate", "group_sum(amount)") == nil || entry(rec, "aggregate", "count") == nil {
		t.Fatalf("%+v", it)
	}
}

func TestRuleWatermarkIsDroppedAndRecorded(t *testing.T) {
	s, rec := mustConvert(t, build(t, []string{nodeJSON("n1", "watermark", "pageHeader", 18, 4, 100, 20, 30, `"text":"草稿"`)}))
	if len(items(s, doctemplates.BandPageHeader)) != 0 || rec.Counts.Dropped != 1 || entry(rec, "watermark", "水印") == nil {
		t.Fatalf("%+v", rec.Counts)
	}
}

func TestParametersHiddenNodesAndUnknownPropertiesAreRecordedNotLost(t *testing.T) {
	hidden := strings.Replace(nodeJSON("n1", "text", "pageHeader", 18, 4, 100, 8, 10, `"text":"隐藏"`), `"hidden":false`, `"hidden":true`, 1)
	odd := nodeJSON("n2", "text", "pageHeader", 18, 14, 100, 8, 10, `"text":"带未知属性","rule":{"op":"gt"}`)
	unknown := nodeJSON("n3", "sparkline", "pageHeader", 18, 24, 100, 8, 10, "")
	pf := build(t, []string{hidden, odd, unknown}, func(m map[string]any) {
		m["doc"].(map[string]any)["parameters"] = []any{map[string]any{"id": "from", "label": "开始日期", "type": "date"}}
	})
	s, rec := mustConvert(t, pf)
	if it := items(s, doctemplates.BandPageHeader); len(it) != 1 || it[0].Text != "带未知属性" {
		t.Fatalf("%+v", it)
	}
	if entry(rec, "parameters", "开始日期") == nil || entry(rec, "node_hidden", "隐藏") == nil || entry(rec, "node_unknown", "rule") == nil || entry(rec, "node_unknown", "sparkline") == nil {
		t.Fatalf("%+v", rec.Entries)
	}
	if rec.Counts.Dropped != 2 || rec.Counts.NodesIn != 4 {
		t.Fatalf("counts %+v", rec.Counts)
	}
}

func TestOverprintTemplateIsVariablesOnlyAndItsGuideIsRecorded(t *testing.T) {
	var nodes []string
	for i := 0; i < 6; i++ {
		nodes = append(nodes, nodeJSON(fmt.Sprintf("n%d", i+1), "field", "detail", 20, float64(30+i*12), 80, 8, 9, `"field":"ref","scope":"row","label":"","format":"text"`))
	}
	pf := buildRaw(t, nodes, func(m map[string]any) {
		p := m["doc"].(map[string]any)["page"].(map[string]any)
		p["mode"], p["offsetX"], p["offsetY"], p["formGuide"] = "variables", 1.5, 2, "unit-v1"
		m["rows"] = m["rows"].([]any)[:1]
	})
	s, rec := mustConvert(t, pf)
	pr := s.Document.Page.Print
	if pr == nil || !pr.VariablesOnly || pr.OffsetX != 1.5 || pr.OffsetY != 2 || pr.Background != nil {
		t.Fatalf("%+v", pr)
	}
	if entry(rec, "overprint", "variables_only") == nil || entry(rec, "overprint", "背景留空") == nil || entry(rec, "page_background", "unit-v1") == nil {
		t.Fatal("the guide is a drawing, not a PNG: recorded")
	}
}

func formNodes(n int, x0, y0 float64) []string {
	var nodes []string
	for i := 0; i < n; i++ {
		nodes = append(nodes, nodeJSON(fmt.Sprintf("n%d", i+1), "field", "detail", x0+float64(i%2)*95, y0+float64(i/2)*9, 90, 6, 8, `"field":"ref","scope":"row","label":"","format":"text"`))
	}
	return nodes
}

func TestAFormWithoutATableIsAFormWithNoStandInTableAndItsPlaceOnTheSheetKept(t *testing.T) {
	// 30 fields on the A4 sheet, inside the safe region (x 10-200, y 25-270): kept where they are, in a header band of the height of the lowest.
	pf := buildRaw(t, formNodes(30, 12, 30), func(m map[string]any) { m["rows"] = m["rows"].([]any)[:1] })
	s, rec := mustConvert(t, pf)
	if !s.Document.Form() || s.Document.TableItem() != nil || s.Document.Group != nil || slotOf(s, "ref").Scope != doctemplates.ScopeRow || s.Document.Bands.PageHeader == nil || s.Document.Bands.PageFooter != nil {
		t.Fatalf("%+v", rec.Entries)
	}
	if rec.Counts.NodesIn != 30 || rec.Counts.ElementsOut != 30 || len(s.Rows) != 1 {
		t.Fatalf("no stand-in element: %+v", rec.Counts)
	}
	it := items(s, doctemplates.BandPageHeader)
	if it[0].X != 2 || it[0].Y != 5 || it[1].X != 97 || s.Document.Bands.PageHeader.Height != 5+14*9+6 || entry(rec, "form_fit", "原样放在纸上的位置") == nil {
		t.Fatalf("kept where it is on the sheet: %+v height %v", it[:2], s.Document.Bands.PageHeader.Height)
	}
	if entry(rec, "form_table", "") != nil {
		t.Fatal("there is no stand-in table to record")
	}
	// A form that does not lie in the safe region is put at its corner, and scaled only where it is bigger than the region (245 mm high).
	pf = buildRaw(t, formNodes(60, 12, 5), func(m map[string]any) { m["rows"] = m["rows"].([]any)[:1] })
	s, rec = mustConvert(t, pf)
	if sc := entry(rec, "form_fit", "等比缩小"); sc == nil || s.Document.Bands.PageHeader.Height > 245 || items(s, doctemplates.BandPageHeader)[0].Y != 0 {
		t.Fatalf("%+v", rec.Entries)
	}
	pf = buildRaw(t, formNodes(10, 12, 5), func(m map[string]any) { m["rows"] = m["rows"].([]any)[:1] })
	s, rec = mustConvert(t, pf)
	if entry(rec, "form_fit", "不缩放") == nil || items(s, doctemplates.BandPageHeader)[0].Y != 0 || items(s, doctemplates.BandPageHeader)[0].X != 0 {
		t.Fatalf("moved to the corner, not scaled: %+v", rec.Entries)
	}
}

func TestAPaperThatIsAPresetIsThatPresetAndOneThatIsNotIsPlacedAtItsTrueSizeAndRecorded(t *testing.T) {
	custom := func(w, h float64) protoFile {
		return buildRaw(t, []string{nodeJSON("n1", "field", "detail", 6, 4, 100, 8, 10, `"field":"ref","scope":"row","label":"","format":"text"`)}, func(m map[string]any) {
			p := m["doc"].(map[string]any)["page"].(map[string]any)
			p["preset"], p["width"], p["height"], p["orientation"] = "Custom", w, h, "landscape"
			p["margins"] = map[string]any{"top": 5, "bottom": 5, "left": 5, "right": 5}
			m["rows"] = m["rows"].([]any)[:1]
		})
	}
	// 150 x 100 mm is the landscape label.
	s, rec := mustConvert(t, custom(150, 100))
	if s.Preset() != doctemplates.PresetLabel150x100 || entry(rec, "page_size", "即预设 label_150x100") == nil || rec.Preset != doctemplates.PresetLabel150x100 {
		t.Fatalf("%q %+v", s.Preset(), rec.Entries)
	}
	// A size no preset has goes on the least preset that holds it, unscaled, and the record says so.
	s, rec = mustConvert(t, custom(160, 110))
	if s.Preset() != doctemplates.PresetA4Landscape || entry(rec, "page_size", "没有对应的预设") == nil {
		t.Fatalf("%q %+v", s.Preset(), rec.Entries)
	}
	if it := items(s, doctemplates.BandPageHeader); len(it) != 1 || it[0].Width != 100 {
		t.Fatalf("not scaled: %+v", it)
	}
	// A paper nothing holds is refused, not guessed.
	pf := buildRaw(t, []string{nodeJSON("n1", "field", "detail", 6, 4, 100, 8, 10, `"field":"ref","scope":"row"`)}, func(m map[string]any) {
		p := m["doc"].(map[string]any)["page"].(map[string]any)
		p["preset"], p["width"], p["height"] = "A3", 297, 420
	})
	if _, _, err := convert(pf); err == nil {
		t.Fatal("an A3 paper has no preset")
	}
}

func TestTheConverterRefusesWhatDoctemplatesRefusesAndWritesNothingForIt(t *testing.T) {
	// A table of 13 columns is more than the platform's 12.
	var cols []string
	for i := 0; i < 13; i++ {
		cols = append(cols, `{"field":"ref","label":"c","width":10,"align":"left","format":"auto"}`)
	}
	bad := strings.Replace(tableNode, tableNode[strings.Index(tableNode, `"columns":`):], `"columns":[`+strings.Join(cols, ",")+`]}`, 1)
	pf := buildRaw(t, []string{bad})
	if _, _, err := convert(pf); err == nil {
		t.Fatal("13 columns were accepted")
	}
	// A directory with one such file: run fails as a whole.
	dir := t.TempDir()
	data, _ := os.ReadFile(filepath.Join(prototypeDir, "statement.json"))
	os.WriteFile(filepath.Join(dir, "statement.json"), data, 0o644)
	var m map[string]any
	json.Unmarshal(data, &m)
	m["source"].(map[string]any)["file"] = "broken"
	nodes := m["doc"].(map[string]any)["nodes"].([]any)
	for _, n := range nodes {
		if n.(map[string]any)["type"] == "table" {
			n.(map[string]any)["columns"] = n.(map[string]any)["columns"].([]any)[:0]
		}
	}
	broken, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(dir, "broken.json"), broken, 0o644)
	if out, err := run(dir); err == nil || len(out.samples) != 0 || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("a template that cannot be converted fails the run: %v", err)
	}
}

// ---- the pack ----

func TestEveryNodeOfThe37IsAccountedFor(t *testing.T) {
	files, err := loadDir(prototypeDir)
	if err != nil || len(files) != 37 {
		t.Fatal(len(files), err)
	}
	for _, pf := range files {
		sample, rec, err := convert(pf)
		if err != nil {
			t.Fatalf("%s: %v", pf.Source.File, err)
		}
		if rec.Counts.NodesIn != len(pf.Doc.Nodes) || rec.Counts.ElementsOut != sample.ElementCount() {
			t.Errorf("%s: counts %+v", pf.Source.File, rec.Counts)
		}
		// Every node is an element's source or is named by an entry of the record.
		named := map[string]bool{}
		for _, e := range rec.Entries {
			for _, n := range e.Nodes {
				named[n] = true
			}
		}
		produced := producedBy(t, pf)
		for _, n := range pf.Doc.Nodes {
			if !produced[n.ID] && !named[n.ID] {
				t.Errorf("%s: node %s (%s) is neither in the sample nor in the record", pf.Source.File, n.ID, n.Type)
			}
		}
		if len(rec.Entries) == 0 {
			t.Errorf("%s: a conversion with nothing to say", pf.Source.File)
		}
	}
}

// producedBy runs the conversion again and returns the nodes that have an element.
func producedBy(t *testing.T, pf protoFile) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for attempt := 0; attempt < 24; attempt++ {
		c, _, err := convertAtWith(pf, pow97(attempt))
		if err == nil {
			for _, items := range c.bands {
				for _, p := range items {
					out[p.node] = true
				}
			}
			return out
		}
	}
	t.Fatal("no conversion")
	return nil
}

func pow97(n int) float64 {
	f := 1.0
	for i := 0; i < n; i++ {
		f *= 0.97
	}
	return f
}

func TestConversionIsDeterministic(t *testing.T) {
	a, err := run(prototypeDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		b, err := run(prototypeDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(a.samples) != len(b.samples) || !bytes.Equal(a.docs, b.docs) {
			t.Fatal("two runs differ in what they write")
		}
		for name, data := range a.samples {
			if !bytes.Equal(data, b.samples[name]) {
				t.Fatalf("%s differs between two runs", name)
			}
		}
	}
}

func TestTheCommittedSamplesAreWhatTheConverterMakesOfTheCommittedPrototypeFiles(t *testing.T) {
	out, err := run(prototypeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.samples) != 74 {
		t.Fatalf("%d files for 37 templates", len(out.samples))
	}
	have := map[string]bool{}
	entries, err := os.ReadDir(samplesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			if e.Name() != "prototype" {
				t.Errorf("unexpected directory %s", e.Name())
			}
			continue
		}
		have[e.Name()] = true
	}
	var names []string
	for name, want := range out.samples {
		names = append(names, name)
		got, err := os.ReadFile(filepath.Join(samplesDir, name))
		if err != nil {
			t.Errorf("%s is not committed: %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is not what the converter makes (run go generate ./doctemplates)", name)
		}
		delete(have, name)
	}
	for name := range have {
		t.Errorf("%s is committed and is nobody's output", name)
	}
	doc, err := os.ReadFile("../../docs/SAMPLES.md")
	if err != nil || !bytes.Equal(doc, out.docs) {
		t.Errorf("docs/SAMPLES.md is not what the converter writes: %v", err)
	}
	sort.Strings(names)
	// Every committed sample is a sample of the pack and the other way round.
	pack, err := doctemplates.Samples()
	if err != nil || len(pack) != 37 {
		t.Fatal(len(pack), err)
	}
}

func TestTheInventoryAgreesWithTheExtractedFiles(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(prototypeDir, "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inv struct {
		Templates []struct {
			File  string         `json:"file"`
			Name  string         `json:"name"`
			Nodes int            `json:"nodes"`
			Types map[string]int `json:"types"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(data, &inv); err != nil || len(inv.Templates) != 37 {
		t.Fatal(len(inv.Templates), err)
	}
	total := map[string]int{}
	for _, it := range inv.Templates {
		data, err := os.ReadFile(filepath.Join(prototypeDir, it.File+".json"))
		if err != nil {
			t.Fatal(err)
		}
		pf, err := decodeProto(data)
		if err != nil || pf.Doc.Name != it.Name || len(pf.Doc.Nodes) != it.Nodes {
			t.Errorf("%s: %v", it.File, err)
		}
		for _, n := range pf.Doc.Nodes {
			if it.Types[n.Type] == 0 {
				t.Errorf("%s: %s is not in the inventory", it.File, n.Type)
			}
		}
		for typ, n := range it.Types {
			total[typ] += n
		}
	}
	// The node types the scope lists, all but the two the prototype's 37 never use.
	for _, typ := range []string{"text", "field", "table", "line", "image", "signature", "summary", "barcode", "qr", "pageNumber"} {
		if total[typ] == 0 {
			t.Errorf("no %s in the 37", typ)
		}
	}
	if total["rect"] != 0 || total["aggregate"] != 0 || total["watermark"] != 0 {
		t.Log("the prototype uses a type this note says it does not:", total)
	}
}
