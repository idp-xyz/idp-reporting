# 样例模板（S06-B 第二部分）

> 本文件由 `go generate ./doctemplates`（`cmd/reporting-samples`）生成，不要手改。每个样例旁边还有一份机器可读的
> `doctemplates/samples/<kind>.record.json`。

重新生成：先取得原型的内容（原型文件不入库；Chrome 用自己的端口，不用共享的 9336）：

```
chrome --headless=new --remote-debugging-port=<端口> --user-data-dir=<新目录> --no-proxy-server --no-first-run about:blank &
node scripts/prototype_templates.mjs <IDP_Reporting_Designer_UIUX_V3.0.html> doctemplates/samples/prototype <端口>
go generate ./doctemplates
```

原型 3.0 的 37 个模板（`IDP_Reporting_Designer_UIUX_V3.0.html`，不入库）由 `scripts/prototype_templates.mjs` 原样读出，存为
`doctemplates/samples/prototype/<kind>.json`（最后一版的文档、它绑定的数据集、原型自带的样例行、单据级字段的取值）；
`cmd/reporting-samples` 把它们逐个转成 2.1 版样例。转换是确定的：同样的输入得到同样的输出，测试比较提交的样例与转换结果，
转换结果还必须通过 `doctemplates` 的校验才会写出。

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
空隙处切开。表单没有分组字段：一行一份文档，按行出具（`POST /issues` 只给 `row_index`，记录带行号、不带分组，与 1.x 相同），与原型的一条记录一张表单相同，字段槽都是明细字段（scope row），样例只留第一行。

**变更记录。** 本文件的上一版把这些固定版式转成「页眉 + 单列占位表（单据主键）+ 页脚」，占位表会在运单、标签中间印出一行主键和「合计」，
不是原型的文档；平台因此改为 2.x 模板的明细带可以没有（表单），并新增预设 `label_150x100`（150 × 100 毫米横向标签，安全区左右 4 毫米、
y 12–94，横幅条 4, 3, 142 × 6，印章 2 行 6 磅），四张 WMS 标签按原纸张尺寸转到这一预设上，不再放在 `a4_landscape` 上。

两个 `mawb` 模板（「MAWB 航空主运单 · 套打」与「航空运单 · Air Waybill (MAWB)」）内容相同，第二个存为 `mawb_air_waybill`。
样例取值不含控制字符，原型取值里的换行换成「 / 」。

## 范围没有规定、由转换自行决定的事

- 样例的 `kind` 是原型模板种类的小写下划线形式（`exportPacking` → `export_packing`）；原型里有两个种类同为 `mawb` 的模板，第二个是 `mawb_air_waybill`。
- 业务域与流程取原型模板中心的默认分类（`catalogDefault`），描述取原型数据集的说明。
- 样例的字段槽只列文档用到的字段，类型取数据集字段的类型；取值不全是精确数字的数值字段（空值）按文本处理；样例取值不含控制字符。
- 表格版面保留原型的全部样例行（至多 50 行）；表单一张表单一条记录，只留第一行（WMS 标签原有 3 行、各一张标签）。
- 平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列放不下就渲染失败，所以首列加宽到放得下为止，宽度从其余各列按比例让出。
- 平台的条码条高不小于符号宽度的 15 %，二维码模块不小于 0.4 毫米：框对高度太宽的条码居中收窄，二维码取够大的边长。
- 文本框最小 10 × 5 毫米且不小于一行字所需；放大后与先出现的文本、码、图框相叠时，沿纵向移到最近的空处，移不开的再收窄，最后才舍去并记录。
- 页脚的页码文字与原文字各占一个文本（原文字占左边 55 %）。

## 记录里的代码

| 代码 | 含义 |
|---|---|
| `band_height` | 区段高度低于下限 5 毫米而提高，或为让开互相重叠的文本而略加高（至多 12 毫米） |
| `barcode_symbology` | 条码类型换成 Code 128 / 二维码类型换成 QR |
| `box_grown` | 文本框小于平台下限（宽 10、高 5 毫米，高度不小于字号所需），已放大 |
| `element_overlap` | 与先出现的文本 / 码 / 图框重叠，已调整位置或舍去 |
| `field_label` | 字段节点的标签，另出一个文本（上下或左右）或舍去 |
| `font_raised` | 字号低于 8 磅，提高到 8 磅 |
| `font_rounded` | 字号取整磅 |
| `font_scaled` | 整版缩放后字号随之缩小 |
| `form_fit` | 固定版式转成表单（没有明细带）：整版放在页眉区段，放不进安全区时才等比缩小 |
| `image` | 图片转成方框加名称 |
| `line_break` | 样例取值里的换行换成「 / 」（样例取值不含控制字符） |
| `not_numeric` | 字段取值不是精确数字，样例里按文本处理 |
| `overprint` | 套打：只印变量，背景留空 |
| `page_background` | 原型的底图 / 透明度 / 版式参考图 |
| `page_number` | 页码节点转成页脚文字和页码文字 |
| `page_size` | 纸张尺寸与平台预设的对应 |
| `parameters` | 原型的查询参数（平台的查询面板就是参数） |
| `rows_kept` | 样例保留的行 |
| `signature` | 签章与签名属于后续阶段，转成方框加文字 |
| `slot_collision` | 单据级字段与明细字段同名，单据级字段改名 |
| `style_dropped` | 样式里平台没有的属性（颜色、斜体、字距、行距） |
| `summary` | 汇总卡片转成各数值列的合计 |
| `table_option` | 明细表的选项：平台版式已固定，或平台没有对应 |

## 总表

