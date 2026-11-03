package main

import (
	"fmt"
	"sort"
	"strings"
)

// samplesDoc is docs/SAMPLES.md: the rules of the conversion, the legend of the record's codes, a table of the 37 and the record of each. It is
// generated from the records, so that it cannot say anything the converter did not.
func samplesDoc(records []*Record, files []protoFile) string {
	var b strings.Builder
	b.WriteString(`# 样例模板（S06-B 第二部分）

> 本文件由 ` + "`go generate ./doctemplates`" + `（` + "`cmd/reporting-samples`" + `）生成，不要手改。每个样例旁边还有一份机器可读的
> ` + "`doctemplates/samples/<kind>.record.json`" + `。

重新生成：先取得原型的内容（原型文件不入库；Chrome 用自己的端口，不用共享的 9336）：

` + "```" + `
chrome --headless=new --remote-debugging-port=<端口> --user-data-dir=<新目录> --no-proxy-server --no-first-run about:blank &
node scripts/prototype_templates.mjs <IDP_Reporting_Designer_UIUX_V3.0.html> doctemplates/samples/prototype <端口>
go generate ./doctemplates
` + "```" + `

原型 3.0 的 37 个模板（` + "`IDP_Reporting_Designer_UIUX_V3.0.html`" + `，不入库）由 ` + "`scripts/prototype_templates.mjs`" + ` 原样读出，存为
` + "`doctemplates/samples/prototype/<kind>.json`" + `（最后一版的文档、它绑定的数据集、原型自带的样例行、单据级字段的取值）；
` + "`cmd/reporting-samples`" + ` 把它们逐个转成 2.1 版样例。转换是确定的：同样的输入得到同样的输出，测试比较提交的样例与转换结果，
转换结果还必须通过 ` + "`doctemplates`" + ` 的校验才会写出。

## 转换规则

| 原型 | 样例 |
|---|---|
| 区段与高度 | 页眉 / 页脚的高度保留；报表头 → 文档头，报表尾 → 文档尾；明细区段 → 明细表 |
| text | 文本（字号取整磅、粗体、对齐；自动换行按框高与取值判断）；颜色、斜体、字距、行距不转换 |
| field（scope meta） | 单据级字段槽（scope document）；字段的标签另出一个文本，在取值上方，框太矮则在左侧，再小则舍去 |
| field（scope row，表格外） | 明细字段槽，取第一行的值（固定版式里一张表单只有一行，槽为单据级） |
| table | 明细表：列的字段、标题、宽度、对齐保留；carry / subtotal → 平台表格的承前页、转下页和合计行（数值列标为度量列，单价除外）；groupField → 分组字段；striped、sortField、negativeRed、headerColor、rowsPerPage、行高、内边距不转换 |
| line / rect | line / rect（线宽取 1/4 磅的倍数；填充色换成最浅的灰） |
| image | 方框加名称（原型的图片为空，只显示内置标识图形） |
| barcode / qr | barcode（Code 128，showText → 显示文字）/ qrcode（纠错级别 M），取值来自字段槽；PDF417 换成 QR 码 |
| pageNumber | 页脚文字（原文字）加页码文字（{page} / {pages}） |
| signature | 方框加原文字（签章与签名属于后续阶段） |
| summary / aggregate | 各数值列的合计（group_sum）加标签；不是单列求和的计算转成带标签的文字 |
| watermark | 不转换 |
| parameters | 不转换（平台的查询面板就是参数） |
| mode: variables（套打） | page.print.variables_only；背景留空（原型的预印版面是自绘图形） |
| 页面 | A4 → a4，4×6 → label_4x6in，150×100 毫米的 WMS 标签 → label_150x100（与原纸张等大） |

没有明细表的固定版式（运单、提单、快递面单、WMS 标签）转成**表单**：模板没有明细带，也没有任何占位表。表单只画页眉、单据头、单据尾与页脚四个带，
一页，没有明细行、承前与合计；整个版式放在页眉区段，区段高度取最低元素的底边。外框在页面安全区内就原样放在纸上的位置；放不进（例如页面顶部
留给横幅与印章的条带占去了原版式的位置）就把外框左上角对齐安全区左上角，只在外框比安全区大时才等比缩小（字号随之取整，不低于 8 磅），不再在
空隙处切开。表单没有分组字段：一行一份文档，按行出具（` + "`POST /issues`" + ` 只给 ` + "`row_index`" + `，记录带行号、不带分组，与 1.x 相同），与原型的一条记录一张表单相同，字段槽都是明细字段（scope row），样例只留第一行。

**变更记录。** 本文件的上一版把这些固定版式转成「页眉 + 单列占位表（单据主键）+ 页脚」，占位表会在运单、标签中间印出一行主键和「合计」，
不是原型的文档；平台因此改为 2.x 模板的明细带可以没有（表单），并新增预设 ` + "`label_150x100`" + `（150 × 100 毫米横向标签，安全区左右 4 毫米、
y 12–94，横幅条 4, 3, 142 × 6，印章 2 行 6 磅），四张 WMS 标签按原纸张尺寸转到这一预设上，不再放在 ` + "`a4_landscape`" + ` 上。

两个 ` + "`mawb`" + ` 模板（「MAWB 航空主运单 · 套打」与「航空运单 · Air Waybill (MAWB)」）内容相同，第二个存为 ` + "`mawb_air_waybill`" + `。
样例取值不含控制字符，原型取值里的换行换成「 / 」。

## 范围没有规定、由转换自行决定的事

- 样例的 ` + "`kind`" + ` 是原型模板种类的小写下划线形式（` + "`exportPacking`" + ` → ` + "`export_packing`" + `）；原型里有两个种类同为 ` + "`mawb`" + ` 的模板，第二个是 ` + "`mawb_air_waybill`" + `。
- 业务域与流程取原型模板中心的默认分类（` + "`catalogDefault`" + `），描述取原型数据集的说明。
- 样例的字段槽只列文档用到的字段，类型取数据集字段的类型；取值不全是精确数字的数值字段（空值）按文本处理；样例取值不含控制字符。
- 表格版面保留原型的全部样例行（至多 50 行）；表单一张表单一条记录，只留第一行（WMS 标签原有 3 行、各一张标签）。
- 平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列放不下就渲染失败，所以首列加宽到放得下为止，宽度从其余各列按比例让出。
- 平台的条码条高不小于符号宽度的 15 %，二维码模块不小于 0.4 毫米：框对高度太宽的条码居中收窄，二维码取够大的边长。
- 文本框最小 10 × 5 毫米且不小于一行字所需；放大后与先出现的文本、码、图框相叠时，沿纵向移到最近的空处，移不开的再收窄，最后才舍去并记录。
- 页脚的页码文字与原文字各占一个文本（原文字占左边 55 %）。

## 记录里的代码

| 代码 | 含义 |
|---|---|
`)
	used := map[string]bool{}
	for _, r := range records {
		for _, e := range r.Entries {
			used[e.Code] = true
		}
	}
	var codes []string
	for c := range used {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		fmt.Fprintf(&b, "| `%s` | %s |\n", c, legendOf(c))
	}
	b.WriteString("\n## 总表\n\n| 样例 | 名称 | 业务域 / 流程 | 纸张 | 节点 → 元素 | 舍去 | 改写 |\n|---|---|---|---|---|---|---|\n")
	meta := map[string]protoFile{}
	for _, pf := range files {
		meta[snake(pf.Source.File)] = pf
	}
	order := []string{"仓储 WMS", "运输 / 货代", "国际贸易", "通用业务"}
	sorted := append([]*Record(nil), records...)
	rank := func(r *Record) int {
		for i, d := range order {
			if meta[r.Kind].Catalog.DomainLabel == d {
				return i
			}
		}
		return len(order)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if rank(sorted[i]) != rank(sorted[j]) {
			return rank(sorted[i]) < rank(sorted[j])
		}
		return sorted[i].Kind < sorted[j].Kind
	})
	for _, r := range sorted {
		pf := meta[r.Kind]
		fmt.Fprintf(&b, "| `%s` | %s | %s / %s | %s | %d → %d | %d | %d |\n", r.Kind, r.Name, pf.Catalog.DomainLabel, pf.Catalog.FlowLabel, r.Preset, r.Counts.NodesIn, r.Counts.ElementsOut, r.Counts.Dropped, r.Counts.Substituted)
	}
	b.WriteString("\n## 每个模板转了什么、舍了什么\n\n")
	for _, r := range sorted {
		b.WriteString(r.markdown())
	}
	return b.String()
}