| 样例 | 名称 | 业务域 / 流程 | 纸张 | 节点 → 元素 | 舍去 | 改写 |
|---|---|---|---|---|---|---|
| `packing` | 出库装箱单 | 仓储 WMS / 出库 | a4 | 14 → 25 | 0 | 5 |
| `wms_aging` | WMS · 库龄与呆滞库存表 | 仓储 WMS / 库存 | a4 | 17 → 29 | 0 | 1 |
| `wms_asn` | WMS · 到货通知单 ASN | 仓储 WMS / 入库 | a4 | 17 → 29 | 0 | 1 |
| `wms_backlog` | WMS · 作业进度与积压表 | 仓储 WMS / 运营监控 | a4 | 17 → 29 | 0 | 1 |
| `wms_balance` | WMS · 库存余额表 | 仓储 WMS / 库存 | a4 | 17 → 29 | 0 | 1 |
| `wms_carton_label` | WMS · 箱唛标签 | 仓储 WMS / 仓储标签 | label_150x100 | 11 → 17 | 0 | 0 |
| `wms_count` | WMS · 盲盘任务单 | 仓储 WMS / 盘点 | a4 | 17 → 29 | 0 | 1 |
| `wms_damage` | WMS · 破损异常处理表 | 仓储 WMS / 异常处理 | a4 | 17 → 29 | 0 | 1 |
| `wms_dispatch` | WMS · 发货交接单 | 仓储 WMS / 出库 | a4 | 17 → 29 | 0 | 1 |
| `wms_ledger` | WMS · 库存流水表 | 仓储 WMS / 库存 | a4 | 17 → 30 | 0 | 1 |
| `wms_location_label` | WMS · 库位标签 | 仓储 WMS / 仓储标签 | label_150x100 | 11 → 17 | 0 | 0 |
| `wms_pack` | WMS · 复核装箱单 | 仓储 WMS / 出库 | a4 | 17 → 29 | 0 | 1 |
| `wms_pallet_label` | WMS · 托盘标签 | 仓储 WMS / 仓储标签 | label_150x100 | 11 → 17 | 0 | 0 |
| `wms_pick` | WMS · 波次拣货单 | 仓储 WMS / 出库 | a4 | 17 → 29 | 0 | 1 |
| `wms_picking_exception` | WMS · 出库异常跟踪表 | 仓储 WMS / 异常处理 | a4 | 17 → 29 | 0 | 1 |
| `wms_putaway` | WMS · 上架任务单 | 仓储 WMS / 入库 | a4 | 17 → 29 | 0 | 1 |
| `wms_receive` | WMS · 收货验收单 | 仓储 WMS / 入库 | a4 | 17 → 29 | 0 | 1 |
| `wms_receiving_exception` | WMS · 收货差异跟踪表 | 仓储 WMS / 异常处理 | a4 | 17 → 29 | 0 | 1 |
| `wms_return_disposition` | WMS · 退货处理明细 | 仓储 WMS / 退货 | a4 | 17 → 29 | 0 | 1 |
| `wms_return_receipt` | WMS · 退货收货单 | 仓储 WMS / 退货 | a4 | 17 → 29 | 0 | 1 |
| `wms_sku_label` | WMS · 商品标签 | 仓储 WMS / 仓储标签 | label_150x100 | 11 → 17 | 0 | 0 |
| `wms_status` | WMS · 库存状态表 | 仓储 WMS / 库存 | a4 | 17 → 29 | 0 | 1 |
| `wms_transfer` | WMS · 移库补货任务单 | 仓储 WMS / 移库补货 | a4 | 17 → 29 | 0 | 1 |
| `wms_variance` | WMS · 盘点差异表 | 仓储 WMS / 盘点 | a4 | 17 → 29 | 0 | 1 |
| `bol` | 海运提单 B/L · 集装箱 | 运输 / 货代 / 海运 | a4 | 52 → 52 | 0 | 0 |
| `booking` | 海运订舱委托 · Ocean Booking | 运输 / 货代 / 海运 | a4 | 22 → 37 | 0 | 1 |
| `fedex` | FedEx 快递面单 · 4×6 | 运输 / 货代 / 快递面单 | label_4x6in | 20 → 20 | 0 | 1 |
| `manifest` | 货物舱单 · Cargo Manifest | 运输 / 货代 / 海运 | a4 | 22 → 37 | 0 | 1 |
| `mawb` | MAWB 航空主运单 · 套打 | 运输 / 货代 / 航空运输 | a4 | 72 → 72 | 0 | 0 |
| `mawb_air_waybill` | 航空运单 · Air Waybill (MAWB) | 运输 / 货代 / 航空运输 | a4 | 72 → 72 | 0 | 0 |
| `usps` | USPS 快递面单 · 4×6 | 运输 / 货代 / 快递面单 | label_4x6in | 18 → 18 | 0 | 0 |
| `commercial` | 商业发票 · Commercial Invoice | 国际贸易 / 贸易单证 | a4 | 41 → 57 | 0 | 2 |
| `export_packing` | 出口装箱单 · Packing List | 国际贸易 / 贸易单证 | a4 | 26 → 44 | 0 | 2 |
| `origin` | 原产地证书 · Certificate of Origin | 国际贸易 / 贸易单证 | a4 | 21 → 35 | 0 | 1 |
| `proforma` | 形式发票 · Pro Forma Invoice | 国际贸易 / 贸易单证 | a4 | 41 → 57 | 0 | 2 |
| `order` | 销售订单 | 通用业务 / 销售 | a4 | 14 → 25 | 0 | 5 |
| `statement` | 客户往来对账单 | 通用业务 / 财务 | a4 | 14 → 26 | 0 | 5 |

## 每个模板转了什么、舍了什么

### `packing` · 出库装箱单

原型数据集 `warehouse`，纸张 A4 210×297 → `a4`；节点 14 → 元素 25（舍去 0 个节点，改写 5 个）。

- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n14）
- `font_rounded`：字号 7.8 磅取整为 8 磅（n3）
- `font_rounded`：字号 11.5 磅取整为 12 磅（n5）
- `font_rounded`：字号 9.5 磅取整为 10 磅（n7、n8）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n10）
- `image`：图片节点转成方框加名称：原型的图片为空，只显示内置的标识图形，平台的样例不带私有图片（n1）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n14）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `parameters`：4 个查询参数（开始日期、结束日期、搜索摘要、业务类别）不转换：平台的查询面板就是参数
- `rows_kept`：保留全部 28 行样例数据
- `signature`：签章与签名属于后续阶段：转成方框加原文字（n12、n13）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n1、n2、n4、n5、n6、n7 等 13 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色、字距（n3）
- `summary`：汇总卡片转成 2 栏：标签文字加合计（group_sum）（n9）
- `table_option`：首列 23 → 36 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n10）
- `table_option`：承前页 / 转下页累计（carry=false）和本页加和（subtotal=true）对应平台表格的承前、转下页和合计行；数值列（单价除外）标为度量列（n10）
- `table_option`：不转换：striped（隔行底色）、negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n10）

### `wms_aging` · WMS · 库龄与呆滞库存表

原型数据集 `wmsAging`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 237 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 19.6 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_asn` · WMS · 到货通知单 ASN

原型数据集 `wmsAsn`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：首列 25 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_backlog` · WMS · 作业进度与积压表

原型数据集 `wmsBacklog`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 225 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 23.1 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_balance` · WMS · 库存余额表

原型数据集 `wmsBalance`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：首列 25 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_carton_label` · WMS · 箱唛标签

原型数据集 `wmsCartonLabel`，纸张 Custom 150×100 → `label_150x100`；节点 11 → 元素 17（舍去 0 个节点，改写 0 个）。

- `box_grown`：条码框按平台的条高下限调整：宽 115.7 → 113 毫米（居中），高 19 → 19 毫米（n10）
- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n11）
- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3、n4、n5、n8、n9）
- `field_label`：字段的标签舍去（框太小，放不下标签和取值）（n6、n7）
- `font_scaled`：整版按 0.864 缩放：字号 13 磅 → 11 磅（取整，范围 8～28 磅）（n1、n2）
- `font_scaled`：整版按 0.864 缩放：字号 9 磅 → 8 磅（取整，范围 8～28 磅）（n5、n6、n8、n9）
- `font_scaled`：整版按 0.864 缩放：字号 8.8 磅 → 8 磅（取整，范围 8～28 磅）（n10）
- `font_scaled`：整版按 0.864 缩放：字号 6.5 磅 → 8 磅（取整，范围 8～28 磅）（n11）
- `form_fit`：固定版式转成表单（没有明细带、没有明细表）：整版放在页眉区段，高度取最低元素的底边；外框 138×92 毫米；外框左上角对齐安全区左上角，整体等比缩小到 0.864（字号随之取整，不低于 8 磅）
- `page_size`：纸张 Custom（150×100 毫米）即预设 label_150x100
- `rows_kept`：原型有 3 行样例数据，每行一张表单；样例只留第一行，其余 2 行舍去
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n6 等 11 个）

### `wms_count` · WMS · 盲盘任务单

原型数据集 `wmsCount`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `not_numeric`：字段 countedQty（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 196 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 25.6 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_damage` · WMS · 破损异常处理表

原型数据集 `wmsDamage`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `slot_collision`：单据级字段与明细字段同名 sourceRef：明细字段改名为 row_sourceRef
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 228 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 21.2 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_dispatch` · WMS · 发货交接单

原型数据集 `wmsDispatch`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 195 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 30.5 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_ledger` · WMS · 库存流水表

原型数据集 `wmsLedger`，纸张 A4 210×297 → `a4`；节点 17 → 元素 30（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n10 等 10 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `slot_collision`：单据级字段与明细字段同名 sourceRef：明细字段改名为 row_sourceRef
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 219 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 21.2 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_location_label` · WMS · 库位标签

原型数据集 `wmsLocationLabel`，纸张 Custom 150×100 → `label_150x100`；节点 11 → 元素 17（舍去 0 个节点，改写 0 个）。

- `box_grown`：条码框按平台的条高下限调整：宽 115.7 → 113 毫米（居中），高 19 → 19 毫米（n10）
- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n11）
- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3、n4、n5、n8、n9）
- `field_label`：字段的标签舍去（框太小，放不下标签和取值）（n6、n7）
- `font_scaled`：整版按 0.864 缩放：字号 13 磅 → 11 磅（取整，范围 8～28 磅）（n1、n2）
- `font_scaled`：整版按 0.864 缩放：字号 9 磅 → 8 磅（取整，范围 8～28 磅）（n5、n6、n8、n9）
- `font_scaled`：整版按 0.864 缩放：字号 8.8 磅 → 8 磅（取整，范围 8～28 磅）（n10）
- `font_scaled`：整版按 0.864 缩放：字号 6.5 磅 → 8 磅（取整，范围 8～28 磅）（n11）
- `form_fit`：固定版式转成表单（没有明细带、没有明细表）：整版放在页眉区段，高度取最低元素的底边；外框 138×92 毫米；外框左上角对齐安全区左上角，整体等比缩小到 0.864（字号随之取整，不低于 8 磅）
- `page_size`：纸张 Custom（150×100 毫米）即预设 label_150x100
- `rows_kept`：原型有 3 行样例数据，每行一张表单；样例只留第一行，其余 2 行舍去
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n6 等 11 个）

### `wms_pack` · WMS · 复核装箱单

原型数据集 `wmsPack`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 204 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 25.5 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_pallet_label` · WMS · 托盘标签

原型数据集 `wmsPalletLabel`，纸张 Custom 150×100 → `label_150x100`；节点 11 → 元素 17（舍去 0 个节点，改写 0 个）。

- `box_grown`：条码框按平台的条高下限调整：宽 115.7 → 113 毫米（居中），高 19 → 19 毫米（n10）
- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n11）
- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3、n4、n5、n8、n9）
- `field_label`：字段的标签舍去（框太小，放不下标签和取值）（n6、n7）
- `font_scaled`：整版按 0.864 缩放：字号 13 磅 → 11 磅（取整，范围 8～28 磅）（n1、n2）
- `font_scaled`：整版按 0.864 缩放：字号 9 磅 → 8 磅（取整，范围 8～28 磅）（n5、n6、n8、n9）
- `font_scaled`：整版按 0.864 缩放：字号 8.8 磅 → 8 磅（取整，范围 8～28 磅）（n10）
- `font_scaled`：整版按 0.864 缩放：字号 6.5 磅 → 8 磅（取整，范围 8～28 磅）（n11）
- `form_fit`：固定版式转成表单（没有明细带、没有明细表）：整版放在页眉区段，高度取最低元素的底边；外框 138×92 毫米；外框左上角对齐安全区左上角，整体等比缩小到 0.864（字号随之取整，不低于 8 磅）
- `page_size`：纸张 Custom（150×100 毫米）即预设 label_150x100
- `rows_kept`：原型有 3 行样例数据，每行一张表单；样例只留第一行，其余 2 行舍去
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n6 等 11 个）

### `wms_pick` · WMS · 波次拣货单

原型数据集 `wmsPick`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 199 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 23.4 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_picking_exception` · WMS · 出库异常跟踪表

原型数据集 `wmsPickingException`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 219 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 22.9 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_putaway` · WMS · 上架任务单

原型数据集 `wmsPutaway`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：首列 24 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_receive` · WMS · 收货验收单

原型数据集 `wmsReceive`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 196 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 21.8 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_receiving_exception` · WMS · 收货差异跟踪表

原型数据集 `wmsReceivingException`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 211 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 24.7 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_return_disposition` · WMS · 退货处理明细

原型数据集 `wmsReturnDisposition`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 230 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 21.8 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_return_receipt` · WMS · 退货收货单

原型数据集 `wmsReturnReceipt`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 217 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 23.1 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_sku_label` · WMS · 商品标签

原型数据集 `wmsSkuLabel`，纸张 Custom 150×100 → `label_150x100`；节点 11 → 元素 17（舍去 0 个节点，改写 0 个）。

- `box_grown`：条码框按平台的条高下限调整：宽 115.7 → 113 毫米（居中），高 19 → 19 毫米（n10）
- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n11）
- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3、n4、n5、n8、n9）
- `field_label`：字段的标签舍去（框太小，放不下标签和取值）（n6、n7）
- `font_scaled`：整版按 0.864 缩放：字号 13 磅 → 11 磅（取整，范围 8～28 磅）（n1、n2）
- `font_scaled`：整版按 0.864 缩放：字号 9 磅 → 8 磅（取整，范围 8～28 磅）（n5、n6、n8、n9）
- `font_scaled`：整版按 0.864 缩放：字号 8.8 磅 → 8 磅（取整，范围 8～28 磅）（n10）
- `font_scaled`：整版按 0.864 缩放：字号 6.5 磅 → 8 磅（取整，范围 8～28 磅）（n11）
- `form_fit`：固定版式转成表单（没有明细带、没有明细表）：整版放在页眉区段，高度取最低元素的底边；外框 138×92 毫米；外框左上角对齐安全区左上角，整体等比缩小到 0.864（字号随之取整，不低于 8 磅）
- `page_size`：纸张 Custom（150×100 毫米）即预设 label_150x100
- `rows_kept`：原型有 3 行样例数据，每行一张表单；样例只留第一行，其余 2 行舍去
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n6 等 11 个）

### `wms_status` · WMS · 库存状态表

原型数据集 `wmsStatus`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 213 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 19.2 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_transfer` · WMS · 移库补货任务单

原型数据集 `wmsTransfer`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 200 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 25.1 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `wms_variance` · WMS · 盘点差异表

原型数据集 `wmsVariance`，纸张 A4 210×297 → `a4`；节点 17 → 元素 29（舍去 0 个节点，改写 1 个）。

- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n2、n3）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8、n9、n12 等 9 个）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n16）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n5、n6、n7、n9、n12、n13 等 8 个）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n8）
- `font_rounded`：字号 7.6 磅取整为 8 磅（n10）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n17）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n5、n6、n7 等 15 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n4、n17）
- `table_option`：列宽之和 206 与表格宽 186 不等，按比例调整（n11）
- `table_option`：首列 22.6 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n11）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n11）
- `table_option`：不转换：negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n11）

### `bol` · 海运提单 B/L · 集装箱

原型数据集 `bol`，纸张 A4 210×297 → `a4`；节点 52 → 元素 52（舍去 0 个节点，改写 0 个）。

- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n1、n19、n20、n21、n22、n23 等 30 个）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+0.7 毫米）（n20）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+1.8 毫米）（n21）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+2.9 毫米）（n22、n31）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+0.6 毫米）（n29）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+1.7 毫米）（n30）
- `font_scaled`：整版按 0.886 缩放：字号 9.5 磅 → 8 磅（取整，范围 8～28 磅）（n1）
- `font_scaled`：整版按 0.886 缩放：字号 8.4 磅 → 8 磅（取整，范围 8～28 磅）（n2、n3、n4、n25、n26、n27 等 13 个）
- `font_scaled`：整版按 0.886 缩放：字号 9 磅 → 8 磅（取整，范围 8～28 磅）（n5、n13、n23、n32、n42、n52）
- `font_scaled`：整版按 0.886 缩放：字号 8.3 磅 → 8 磅（取整，范围 8～28 磅）（n6、n7、n8、n9）
- `font_scaled`：整版按 0.886 缩放：字号 8.5 磅 → 8 磅（取整，范围 8～28 磅）（n10、n11、n12、n14、n15、n16 等 16 个）
- `font_scaled`：整版按 0.886 缩放：字号 7.5 磅 → 8 磅（取整，范围 8～28 磅）（n20、n21、n22、n24、n29、n30 等 10 个）
- `font_scaled`：整版按 0.886 缩放：字号 7.8 磅 → 8 磅（取整，范围 8～28 磅）（n39）
- `form_fit`：固定版式转成表单（没有明细带、没有明细表）：整版放在页眉区段，高度取最低元素的底边；外框 192×268 毫米；外框左上角对齐安全区左上角，整体等比缩小到 0.886（字号随之取整，不低于 8 磅）
- `line_break`：样例取值里的 22 处换行换成「 / 」，0 处其他控制字符换成空格（样例行不含控制字符）
- `page_background`：版式参考图 "bol-v1" 是原型自己画的图形，不是位图，不转换
- `page_background`：底图透明度 1 不转换
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n6 等 52 个）

### `booking` · 海运订舱委托 · Ocean Booking

原型数据集 `booking`，纸张 A4 210×297 → `a4`；节点 22 → 元素 37（舍去 0 个节点，改写 1 个）。

- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n1、n2）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n7、n8、n9、n12、n13、n14 等 12 个）
- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n10、n11）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n2）
- `font_raised`：字号 7.1 磅低于下限 8 磅，提高到 8 磅（n5）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n26）
- `font_rounded`：字号 10.5 磅取整为 11 磅（n1）
- `font_rounded`：字号 8.3 磅取整为 8 磅（n7、n8、n9、n10、n11、n15 等 7 个）
- `font_rounded`：字号 8.2 磅取整为 8 磅（n12、n13）
- `font_rounded`：字号 7.8 磅取整为 8 磅（n14）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n17）
- `line_break`：样例取值里的 16 处换行换成「 / 」，0 处其他控制字符换成空格（样例行不含控制字符）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n26）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n7 等 19 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n6、n26、n17）
- `table_option`：首列 23 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n17）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n17）
- `table_option`：不转换：headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n17）

### `fedex` · FedEx 快递面单 · 4×6

原型数据集 `fedex`，纸张 4x6 101.6×152.4 → `label_4x6in`；节点 20 → 元素 20（舍去 0 个节点，改写 1 个）。

- `barcode_symbology`：二维码类型 pdf417 换成 QR 码（纠错级别 M）：PDF417 是宽扁的矩形，QR 码是正方形，边长取框的短边，不小于模块 0.4 毫米所需的大小（n19）
- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n1、n2、n3、n4、n5、n6 等 18 个）
- `box_grown`：条码框按平台的条高下限调整：宽 88.6 → 82.4 毫米（居中），高 11.3 → 11.3 毫米（n18）
- `box_grown`：二维码边长 10 → 13.3 毫米（模块不小于 0.4 毫米）（n19）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+1.1 毫米）（n2）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+2.2 毫米）（n3）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+3.4 毫米）（n4）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+0.4 毫米）（n6）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+0.8 毫米）（n7）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+0.7 毫米）（n9）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+2.0 毫米）（n20）
- `font_scaled`：整版按 0.947 缩放：字号 7.3 磅 → 8 磅（取整，范围 8～28 磅）（n1、n2、n3、n4）
- `font_scaled`：整版按 0.947 缩放：字号 7.2 磅 → 8 磅（取整，范围 8～28 磅）（n5、n7）
- `font_scaled`：整版按 0.947 缩放：字号 8.5 磅 → 8 磅（取整，范围 8～28 磅）（n6）
- `font_scaled`：整版按 0.947 缩放：字号 11.5 磅 → 11 磅（取整，范围 8～28 磅）（n8、n12）
- `font_scaled`：整版按 0.947 缩放：字号 9.4 磅 → 9 磅（取整，范围 8～28 磅）（n9）
- `font_scaled`：整版按 0.947 缩放：字号 10.5 磅 → 10 磅（取整，范围 8～28 磅）（n10、n11、n17）
- `font_scaled`：整版按 0.947 缩放：字号 7.5 磅 → 8 磅（取整，范围 8～28 磅）（n13）
- `font_scaled`：整版按 0.947 缩放：字号 15 磅 → 14 磅（取整，范围 8～28 磅）（n14）
- `font_scaled`：整版按 0.947 缩放：字号 22 磅 → 21 磅（取整，范围 8～28 磅）（n15）
- `font_scaled`：整版按 0.947 缩放：字号 14 磅 → 13 磅（取整，范围 8～28 磅）（n16）
- `font_scaled`：整版按 0.947 缩放：字号 6.7 磅 → 8 磅（取整，范围 8～28 磅）（n20）
- `form_fit`：固定版式转成表单（没有明细带、没有明细表）：整版放在页眉区段，高度取最低元素的底边；外框 93.6×137.6 毫米；外框左上角对齐安全区左上角，整体等比缩小到 0.947（字号随之取整，不低于 8 磅）
- `page_background`：版式参考图 "fedex-v1" 是原型自己画的图形，不是位图，不转换
- `page_background`：底图透明度 1 不转换
- `page_size`：纸张 4x6（101.6×152.4 毫米）即预设 label_4x6in
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n6 等 18 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n18、n19）

### `manifest` · 货物舱单 · Cargo Manifest

原型数据集 `manifest`，纸张 A4 210×297 → `a4`；节点 22 → 元素 37（舍去 0 个节点，改写 1 个）。

- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n1、n2）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n7、n8、n9、n12、n13、n14 等 12 个）
- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n10、n11）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n2）
- `font_raised`：字号 7.1 磅低于下限 8 磅，提高到 8 磅（n5）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n26）
- `font_rounded`：字号 10.5 磅取整为 11 磅（n1）
- `font_rounded`：字号 8.3 磅取整为 8 磅（n7、n8、n9、n10、n11、n15 等 7 个）
- `font_rounded`：字号 8.2 磅取整为 8 磅（n12、n13）
- `font_rounded`：字号 7.8 磅取整为 8 磅（n14）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n17）
- `line_break`：样例取值里的 16 处换行换成「 / 」，0 处其他控制字符换成空格（样例行不含控制字符）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n26）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n7 等 19 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n6、n26、n17）
- `table_option`：首列 32 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n17）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n17）
- `table_option`：不转换：headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n17）

### `mawb` · MAWB 航空主运单 · 套打

原型数据集 `mawb`，纸张 A4 210×297 → `a4`；节点 72 → 元素 72（舍去 0 个节点，改写 0 个）。

- `box_grown`：文本框宽度小于 10 毫米，放大到 10 毫米（n1、n20、n21、n22、n23、n24 等 13 个）
- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n1、n2、n3、n4、n6、n9 等 50 个）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（-2.6 毫米）（n6）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（-2.5 毫米）（n9）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+5.1 毫米）（n24、n41）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（-5.1 毫米）（n27、n29）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+0.9 毫米）（n30）
- `font_scaled`：整版按 0.848 缩放：字号 12 磅 → 10 磅（取整，范围 8～28 磅）（n1、n2、n3、n4、n72）
- `font_scaled`：整版按 0.848 缩放：字号 6.5 磅 → 8 磅（取整，范围 8～28 磅）（n6、n9）
- `font_scaled`：整版按 0.848 缩放：字号 7.5 磅 → 8 磅（取整，范围 8～28 磅）（n15、n18、n19、n20、n21、n22 等 12 个）
- `font_scaled`：整版按 0.848 缩放：字号 7 磅 → 8 磅（取整，范围 8～28 磅）（n16、n17、n33、n34）
- `form_fit`：固定版式转成表单（没有明细带、没有明细表）：整版放在页眉区段，高度取最低元素的底边；外框 192×280 毫米；外框左上角对齐安全区左上角，整体等比缩小到 0.848（字号随之取整，不低于 8 磅）
- `line_break`：样例取值里的 16 处换行换成「 / 」，0 处其他控制字符换成空格（样例行不含控制字符）
- `not_numeric`：字段 collectWeight（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 prepaidValuation（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 collectValuation（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 prepaidTax（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 collectTax（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 collectDueAgent（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 collectDueCarrier（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 collectTotal（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 destinationCollect（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 destinationCharges（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 destinationTotal（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `overprint`：套打（mode=variables）转成 page.print.variables_only：只印变量，文字、线、框不印；偏移取原型的偏移
- `overprint`：背景留空：原型的预印版面是自己画的图形，不是位图，平台的 page.print.background 需要一张 PNG
- `page_background`：版式参考图 "mawb-v1" 是原型自己画的图形，不是位图，不转换
- `page_background`：底图透明度 0.85 不转换
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n6 等 72 个）

### `mawb_air_waybill` · 航空运单 · Air Waybill (MAWB)

原型数据集 `mawb`，纸张 A4 210×297 → `a4`；节点 72 → 元素 72（舍去 0 个节点，改写 0 个）。

- `box_grown`：文本框宽度小于 10 毫米，放大到 10 毫米（n1、n20、n21、n22、n23、n24 等 13 个）
- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n1、n2、n3、n4、n6、n9 等 50 个）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（-2.6 毫米）（n6）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（-2.5 毫米）（n9）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+5.1 毫米）（n24、n41）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（-5.1 毫米）（n27、n29）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+0.9 毫米）（n30）
- `font_scaled`：整版按 0.848 缩放：字号 12 磅 → 10 磅（取整，范围 8～28 磅）（n1、n2、n3、n4、n72）
- `font_scaled`：整版按 0.848 缩放：字号 6.5 磅 → 8 磅（取整，范围 8～28 磅）（n6、n9）
- `font_scaled`：整版按 0.848 缩放：字号 7.5 磅 → 8 磅（取整，范围 8～28 磅）（n15、n18、n19、n20、n21、n22 等 12 个）
- `font_scaled`：整版按 0.848 缩放：字号 7 磅 → 8 磅（取整，范围 8～28 磅）（n16、n17、n33、n34）
- `form_fit`：固定版式转成表单（没有明细带、没有明细表）：整版放在页眉区段，高度取最低元素的底边；外框 192×280 毫米；外框左上角对齐安全区左上角，整体等比缩小到 0.848（字号随之取整，不低于 8 磅）
- `line_break`：样例取值里的 16 处换行换成「 / 」，0 处其他控制字符换成空格（样例行不含控制字符）
- `not_numeric`：字段 collectWeight（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 prepaidValuation（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 collectValuation（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 prepaidTax（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 collectTax（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 collectDueAgent（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 collectDueCarrier（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 collectTotal（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 destinationCollect（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 destinationCharges（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `not_numeric`：字段 destinationTotal（decimal）的取值不全是精确数字（有空值或非数字），样例里按文本处理
- `overprint`：套打（mode=variables）转成 page.print.variables_only：只印变量，文字、线、框不印；偏移取原型的偏移
- `overprint`：背景留空：原型的预印版面是自己画的图形，不是位图，平台的 page.print.background 需要一张 PNG
- `page_background`：版式参考图 "mawb-v1" 是原型自己画的图形，不是位图，不转换
- `page_background`：底图透明度 0.85 不转换
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n6 等 72 个）

### `usps` · USPS 快递面单 · 4×6

原型数据集 `usps`，纸张 4x6 101.6×152.4 → `label_4x6in`；节点 18 → 元素 18（舍去 0 个节点，改写 0 个）。

- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n1、n3、n4、n5、n6、n7 等 16 个）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+1.3 毫米）（n4）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+2.6 毫米）（n5）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+3.8 毫米）（n6）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+0.6 毫米）（n10）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+0.5 毫米）（n12）
- `font_scaled`：整版按 0.951 缩放：字号 30 磅 → 28 磅（取整，范围 8～28 磅）（n1）
- `font_scaled`：整版按 0.951 缩放：字号 13.3 磅 → 13 磅（取整，范围 8～28 磅）（n2）
- `font_scaled`：整版按 0.951 缩放：字号 7.5 磅 → 8 磅（取整，范围 8～28 磅）（n3、n4、n5、n6、n14）
- `font_scaled`：整版按 0.951 缩放：字号 7.2 磅 → 8 磅（取整，范围 8～28 磅）（n7）
- `font_scaled`：整版按 0.951 缩放：字号 8.5 磅 → 8 磅（取整，范围 8～28 磅）（n8）
- `font_scaled`：整版按 0.951 缩放：字号 11.5 磅 → 11 磅（取整，范围 8～28 磅）（n9、n13）
- `font_scaled`：整版按 0.951 缩放：字号 9.4 磅 → 9 磅（取整，范围 8～28 磅）（n10）
- `font_scaled`：整版按 0.951 缩放：字号 10.5 磅 → 10 磅（取整，范围 8～28 磅）（n11、n12）
- `font_scaled`：整版按 0.951 缩放：字号 7 磅 → 8 磅（取整，范围 8～28 磅）（n17、n18）
- `form_fit`：固定版式转成表单（没有明细带、没有明细表）：整版放在页眉区段，高度取最低元素的底边；外框 93.6×137 毫米；外框左上角对齐安全区左上角，整体等比缩小到 0.951（字号随之取整，不低于 8 磅）
- `page_background`：版式参考图 "usps-v1" 是原型自己画的图形，不是位图，不转换
- `page_background`：底图透明度 1 不转换
- `page_size`：纸张 4x6（101.6×152.4 毫米）即预设 label_4x6in
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n6 等 17 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n16）

### `commercial` · 商业发票 · Commercial Invoice

原型数据集 `commercial`，纸张 A4 210×297 → `a4`；节点 41 → 元素 57（舍去 0 个节点，改写 2 个）。

- `band_height`：区段 reportFooter 高 78 → 79.7 毫米：为让开互相重叠的文本，其中的元素需要往下挪出原来的区段
- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n1、n2、n39）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+8.1 毫米）（n38）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+5.2 毫米）（n39）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+3.7 毫米）（n40）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n7、n8、n9、n12、n13、n14 等 12 个）
- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n10、n11）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n2）
- `font_raised`：字号 7.1 磅低于下限 8 磅，提高到 8 磅（n5）
- `font_raised`：字号 6.5 磅低于下限 8 磅，提高到 8 磅（n39）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n41）
- `font_rounded`：字号 10.5 磅取整为 11 磅（n1）
- `font_rounded`：字号 16.5 磅取整为 17 磅（n3）
- `font_rounded`：字号 8.3 磅取整为 8 磅（n7、n8、n9、n10、n11、n15 等 7 个）
- `font_rounded`：字号 8.2 磅取整为 8 磅（n12、n13）
- `font_rounded`：字号 7.8 磅取整为 8 磅（n14、n24、n26、n28、n30、n32 等 7 个）
- `font_rounded`：字号 7.5 磅取整为 8 磅（n20、n21）
- `font_rounded`：字号 9.5 磅取整为 10 磅（n37）
- `font_rounded`：字号 12.5 磅取整为 13 磅（n38）
- `font_rounded`：字号 8.6 磅取整为 9 磅（n40）
- `font_scaled`：取值放不下，字号缩小到能放下为止（不低于 8 磅）（n40）
- `line_break`：样例取值里的 27 处换行换成「 / 」，0 处其他控制字符换成空格（样例行不含控制字符）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n41）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `signature`：签章与签名属于后续阶段：转成方框加原文字（n40）
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n7 等 36 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n6、n18、n36、n41、n17）
- `table_option`：首列 20 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n17）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n17）
- `table_option`：不转换：headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n17）

### `export_packing` · 出口装箱单 · Packing List

原型数据集 `exportPacking`，纸张 A4 210×297 → `a4`；节点 26 → 元素 44（舍去 0 个节点，改写 2 个）。

- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n1、n2、n3）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+0.3 毫米）（n4）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n7、n8、n9、n12、n13、n14 等 14 个）
- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n10、n11）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n2）
- `font_raised`：字号 7.1 磅低于下限 8 磅，提高到 8 磅（n5）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n26）
- `font_raised`：字号 7.2 磅低于下限 8 磅，提高到 8 磅（n17）
- `font_rounded`：字号 10.5 磅取整为 11 磅（n1）
- `font_rounded`：字号 8.3 磅取整为 8 磅（n7、n8、n9、n10、n11、n15 等 7 个）
- `font_rounded`：字号 8.2 磅取整为 8 磅（n12、n13）
- `font_rounded`：字号 7.8 磅取整为 8 磅（n14、n24）
- `font_rounded`：字号 8.6 磅取整为 9 磅（n25）
- `line_break`：样例取值里的 16 处换行换成「 / 」，0 处其他控制字符换成空格（样例行不含控制字符）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n26）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `signature`：签章与签名属于后续阶段：转成方框加原文字（n25）
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n7 等 22 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n6、n18、n26、n17）
- `table_option`：首列 24 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n17）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n17）
- `table_option`：不转换：headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n17）

### `origin` · 原产地证书 · Certificate of Origin

原型数据集 `origin`，纸张 A4 210×297 → `a4`；节点 21 → 元素 35（舍去 0 个节点，改写 1 个）。

- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n1、n2）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n7、n8、n9、n12、n13、n14 等 11 个）
- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n10、n11）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n2）
- `font_raised`：字号 7.1 磅低于下限 8 磅，提高到 8 磅（n5）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n26）
- `font_rounded`：字号 10.5 磅取整为 11 磅（n1）
- `font_rounded`：字号 13.4 磅取整为 13 磅（n3）
- `font_rounded`：字号 8.3 磅取整为 8 磅（n7、n8、n9、n10、n11、n15 等 7 个）
- `font_rounded`：字号 8.2 磅取整为 8 磅（n12、n13）
- `font_rounded`：字号 7.8 磅取整为 8 磅（n14）
- `font_rounded`：字号 7.7 磅取整为 8 磅（n17）
- `line_break`：样例取值里的 16 处换行换成「 / 」，0 处其他控制字符换成空格（样例行不含控制字符）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n26）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n7 等 18 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n6、n26、n17）
- `table_option`：首列 25 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n17）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n17）
- `table_option`：不转换：headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n17）

### `proforma` · 形式发票 · Pro Forma Invoice

原型数据集 `proforma`，纸张 A4 210×297 → `a4`；节点 41 → 元素 57（舍去 0 个节点，改写 2 个）。

- `band_height`：区段 reportFooter 高 78 → 79.7 毫米：为让开互相重叠的文本，其中的元素需要往下挪出原来的区段
- `box_grown`：文本框高度小于平台下限（5 毫米，且不小于所需行数的高度），已放大（n1、n2、n39）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+8.1 毫米）（n38）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+5.2 毫米）（n39）
- `element_overlap`：与先出现的元素重叠，纵向移到空处（+3.7 毫米）（n40）
- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n7、n8、n9、n12、n13、n14 等 12 个）
- `field_label`：字段的标签在取值左侧另出一个 8 磅文本（框不够高放不下上下两行）（n10、n11）
- `font_raised`：字号 6.8 磅低于下限 8 磅，提高到 8 磅（n2）
- `font_raised`：字号 7.1 磅低于下限 8 磅，提高到 8 磅（n5）
- `font_raised`：字号 6.5 磅低于下限 8 磅，提高到 8 磅（n39）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n41）
- `font_rounded`：字号 10.5 磅取整为 11 磅（n1）
- `font_rounded`：字号 16.5 磅取整为 17 磅（n3）
- `font_rounded`：字号 8.3 磅取整为 8 磅（n7、n8、n9、n10、n11、n15 等 7 个）
- `font_rounded`：字号 8.2 磅取整为 8 磅（n12、n13）
- `font_rounded`：字号 7.8 磅取整为 8 磅（n14、n24、n26、n28、n30、n32 等 7 个）
- `font_rounded`：字号 7.5 磅取整为 8 磅（n20、n21）
- `font_rounded`：字号 9.5 磅取整为 10 磅（n37）
- `font_rounded`：字号 12.5 磅取整为 13 磅（n38）
- `font_rounded`：字号 8.6 磅取整为 9 磅（n40）
- `font_scaled`：取值放不下，字号缩小到能放下为止（不低于 8 磅）（n40）
- `line_break`：样例取值里的 27 处换行换成「 / 」，0 处其他控制字符换成空格（样例行不含控制字符）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n41）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `rows_kept`：保留全部 3 行样例数据
- `signature`：签章与签名属于后续阶段：转成方框加原文字（n40）
- `style_dropped`：样式里平台没有的属性不转换：颜色、行距（n1、n2、n3、n4、n5、n7 等 36 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n6、n18、n36、n41、n17）
- `table_option`：首列 20 → 32.5 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n17）
- `table_option`：原型的表格没有累计和加和：数值列不标为度量列，平台的表格就不对它们求和（n17）
- `table_option`：不转换：headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n17）

### `order` · 销售订单

原型数据集 `orders`，纸张 A4 210×297 → `a4`；节点 14 → 元素 25（舍去 0 个节点，改写 5 个）。

- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n14）
- `font_rounded`：字号 7.8 磅取整为 8 磅（n3）
- `font_rounded`：字号 11.5 磅取整为 12 磅（n5）
- `font_rounded`：字号 9.5 磅取整为 10 磅（n7、n8）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n10）
- `image`：图片节点转成方框加名称：原型的图片为空，只显示内置的标识图形，平台的样例不带私有图片（n1）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n14）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `parameters`：4 个查询参数（开始日期、结束日期、搜索摘要、业务类别）不转换：平台的查询面板就是参数
- `rows_kept`：保留全部 28 行样例数据
- `signature`：签章与签名属于后续阶段：转成方框加原文字（n12、n13）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n1、n2、n4、n5、n6、n7 等 13 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色、字距（n3）
- `summary`：汇总卡片转成 2 栏：标签文字加合计（group_sum）（n9）
- `table_option`：首列 23 → 36 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n10）
- `table_option`：承前页 / 转下页累计（carry=false）和本页加和（subtotal=true）对应平台表格的承前、转下页和合计行；数值列（单价除外）标为度量列（n10）
- `table_option`：不转换：striped（隔行底色）、negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n10）

### `statement` · 客户往来对账单

原型数据集 `ledger`，纸张 A4 210×297 → `a4`；节点 14 → 元素 26（舍去 0 个节点，改写 5 个）。

- `field_label`：字段的标签在取值上方另出一个 8 磅文本（n5、n6、n7、n8）
- `font_raised`：字号 7 磅低于下限 8 磅，提高到 8 磅（n14）
- `font_rounded`：字号 7.8 磅取整为 8 磅（n3）
- `font_rounded`：字号 11.5 磅取整为 12 磅（n5）
- `font_rounded`：字号 9.5 磅取整为 10 磅（n7、n8）
- `font_rounded`：字号 8.5 磅取整为 9 磅（n10）
- `image`：图片节点转成方框加名称：原型的图片为空，只显示内置的标识图形，平台的样例不带私有图片（n1）
- `page_number`：页码节点转成两个文字：左侧原文字，右侧页码（{page} / {pages}）（n14）
- `page_size`：纸张 A4（210×297 毫米）即预设 a4
- `parameters`：4 个查询参数（开始日期、结束日期、搜索摘要、业务类别）不转换：平台的查询面板就是参数
- `rows_kept`：保留全部 28 行样例数据
- `signature`：签章与签名属于后续阶段：转成方框加原文字（n12、n13）
- `style_dropped`：样式里平台没有的属性不转换：颜色（n1、n2、n4、n5、n6、n7 等 13 个）
- `style_dropped`：样式里平台没有的属性不转换：颜色、字距（n3）
- `summary`：汇总卡片转成 3 栏：标签文字加合计（group_sum）（n9）
- `summary`：「本期差额」是两列合计之差，平台的合计只对单列求和：只留标签，不出数值（n9）
- `table_option`：首列 23 → 36 毫米：平台的表格在首列放「承前（第 n 页）」「过次（转第 n 页）」「合计」，首列要放得下；多出的宽度从其余各列按比例让出（n10）
- `table_option`：承前页 / 转下页累计（carry=true）和本页加和（subtotal=true）对应平台表格的承前、转下页和合计行；数值列（单价除外）标为度量列（n10）
- `table_option`：不转换：striped（隔行底色）、negativeRed（负数红字）、headerColor（表头底色）、minRowHeight、padding（行高与内边距，平台的行高由字号定）（n10）

