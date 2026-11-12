# 历次切片说明

2026-11-06 之前，每个切片都在 README.md 开头加一节，README 因此成了 600 多行的历史记录。下面是那份 README 的原文，没有改动，最新的切片在前。末尾“当前链路”“构建与启动”“测试与证据”“后续范围”四节始于 S03-A，此后只零星更新，有已经过时的地方（例如“后续范围”仍以 S03-I 为下一步）；当前的说明以 README.md 为准。

以后的切片把说明加在本文件开头（这段说明之后），README.md 只更新“现状”。

---

# IDP Reporting Platform · S23-B

2026-11-12。表格报表表现层的控制台一侧（ADR 0024 决定 5，docs/S23B_SCOPE.md）：报表布局设计器多了标题、脚注、列标签、居中、数值列格式（千分位 / 负数 / 零）、按一个维度列的小计与至多 8 条标记规则，用到的布局以 1.1 保存、没用到的仍是 1.0 一字不差；设计器不提供服务端会拒绝的东西并以规则说明拒绝（`console/assets/present.js` 是服务端规则与表现层在控制台的副本：精确十进制求和、比较、格式、分组）；1.1 布局下表格就是导出会打印的样子（标签、格式、精确值在 `title`、小计行、标记底色、标题与脚注；屏幕按显示的行分组，文件按整个快照）；“导出 Excel / PDF”带已保存布局的 `layout_id`，任务行写明布局版本，有未保存修改时先“导出前确认”（按已保存的版本导出 / 保存后导出）；嵌入页共用设计器；任务基线新增 T11（6 次点击、1 次页面切换），守住的合计 41 / 11 / 0 / 0。Go 代码未改。

---

# IDP Reporting Platform · S25-A

2026-11-12。结果的三个期限（ADR 0026，docs/S25A_SCOPE.md）：核心与两个存储把来源读取的期限（`PreparedRead.ExpiresAt`，至多 15 分钟）、来源担保的授权范围的期限（`ScopeExpiresAt`，可选）与结果的保留期限（`Job.KeepUntil`）分开。工作快照仍随读取结束；导出文件与定时运行的快照保留 `retention.results`（默认 7 天），正式出具的文件保留 `retention.issued`（默认 10 年），都不超过来源担保的范围。每次下载、每页数据仍向来源询问当前授权，来源拒绝或不可达即拒绝；保留超过读取的文件自带依据（冻结的查询与读取），快照清理后仍按来源的回答提供。来源协议还不能声明范围（S25-B），所以部署中的行为、存储的编码与摘要都不变；只有内存合同模拟器可以担保范围，生命周期合同在内存与 PostgreSQL 两个存储上对它运行。无 SQL 迁移。配额的调整（ADR 0026 决定 7）与对象存储对账的节奏在 S25-B，都须先于任何来源声明范围。

---

# IDP Reporting Platform · S23-A

2026-11-11。表格报表的表现层（ADR 0024，docs/S23A_SCOPE.md）：报表布局 schema 1.1（标题、脚注、列标签、居中、数值列格式、一级小计、至多 8 条标记规则；1.0 不变），`POST /exports` 可带 `layout_id`，导出按布局最新保存版本渲染并在任务与产物上记录布局 id 与版本（任务视图 `layout`，幂等键绑定版本）；PDF 与工作簿共用表现层 `export/present`（精确小计沿用文档表格的十进制规则）；每份工作簿的数值列改为带数字格式的数值单元格并在旁边加 `<标签>（精确）` 文本列，`Totals` 加 `value`，`Metadata` 加 `inexact_cells` 与布局信息；不带布局的 PDF 逐字节不变（golden 测试）。无 SQL 迁移。控制台部分是 S23-B。

---

# IDP Reporting Platform · S07-H

2026-11-06。迁移 010 `jobs_identity_kind_stats`（docs/CAPACITY.md “Migration 010”）：给 `jobs` 的 (tenant, issuer, subject, kind) 加扩展统计并 ANALYZE。签发记录很多的身份，签发记录列表在 1,000,000 行时从约 101 ms 降到约 1.1 ms（p50，两次各量）：规划器以前把四列当成互相独立、估计 91 行（实际 20,000），保留唯一键并排序全部 20,000 行；有了统计后按 `jobs_identity_created` 从新到旧读，读到 25 行就停。没有新索引，没有改语句；009 不变（账本按校验和核对已应用的迁移）。

2026-11-06。报表工作台成为首页，数据中心并进数据抽屉（ADR 0011，docs/S07H_SCOPE.md）：登录后直接进入报表工作台，空状态里是三步流程；应用栏去掉“概览”和“数据”，剩六个入口；数据抽屉的“数据集字段”列出所选数据集的字段、类型与币种；模板中心的“数据中心”与旧地址 `#catalog` 打开数据抽屉。任务基线：T1 4 → 3 次点击，T7 3 → 2 次点击、2 → 0 次页面切换；合计 33 / 12 → 31 / 9。

2026-11-06。三个设计器一个版本与发布模型（ADR 0011，docs/S07G_SCOPE.md）：报表布局设计器有了和两个工作室相同的“版本 · 保存版本 · 发布”、发布前确认与“保存并发布”；看板工作室的“版本”是同一个版本对话框（检查器不再有“版本”页签）；两处都能把保存版本恢复到编辑器或发布历史版本。任务基线 T6：8 → 5 次点击，死胡同 2 → 0。

2026-11-05。一个壳子（ADR 0010，docs/S07F_SCOPE.md）：控制台页眉留在每个页面上，文档工作室、模板中心、看板工作室在它下面；它们自己的顶栏成为工具栏。任务基线的壳子切换 8 → 0，其余不变。顺带修好模板中心卡片视图的横向溢出（卡片继承了数据中心的列布局）。

2026-11-05。任务列表的筛选与分页（docs/S07E_SCOPE.md）：`GET /jobs` 新增 `kind`、`state` 与游标 `before`（上一页最后一个任务 ID）；任务面板有类型、状态两个筛选和“加载更多”（最多保留 500 项）。

2026-11-04。按身份列任务（docs/S07C_SCOPE.md）：

- 服务端新增 `GET /jobs?limit=N`（1..100，默认 50）：当前身份在其持有的产品连接下的全部任务（快照、导出、出具），最新在前，每项即 `GET /jobs/{id}` 的任务视图（含 `expired` 与出具编号），不向来源重新授权——结果在读取时授权。仓库接口新增 `Jobs`（内存与 PostgreSQL 实现，`jobs_identity_created` 索引服务于它，计划测试与容量语句表已加入）。
- 帮助图标（S07-D）：应用栏底部与登录卡上的“帮助”在新窗口打开随服务提供的用户手册 `/console/manual/`（`docs/manual` 嵌入二进制，docs/CONSOLE.md）。
- 控制台的任务面板由此列表驱动：登录后、打开面板时、“刷新状态”时读取；不再是“本次会话任务”。列表之外（更早）的任务仍用“恢复查看”按 ID 打开。

# IDP Reporting Platform · S07-B

2026-11-04。ADR 0009 §5 的下一步（docs/S07B_SCOPE.md），同一套任务基线量前后：

- 任务面板：应用栏的“任务”在任何壳子上拉开右侧面板（任务页的恢复行、列表与“刷新状态”搬进去，关上放回；“查看结果”落到工作台并关闭面板）；任务页保留但不再是导航目的地。
- 保存并发布：看板工作室与文档工作室的“发布前确认”在有未保存修改或尚未保存时不再拒绝，按钮变为“保存并发布”，确认即先保存为下一个版本、读取发布状态、再发布它；干净的保存版本仍是“确认发布”。

数字在 docs/UX_BASELINE_S07.md（新增 S07-B 一列）。服务端无改动。

以下为此前阶段历史说明：

# IDP Reporting Platform · S07-A

2026-11-04。ADR 0009（docs/ADR_0009_SHELL_CONTINUITY.md，docs/S07A_SCOPE.md）：原型只规定屏幕，不规定屏幕之间的通道。S06 之后控制台有四套互相替换的壳子，数据上下文只在工作台页里；用任务基线（`scripts/ux_baseline_s07.mjs`，五个任务）量出代价后，做两件便宜、可逆的事：

- 应用栏：56 px 的常驻导航在所有壳子上（文档工作室、模板中心、看板工作室的全屏盒子从它右侧开始），带数据集数与任务数角标；S06-C 的 222 px 导航列去掉；≤ 700 px 变顶部横条。
- 数据抽屉：页眉与模板中心页眉的“当前数据”芯片（数据集@版本 · 公司范围 · 时点 · 查询预览/冻结快照）、看板工作室的“参数与筛选”与“选择数据…”都打开同一个抽屉，里面是工作台的查询面板（搬入，关上放回；文档工作室自己的“参数与筛选”用的就是这个机制）；结果到来抽屉自动关闭。看板起始卡与模板中心不再把人送回工作台。

前后对比在 docs/UX_BASELINE_S07.md。服务端无改动；嵌入页没有应用栏、芯片与抽屉。测试：DOM 新增“S07-A A1–A3”，既有断言只改选择器；`scripts/browser_console_s06c.mjs` 按应用栏几何更新重跑，证据在 `evidence/s07a/`。

以下为此前阶段历史说明：

# IDP Reporting Platform · S06-C

2026-11-03。控制台其余页面换成原型的外壳（ADR 0008 §4，docs/S06C_SCOPE.md；地图、偏离清单与截图索引在 docs/STUDIO.md 的“控制台外壳”与“看板工作室”两节）。服务端没有改动。

- 外壳（C1–C5、C7、C9）：每个不是工作室的页面——登录卡、工作区概览、数据中心（原“数据目录”）、报表工作台、任务工作区、能力与环境——按原型模板中心与数据中心的版式：68 px 页眉（字标带页面标签、环境标记、身份、退出工作区）、222 px 导航列（原型的 `catalog-nav` 行，带数据集数与任务数，底部是连接状态）、带标题块的内容列；组件改写为原型的词汇（`.btn` 家族、药丸、`data-table`、`dc-*` 卡片与说明）。`styles.css` 重写，S03-A 的外壳与组件规则移除，画布规则不变；亮色是模板中心的令牌，暗色由同样的推导得出（`prefers-color-scheme` 与 `data-theme`）；≤ 1100 px 导航 190 px，≤ 700 px 横向芯片行。
- 看板工作室（C6）：看板设计器成为自己的一种工作室——顶栏（字标 DASHBOARD STUDIO、面包屑、标题与状态徽标 未查询 / 草稿 / 已保存 v{n} / 已发布 v{n} / · 有未保存修改、帮助、版本、保存版本、发布）、工具栏（组件 / 结构 / 数据）、侧栏（组件库里的三种组件与字段选择、组件与顺序、数据来源）、工作区（上下文条、起始卡、组件卡片的桌面）、检查器（属性 / 版本 / 文件）、状态栏、发布前确认与帮助对话框。`dashboard.js` 的每个函数与 id 不变，新增 `state()`、`ports.changed` 与返回 Promise 的动作；`board.js` 是外壳层。窄屏时侧栏与检查器是抽屉。
- 嵌入页（C8）：三个区段仍与控制台逐字节相同；`embed.css` 重写为新类名，看板工作室在框内随内容增高；暗色经 `data-theme` 走同一套令牌。

测试：DOM 顶层 179 → 181 项（新增“S06-C C1/C2/C3/C4/C9”与“S06-C C6”），嵌入页 23 → 24 项，既有断言只改选择器（S06-A 的几处限定到 `#page-documents`）；Go 静态文件测试加 `board.js`；`node --check`、无 `innerHTML`。真实浏览器脚本 `scripts/browser_console_s06c.mjs`（Windows Chrome，CDP）：控制台各页与原型的模板中心、数据中心并排（1440），单独在 1280/768/390 亮、暗（无水平溢出，每个可见文本对比度 ≥ 4.5:1），以及一段真实会话（登录、概览、数据中心、工作台预览与快照、看板工作室的三个组件、结构面板、保存、版本、发布确认、帮助、任务、能力、退出）；证据在 `evidence/s06c/console/`。

以下为此前阶段历史说明：

# IDP Reporting Platform · S06-B parts 1 and 2

2026-11-03。模板中心的服务端与渲染器部分（docs/S06B_SCOPE.md 的第一部分，B1–B4；第二部分转换 37 个样例，见下；第三部分是模板中心页面）：页面预设、2.x 套印、抬高的元素上限、样例包与目录元数据的路由、每身份 64 个模板。

- 页面预设（B1）：2.x 模板的 `page.preset`——`a4`（缺省，所有已存模板都是它）、`a4_landscape`、`a5`、`label_4x6in`、`label_100x150`、`label_100x100`。预设表只有一份，在 `doctemplates/schema2.go`（宽、高、安全区、横幅/印章条）；Typst 页面（`document.typ` 读 `data.json` 的页面尺寸，A4 不写该键所以字节不变）、版面、检查器（读测试写入的期望，没有自己的另一张表）、画布（`canvas.js` 里一份由测试与预设表比对的副本）都从它取数。出具印章条每个预设都有；窄条把最宽的合法印章在第一个 “ - ” 后折成两行（A5 8 pt，标签 6 pt），测试证明最宽的合法印章在每个预设里都放得下。1.x 仍只有 A4，带 `page` 被拒绝。
- 2.x 套印（B2）：`page.print {offset_x, offset_y, variables_only, background?}`，语义同 1.x（偏移移动每个元素、表格与分页；底图在完整模式画在页面下、仅变量模式不画；仅变量模式去掉所有非变量——固定文字与固定条码、线、矩形、图片、表的线框与表头）。偏移须使打印内容仍在安全区内；底图须与预设同长宽比。预览、检查、出具与检查器两种模式都覆盖。
- 上限（B3）：`MaxItems` 64→128、`MaxBandItems` 24→96，元素编号 e1–e128；在最坏模板（128 元素、单带 96）上重测：预览约 52 ms、出具前检查约 6 ms、最宽编号出具约 65 ms（预算 15 s），数字在 docs/CAPACITY.md。
- 样例与目录（B4）：`doctemplates/samples/`（`embed.FS`）；`GET /document-templates/samples`、`GET …/samples/{kind}`、`POST …/samples/{kind}/previews`（用样例自带行、带 PREVIEW 横幅、无数据集）、`POST …/samples/{kind}/copy`（服务端按映射绑定、与保存同样校验并存为新模板第 1 版，未映射的槽位是字面量 `[标签]`）；`GET/PUT /document-templates/{id}/catalog`（领域、流程、收藏、归档、至多 16 个各至多 40 字的集合，`expected_revision` 冲突 409，读取某版本时服务端记 `last_opened`），列表带元数据；每身份模板上限 16→64（`privatedocs.OpenCap`，其余存储仍 16）。本部分只有两个手写夹具，第二部分已以转换出的 37 个样例取代它们。
- 为让共享校验用例两侧一致，`documents.js` 的 2.x 校验器随上限与 `page` 更新（限制、预设、套印），`canvas.js` 按预设画页面尺寸；页面中心 UI 不在本部分。

测试：Go 无环境顶层测试 416 → 443 项通过（跳过 18 → 23，新增 5 项需 Typst，已加入 ci.yml 的三份跳过清单），带 Typst 的 Go 套件 458 项通过、跳过 8 项（需 PostgreSQL / S3 的既有项）；普通与 race（doctemplates、privatedocs、transport、export）都通过；DOM 186 项不变（无 Typst 166 通过、20 跳过，有 Typst 186/186，共享的 2.x 校验用例 323 → 347 条，两侧一致）；检查器对 78 份文档通过（改动前 76 份中的 70 份 PDF 与基线逐字节相同，其余 6 份本来就每次不同：随机记录号的印章与生成时间），golden.json 只新增 22 项。

## S06-B 第二部分：37 个样例的提取、转换与验收（B5–B7）

2026-11-03。原型 `IDP_Reporting_Designer_UIUX_V3.0` 的 37 个模板成为平台的样例包，取代第一部分的两个手写夹具（逐模板的转换记录在 docs/SAMPLES.md）。

- 提取（B5）：`scripts/prototype_templates.mjs <原型文件> <输出目录> <CDP 端口>`（Node 24，无依赖，经 CDP 驱动自己端口的无头 Chrome；原型从本机回环地址临时提供，只在送出的那份副本里加一行把闭包里的四五个函数交出来，不抄原型的 JS）。写出 `doctemplates/samples/prototype/<kind>.json`：最后一版的文档、它绑定的数据集、原型自带的样例行、单据级字段的取值、原型模板中心的默认分类，另有 `inventory.json`。两次提取逐字相同（原型每次启动随机生成的模板编号不写出），提取结果提交入库，原型文件不入库。
- 转换（B6）：`cmd/reporting-samples`（`convert --in … --out … --docs …`，`go generate ./doctemplates` 运行）。确定性；每种节点一条规则（text、field、table、line、rect、image、signature、summary、aggregate、barcode、qr、pageNumber、watermark，加区段、参数、套打）；转换结果必须通过 `doctemplates` 校验才写出，否则整次运行失败、什么都不写。固定版式（两张运单、提单、两张快递面单、四张 WMS 标签）转成**表单**，不放任何占位表：整版放在页眉区段，高度取最低元素的底边，外框在安全区内就原样放在纸上的位置，放不进才对齐安全区角并等比缩小，不再切开；一行一份文档。每个舍去或改写进 `doctemplates/samples/<kind>.record.json` 与 docs/SAMPLES.md。37 个模板共 816 个节点、转成 1197 个元素，没有一个节点整个丢掉；改写 44 个节点（页码、签章、图片、汇总）；字号低于 8 磅的提到 8 磅、颜色/斜体/字距/行距、查询参数、版式参考图、水印等逐条记录。测试断言提交的样例、记录与 docs/SAMPLES.md 等于转换结果、同一输入两次输出逐字相同、每个节点要么有元素要么在记录里。
- 验收（B7）：37 个样例各用自带的行经预览路径渲染、过出具前检查，`scripts/check_pdf_export.py` 加 `--samples doctemplates/samples`：包里每个样例都有文档、页面是它自己的预设、条码与二维码由独立解码器解出；37 份 PDF 与检查报告在 `evidence/s06b/`；渲染时间最重的在 docs/CAPACITY.md（全部 27–61 毫秒）；DOM 测试把 37 个样例各经真实的复制路由绑到当前数据集并在工作室画布里打开。
- 检查器的两个负向对照原来只对线宽 0.25/0.5 与「首个自动换行的文本」成立，对转换出的样例（线宽 0.75/1.5、短标签）无法成立，已改成取首条线的实际线宽、取够长的自动换行文本。
- 表单（2.x 的明细带可以没有）：`bands.detail` 缺省的模板只画页眉、单据头、单据尾与页脚四个带，一页，没有明细行、承前与合计，单据尾紧接单据头；放不下页面按 DOES_NOT_FIT 拒绝而不截断；至少要有一个带，`"detail": null` 与 1.x 都拒绝。分组字段仍按取值选行，没有分组字段时一行一份文档（与 1.x 的行字段相同），`POST /issues` 按 `row_index` 出具（记录带行号、不带分组，`group` 为 400），工作室的正式出具走 1.x 的按行路径。涉及文档模型与校验、版面、预检与出具（同一分组的表单是一份文档）、画布与结构面板（没有明细区，“添加明细表”加回明细带，也可移除明细表变回表单）、控制台校验与共享用例、检查器（没有表就没有表的断言）。
- 预设 `label_150x100`（150 × 100 毫米横向标签，安全区左右 4 毫米、y 12–94，横幅条 4, 3, 142 × 6，两行 6 磅印章）：进预设表、控制台副本、印章宽度证明测试、画布与 docs/API.md；四张 WMS 标签按原纸张转到它上面（安全区高 82 毫米，小于版式的 92 毫米，所以内容等比缩小到约 0.86）。
- 第一部分的夹具测试改用真实样例（`statement` 与 `usps`），测试内容不变。

测试（第二部分）：Go 无环境顶层测试 443 → 471 项通过（跳过 23 → 25，新增的 2 项需 Typst，已加入 ci.yml 的三份跳过清单；含子测试 1086 → 1114），带 Typst 的 Go 套件顶层 458 → 488 项通过、跳过 8 项不变（含子测试 1171 → 1237）；DOM 186 → 188 项（无 Typst 166 → 168 通过、20 跳过；有 Typst 188/188）；检查器对 CI 证据目录（含 37 个样例）全部通过，含负向对照；`gofmt`、`go vet ./...` 干净。

以下为此前阶段历史说明：

# IDP Reporting Platform · S06-B part 3

2026-11-03。模板中心页面、新建模板向导与字段映射对话框（docs/S06B_SCOPE.md B8–B10；区域到函数的地图、偏离清单与决定见 docs/STUDIO.md“模板中心”）：`console/assets/library.js`、`library.css`，控制台导航“模板中心”，工作室的面包屑、字标与左侧栏去往该页；本身份的模板（收藏、最近使用、归档、业务域/流程、集合，均经目录路由与 `expected_revision`）、37 个只读样例（预览、复制到我的模板的字段映射）、向导、导入。嵌入页不受影响。列表路由新增 `name`、`schema_version`、`preset`、`published_version`（不打开模板，不改 `last_opened`）。

测试：DOM 188 → 202（有 Typst 202/202；无 Typst 182 通过、20 跳过），Go 无环境 474 通过、25 跳过（新增 1 项）；真实浏览器脚本 `scripts/browser_library_s06b.mjs` 41/41（证据 `evidence/s06b/library/`）。

# IDP Reporting Platform · S06-A

2026-11-02。文档工作室（docs/S06A_SCOPE.md，docs/STUDIO.md，ADR 0008）：控制台的“文档模板设计器”页换成原型 `IDP_Reporting_Designer_UIUX_V3.0` 的全屏工作室——顶栏（可原地改名的模板名、状态徽标、版本、保存、预览与导出、发布）、五个页签的功能区、竖向工具栏与四个侧栏面板、带上下文条/标尺/区段标签/页码器/缩放的工作区、三个页签的检查器、状态栏，以及版本、发布前确认、帮助、纸张、区段、套打、插入选项、预览与导出、JSON、出具、模板选择、参数抽屉与数据抽屉。行为层不重写：每个工作室控件调用旧按钮调用的函数，或禁用并写明原因；新增会话内的剪贴板、格式刷、锁定与隐藏、网格、吸附开关、缩放、Space 平移，模板新增可选 `name`（≤ 80 字符，1.x 与 2.x，`doctemplates` 与控制台同一规则，共享用例 +10）。CSP 不变，无 `innerHTML`、无内联样式。

测试：DOM 套件 167 → 186 项（无 Typst：148 → 166 通过，跳过 19 → 20，新增的 1 项需 Typst；有 Typst：186/186），Go 套件全过；真实浏览器脚本 `scripts/browser_studio_s06a.mjs`（41 项检查、与原型并排的 7 个状态、1280/768/390 的亮暗截图，在 `evidence/s06a/`）。保真由人对截图判断；偏离清单与理由见 docs/STUDIO.md。不在本片：模板中心页、样本包、向导（S06-B），其余控制台页（S06-C）。

以下为此前阶段历史说明：

# IDP Reporting Platform · S05-A

2026-11-02。生产服务器（docs/S05A_SCOPE.md，docs/DEPLOYMENT.md）：此前仓库里唯一的服务器是 `cmd/reporting-demo`（演示令牌、只连受保护的夹具库、必须带 `-profile contract-simulator`），运维手册的“启动服务器”没有对应命令。本阶段写出生产组合：包 `idp.local/reporting/server` 与命令 `cmd/reporting-server`（`check-config`、`run`、`version`），把严格的配置文件、来自文件与环境的密钥、宿主注册的适配器变成一个运行的服务器。**不含任何真实来源产品的适配器**，也不给出生产准入：`GET /capabilities` 仍是 `production_approved: false`，配置里没有可改它的字段。

配置：一份 JSON（至多 256 KiB），严格——未知字段、大小写不符、重复键（含适配器 `options` 内）、`null`、尾随数据、非 UTF-8 均被拒绝；名字像密钥的字段（`dsn`、`password`、`secret`、`token`、`…key`）被拒绝，密钥只来自环境（`REPORTING_STORE_DSN`、`REPORTING_OBJECT_STORE_ACCESS_KEY`/`_SECRET_KEY`）与文件（游标密钥 1 到 4 个、TLS 私钥：普通文件、属主为进程用户、0600/0400、拒绝符号链接；JWKS 与绑定：不得被组或他人写）。TLS 只允许 1.3；非回环地址不带 TLS 的监听被拒绝，明文 HTTP 只在回环 IP 且显式 `allow_plain_loopback` 时接受。命令不注册任何适配器，`run` 以 `no adapter is registered for integration "X"` 拒绝启动；宿主写三行 `main` 调用 `server.Main(os.Args, registry)`。契约模拟器自报 `IsSimulator()`，除非配置写 `"allow_simulator": true` 否则 `Run` 拒绝，此时 `/capabilities` 的 profile 为 `simulated:<label>`。

启动顺序（每步失败是固定文本、点名步骤、不含任何密钥）：配置 → 密钥 → 签发方 → 适配器 → `OpenProduction`（verify-full、生产画像、台账版本、角色检查）→ 对象存储 `HEAD bucket` → PDF 与私有目录 → 服务 → 工作者与清理 → 监听。`GET /healthz`（200，不触依赖）与 `GET /readyz`（存储可答、台账与画像、角色与行级安全仍合规、对象存储最近一次检查（缓存 10 秒）、未在排空；否则 503 与唯一原因 `store|schema|roles|object_store|draining`）无需认证，带 API 的安全头与 `no-store`，不经控制台静态处理器。日志为每事件一行 JSON，只有计数与代码：启动、重载结果、清理计数、工作者失败代码、排空起止、每个请求一行（方法、路由模式、状态、毫秒、随机请求号；无查询串、正文、头、主体、租户）。

生命周期：SIGHUP（或 `Server.Reload()`）重读 `jwks_file` 与 `bindings_file` 并调用 `Verifier.Replace`，成功则下一次认证起生效并记录受信任的 `kid`，失败保留旧配置并只记录代码；其余配置需重启。SIGTERM/SIGINT：`/readyz` 立即 503 `draining`，监听关闭，在途请求与工作者手中的任务在 `shutdown_grace` 内完成，之后关闭连接池；退出码 0 干净排空、1 拒绝启动、2 排空超时（任务保留租约，租约过期后由别的进程领取，日志记录剩余数）。两个服务器可同连一个数据库，共用任务队列与同一身份的配额。

测试：新增顶层测试 36 项（`server` 包 32 项：配置拒绝与不含密钥、名字像密钥的字段、命令无法触及仅测试用选项、文件权限与符号链接、公共文件、绑定文件、签发方文件文本、启动拒绝逐步与顺序与回收、真实 TLS 与真实 RS256 令牌的全程会话、重载与 SIGHUP、就绪原因与对象存储缓存与探测不排队、排空三种情形、退出码、清理与工作者日志、panic 值不入日志、check-config、命令用法、示例配置、受门控的 `TestServerAgainstPostgres`（含对象存储子测试）；`internal/servertesthost` 1 项：独立进程；`httpapi` 3 项，含数据类别头随档案变化 `TestDataClassFollowsTheProfile` 与默认值测试），含对 goroutine 与在途名额的核算。无环境的 Go 套件 415 项通过（改动前 380）、跳过 18 项（新增 `TestServerAgainstPostgres`，两份跳过清单已更新）；数据库、S3 兼容服务与 Typst 全开 433 项通过、无跳过（改动前 397）；DOM 167 项通过（未改）；普通与 race 都通过。

不在本阶段：任何真实来源产品的适配器、真实身份提供方（发现、紧急撤销）、生产准入、systemd 之外的容器与编排清单、指标与追踪端点、证书自动续期、除签发方文件外的任何热重载。

# IDP Reporting Platform · S04-H

2026-11-02。密钥生命周期（docs/S04H_SCOPE.md，docs/OPERATIONS.md “Keys and credentials (S04-H)”，docs/ACCESS_JWT.md “签名密钥轮换与两个暴露窗口”）：平台持有的每个密钥与信任锚现在都有经过测试证明、有界限、写明的轮换办法；手册只在测试证明时才说“无停机”，其余写窗口或不作此声明。不含自动轮换、KMS/HSM、信封加密、单个令牌的撤销。

游标密钥环：`reporting.Config.CursorKeys`（新钥在前，1 到 4 把，每把至少 32 字节，互不相同），第一把签发，任一把验证；`CursorKey` 仍是单钥环；两者同设、都不设、过短、重复、超过四把在 `NewService` 得 `ErrInvalid`，错误文本不含任何密钥。游标格式不变（无 key id），旧二进制签发的游标在仍含其密钥的环下继续有效；验证对环中每把钥都计算、不提前退出，用 `hmac.Equal`。四步滚动轮换 `[K1]`→`[K1,K2]`→`[K2,K1]`→`[K2]` 在相邻两步的服务器并存时不拒绝任何游标（测试逐对证明），HTTP 层的真实游标跨越“轮换密钥的重启”继续分页；游标的寿命就是其快照的寿命（至多 15 分钟）。demo 的密钥推导不变，新增可选 `REPORTING_DEMO_PREVIOUS_TOKENS`（逗号分隔、新在前、至多 3 个）以排练轮换，启动行只说密钥个数，输出里没有令牌与密钥（以已知令牌运行 demo 并检查其输出）。

签发方密钥：`accessjwt.Verifier.KeyIDs()` 返回已排序的 `kid`（仅 id）；用真实 RS256 令牌证明重叠轮换（旧钥+新钥都接受，再只留新钥：旧钥令牌自下一次认证起 401 `invalid_token`）、验证失败的 `Replace` 什么也不改、已开始的认证在旧配置下完成、未受信 `kid` 被拒且不点名任何密钥。文档写明两个窗口：泄露的签名密钥暴露到 `Replace` 移除它为止（下一次认证起生效，无需重启）；被盗令牌暴露到过期，至多 `MaxTokenLifetime`，没有单个令牌的撤销。demo 不用该验证器，所以没有签发方密钥可记录。

凭据：应用角色、清理角色、迁移角色的数据库口令与对象存储密钥对。数据库：`ALTER ROLE … PASSWORD` 之后，已打开的连接继续工作，用旧口令的新连接失败，`OpenProduction` 用旧口令被拒（文本不含口令、验证串或连接串），用新口令打开；运行中的服务器在会话被断开后无法用旧口令新建连接，其应答不含任何凭据；因此手册给的是**有窗口的**滚动重启，不称无停机。**诚实的限定**：受保护的一次性 PostgreSQL（本地容器与 CI 服务）用 `trust` 认证，不检查口令，所以测试经 `internal/pgauthproxy` 连接——它对服务器自己保存的 SCRAM 验证串做 SCRAM-SHA-256，通过才转发给真实服务器；`ALTER ROLE`、验证串、会话与角色是服务器的，拒绝新连接的决定是代理的，没有对真正强制口令的 PostgreSQL 做过测试。对象存储：一次性服务器只有一对根密钥，无法造第二对，所以“已撤销的密钥”用服务器不接受的密钥代替并如实写明：启动检查失败文本恰为桶名加状态 403，运行中的客户端每种请求都以“存储不可用”失败（API 为 503 与 `Retry-After`），文本不含密钥、端点或服务器的错误码；新旧两对并用未被测试。
Bug 修复（范围内）：连接串无法解析时，连接池库把整个串原样放进错误（口令放错位置时即泄露），`OpenProduction` 与 `reporting-migrate` 因此可能把口令带进错误文本（demo 的 `openFailure` 还会保留 `ErrInvalid` 的文本）；现由 `postgres.ParseDSN` 换成固定文本。

清单与应急顺序：OPERATIONS.md 一张表，每个密钥或信任锚一行（游标密钥环、签发方 JWKS 与其私钥、三个角色的口令、PostgreSQL TLS 信任、对象存储密钥对与 CA、Typst 与字体的钉、demo 令牌——仅演示，绝不是生产凭据），逐项写明保护什么、由谁提供、泄露的后果、轮换步骤与停机（无 / 滚动重启 / 窗口）、如何确认、建议的最长使用期（仅是建议，平台不强制），最后是应急轮换顺序。

测试：新增顶层测试 16 项（游标环 5、HTTP 分页跨重启 1、签发方轮换 6、demo 2、拒绝文本无口令 2），全部不需要数据库或对象存储。无环境的 Go 套件 380 项通过（改动前 364）、跳过 17 项不变；数据库、S3 兼容服务与 Typst 全开 397 项通过、无跳过（改动前 381）；DOM 167 项通过；普通与 race 都通过。需要 PostgreSQL 或对象存储的证明都是既有受门控测试的子测试或其函数，所以 ci.yml 的两份跳过清单不变，CI 的对象存储仍是 versitygw。

不在本阶段：自动轮换或密钥年龄强制、KMS/HSM、信封加密、备份加密密钥、TLS 证书轮换（超出手册步骤）、单个令牌的撤销、动态 JWKS 发现（等真实 IdP）、生产准入。

# IDP Reporting Platform · S04-G

2026-11-02。容量（docs/S04G_SCOPE.md，docs/OPERATIONS.md “Capacity”，docs/CAPACITY.md，docs/API.md “Limits”）：此前热路径缺索引、若干路径无上界，也没有测量。本阶段加索引、给无界路径设上界，并给出可复现的测量工具与有边界的数字。

索引（迁移 009 `capacity_indexes`，台账、校验和、单事务、可回滚，003–008 字节不变，`integration/testdata/catalog_009.txt`）：`jobs (tenant, issuer, subject, created_at DESC)`、`jobs (tenant, issuer, subject) WHERE state IN ('queued','running')`、`assets (tenant, issuer, subject, kind, expires_at)`，以及清理用的 `jobs (source_expires_at) WHERE state IN ('succeeded','failed','cancelled') AND NOT (data ? 'Reduced')`。迁移用 `IF NOT EXISTS`，所以可以先用 `CREATE INDEX CONCURRENTLY` 以同名同定义在迁移前建好（普通 `CREATE INDEX` 在构建期间阻塞该表的写入，OPERATIONS.md 说明）；迁移随后核对：同名但定义不同或失效（并发构建失败遗留）的索引被拒绝。行级安全策略下的执行计划由门控子测试 `TestPostgresMigrations/capacity_plans_*` 以应用角色加身份上下文断言（索引名出现、`jobs`/`assets` 无顺序扫描，不断言时间）；诚实的结果：发行列表对历史短的身份，规划器仍用唯一键索引（其前缀已被语句自己的谓词填满），对历史长的身份才用 `jobs_identity_created`。

上界：请求体按路由设上限（4 KiB / 64 KiB / 1 MiB，超限 413 `REQUEST_TOO_LARGE`，不解析；见 API.md “Limits”）；`httpapi.WithMaxInFlight`（demo `-max-in-flight`，默认 128，1..4096）超限回 503 `{"error":"OVERLOADED"}` 与 `Retry-After: 1`，不做任何工作，槽位在正常、出错、panic、客户端断开时都释放；第四项配额 `queries_in_flight`（默认 4，1..64，demo `-quota-queries`，服务内、按进程、按身份，`POST /queries` 与快照行读取共用）超限回 429 与 `Retry-After: 1`；`OpenProduction` 现在读 DSN 的 `pool_max_conns`（低于 workers+2 或高于 100 拒绝；缺省仍是 8）。控制台把 `OVERLOADED` 与授权来源的 503 分开，接受第四个配额名（仅限精确形状），`capabilities.quotas` 现在恰好四个键。

测量：`scripts/capacity_run.sh`（`internal/capacity`，只在受保护的夹具库里用 SQL 播种并清理）在迁移 009 之前与之后测准入、发行列表、按 id 读取、Claim、一次清理、`verify-objects` 与 1/8/32 并发写入的 p50/p95/p99 与吞吐；`-growth` 打印真实服务写出的行的字节数。数字、机器与它们**不**说明的事（一台主机、无网络、无 TLS、热缓存、合成数据）在 docs/CAPACITY.md；它们不是生产容量规划。

清理修复：测量发现 S04-B 的清理在 200,000 行 `jobs` 上清 2,000 条要 95 秒，主因是行级安全下“产物所指的快照”这一探测无法走主键（谓词里的 `a.data->>'SnapshotID'` 不是 leakproof），退化成对每个候选整表位图扫描；探测改为对普通列比较（`LATERAL … OFFSET 0`），并加上 `jobs_reducible` 部分索引，同样的行现在约 2 秒（docs/CAPACITY.md）。清理的规则与 `countSweep` 的计数不变。

不在本阶段：清理或归档 `jobs`、分区、只读副本、读路径的时间限流、自动伸缩、生产容量声明、密钥生命周期。

# IDP Reporting Platform · S04-F

2026-11-02。备份与恢复（docs/S04F_SCOPE.md，docs/OPERATIONS.md “Backup and restore (S04-F)”）：自 S04-E 起平台状态分在两处——PostgreSQL 存储（任务、快照、产物行、出具记录、各账本）与对象存储里产物行所指的文件；只备份其一不是备份平台。本阶段定义恢复后必须成立的不变量、给出只读的核对命令、用测试与一次真实的转储恢复演练证明各种情形、写出运维手册。**平台不带备份工具**：`pg_dump`/`pg_basebackup`/WAL 归档与桶的版本化或复制是运维方的，手册说明怎么用、怎么知道成功。

不变量（手册 “What must be true after a restore”）：模式、画像与校验和是二进制的（`reporting-migrate status` 干净）；角色与策略就位（重跑 `roles` 幂等，行级安全与两条策略在，`status` 的角色检查如实说）；**产物行绝不承诺不存在的文件**（有 `Object` 的行必有该对象，长度与 SHA-256 相符；没有行指向的对象是孤儿，无害，由清理的对账在宽限后删除）；出具记录是来源产品所确认之物在平台的副本，早于产品确认的恢复会留下平台不认识的编号，平台从不凭空造号或复用编号，手册给出列出已恢复出具任务的查询以及与产品按任务 id 对账的做法；排队或执行中的任务在租约（数据库时钟）过期后被重新领取，出具任务以同一任务 id 重跑、产品幂等应答，不会重复出具。恢复顺序：数据库、角色、桶、`status`、`verify-objects --deep`，然后才启动服务；桶的副本应取在转储完成之后。

命令：`reporting-migrate verify-objects [--deep] [--ids] --object-store-…`（`REPORTING_STORE_DSN`，对象存储参数与 `sweep` 相同，密钥只从环境读取、给成参数即拒绝；没有对象存储参数则拒绝）。检查在 `storage/postgres.VerifyObjects`：以只读事务（工作者范围，只需对 `assets` 的 SELECT）按 id 分页读出有 `Object` 的产物行，按键序分页列出桶的 `artifacts/`，两路归并，内存只与一页相关。默认只打印计数：`rows`（有对象的行）、`objects`、`missing`（行有对象无）、`orphans`（对象无行，仅提示）、`invalid`（行没有按要求说明文件在哪、或载荷过不了摘要）、`mismatched`（仅 `--deep`：对象的长度或 SHA-256 不是行的；`--deep` 把每个对象读一遍，至多 8 MiB，是恢复后的慢检查，不是探针）；`missing`、`mismatched`、`invalid` 全为零退出 0，否则退出非零。**默认不打印任何 id、键或值**；`--ids` 另行打印失败行的产物 id（文档注明是标识符，默认关）。它只用现有客户端的 `List` 与 `Get`，没有新增对象存储操作，也从不写库、写桶（源码测试固定其只用只读事务，测试以仅有 `assets` 的 SELECT 的角色运行它）。

测试（需要 PostgreSQL 或对象存储的都是既有受门控测试的子测试，新增的顶层测试都不需要二者，所以两份跳过名单不变）：`TestDurablePostgresContract/object_storage/{memory_objects,s3}/restore`（桶比库新：孤儿被计数、退出 0、被删行的下载为 404、对账在宽限后只删孤儿；库比桶新：缺失被计数并退出非零、`--ids` 恰好列出缺失行、下载是 409 INTEGRITY_FAILURE 而不是错误的文件、放回相同字节后干净且可下载、放回不同字节（同长或不同长）则 `--deep` 报 mismatched、下载 409、浅检查看不出；行本身残缺为 invalid；验证不改库不改桶；两千三百行与对象交错分页；对象存储不可达是错误而不是计数）、`TestDurablePostgresContract/restore`（恢复时在执行的任务租约未过不被领取、过期后被领取并以第 2 代完成、旧领取者被围栏；幂等键对已恢复的任务应答该任务、对不在的任务重试则准入新任务；配额按恢复的行计数）、`TestDurablePostgresIssues/restore_…`（产品在备份之后已确认编号：任务以同一 id 重跑，产品与平台各一条记录、一个编号，下一张是下一个编号；手册的查询列出已恢复的出具任务）、`TestPostgresMigrations/row_level_security/a_restore_…`（倒回后 `status` 干净、两种角色检查通过、`OpenProduction` 可开、两张表的行级安全与策略在、各身份只见自己的行；以应用角色、清理角色与仅有 SELECT 的角色运行核对）、`TestMigrateCommand`（命令的计数、退出状态、默认无 id、`--ids`、`--deep`、桶不存在、不写库）与参数测试（无对象存储参数拒绝、密钥作参数拒绝）。恢复由倒回模拟（受保护夹具库不能删了重建）：备份是两张表某一刻的内容，恢复是整表替换，对象由测试保留、删除或替换。

演练（本地证据，不进 CI）：`scripts/restore_drill.sh`（bash；只需 docker、`docker exec` 里的 `pg_dump`/`pg_restore`、与它自己构建的两个二进制 `reporting-migrate` 和 `internal/restoredrill`）。它在一次性 PostgreSQL 容器里自建并在结束时删除 `reporting_restore_s04f_*` 数据库与两个角色，在一次性 S3 兼容服务器上只用自己的桶 `reporting-restore-s04f`，从不碰夹具库与 55432 端口，结束时按 pid 停掉自己启动的所有进程。一次运行（2026-11-02，PostgreSQL 16.14，MinIO RELEASE.2025-09-07T16-13-09Z，一台机器）：以生产路径（应用角色）跑演示会话——导出、出具、下载，对象都在桶里；`pg_dump -Fc`、经客户端把桶里的对象拷到一边；再做一轮工作；`pg_restore --no-owner --no-acl` 进新库，重跑 `roles`，`status` 干净，行级安全与两条策略在。**桶比库新**：4 个对象、2 个孤儿、`verify-objects --deep` 退出 0，旧的 2 个产物下载 200 且字节与会话所见相同，丢失的 2 个下载 404；**桶恢复到转储时刻**：2 个对象、无孤儿、退出 0，同样 200/404；**库比桶新**（原库配恢复后的桶）：2 个缺失、退出 1，`--ids` 恰好列出这 2 个，下载 409。**kill -9**：出具任务停在渲染里（产品已预留编号、尚未确认），杀掉服务进程，任务仍是第 1 代 running；重启后约 30 秒（租约过期后）由第 2 代领取完成，库里一条出具记录，产品对该任务 id 只有一个已确认的编号，随后 `verify-objects --deep` 干净。演练还显示：备份之后产品确认的编号（AR-000002）对应的任务恢复后的平台根本没有，这正是手册要求运维方与产品对账的情形；平台随后给的下一个编号是 AR-000003，没有复用。完整记录由运行者保存（不入库）；它是一台机器上一次运行的证据，不是资格。

未验：时间点恢复（PITR）、跨区域复制、加密备份、用云厂商快照恢复、AWS S3 本身、私有目录（版面、看板、模板、背景、图片）的备份——它们是磁盘上的文件，不在数据库里，需要文件级备份，手册已写明。CI 的 PostgreSQL 任务仍用 versitygw 对象存储（MinIO 的镜像已不能公开拉取），新测试只用既有客户端操作。

不在本阶段：备份调度或备份工具、时间点恢复资格、跨区域复制、备份加密、私有目录的备份、容量、密钥生命周期。

# IDP Reporting Platform · S04-E

2026-11-01。产物的对象存储（ADR 0007 docs/ADR_0007_OBJECT_STORAGE.md，docs/S04E_SCOPE.md，docs/OPERATIONS.md “Object storage”，docs/API.md “Object storage”）：此前导出的 XLSX/PDF 与出具的 PDF 以 base64 存在 `assets` 行的 JSON 载荷里（最大 8 MiB），数据库的备份、复制与清理都背着文件；本阶段让文件离开数据库，进入 S3 兼容的对象存储，行只留元数据与摘要。配置了对象存储时，新产物的行带 `Object`（`artifacts/<产物 id>`，id 随机，键不透露任何信息）、`Size`（字节数）与原有的 `SHA256`，不再带 `Content`，载荷摘要覆盖这些元数据；此前存下的产物保持内联、照旧提供，直到过期被清理，不做批量迁移。快照（业务行）、任务、出具记录、配额（一个产物算一个存储结果）、保留规则与行级安全都不变；内存存储仍内联（它是明确的临时存储，demo 拒绝 `-memory` 与对象存储参数同用）；不配置对象存储时一切照旧。
客户端（`storage/objects`，只用标准库的 net/http 与 crypto，`go.mod` 没有新模块）：`objects.S3` 实现 `reporting.ObjectStore` 的五个操作——PUT（`x-amz-content-sha256` 为请求体的 SHA-256）、GET（先按行里的 SHA-256 与长度核对再返回，至多读长度加一字节）、DELETE、ListObjectsV2（按前缀，续页令牌）与 HEAD bucket——AWS Signature Version 4，路径式，一个桶。签名用 AWS 文档的四个已发布示例（GET Object、PUT Object 含 `$` 键、GET Bucket (List Objects) 含查询串排序、GET Bucket lifecycle）逐项核对规范请求、待签字符串与签名；键与查询串的规范化另有表驱动测试，并用一个按规范独立重算签名的服务器验证客户端发出的就是签名的。每个请求 10 秒超时，网络错误（无应答）时对幂等请求至多重试一次（有应答、超时、取消都不重试），不跟随重定向；端点只能是 `https://`（系统根证书，或 `-object-store-ca` 命名的 CA 独自验证）或回环 IP 上的 `http://` 且须显式 `-object-store-loopback-http`，其余在构造时拒绝；任何错误都不含响应体、键、端点或凭据（意外状态是 `ErrUnavailable`，只带状态码），客户端值打印时不含密钥。`objects.Memory` 是同契约的 map 实现，供不需要服务器的测试，并可注入故障、篡改与老化。
顺序与失败：worker 先 PUT 对象、再在完成任务的同一事务里发布行（`Publish` 对有对象存储的存储拒绝仍带内容的产物，对没有的拒绝带 `Object` 的产物，两者配置不一致时任务以 INTEGRITY_FAILURE 失败、什么也不发布）；出具任务把 PDF 在通知来源产品“文件已存在”（`ConfirmIssue`）之前放进对象存储，所以 PUT 失败时产品只有会被作废的预留、从未确认过一个平台没留住的文件；PUT 失败的导出或出具任务以 EXECUTION_FAILED 失败，什么也不发布（出具任务照 S03-H 作废），错误文本不含存储的任何东西；PUT 之后发布失败，对象成为孤儿，由对账在宽限后删除。读取：先读行（来源授权、过期、身份、出具交叉核对），再 GET，再把字节与行的 SHA-256 和长度核对一次（不信任任何 ObjectStore 实现）；不一致或对象缺失是 409 INTEGRITY_FAILURE，存储不可达是 503（`AUTHORITY_UNAVAILABLE`，已有映射）加 `Retry-After: 10`，正文只有代码；任务轮询只读行、不读对象，所以存储宕机时任务视图照常，控制台对下载的 503 与其他 503 一样处理（只加了一条 DOM 测试）。能力文档不说文件在哪里。
清理与对账：清理删行的批次提交之后，才删这些行所指的对象（键从该批的 `RETURNING` 读出，且必须恰是该行自己 id 的键，否则只删行、不动键）；删除失败只计数、留给对账。对账是清理的第四步：分页列出 `artifacts/`，删除键是规范产物 id、早于宽限、且 `assets` 里任何一行（任何种类、任何身份，一次 `= ANY($1)` 查询，在工作范围事务内）都没有提到的对象；别人的键、年轻的对象、行所提到的对象一律不动。对象的年龄由平台主机时钟对照存储盖的时间判断（这是清理里唯一用主机时钟的地方，因为盖章的是对象存储的时钟，行的过期仍是数据库的；源码测试守住），且不小于 15 分钟（`MinOrphanAge`），免得把慢 worker 刚放好、行即将发布的对象当孤儿删掉。账本 `sweeps` 新增 `objects`、`orphans` 两列（迁移 008 `object_storage`，默认 0；`008 down` 在账本有删除对象的记录或有以对象存文件的产物时拒绝，除非 `--accept-data-loss`；目录 `catalog_008.txt`，003 至 007 逐字节不变）。`reporting-migrate sweep` 带同样的对象存储参数时在删行之后删对象并对账，先确认桶可达；不带时只删行、打印对象等待对账、退出码 0。demo 的清理循环经所开的存储做同样的事并只记计数。
配置：`-object-store-endpoint`、`-object-store-bucket`、`-object-store-region`（默认 `us-east-1`）、`-object-store-ca`、`-object-store-loopback-http`（demo 与 `reporting-migrate sweep` 相同）；访问密钥与私密密钥只从环境 `REPORTING_OBJECT_STORE_ACCESS_KEY`、`REPORTING_OBJECT_STORE_SECRET_KEY` 读取，给其中任何一个参数都被拒绝（拒绝文本不复述其后的值）；启动时 HEAD bucket 必须通过，否则服务器不启动，文本只说桶名与状态；平台从不创建或配置桶、生命周期规则或加密。OPERATIONS.md 给出凭据所需的最小策略（对该桶的 PutObject、GetObject、DeleteObject、ListBucket）、核对办法（有 `Object` 的行数对桶内对象数，差为孤儿）、对账与时钟、成本与未覆盖项。
测试：SigV4 示例与规范化、构造拒绝、Get 的长度与摘要核对（含分块无 Content-Length 的响应）、错误文本无响应体/键/端点/凭据、重试与超时与重定向、https 与命名 CA、列表解析、`CreateBucketForTests` 只许测试调用（源码测试）；同一份对象存储契约（`TestMemoryObjectContract`，PostgreSQL 的 `TestDurablePostgresContract/object_storage/{memory_objects,minio}`）在两种存储与两种对象存储上跑：文件是对象而行只有元数据、先 PUT 后发布行与先删行后删对象（注入的顺序观察）、PUT 失败的导出与出具（出具的产品只有被作废的预留）、PUT 与发布之间的注入故障留下孤儿而对账在宽限后删除、下载逐字节核对（同长度异摘要、更短、更长、别的文件、缺失均 409）、说谎的存储不能让服务提供错误文件、不可达的存储 503 而任务视图不依赖它、清理删对象并计数（含试运行）、删除失败被计数且由对账收尾、不带对象存储的清理留下对象、对账保留年轻的/行所提到的/不是我们的键且不删年龄不足下限的、此前内联的产物照旧提供与清理；PostgreSQL 独有的：Publish 接受什么、行必须恰好说一次文件在哪里并只提自己的对象、账本只有计数、被篡改指向别人对象的行不让清理删别人的对象、服务与存储配置不一致时失败得安全、清理角色在行级安全下的对账能看到所有身份所有种类的行；迁移 008 往返、目录与拒绝；HTTP（503 与 `Retry-After: 10`、409、轮询、能力文档、来源产品自己的 503 无 `Retry-After`）；命令（凭据参数被拒、端点缺桶、桶不可达在删行之前停止、带与不带参数的清理对真实 S3 兼容服务）；demo 启动检查与清理循环日志；DOM 一条。S3 兼容服务部分由 `REPORTING_OBJECT_STORE_TEST_ENDPOINT`/`_ACCESS_KEY`/`_SECRET_KEY` 开启（`REPORTING_OBJECT_STORE_REQUIRE=1` 时缺失即失败），CI 的 postgres 作业在测试前以摘要固定的镜像启动一次性 versitygw（Apache-2.0 的 S3 网关；MinIO 的镜像在 quay.io 与 Docker Hub 都已不再公开可拉取，本机资格测试用的是早已存在的 MinIO 镜像；服务容器无法传 `posix /data` 参数，所以是该作业里的一个 `docker run` 步骤）。Go 无环境 347 项顶层测试通过（改动前 316）、跳过 17 项（新增 `TestS3Objects`，已加入 ci.yml 两份跳过清单），数据库、S3 兼容服务与 Typst 全开 364 项通过、无跳过（改动前 332），普通与 race 都通过；DOM 165 项（改动前 164；无 Typst 146 项通过、19 项跳过，含 Typst 全部通过）。AWS S3 本身没有测试资格，OPERATIONS.md 如实列出。
不在本阶段：迁移已有的内联产物、对象存储中的快照、服务端加密、桶的生命周期规则、分段上传、AWS S3 的测试资格、备份、容量、密钥生命周期。

# IDP Reporting Platform · S04-D

2026-11-01。配额（docs/S04D_SCOPE.md，docs/OPERATIONS.md “Quotas”，docs/API.md “Quotas”）：S03-H 审查的 N10 指出准入从不限量——任何通过准入的主体都能无限排队任务、把结果留到过期、想多快提交就多快。现在每个身份（租户、签发方、主体；不含 integration）在准入处受三项上限约束，两种存储一致。
三项配额与默认值（可在界内修改，0 为默认）：`jobs_in_flight`（排队与执行中的任务，默认 8，1..1000）、`stored_results`（按存储时钟尚未过期的快照与产物，默认 64，1..100000；出具记录永不计，已过期的等清理、不计）、`admissions_per_hour`（按存储时钟最近一小时内准入的任务，默认 120，1..100000）。`reporting.Quotas` 在 `reporting.Config.Quotas`，`NewService` 对越界值返回 `ErrInvalid`；demo 有 `-quota-jobs`、`-quota-results`、`-quota-admissions`，同界，越界则启动失败。
执行：`Repository.Admit(ctx, job, Quotas)` 在准入事务内、幂等重放判定之后、插入之前计数，所以已有幂等键的重放永不被配额拒绝，被拒的请求什么也不留下。PostgreSQL 先取按身份（租户、签发方、主体，各带长度前缀）散列的事务级咨询锁 `pg_advisory_xact_lock(hashtextextended(key, 0))`，同一身份的并发准入排队、不同身份互不等待，锁随事务结束；计数是三条带身份谓词的语句，时间只用数据库的 `clock_timestamp()`；内存存储在互斥锁下按同样规则计数。拒绝是 `reporting.ErrQuota`（`*QuotaError`，文本 “quota exceeded: <名称>”，不含数量或他人信息）。
调用方所见：三条提交路由（`POST /snapshot-jobs`、`/exports`、`/issues`）回 429 `{"error":"QUOTA_EXCEEDED","quota":…}` 加 `Retry-After: 60`，不准入、无 Location；`GET /capabilities` 的 `quotas` 给出生效数字；控制台校验后用能力里的数字显示配额提示、保持页面原样，其他形状按无法验证的响应处理；嵌入页同样，且不向父页面发送（`reporting:error` 仍是封闭列表）。
测试：两种存储共用的配额契约（`TestMemoryQuotaContract`，PostgreSQL 的 `TestDurablePostgresContract/quotas`：各配额在限额与超一；小时与过期按存储时钟；取消、失败、成功释放名额；出具记录与过期结果不计；已有键的重放在限额时成功、改了请求仍是冲突；同租户另一身份不受影响；每种任务都计；拒绝不准入；存储自身的默认与界）；PostgreSQL 并行准入（同一身份 32 个并行、限额 K 恰好准入 K；以 `pg_locks` 证明锁按身份、他人不等、事务结束即释放）与源码测试（只用数据库时钟、先锁后计数）；HTTP（三条路由的 429 形状与头、重放、能力）；DOM（每项配额的提示与数字、畸形体、能力缺失、导出被拒、真实服务限额为 1、嵌入页不上报）；`NewService` 与 demo 的越界拒绝。Go 无环境 316 项顶层测试通过（改动前 306）、跳过清单与 CI 一致，数据库与 Typst 全开 332 项通过、无跳过，普通与 race 都通过；DOM 164 项（改动前 158；无 Typst 145 项通过、19 项跳过，含 Typst 164 项全部通过；两个 DOM 夹具的 demo 以 `-quota-jobs 1000 -quota-results 100000 -quota-admissions 100000` 启动，因为整套测试共用一个身份）。
不在本阶段：`POST /queries` 与读取的限流（那里没有准入；留给后续容量阶段）、按租户或按 integration 的配额、按字节的配额、与来源产品协商配额、对象存储、备份、容量、密钥生命周期。

# IDP Reporting Platform · S04-C

2026-11-01。行级安全（docs/S04C_SCOPE.md，docs/OPERATIONS.md “Row-level security”）：此前租户隔离只靠服务——每条存储查询都把所有者（租户、签发方、主体）写进谓词，应用角色对所有租户是同一个角色；本阶段让数据库自己拒绝：没说明自己在处理谁的行的连接，看不到、也写不进任何业务行。这是纵深防御，防的是平台自己的错误（将来某条查询漏了谓词、某个连接忘了上下文），不防已被攻破的服务进程（它自己能设任何上下文；那一层由 S04-A 的角色与真实身份提供方负责）。没有上下文，就没有行。
策略（迁移 007 `row_level_security`）：`jobs` 与 `assets` 启用（ENABLE，不 FORCE：属主迁移角色与超级用户绕过，迁移及其数据检查照旧）行级安全，各有两条宽容策略，对所有命令、对 PUBLIC，USING 与 WITH CHECK 相同：`identity`（`tenant`、`issuer`、`subject` 三列分别等于 `nullif(current_setting('reporting.tenant', true), '')` 等三个设置）与 `worker`（`current_setting('reporting.scope', true) = 'worker'`）。设置缺失或为空串，比较为 NULL：不可见、不可写（池里的连接在设过事务局部设置之后显示空串而不是 NULL，`nullif` 就为此）；写入的行若属于别的身份，由数据库以 SQLSTATE 42501 “new row violates row-level security policy” 拒绝，而不是由 Go。账本（`schema_migrations`、`sweeps`）、`profile`、`issue_numbers` 不含业务行，不设策略。`007 down` 删策略并关闭行级安全，不动任何行。
存储端（`storage/postgres/context.go`）：身份范围的每个调用（Admit、Job、Cancel、Snapshot、Artifact、Issue、Issues）在一个事务里先 `set_config('reporting.tenant'|'issuer'|'subject', $n, true)`，再执行原语句；工作进程的调用（Claim、Publish、Fail、ResolveFailure）与清理的每一批（含试运行）同样设 `reporting.scope = 'worker'`。值一律是参数，上下文只在事务内有效，事务结束即消失，池里的连接不会把它带进下一次调用；包内不出现 `set_config(..., false)`、`SET`、`SET SESSION`（源码测试守住，并要求存储的每条语句都经上下文事务）。身份谓词仍在每条查询里（服务自己的隔离），策略是数据库的；两者各自成立。内存存储不变；夹具路径（超级用户连接）绕过策略，OPERATIONS.md 写明这一点，既有套件原样通过（只有手工拼出旧版本库的几处夹具多删了新增的策略）。
角色与启动：应用角色与清理角色都受策略约束（二者都没有 BYPASSRLS，S04-A 已拒绝该属性）；`roles` 不需新增授权。对版本不低于 7 的库，`RoleProblems` 与 `SweeperProblems` 新增：`jobs`、`assets` 未启用行级安全，或两条策略缺失、限定条件被改、命令或角色不是“全部命令、PUBLIC”、宽容性被改，或表上多出别的策略，都报出带表名的问题；`OpenProduction` 因此拒绝启动，`reporting-migrate status` 打印同样的问题（管理员的 `DISABLE ROW LEVEL SECURITY` 或删策略不动账本，只靠账本看不出）。
测试（`TestPostgresMigrations/row_level_security`，自建迁移、应用、清理三个角色，经生产路径，同租户两个身份 A、B 与另一租户的 C，各经服务做出任务、快照、产物与出具记录）：故意犯错——去掉 `Job` 与 asset 读取语句里的身份谓词，在 A 的上下文下只返回 A 的行，同一语句以超级用户执行则返回所有人的行（策略缺失时失败的就是它）；无上下文（什么都没设、设为空串、只设一部分、别的范围）两个角色的 SELECT 都无行，INSERT 被 42501 拒绝，UPDATE 与 DELETE 不触及任何行，数据库内容摘要不变；A 的上下文下只见 A 的行（服务与裸 SQL），把行写成 B 或另一租户的（INSERT 或 UPDATE）被拒绝，B 的行读不到、改不了、取消不了、清理角色也删不掉，B 以 A 的任务、快照、产物、出具记录调用服务仍是“不存在”；工作范围：Claim 按受理顺序领取所有身份的任务，Publish 把资产写在任务所属身份名下，Fail 与 ResolveFailure 作用于任何身份的任务，清理跨身份删除而出具记录保留，没有范围设置的工作事务什么也领不到；调用之间不泄漏：单连接池里每一种调用与清理之后，探针看到的上下文设置不是值（NULL 或空串）；迁移 007 的往返与目录（`testdata/catalog_007.txt`，004、005、006 的目录逐字节不变，因为目录只在有标志或策略时才打印行级安全）、从有数据的版本 6 升级、`down --to 6`；关闭行级安全、删除或改动或增添策略都使 `OpenProduction` 拒绝（问题带表名）、`status` 打印、恢复后重新可开。Go 无环境 306 项顶层测试通过（改动前 305；新增源码测试一项，其余是 PostgreSQL 资格测试的子测试，跳过清单与 CI 一致，CI 无需改动），普通与 race 都通过；数据库与 Typst 全开 322 项通过、无跳过，PostgreSQL 全量普通与 race 都通过；DOM 与上一阶段相同（158 项，本阶段未改控制台）。
不在本阶段：每租户一个角色或连接、账本上的策略、产物对象存储、配额、备份与恢复、容量、密钥生命周期。

# IDP Reporting Platform · S04-B

2026-11-01。数据保留与清理（docs/S04B_SCOPE.md，docs/OPERATIONS.md “Retention”）：此前平台存下的东西从不删除——过期的冻结快照仍保留业务数据行，过期的导出文件与出具 PDF 仍保留文件，终态任务仍保留全部冻结内容；本阶段按来源授权的生命周期删除过期结果、保留必须保留的，并对两者都给出测试证明。
保留规则（每条都写在执行删除的那条 SQL 里，并有一旦违反即失败的测试）：快照（业务行）在其过期时间（来源读取的过期时间，下载与翻页一直据此拒绝）加 24 小时宽限期之后删除，以数据库时钟（`clock_timestamp()`）为准，与平台主机时钟无关；产物（XLSX、PDF、出具 PDF）同规则删除，且绝不先于其快照；出具记录永不删除（ADR 0003 的永久记录，`expires_at` 为 NULL，删除语句按 kind 限定）；无法读出过期时间的行不删；排队中或执行中的任务引用的快照与产物不删；任务行永远保留（状态、错误码、ID、时间：发生过什么的审计），终态任务在其过期加宽限之后、且快照与产物都已不在时，载荷缩减为视图与审计所需（去掉冻结查询、来源读取与列定义，保留请求哈希、ID、格式、出具任务的冻结模板与行或分组，标记 `Reduced` 并重算摘要）。磁盘上的私有配置（列布局、看板、模板、底稿、图片）不在清理范围。
清理过程：先产物、再快照、再任务载荷，每批最多 `--batch` 行（默认 500，上限 10000），每批一个事务，`FOR UPDATE SKIP LOCKED` 加 10 秒锁超时：失败的批次不改任何行，并发清理各取各的行，工作进程、下载或取消正持有的行留给下一次，不取迁移的咨询锁。宽限期（`--grace`，默认 24 小时，上限十年）是保留期：零视为默认（命令行直接拒绝），负数或超限拒绝，所以手误只会多留、不会早删。清理前先确认账本在二进制的版本且连接角色具备所需权限，只返回计数，可重复执行。
运行方式：`reporting-migrate sweep [--grace D] [--batch N] [--dry-run]`（在 `REPORTING_STORE_DSN` 上，以清理角色或迁移角色运行，打印计数并以退出码报告），demo 的工作循环每 `-sweep-interval`（默认 10 分钟，0 关闭，`-pause-workers` 时不跑）用默认宽限与批量清理一次，只记录计数。每次清理（含试运行与中途停止的）在账本表 `sweeps`（迁移 006）写一行：时间、是否试运行、是否完成、宽限、批量与四个计数，不含任何业务资源 ID 或值；`006 down` 在账本有行时拒绝，除非 `--accept-data-loss`。
权限决定：应用角色仍然没有 DELETE。新增可选的第三个角色（`reporting-migrate roles --sweep-role NAME`）：USAGE、全部表 SELECT、`assets` 的 UPDATE 与 DELETE（UPDATE 是 PostgreSQL 行锁所需）、`jobs` 的 UPDATE、`sweeps` 的 INSERT，其余规则与应用角色相同（无 TRUNCATE、DDL、所有权、属性、成员关系），是唯一会删除的角色；没有它时由迁移角色执行清理。理由：服务进程被攻破也删不了数据，删除权只在定时任务所用的角色上，且该主机不必持有迁移角色的凭据；`status` 同时按两套规则报告连接角色（`role:` 与 `sweeper:`），`GrantSweeper` 与 `SweeperProblems` 有角色资格测试。
迁移 005：`assets.expires_at timestamptz` 与索引，从载荷的 `ExpiresAt` 回填（截到微秒，与驱动写入一致；摘要只覆盖载荷，不重签），无法读出的留 NULL（永不删除），该步永不拒绝；`005 down` 只删列与索引，载荷仍有过期时间，无损。存储每次读快照与产物都核对该列与载荷一致（出具记录必须为 NULL），两者不会悄悄分歧。
视图（docs/API.md “Retention”）：已成功任务的来源读取过期后，`GET /jobs/{id}` 不再向来源重新授权（没有结果可授权），回 200 与 `expired: true`、原状态与原 ID，出具任务带记录里的编号；`GET /issues` 与 `GET /issues/{id}` 带 `expired: true`，记录不变；宽限期内行与文件照旧 409，清理后 `GET /snapshots/{id}/rows`、`GET /artifacts/{id}/content`、再次 `POST /exports`/`POST /issues` 为 404，同幂等键重放为 403（与过期任务一贯相同），取消原样返回；绝无 500，绝无清理造成的完整性失败。控制台在原“查看结果”“下载”处显示“已过期并清除”，只保留“刷新”，出具记录列表同样，编号、摘要、页数与确认信息照常；`expired` 只能为 `true` 且只在已成功的任务或记录上，否则按无法验证的响应处理。
测试：两种存储共用的保留契约（每种资源在过期前、过期后宽限内、宽限后；存储时钟优先于主机时钟；出具记录永不删除；排队与执行中任务引用的不删；试运行不改任何行且计数与分批执行一致；并发清理每行只删一次；清理期间工作进程发布不丢数据；清理后的视图），PostgreSQL 独有的（过期列随资源写入并在读取时核对；缺失过期与损坏载荷不动且清理继续；失败批次不改行且账本记录进度；账本只有计数与时间；不取迁移锁且绕过被事务持有的行；产物绝不先于快照；默认批量约束每个事务；选项手误只会多留），迁移 005/006 在迁移资格测试里回填、往返与拒绝（testdata/catalog_004/005/006），`reporting-migrate sweep` 与 `roles --sweep-role` 的命令测试，HTTP 的清理后视图，demo 清理循环只记计数，`sweep.go` 源码不读主机时钟的测试，DOM 的两条标记测试。Go 无环境 305 项顶层测试通过（改动前 296；18 个包），跳过清单与 CI 一致，普通与 race 都通过；数据库与 Typst 全开 321 项通过、无跳过，PostgreSQL 全量普通与 race 都通过；DOM 158 项（改动前 156；无 Typst 139 项通过、19 项跳过，含 Typst 158 项全部通过）；`reporting-migrate status`、`sweep --dry-run` 与 `sweep` 在 demo 会话之后的测试库上的运行记录（读取过期前计数为零，过期加宽限后删除 1 个产物、1 个快照并缩减 2 个任务载荷，再次运行计数为零，账本逐次记录）为本地证据，不在仓库中。
不在本阶段：产物的对象存储、配额、行级安全、备份与恢复、删除私有配置存储、法律保留（如有产品需要再议）。

# IDP Reporting Platform · S03-N

2026-10-29。嵌入（ADR 0006，docs/ADR_0006_EMBEDDING.md，docs/S03N_SCOPE.md，docs/EMBEDDING.md）：产品可以把平台的报表查看器、看板与文档设计器放进自己页面的 `<iframe>`，为自己已登录的用户展示，用户无需打开控制台。独立控制台的安全姿态不变。
入口与页面：`GET /embed/report`（`dataset`）、`/embed/dashboard`（`dashboard`）、`/embed/document-designer`（`template`，可选 `version`）都返回同一个嵌入页面（`console/assets/embed.html`），查询里固定有 `integration` 与 `parent`（产品页面的精确来源），别无其他参数。页面运行控制台自己的脚本（看板、画布、文档与 app.js，增加 `embed.js`），三个视图的区段与控制台的逐字节相同（测试比较）：只显示地址所指的那一个视图，没有导航、令牌表单与退出；查询面板随看板与设计器视图显示在上方，用户仍在框架里给出公司范围与数据时点；地址所指的数据集被选中，已保存的看板或模板先读一次以得知它绑定的数据集，授权结果出现后才打开保存的版本（不属于该身份、绑定了别的 integration 或不存在，同为 `RESOURCE_UNAVAILABLE`）。其余（授权、拒绝、摘要校验下载、画布、预览、出具）与控制台完全相同。
谁能嵌入：每个 integration 在服务端可信配置里有一份精确来源名单（demo 用可重复的 `-embed-origin integration=origin`；服务用 `embedding.New(map, Options)` 交给 `console.HandlerWithEmbedding` 与 `httpapi.WithEmbedding`）；来源只能是 `https://host[:port]` 的精确序列化形式（小写、无默认端口、无路径、查询、片段、用户信息、通配符或列表），`http://` 只限回环 IP 且仅 demo 允许；启动时校验，格式错误、重复或不存在的 integration 使启动失败。integration 未知、parent 缺失、重复、格式不对或不在名单上，一律 403；名单内来源但资源参数不对为 400；两者都带 `X-Frame-Options: DENY` 与 `frame-ancestors 'none'` 且无页面。名单内来源得到 200，策略与控制台相同，仅 `frame-ancestors` 换成那一个来源，没有 `X-Frame-Options`。控制台页面、每个静态文件、重定向、404、API 的每个路由（`secured` 现在给每个 API 答复加上 `X-Frame-Options: DENY` 与 `frame-ancestors 'none'`，图片内容路由自带的策略也含 `frame-ancestors 'none'`）一律不可嵌入；`GET /capabilities` 新增 `embedding: true|false`（调用者的任一 integration 已配置，绝不含来源）。
令牌：框架页启动时不持有任何凭据，向 `window.parent` 发 `reporting:ready`，目标来源是服务时的 parent 来源，从不是 `"*"`；只在 `event.source === window.parent` 且 `event.origin` 恰为该来源时才收 `reporting:token`，令牌只在内存里、只作为 Bearer 头发往本站（`credentials: 'omit'`），不进 URL、Cookie、任何存储、DOM 或消息。401 时向父页面发一次 `reporting:token-request` 并等待（10 秒）一个不同的令牌，只重试一次；第二次 401 或超时即控制台的退出状态（数据清空、错误码 `UNAUTHENTICATED`），之后父页面推送令牌或点“重新请求令牌”可重新开始；新令牌先经 `/session` 核对为同一身份（租户、签发方、主体）才替换，另一身份的令牌使页面整体重来，不留前一个用户的任何内容；JWT 令牌在声明的 `exp` 前一分钟主动再要一次（至少相隔 15 秒）。未被嵌入（`window.parent === window`）或地址不是服务器会返回的形式的页面不启动、不监听。
消息（均带 `v: 1`，其他一律忽略）：父到框架 `reporting:token`、`reporting:theme`（`light`/`dark`，深色调色板在 `embed.css`）；框架到父 `reporting:ready`、`reporting:token-request`、`reporting:resize`（`height` 整数像素，50 毫秒内合并，每次变化都发）、`reporting:event`（`saved`/`published` 带 `template_id` 与 `version`，`issued` 带 `job_id`，仅此；看板与列布局不发事件）、`reporting:error`（只有 `UNAUTHENTICATED`、`FORBIDDEN`、`NOT_FOUND`、`RESOURCE_UNAVAILABLE`、`UNAVAILABLE`、`INVALID_RESPONSE`、`INTEGRITY_FAILURE`、`REQUEST_FAILED` 之一）。任何行值、字段值、文件、文字都不会传给父页面。
参考主机：`console/embed-host/`（平台不提供它；`index.html` 与 `host.js`，约二十行是集成本身）。文档：docs/EMBEDDING.md（协议、配置、安全规则、主机代码）、docs/API.md、docs/CONSOLE.md、docs/OPERATIONS.md。
测试：Go 无环境 296 项顶层测试通过（改动前 281；18 个包，改动前 17），跳过清单与 CI 的一致，普通与 race 都通过；DOM 156 项（改动前 136；新增 `console/embed.dom.test.mjs` 20 项，CI 与 `npm test` 一并运行；无 Typst 137 项通过、19 项跳过，含 Typst 156 项全部通过），协议、来源与窗口检查、401 重试、身份切换、续期、尺寸、事件、主题、无令牌泄漏与嵌入页面和控制台区段逐字节相同都经真实 Go 服务；`scripts/browser_embed_s03n.mjs`（Windows Chrome 经 CDP，本地证据，不在 CI）42 项通过：主机页面与平台在不同的回环来源，三个视图在令牌交接后可用（真实鼠标与键盘、下载在框架内完成），第三个未列出的来源、冒充名单来源的主机、未列出来源的中间框架、控制台页面与格式错误的地址都被浏览器拒绝，网络日志、存储、DOM 与全部消息表明令牌只出现在发往平台本站的 Authorization 头里；S03-I 的画布浏览器脚本 76 项仍全部通过。
不在本阶段：嵌入用户的真实身份提供方令牌签发（随真实接入）、产品页面里的 JavaScript SDK（ADR 0006 已否决）、单独嵌入出具记录列表、地址里带公司范围或数据时点（用户在框架里给出）。

# IDP Reporting Platform · S03-M

2026-10-29。条码、二维码、图片与出具编号（模板 schema 2.1；ADR 0005，docs/S03M_SCOPE.md）：正式文件现在可以带 Code 128 条码、QR 二维码、图片，并把来源产品给的单据编号印在文本和码里。
模板：`schema_version: "2.1"` 是 2.0 加三种元素与一种文本来源，同样封闭——`barcode`（`symbology` 为 `code128`，`human_readable` 为真时才有 `font_size`）、`qrcode`（方形 `size`，纠错 `L/M/Q/H`）、`image`（私有图片 `{id, sha256}`）与文本来源 `issue_number`；码的值可以是固定文字、分组字段（与文本字段同一规则）或出具编号；除明细带外各带都可放置，文本、码与图片各占一个框且互不重叠。Go 与控制台的校验器共用 doctemplates/testdata/schema2_cases.json（现 313 例，其中 110 例为 2.1）。
编码与版面：`github.com/boombuler/barcode` v1.1.0（MIT，go.mod/go.sum 固定）只经 `export/barcode` 使用，Go 把模块合并成矢量矩形并按精确位置排版，Typst 只画矩形；没有位图码、浏览器库或 Typst 插件。静区在元素框内（Code 128 两侧各 10 个模块，QR 四周各 4 个），模块宽是框宽除以模块数（QR 为边长），Code 128 至少 0.25 毫米、QR 至少 0.4 毫米，条高至少 5 毫米与符号宽 15% 中的较大者（印出值的行从框高中扣除）；放不下一律 DOES_NOT_FIT 并指出元素，绝不缩小；值不在字符集内（Code 128 为 ASCII 32–126、1–64 个字符，QR 为无控制字符的 UTF-8、1–256 字节）为 NOT_COVERED，绝不改动。
出具编号：预览把它印成并编码 `PREVIEW-NUMBER`，所以预览不含任何像编号的值；正式出具前的检查（样本检查与工作进程的版面检查，都不带编号）对 1–32 位 `[A-Za-z0-9._-]` 的每个合法编号证明排得下——文本按字体与字号的最宽编号，Code 128 按 32 个非数字字符（编码器的字符集切换不会使符号数多于字符数，测试穷举到 16 位的数字/其他字符模式并随机抽样 32 位）所需的最多模块（407 个含静区），QR 按 32 字节与所选纠错级别——所以产品给的编号不会在预订之后使渲染失败；该证明经真实 Typst 对最宽与最长的编号在检查刚好通过的最紧框内逐个出具。出具时 `IssueRequest.Number`（核心类型增加的一个字段）把预订的编号交给渲染，出具方要求它就是印章里的编号，每个显示编号的元素都印它。
图片：`POST /document-images`（PNG 或 JPEG，base64，≤512 KiB、≤2000×2000 像素，格式以内容为准，服务端完整解码并规范化：PNG 重新编码，JPEG 不改像素但去掉 EXIF/XMP/ICC/注释，CMYK 拒绝）、`GET /document-images`、`GET /document-images/{id}/content`（只给所有者，`no-store`、`nosniff`、`X-Content-SHA256`）；与底稿同属私有、不可变，按身份与数据集每组最多 16 张，存于底稿目录的 `images` 子目录（无新参数）；有渲染器时上传先在探测页上画一次，Typst 嵌入不了或 PDF 超限的图片当场拒绝。模板、预览、检查与出具用与底稿相同的授权解析图片：他人的图片 404，别的数据集的 403，摘要不符 409。PDF/A-2b 经 veraPDF 验证，含带 alpha 的 PNG、16 位与调色板 PNG、彩色与灰度 JPEG。
控制台：画布把码画成带标签的占位符（符号、值来源、框与按值长度估算的静区），图片经内容路由读取并以 data: 地址绘制（CSP 不变）；面板可添加条码、二维码、图片与出具编号文字并编辑每个键，加入这些元素的 2.0 模板成为 2.1（一步撤销回到 2.0）；图片上传与列表在面板里。仍只用 DOM 节点与 CSSOM。
检查器：用 zxing-cpp 3.1.1（Apache-2.0，带哈希固定在 scripts/requirements-pdfcheck.txt）对渲染后的页面解码每个码，与期望值、位置、模块大小比较；条码与 QR 的模块、静区、条高、版本与定位图形由检查器自己从矩形推导；图片按框、像素与是否存在核对；出具文件必须显示记录里的编号。反向对照：值错误、码移动、框变宽或变矮、模块低于最小值、覆盖一个模块或定位图形、画成图片、图片缺失或移位等。
1.x 与 2.0 不变：基线的 49 份 1.x/2.0 证据 PDF 中 45 份逐字节相同，其余 4 份（三份 `banded_issue_service_*` 的印章含随机记录号，`financial.pdf` 含生成时间）本来就是基线两次运行即不同，其差异只在同一印章行与页脚行；golden.json 只新增 16 项 `coded_*`。
测试：Go 无环境 278 项顶层测试通过（改动前 244），数据库与 Typst 全开 294 项通过（改动前 260）、无跳过，普通与 race 的 CI 跳过清单一致；DOM 136 项（改动前 128；无 Typst 118 项通过、18 项跳过，含 Typst 136 项全部通过）；检查器 57 份文档通过（改动前 41；含 16 份 `coded_*`）、3213 项反向对照全部捕获（改动前 1962），veraPDF PDF/A-2b 85 份通过（改动前 56；含控制台的 2.1 预览与出具各 1 份；去掉 XMP 的反向对照仍为 FAIL）。
依赖与运行：go.mod 增加 boombuler/barcode v1.1.0（go.sum 两行），检查器依赖 zxing-cpp（docs/OPERATIONS.md）；1.x 与 2.0 的模板与文件不变，无 SQL 迁移，2.1 模板需要本阶段的二进制。
不在本阶段：其他码制（EAN-13、GS1-128、Data Matrix、PDF417）、明细表中的图片、电子印章与签名（图片只是图片）、2.x 的套打与底稿；嵌入入口与 S04 的其余部分随后。

# IDP Reporting Platform · S03-L

2026-10-29。2.0 分带模板的画布与 1.x 到 2.0 的转换（ADR 0004 的控制台部分，docs/S03L_SCOPE.md）：仅控制台，服务端与 Go 代码不变；S03-I 的画布现在也编辑 schema 2.0 模板。
画布：2.0 模板按页面画出——横幅与印章保留区，从安全区顶部起的页眉带、单据头带、明细带（占满其余各带留下的空间，不少于明细表的最小高度，表头、列边界、字段名或字面表头与一行示例行）、单据尾带，页脚带在底部；每带的名称与高度写在页面右侧；元素画在带内（文本显示来源、对齐、粗体、换行记号，页码文字显示 {page} 与 {pages}，线、矩形按线宽与灰度）。
编辑：S03-I 的全部操作对 2.0 元素同样适用（选择、多选、拖动、手柄、吸附 1 毫米与 Alt 的 0.1 毫米及同带元素与带的边和中心、方向键、对齐、等距、复制到最小空闲 e 编号、删除、撤销与重做、命令面板）；拖过带的分界线移入指针所在的带，被规则拒绝的移动回到原位并说明原因；固定带的下边缘可拖动或用方向键调高（明细带不可）；线用本体移动、用两个端点手柄编辑；明细表列边界可拖动，列的添加、移除、排序、字面表头与对齐在表的面板。
面板：分组字段（1–3 个维度或无）、带（添加或移除可选带与带高）、向所选带添加元素（固定文字、字段、分组合计、页码文字、线、矩形）、所选元素的每个键（经校验的输入）、明细表的位置、宽度、字号与各列；与画布双向同步，每次修改一步撤销，面板就地更新不丢焦点。
转换：“转换为 2.0”把 1.x 模板变成未保存的 2.0 配置（一步撤销，保存才成为下一个版本，服务器不转换）：表格上方的文本框成为单据头（高到表格顶边，位置相对安全区顶部，所以页面位置与 1.x 预览相同），表格成为明细带，表格下方的文本框成为单据尾（2.0 把它放在最后一行之后，转换时告知），行字段成为分组字段文本，合计成为分组合计，不设分组；与明细表纵向重叠的文本框使转换被拒绝并指出该框；套打偏移、变量层与底稿不属于 2.0，丢弃并警告；没有明细表的模板加默认明细表。
拒绝位置：预览或检查的 `detail` 在 2.0 画布上描出带、元素与列，与 1.x 相同（只指向带的拒绝描出该带）。S03-J 画布处的“画布只显示 1.x 模板”说明已删除。
测试：DOM 128 项（改动前 104；无 Typst 111 项通过、17 项跳过，含 Typst 128 项全部通过），含转换映射的代表模板、2.0 每条编辑路径、换带与带高、面板、拒绝轮廓，以及经真实 PDF 预览核对转换后单据头文本框与 1.x 预览逐字形位置相同；S03-J 共用的 2.0 用例在 Go 与控制台仍一致；S03-I 的 DOM 测试与 scripts/browser_canvas_s03i.mjs 原样通过（76 项）；真实浏览器脚本 scripts/browser_canvas_s03l.mjs（Windows Chrome 经 CDP，真实鼠标与键盘，390/768/1280 像素，浅色与深色对比度）64 项通过，是本地证据，不在 CI 中运行。
不在本阶段：条码、二维码、图片（ADR 0005）、2.0 的套打与底稿、子分组、服务端改动、整个快照逐组批量出具。

# IDP Reporting Platform · S03-K

2026-10-29。按分组出具（ADR 0004 决定 7，docs/S03K_SCOPE.md）：schema 2.0 模板一个行组出具一份正式单据，仍由来源产品授权、编号与确认（ADR 0003），平台不编号、不单独出具。1.x 按行出具不变。
请求：`POST /issues` 对 2.0 模板接受 `group`（按 `group.fields` 顺序的键值列表；无分组的模板为空列表即整个快照）或 `row_index`（该行所在的组，控制台用），两者都给或都不给为 400；1.x 模板带 `group` 为 400。受理时在冻结快照中解析该组并冻结：键值、行序号与这些行的摘要；请求哈希覆盖解析后的组，故 `row_index` 与 `group` 指同一组即同一请求；键无此组为 404，拒绝与错误从不带值。
合同版本 2：`IssueIntent.Group`（Fields、Key、Rows、RowsSHA256；无 RowIndex/RowSHA256）。适配器实现 `IssueContractVersion() int` 返回 2 才声明；平台绝不向未声明的适配器发送分组意图（受理 422，已存任务以 UNSUPPORTED_COMBINATION 失败且不询问来源产品）。两个合同模拟器（内存与 PostgreSQL 来源库）声明版本 2，核对组内每一行（行在其冻结读取中、键相同、组完整、行摘要一致），不符即拒绝并作废，如对行。
工作进程：先按冻结快照复核冻结的组（不符 INTEGRITY_FAILURE，不询问来源产品），再由出具方绑定该组并做不启动 Typst 的版面检查，再向产品请求编号；渲染该组的文档（印章在每一页），确认后发布。失败顺序（ISSUE_UNRECONCILED 栅栏、作废、改记）与 S03-H 相同。记录以组（字段、键值、行序号、行摘要）取代行；任务视图、出具记录列表与控制台任务/记录显示分组键值而不是行号。无需 SQL 迁移（组在任务与记录的 JSON 载荷中）。
控制台：已发布的 2.0 模板打开后未改动即可出具，请求仍是模板、版本、快照与预览行号，由服务端确定分组；确认、任务与记录显示分组键值。检查器：`document2` 带 `issue` 记录（每页印章、记录摘要与页数、文件日期），与 1.x 出具共用同一套反向对照。
1.x 不变：Go 测试写出的全部 1.x 证据 PDF（含出具与其预览）逐字节与改动前相同，golden.json 只新增 6 个 2.0 出具条目；意图摘要、请求哈希、任务/记录 JSON 在 1.x 下不变（无 Group 键）。
测试：Go 无环境 244 项顶层测试通过（改动前 221），数据库与 Typst 全部开启 260 项通过（改动前 237）、无跳过；本机 PostgreSQL 16 上 CI 同一跳过清单 250 项通过（普通与 race）；DOM 104 项（含 Typst 全部通过；改动前 101）；检查器 41 份文档通过、1962 项反向对照全部捕获，veraPDF PDF/A-2b 56 份通过（含 8 份 2.0 出具样张与控制台 2.0 出具）。
不在本阶段：整个快照逐组批量出具、区域画布（S03-L）、条码/二维码/图片（ADR 0005）。真实来源产品实现合同版本 2 随真实接入进行；操作约束见 docs/OPERATIONS.md（二进制一起滚动）。

# IDP Reporting Platform · S03-J

2026-10-29。模板 schema 2.0 的分组、版面区域与排版（ADR 0004，docs/S03J_SCOPE.md）：与 1.0–1.3 并存的新封闭格式。
`group.fields`（1–3 个维度字段）把冻结快照按精确字符串分组，按首行排序，一组一份单据；不设分组即整个快照一份。
版面区域：页眉与页脚（每页）、单据头（首页）、明细（唯一可增长并跨页的区域，即 1.3 的多页明细表：表头每页重复、承前过次、末尾合计）、单据尾（最后一行之后整体放置，放不下则整体移到新页，空页也放不下即拒绝）；横幅/印章保留带每页不变。
元素：文本（固定文字、组字段、组合计、含 {page}/{pages} 的页码文字；可按表格单元格的断行规则换行，超出框高即拒绝，绝不截断）、直线、矩形、明细表；组字段须是分组字段或在组内各行相同，否则 INCONSISTENT_GROUP；组合计按明细表合计的同一规则精确累加并同样分组显示。
拒绝原因带位置：预览、检查与出具的 422 回答增加 `detail`（reason：DOES_NOT_FIT / NOT_COVERED / INCONSISTENT_GROUP / BAND_OVERFLOW，以及 band、element、row、column），只有名称与序号、不含数值；1.x 同样带元素或列，画布据此框出出问题的框或列。
预览渲染 row_index 所在的组；样本检查按组检查并给出组内每行相同的结果。控制台：2.0 模板以 JSON 打开、预览、保存与发布，画布暂只显示 1.x。2.0 模板暂不能正式出具（受理即 422；按分组出具在 S03-K）。
1.x 不变：基线全部确定性 PDF 逐字节相同（financial.pdf 内含随机快照 ID，基线两次运行即不同），golden.json 只新增 2.0 条目。
测试：Go 无环境 221 项顶层测试通过（520 个通过事件，16 项数据库/Typst 入口跳过）；Typst 排版测试与证据下检查器全部通过，1375 项反向对照全部捕获，veraPDF PDF/A-2b 43 份通过（含 7 份 2.0 样张与控制台 2.0 预览）；DOM 101/101（含 Typst）；Go 与控制台共用 203 个 2.0 校验样例，两侧判定一致。
不在本阶段：按分组出具（S03-K）、区域画布与 1.x→2.0 转换（S03-L）、条码/二维码/图片（ADR 0005）、2.0 的套打偏移与底稿、组内小计与组页眉。

# IDP Reporting Platform · S03-I

2026-10-28。文档画布编辑器交互内核（docs/S03I_SCOPE.md）：文档模板设计器新增 A4 画布（console/assets/canvas.js，SVG，无第三方代码，CSP 不变），显示标尺、安全区、横幅/印章保留带、文本框与明细表（列线与表头）。
点击/Shift 多选/Ctrl(Cmd)+A 全选，拖动移动、八向手柄缩放、拖动列线调整列宽（满足最小列宽与首列标签宽），1 毫米网格吸附（Alt 0.1 毫米）与其他项目边缘/中心 1 毫米内吸附并显示参考线，方向键微调，复制、删除、六种对齐与两种等距分布。
每次编辑都经设计器原有校验，被拒绝的修改不生效并说明原因；撤销/重做覆盖每一次已应用的修改（200 步，一次拖动一步），打开、重置或新查询清空历史，撤销不会恢复“已打开发布版本、未改动”的出具前提。
Ctrl/Cmd+K 命令面板只列出页面上可见且可用的按钮，键盘可达、焦点受限。新只读接口 `POST /document-checks`：用正式出具前的同一版面检查（不启动 Typst）核对冻结快照中指定的行或边界样本（首行、末行、各行字段最长/最短的行），授权与预览相同，结果只含行号、通过与否、错误码与入选原因。
顺带修正：带明细表的模板修改套打设置或底稿时保持 schema 1.3。
测试：DOM 92 项（含 Typst 全部通过），Go 路由与抽样测试；真实浏览器脚本 scripts/browser_canvas_s03i.mjs（Windows Chrome 经 CDP，真实鼠标键盘，390/768/1280 像素）本机 76/76 通过，不在 CI 中。
未验：触屏操作（指针代码按触屏编写，只用鼠标验证）。不在本阶段：分组与版面区域（schema 2.0，ADR 0004）、文字换行、条码、图片、锁定与组合、画布显示底稿。

# IDP Reporting Platform · S03-H

2026-10-28。来源产品确认的正式出具（ADR 0003，docs/S03H_SCOPE.md）：纠正 S03-G 违反架构基线之处——审批、编号与出具确认归来源产品，平台只渲染并保存正式文件与记录，自己不编号、不单独出具。
接入可选实现 `reporting.DocumentAuthority`：PrepareDocument（授权、按来源自身快照核对该行、审批规则、预留编号与出具时间）、ConfirmIssue（把编号绑定到文件摘要与页数）、VoidIssue（作废该任务名下的预留或确认），三者均以平台任务 ID 幂等；未实现的接入出具为 422。
工作进程先做不启动 Typst 的版面检查（Writer.CheckIssue），通过才向产品请求编号；按冻结模板、快照、行与产品给的编号和时间确定性渲染（产品出具时间即 PDF 创建时间，重跑字节相同），确认后再核对一次才发布。
失败时：先以 ISSUE_UNRECONCILED 记录失败（以本次领取为栅栏），再通知产品作废，作废成功后改记任务自身的失败码；作废送不达则保持 ISSUE_UNRECONCILED，产品可按任务 ID 找到该预留。真正丢失租约的任务不作废；执行中的出具任务不可取消。
迁移 004 删除平台编号表 issue_numbers，存在 S03-G 平台编号的任务或记录时拒绝执行。产品给的编号为 1–32 位 [A-Za-z0-9._-]，引用为 1–128 字节可打印 ASCII，时间为 1970–9999 年内的 UTC 整秒。
两个合同模拟器（内存与 PostgreSQL 来源库，含作废墓碑）实现该能力，控制台与演示可出具；真实产品接入最后进行。
审查：两路独立审查（docs/CODE_REVIEW_S03H.md）；唯一 Major（PostgreSQL 上来源授权在发布前几秒到期被误判为丢失租约，产品留有已确认编号而无文件）在 PR #12 修复，其余 Minor 与测试/检查器/文档问题在后续 PR 修复。
Go 普通/race 各 178/178 项顶层测试通过（489/489 个通过事件），仅 6 项数据库入口跳过；本机 PostgreSQL 16：18 项顶层测试通过（出具持久化 13 项子测试），两个模拟器的出具能力一致性 77 项子测试通过；DOM 68/68；vet、go mod verify、JS 语法通过；检查器 28 份文档通过、968 项反向对照全部捕获（出具文档 218 项），veraPDF PDF/A-2b 35 份通过、反向对照 FAIL。
未验：真实来源产品的出具接口、网络分区下的长时间悬挂调用（只以门控模拟）。不在本阶段：按分组出具（ADR 0004 / S03-K）、编号唯一索引、准入配额、签章与权威时间戳；真实接入最后。
当前证据 evidence/s03h；规格 docs/S03H_SCOPE.md；决策 docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md；审查 docs/CODE_REVIEW_S03H.md。

# IDP Reporting Platform · S04-A

2026-10-28。生产存储基础（docs/S04A_SCOPE.md，操作手册 docs/OPERATIONS.md）：平台库从“守护的测试夹具”变为有完整生命周期的模式。
模式由嵌入二进制的编号迁移对 `storage/postgres/migrations/NNN_name.{up,down}.sql` 定义（001 基础表、002 载荷摘要、003 出具），
逐列逐约束复现夹具原有模式；账本 `schema_migrations` 记录每步的 up 文件 SHA-256、时间与执行者，profile 行版本与账本同步；
每步一个事务并持有事务级 advisory lock（并发迁移串行、失败步骤不影响前一版本）；账本出现二进制不认识的版本、校验和不一致或缺号即拒绝（status 报告、up/down 拒绝、服务拒绝打开）；
没有账本的旧库按 profile 版本引导。回滚 `down --to N` 逐步逆向，会丢行的步骤（001、003）须显式 `--accept-data-loss`；
鉴定：新库 up→快照目录→down 到 0→up，目录（列、类型、默认值、可空、约束、索引）逐字节相同。
最小权限：迁移角色拥有模式与全部表；应用角色只有 USAGE、全表 SELECT、数据表 INSERT/UPDATE（无 DELETE/TRUNCATE/DDL/CREATE/所有权/超级用户/建角色建库/RLS 绕过），
`reporting-migrate roles` 幂等设置所有权、授权与默认权限；`postgres.OpenProduction` 启动时校验 sslmode=verify-full、非夹具库名、production profile、账本版本与连接角色的每条规则，任一不满足即拒绝启动；
夹具路径保留超级用户连接以供破坏性重置，生产库不能重置。`cmd/reporting-migrate`：status/plan/up/down/roles/init-profile，只作用于 REPORTING_STORE_DSN，不进服务进程、不碰来源库；
演示服务的 -initialize-fixture 通过同一迁移器初始化。
测试 `TestPostgresMigrations`（PostgreSQL 任务）：往返目录相等、部分目标、篡改账本（校验和/未知版本/缺号/改名/profile 过期）全部拒绝且目录不变、6 个并发迁移器、丢行拒绝与接受、旧库引导、
创建两个角色后应用角色跑通整条平台契约（出具含）并逐条尝试被禁语句、超级用户与夹具 profile 被生产路径拒绝、DSN 规则（disable/prefer/require/verify-ca/夹具库名）。
Go 普通/race 各 126 项顶层测试通过（273 个通过事件），仅 5 项数据库入口跳过（含迁移鉴定与命令测试）；本机 PostgreSQL 16：17 项顶层测试通过，其中迁移鉴定 7 项子测试；DOM 60/60；vet、go mod verify、JS 语法通过；
检查器与 veraPDF 结果与 S03-G 一致（本阶段不改渲染）。两路独立审查见 docs/CODE_REVIEW_S04A.md。GitHub Actions 四项任务在 PR #10 上通过（run 40673628831）。
未验：真实 TLS 生产库（生产路径的 TLS 规则只验证了拒绝分支）、真实 DBA 角色与密码策略、生产准入。
不在本阶段：行级安全（单一应用角色，租户隔离仍在服务层）、产物对象存储、过期资产清理与保留、配额、备份与恢复鉴定、容量、密钥生命周期；真实接入最后。
当前证据 evidence/s04a；规格 docs/S04A_SCOPE.md；手册 docs/OPERATIONS.md；审查 docs/CODE_REVIEW_S04A.md。

# IDP Reporting Platform · S03-G

（编号与出具规则已被 ADR 0003 / S03-H 取代：平台不再编号，以下为历史说明。）
2026-10-28。正式出具记录（docs/S03G_SCOPE.md）：已发布的模板版本按冻结快照的一行正式出具为不带 PREVIEW 标记、带编号印章的 PDF/A-2b 产物，
并永久留下出具记录。`POST /issues`（幂等键，`{template_id, version, snapshot_id, row_index}`）受理 kind 为 issue 的持久任务：
只接受模板当前发布的版本（草稿、已撤回或其他版本为 409 TEMPLATE_NOT_PUBLISHED），字段与底稿按预览同样授权，快照须为本人当前未过期的冻结快照，行须存在；
任务冻结模板的规范化 JSON（以字符串存储，避免 JSONB 重排对象）及其 SHA-256、模板 id 与版本、快照与行号，请求摘要覆盖全部。
编号由仓储在受理事务中分配：按租户与接入连续、从 1 起、永不重用（PostgreSQL 用 issue_numbers 计数行串行化并发受理，重放回滚增量；失败或取消的任务保留编号作废，缺号总能由该任务解释）。
工作进程重新校验来源授权与快照摘要，再核对冻结配置仍是当前发布版本（版本号与规范化字节一致，否则任务以 TEMPLATE_NOT_PUBLISHED 失败），
按预览同样的页面渲染，只是 PREVIEW 横幅位置改印 `ISSUED No. 000012 - 2026-10-28T06:12:03Z - <记录 ID>`（正文色，变量层同样印），
然后在一个事务里发布 PDF 产物（format pdf、带记录 ID、与导出产物同样随快照授权到期）与记录 `{number, template_id/version/sha256, snapshot_id/sha256, row_index, artifact_id, sha256, pages, stamp, issued_at}`；
契约拒绝记录与产物或任务不一致的发布（摘要、页数 1–100、由编号/时间/ID 重算的印章）。`GET /issues?limit=N`、`GET /issues/{id}` 列出与读取本人记录；记录永不修改或删除。
PostgreSQL 夹具升级到 v3（jobs/assets 的 kind 约束加 issue、issue_numbers 表），升级与篡改用例在持久化测试中覆盖。
控制台：用“打开已发布模板”打开且未改动的发布版本可“正式出具”（幂等键、轮询、按编号下载 issued-000012.pdf），任务列表显示“正式出具 No. 000012”，
“刷新出具记录”逐条校验记录与印章一致后列出并可再次下载；能力未声明 issuance 时不显示按钮。
scripts/check_pdf_export.py 对出具文档在预览全部检查之外核对：横幅位置是记录的印章（编号、UTC 秒、ID 重算）、印章为正文色、全文无 PREVIEW 横幅原句（渲染器也拒绝把该原句作为值或单元格出具）、PDF 摘要与页数等于记录，
负对照含记录各字段篡改、用预览替换出具件、添加 PREVIEW 字样、印章改为横幅红色。
Go 普通/race 各 124 项顶层测试通过（271 个通过事件），仅 3 项数据库入口跳过；本机另对 PostgreSQL 16 运行了持久化与出具测试（6 项出具子测试与全部持久化测试通过）；
DOM 60/60（含真实 Typst 出具：下载 issued-000001.pdf、记录摘要等于下载字节、撤回后拒绝）；vet、go mod verify、JS 语法通过。
检查器核对 27 份 PDF（含 3 份出具件：三页含底稿、三页变量层、单页），829 项负对照全部被抓到，版式与 export/pdf/testdata/golden.json 一致；
veraPDF 对 34 份 PDF（含控制台真实出具的 1 份）判定 PDF/A-2b 通过，负对照判失败。
两路独立审查（正确性与安全；测试有效性与质量），发现与处理见 docs/CODE_REVIEW_S03G.md。
GitHub Actions 四项任务在 PR #9 上通过（run 40661393568）。
未验：真实打印机与实纸对位、真实浏览器、出具 PDF 的保留策略（随快照授权到期即不可下载，记录永久保留）。
不在本阶段：整快照批量出具、租户与接入以外的编号方案、出具件保留/对象存储、撤销或更正记录、签章与权威时间戳、画布编辑器、生产资格；真实接入最后。
当前证据 evidence/s03g；规格 docs/S03G_SCOPE.md；决策 docs/ADR_0002_SINGLE_PDF_ENGINE.md；审查 docs/CODE_REVIEW_S03G.md；接口 docs/API.md。

以下为此前阶段历史说明：

# IDP Reporting Platform · S03-F

2026-10-28。多页单据（docs/S03F_SCOPE.md）：schema 1.3 模板可加一个明细表区域（1–12 个数据集字段、列宽之和等于表宽、8–12 pt），
列出冻结快照的全部行（最多 1000 行），按需续页：每页重复底稿、文本框、PREVIEW 标记和表头，第 2 页起有“承前（第 p 页）”行，
除末页外每页以“过次（转第 p+1 页）”行结束，末页以“合计”行结束；变量层只打印单元格值与承前/过次/合计。
分页由 Go 全权负责（docs/ADR_0002_SINGLE_PDF_ENGINE.md）：单元格按表格导出的保守断行模型换行（整段过宽时切断），
行高由最高单元格决定、整行不跨页，每页先放表头与承前行再装整行，装不下的留给下页；累加金额用缩放整数精确相加（不走浮点），
并与快照自身的原范围合计核对，不一致为 INTEGRITY_FAILURE；金额、打印出的累计金额和承前/过次/合计标签（按 100 页计）从不切断或换行，放不下所在列、
行高超过表区、表头加合计行超过表区、页数超过 100 均为 UNSUPPORTED_COMBINATION。
Typst 只画 Go 算好的表格线、表头底色和每一行文字（逐行定位、断言宽度不超），和 S03-E 的定位契约一致。
控制台设计器新增明细表面板；预览与保存/发布/偏移/底稿接口不变，表格字段同样按当前授权目录校验。
scripts/check_pdf_export.py 跨页核对单据表格：页数与每页 A4、每页的标记/文本框/底稿、每页表头（完整模式）、
每行各单元格完整、有序、同页不拆分、每行文字在列内并按字体原始步进逐字符定位（粗体按 Bold 字面）、
承前/过次/合计由期望行用十进制精确重算并逐页比对、快照合计等于各行之和、无底稿时框外无墨迹，版式基准记录页数与每页首行。
Go 普通/race 各 117 项顶层测试通过（264 个通过事件），仅 2 项数据库入口跳过；DOM 57/57（明细表设计器每条规则单独校验、全字段列宽分配与
第一列标签最小宽度、真实预览与排版拒绝提示；演示数据只有一行，控制台预览为单页，多页由 Go 测试与检查器覆盖）；vet、go mod verify、JS 语法通过。
表格行按连续标点拆成多个文本对象逐段定位，连续中文标点、破折号与省略号不再被 Typst 挤压或连写，难例字符串作为单元格也逐字符核对通过。
检查器核对 24 份 PDF（11 份表格导出、5 份单据、8 份单据表格：三页含底稿、三页变量层、三页十二列窄列、空表、单列含空行、
难例单元格两页含偏移、混合小数位、变量层三行表头），每份表格按页核对表头/承前/过次/合计与各行文字的基线位置、表格线与表头底色及颜色
（变量层无任何图形、末页只有一条合计线），692 项负对照全部被抓到，版式与 export/pdf/testdata/golden.json 一致；
veraPDF 对 29 份 PDF（含控制台真实下载的 5 份）判定 PDF/A-2b 通过，负对照判失败。
两路独立审查（正确性与安全；测试有效性与质量），发现与处理见 docs/CODE_REVIEW_S03F.md。
GitHub Actions 四项任务（含 pdf 任务的单据表格测试与检查器）在 PR #8 上通过（run 40647824620）。
未验：真实打印机与实纸对位、真实浏览器、PostgreSQL 存储下的模板。
不在本阶段：按维度分组出单（每个客户一份）、分组小计、底稿以外的图片、条码/签章、正式出具记录、画布编辑器、生产资格；真实接入最后。
当前证据 evidence/s03f；规格 docs/S03F_SCOPE.md；决策 docs/ADR_0002_SINGLE_PDF_ENGINE.md；审查 docs/CODE_REVIEW_S03F.md；接口 docs/API.md。

以下为此前阶段历史说明：

# IDP Reporting Platform · S03-E

2026-10-27。新增文档模板设计器（S03-E1..E4，docs/S03E_SCOPE.md）：A4 单页文本模板（固定文字、行字段、原范围合计），私有版本与发布，
预印纸偏移与变量层，私有 PNG 底稿，按冻结快照的一行生成带 PREVIEW 标记的 PDF/A-2b 预览；不是正式出具。
来源是另一条用 pdfme/Node 实现的交付（temp/ 存档，提交 79aa396）；按 docs/ADR_0002_SINGLE_PDF_ENGINE.md 全平台只保留一个 PDF 引擎：
业务部分原样迁移，渲染改为 export/pdf 的 Typst（同一沙箱、字库核对与限制），去掉 Node 渲染、pdfme、7.4 MB 设计器包和放宽的 CSP。
对位规则与 pdfme 一致：基线在框顶下 1.16 倍字号，按对齐贴左、贴右或居中，字符按字体原始步进排列，不画末尾空白。
Typst 无法对整串关闭中文标点挤压、中西文间距和中文破折号加宽，因此 Go 逐字簇算位置、逐簇放置（前置一个空行内盒以免句首标点被裁），
模板断言 Typst 量得的宽度不超过 Go 的度量；24 个难例框（中西混排、句首/连续/单独标点、破折号、字距对、组合附加符号、首尾空格、
三种对齐、偏移、8–28 pt）与 pdfme 的字符位置最大相差 0.009 pt（evidence/s03e/cross_engine.json）；唯一差异是 pdfme 把连续两个破折号
替换成一个两倍长破折号字形（其后字符随之左移 0.11 em）、把 fi 画成连字字形、叠加附加符号只画第一个，Typst 均按原字符绘制。
拒绝（UNSUPPORTED_COMBINATION）：缺字、软连字符、韩文声调符号、以组合附加符号开头、单行放不下、Typst 会画得更宽的字簇、值含控制字符/DEL/超过 256 字；
模板固定文字含控制字符为 INVALID_REQUEST。预览与导出共用最多 2 个 Typst 进程，预览最多占其中 1 个（不会饿死导出任务），
等待加渲染合计超过 15 秒为 BUDGET_EXCEEDED；底稿上传时先用渲染器试渲染（锁外、带请求上下文、先查配额），渲染失败即拒绝。
演示服务：-template-directory、-background-directory；预览需同时给出 S03-D1 的三个 -pdf-* 参数，否则控制台不显示两个预览按钮。
Go 普通/race 各 110 项顶层测试通过（257 个通过事件），仅 2 项数据库入口跳过；DOM 54/54（含 9 项真实 Typst 渲染）；vet、go mod verify、JS 语法通过。
scripts/check_pdf_export.py 对 5 份单据预览逐字核对（--font 按字体原始步进独立推算每个字符的坐标）、基线、对齐、无底稿时框外无墨迹、
底稿像素与所引 PNG 一致且适配居中、PREVIEW 标记，连同 11 份表格文档共 379 项负对照全部被抓到（单据侧每项检查各有一个，
含“内容流中单个字形平移 1 pt”“两个字形互换”“字形越出框边 0.6 pt”“底稿平移/替换”“框外图形”），版式与 export/pdf/testdata/golden.json 一致；
veraPDF 对 20 份 PDF（含控制台真实下载的 4 份）判定 PDF/A-2b 通过，负对照判失败。
两路独立审查（正确性与安全；测试有效性与质量），发现与处理见 docs/CODE_REVIEW_S03E.md。
GitHub Actions 四项任务（含 pdf 任务的单据测试与检查器）在 PR #7 上通过（run 40632159958）。
未验：真实打印机与实纸对位、真实浏览器、PostgreSQL 存储下的模板与底稿。
不在本阶段：多页单据与表格、承前过次、底稿以外的图片、条码/签章、正式出具记录、画布编辑器、生产资格；真实接入最后。
当前证据 evidence/s03e；规格 docs/S03E_SCOPE.md；决策 docs/ADR_0002_SINGLE_PDF_ENGINE.md；审查 docs/CODE_REVIEW_S03E.md；接口 docs/API.md。

以下为此前阶段历史说明：

# IDP Reporting Platform · S03-D1

2026-10-27。新增 PDF 导出：冻结快照可另存为 PDF/A-2b 表格，与 Excel 同属异步导出任务（POST /exports 加 `"format":"pdf"`）。
引擎为按校验和固定的 Typst 0.15.1，决策与条件见 docs/ADR_0001_PDF_ENGINE.md。Go 端完成全部准备，Typst 只排版：
数值不计算，指标数字只加千分位；逐字核对字库覆盖（含模板文字），并要求 Typst 自报的字体与 Go 加载的完全一致；
按字体度量定列宽（关闭字距与标点悬挂，度量精确或偏大），比单元格宽的连续串预切成一行一段并强制换行，不溢出、不乱序、不丢字；
软连字符、缺字（含 emoji 与零宽字符）、比单元格还宽的单字或超过32列拒绝（UNSUPPORTED_COMBINATION）。
Typst 在沙箱中运行：空环境、独立用户与网络命名空间、空包目录、仅配置字体、私有根目录，从启动时校验过的文件描述符执行
（二进制、字体及其上级目录只能由 root 或服务用户修改）；
15秒、2GiB、输出≤8MiB，超限为 BUDGET_EXCEEDED；core 限制取1（可挡住管道式崩溃收集器），任何警告都判失败；拒绝以 root 运行。
演示服务需同时给出 -pdf-typst、-pdf-typst-sha256、-pdf-fonts 才提供 PDF；scripts/fetch_pdf_tools.py 按校验和下载 Typst 与 Noto Sans SC。
控制台在服务端提供 PDF 时显示“导出 PDF”，下载按格式校验类型、文件头与摘要。核心 reporting/ 自 S01-A 以来首次改动：
任务与产物增加 format（omitempty，旧记录编码不变），仍为纯标准库。宿主需允许非特权用户命名空间（Ubuntu 24.04 默认由 AppArmor 限制）。
Go 普通/race 各84项顶层测试通过（230个通过事件），仅2项数据库入口跳过；41项 DOM（含真实 Typst 导出）、vet 通过。
scripts/check_pdf_export.py 从快照原值独立推导应显示内容，逐单元格核对11份文档（恶意字符串、超过一页的行、长标点串、字距对、
随机混排、宽表、千行等）：换行与空格、行基线对齐、列内边距、墨迹、行内顺序、加粗与右对齐；275项负对照全部被抓到，
版式与 export/pdf/testdata/golden.json 一致；另有90份随机混排文档全部通过。veraPDF 对12份 PDF 判定 PDF/A-2b 通过，负对照判失败。
两路独立审查各两轮（正确性与安全；测试有效性与质量），发现均已核实并修复，见 docs/CODE_REVIEW_S03D1.md。
回滚注意：回到 cf9960e 后，已存的 PDF 产物会被旧程序当作 xlsx 下发，排队中的 PDF 任务会反复失败；回滚前应清理 PDF 任务与产物。
未验：GitHub Actions 上的 pdf 任务、真实浏览器、无用户命名空间的主机（启动即拒绝）、PostgreSQL 存储下的 PDF 任务；
被攻破的 Typst 仍可读取服务用户可读的文件（未做 Landlock/挂载命名空间隔离）。
不在本阶段：模板设计器/用户模板、分组与承前过次、图片/条码/签章、OFD、生产资格；真实接入最后。
当前证据 evidence/s03d1；规格 docs/S03D1_SCOPE.md；审查 docs/CODE_REVIEW_S03D1.md；接口 docs/API.md。

以下为此前阶段历史说明：

# IDP Reporting Platform · S03-C4

2026-10-27。新增当前页柱状图：选择分类字段与数值字段，逐行显示前50行。
按当前授权结果原顺序绘图，不分组、不汇总；与服务端原范围指标合计的范围不同。
统一中央零线、正负对称尺度；只用BigInt计算比例，精确原始值保留文本。
图形长度为近似比例；小于分辨率的非零值有说明。空页、全零、重复分类与极大数均处理。
数值限512字符，含非法/超长数值时整图显示不可用，不静默跳行或强制转数。
图表配置接入既有私有保存/版本/发布；保存和打开/发布均校验分类及数值字段的当前权限。
先在工作台查询，再进入看板设计器添加当前页柱状图。沿用 -dashboard-directory 和独立报表目录。
既有metric/table文件可读；新增bar组件不被旧程序支持，降级未验。
Go普通/race各47项顶层测试通过（183个含子测试通过事件），2项数据库入口显式跳过；37项软件DOM、vet/JS语法及两轴审查通过。
审查后已用无头Chrome 155做七档视口(含手机/320px模拟)的视觉及Excel下载验收，173项通过，修复6处界面问题(超大数撑破页面、窄屏溢出、总额折行、间距、favicon等)，仅改index.html与styles.css；真机/触控、其他浏览器、无障碍与键盘、PG重跑、生产或崩溃恢复未验。图表只支持当前页柱状图；其他图型、多数据集/共享、文档模板设计器、PDF及运维仍待开发；真实接入最后。
当前证据evidence/s03c4(审查后变更见post_review.json、浏览器截图见browser_visual)；规格docs/S03C4_SCOPE.md；审查docs/CODE_REVIEW_S03C4.md；浏览器验收docs/BROWSER_ACCEPTANCE_S03C4.md。

# IDP Reporting Platform · S03-C3

2026-10-27。新增当前身份私有看板发布：固定保存版本、发布历史、修订冲突保护、撤回及显式回退。
先查询当前授权数据→保存/打开服务器看板→刷新发布状态→发布当前保存版本。
未保存的修改不能发布；后续草稿不会改变发布指针。打开已发布看板仍使用当前授权查询数据。
发布和打开发布版校验实际目标版本当前字段权限；历史/撤回校验当前数据集绑定。
仅当前可信tenant/issuer/subject可见，不共享身份、权限、数据或冻结快照。
冲突或响应丢失后先刷新发布状态，无自动重试；元数据操作失败不清除无关查询结果。
沿用 -dashboard-directory ../reporting-dashboards；与报表目录分开，复用既有单写者存储。
Go普通/race各45项顶层测试通过（181个含子测试通过事件），2项数据库入口显式跳过；34项软件DOM、vet/JS检查及两轴审查通过。
可信本地单进程存储，正常关闭重开保留记录。浏览器视觉/移动/真实下载、PG重跑、崩溃恢复和生产资格未验。
图表、多数据集/共享、文档模板设计器、PDF与运维仍待完成；真实产品和企业身份最后接入。
当前证据 evidence/s03c3；规格 docs/S03C3_SCOPE.md；审查 docs/CODE_REVIEW_S03C3.md。

以下为此前阶段历史说明：

# IDP Reporting Platform · S03-C2

2026-10-27。新增当前身份私有看板的服务器保存、版本列表与指定历史版本打开。
用 -dashboard-directory ../reporting-dashboards 显式启用，独立于 -layout-directory ../reporting-layouts。
看板/报表文档类型、目录和接口分开；共享既有版本存储与认证机制，报表旧文件与发布行为保持兼容。
打开看板后仍使用当前授权查询数据，不保存业务值、公司、日期或令牌。
旧版本保存冲突，响应丢失需先刷新列表核对；退出清空客户端服务器引用。
31项软件DOM、Go普通/race及两轴复审通过。单进程可信本地存储，正常重开保留历史；浏览器、PG重跑、生产与崩溃恢复未验。
看板发布、图表、多数据集/共享及PDF仍未完成；真实产品与企业身份最后接入。
当前证据evidence/s03c2，规格docs/S03C2_SCOPE.md、审查CODE_REVIEW_S03C2.md。

以下保留此前阶段历史说明：

# IDP Reporting Platform · S03-C1

2026-10-27。新增独立看板设计器入口：当前授权单数据集的指标卡、明细表、组件增删排序及配置文件重新打开。
指标卡使用服务端原范围合计，表格为当前预览/冻结页；不以浏览器重算汇总、金额或权限。
配置仅保存绑定及组件元数据（12组件/64KiB）；不含业务数据、公司、日期或令牌。
当前看板为本地配置文件，尚无看板服务端持久化/发布、图表、多数据集或共享；报表列布局服务端发布功能保持原有范围。
29项软件DOM、Go普通/race、两轴复审通过。浏览器视觉/下载/移动、PG重跑、生产资格未验；真实接入最后。
本机启动方式沿用既有服务；先工作台查询，再左侧进入看板设计器。详见docs/S03C1_SCOPE.md与evidence/s03c1。

以下保留此前阶段历史说明：

# IDP Reporting Platform · S03-B3

2026-10-27。新增私有布局发布：草稿/发布分离、固定保存版本、发布修订冲突保护、发布历史与撤回。
只固定当前所有者的布局，不共享数据、作者权限或跨用户模板。未保存的布局变更需先保存才能发布。
旧owner文件在下一次写入迁移为schema2，保留布局ID/版本定义；旧程序不能读schema2，降级回滚未验。
当前26项软件DOM、Go普通/race及双轴复审通过。浏览器/PG重跑/生产/崩溃恢复未验，真实接入最后。
运行仍使用 -layout-directory ../reporting-layouts 显式启用私有0700单进程目录；正常关闭/重开保留发布记录。
先打开或保存服务器布局→读取发布状态→发布当前已保存版本；后续保存草稿不自动替换发布版。
详见docs/S03B3_SCOPE.md、CODE_REVIEW_S03B3.md，当前证据evidence/s03b3。

以下为此前阶段历史说明：

# IDP Reporting Platform · S03-B3

2026-10-27。新增私有布局发布：草稿/发布分离、固定保存版本、发布修订冲突保护、发布历史与撤回。
只固定当前所有者的布局，不共享数据、作者权限或跨用户模板。未保存的布局变更需先保存才能发布。
旧owner文件在下一次写入迁移为schema2，保留布局ID/版本定义；旧程序不能读schema2，降级回滚未验。
当前26项软件DOM、Go普通/race及双轴复审通过。浏览器/PG重跑/生产/崩溃恢复未验，真实接入最后。
运行仍使用 -layout-directory ../reporting-layouts 显式启用私有0700单进程目录；正常关闭/重开保留发布记录。
先打开或保存服务器布局→读取发布状态→发布当前已保存版本；后续保存草稿不自动替换发布版。
详见docs/S03B3_SCOPE.md、CODE_REVIEW_S03B3.md，当前证据evidence/s03b3。

以下为此前阶段历史说明：

# IDP Reporting Platform · S03-B2

2026-10-27。新增当前身份私有布局服务端保存、不可变历史、版本冲突保护与重新打开。
显式增加启动参数 -layout-directory ../reporting-layouts 才启用；目录必须私有0700。
布局文件独立于-memory查询/任务存储，普通关闭/重开后仍可读取。
本切片是可信本地文件目录/单进程，不是生产模板数据库；崩溃可留.lock，必须确认无写进程后由管理员清理。
不含业务数据/公司/身份权限，不替代来源授权。25项软件DOM及Go普通/race通过；
双轴复审通过。浏览器、生产、断电/分布式恢复未验，真实接入最后。
运行和边界见docs/S03B2_SCOPE.md，当前证据evidence/s03b2。

以下保留此前阶段说明作为历史基线：

# IDP Reporting Platform · S03-B1

2026-10-27。新增屏幕报表列布局编辑：列选择/顺序/左右对齐、元数据 JSON 导出和粘贴重新打开。
只作用于当前授权结果；不保存数据、身份或公司范围。Excel仍导出完整冻结结果。
这是本地文件布局切片，不是完整三类设计器或服务端模板发布。23项软件DOM及Go普通/race通过，
双轴复审通过。浏览器验收未完成，未重跑PG；真实接入最后。详见docs/S03B_SCOPE.md及evidence/s03b。

以下为S03-A历史基线说明：

# IDP Reporting Platform · S03-A

本轮新增独立控制台与基础报表查看器，由 Go 服务直接提供 /console/。目录、预览、
快照分页、任务跟踪/取消、恢复私有任务与 Excel 下载均调用既有同源 API。用户已明确
真实接入放在最后；后续先推进三类设计器、模板/PDF与平台运行能力。

实现通过 API 和21项软件DOM回归，但浏览器预览路由被当前环境/URL策略阻断，真实
浏览器/移动视觉/下载验收尚未完成。见 docs/CONSOLE.md、docs/VALIDATION.md。
首次运行后在本机打开 http://127.0.0.1:7810/console/ 并输入当前配置的令牌。

S02-A已交付配置化 RS256 访问令牌认证模块 identity/accessjwt，以及 Brant/Parcel 真实
接入资料核对。模块通过现有 httpapi.Authenticate 入口组合；须由可信宿主提供
issuer/audience/公钥/scope/主体映射，不自动替换下方 reporting-demo 的演示身份。
本地签名令牌及 HTTPS/合成来源验证不等于真实 IdP、两个真实业务来源或生产准入。
组合与限制见 docs/ACCESS_JWT.md，真实接入缺口见 docs/REAL_SOURCE_DISCOVERY_S02A.md，
后续真实接入合同见 docs/REAL_SOURCE_ONBOARDING.md。

独立 Go 报表后端，2026-10-26（上海时间）。已通过的 S01-B 基线增加持久来源合同、真实服务进程/PG
重启验证与完整性/竞争测试。应收账龄与运单统计均为合同模拟器的合成预汇总数据，
不连接真实 Brant/Parcel，不代表真实业务口径、SSO 或生产准入。

## 当前链路

目录 → 有界查询 → 异步快照 → 冻结分页 → 异步 XLSX → 当前授权下私有下载。
通用核心 reporting/ 与 S01-A 完全相同，仅依赖标准库；来源字段/口径位于适配器。
没有产品私有导入、local replace 或 BIP/MCP/EER 依赖。idp.local/reporting 是本地模块名，
未创建外部仓库或发布模块。

平台 PG 只保存任务、快照、产物。adapters/contractsim/pgsource 在另一个数据库持久保存
来源版本、句柄、原范围与当前授权。准备读取不选择来源事实行；后台按同一来源版本读页。
新来源版本和授权扩大不改变旧任务。撤权、币种/时区变化、过期、授权不可用或完整性
失败均拒绝披露。启动只补充缺失模拟配置，已有授权变化、停用和撤权墓碑不会被覆盖。

平台 fixture v1→v2 增加元数据 SHA-256 与 SQL/JSON 资源身份校验，保留任务/文件 ID。
它不能恢复 S01-A 从未持久化的来源句柄，也不是生产迁移资格。

## 构建与启动

固定 Go 1.26.10（go.mod 的 toolchain 行，CI 校验一致）、pgx 5.10.0、Excelize 2.11.0；依赖由 go.mod/go.sum 固定。
此前各阶段的证据用 Go 1.26.5 生成，记录保持原样。

```bash
go build -o bin/reporting-demo ./cmd/reporting-demo
export REPORTING_DEMO_TOKEN="$(openssl rand -hex 32)"
./bin/reporting-demo -profile contract-simulator -memory
```

-memory 是显式临时演示，重启丢失全部状态。默认仅监听 127.0.0.1:7810，所有 API 需要
Authorization: Bearer $REPORTING_DEMO_TOKEN；演示认证不是 OIDC。

持久模式需要两个预先创建、专用于测试的数据库和数字回环地址：

```bash
export REPORTING_S01_TEST_DSN='postgresql://postgres@127.0.0.1:55432/reporting_test_s01?sslmode=disable'
export REPORTING_S01_SOURCE_DSN='postgresql://postgres@127.0.0.1:55432/reporting_source_test_s01?sslmode=disable'
./bin/reporting-demo -profile contract-simulator -initialize-fixture
```

-initialize-fixture 通过 storage/postgres 的迁移器应用全部迁移并写入夹具 profile；生产库的迁移、回滚与角色见 docs/OPERATIONS.md。

普通/race 平台库为 reporting_test_s01 / reporting_test_s01_race，来源库为
reporting_source_test_s01 / reporting_source_test_s01_race。生产库名及非回环地址拒绝。
再次初始化不会重授已撤权限。-pause-workers 只用于恢复夹具，使任务排队而不执行；
没有 HTTP 调度或授权管理后门。游标连续性验证保留同一演示令牌；生产密钥生命周期属S04。

资产目前在 PostgreSQL 内。测试平台/来源共用一个 PG 实例、分别使用两个库，
不宣称独立主机故障域、生产角色/RLS 或对象存储已验收。

## 测试与证据

```bash
npm ci --ignore-scripts --no-audit --no-fund
npm run lint
python3 scripts/run_s03a_checks.py --go /path/to/go1.26.10/bin/go
python3 scripts/verify_s03a_workbooks.py
```

主机缺少 DSN 时，两个真实数据库入口明确 SKIP，不能计为数据库通过。设置
REPORTING_S01_REQUIRE_PG=1 后缺少必需 DSN 直接失败。

通过 Service、四个 Adapter 操作、Repository、HTTP 与 XLSX 接口验证。故障夹具只在
受保护测试库注入最终更新失败、发布期间失租约和字节损坏。scripts/run_pg16_vm.py
执行真实PG普通/race和真实HTTP进程/PG停止启动；历史工具变量见docs/VALIDATION_S01B.md。
cmd/reporting-recovery-probe 是本地验收客户端，显式 bootstrap 会清空受保护夹具；
业务服务不导入它，也不在 HTTP 上开放其控制能力。

当前逐条状态见 docs/ACCEPTANCE_S03A.json；本轮原始日志、文件与摘要在 evidence/s03a/。
S02-A历史证据在 evidence/s02a_baseline/；此前审查见 docs/CODE_REVIEW_S02A.md。
S01-B 历史证据完整保留在 evidence/s01b_baseline/，其中含原 S01-A 历史证据；
历次审查见 docs/CODE_REVIEW_S01A.md、docs/CODE_REVIEW_S01B.md。本轮不重跑 PG/VM。
压缩包附 SOURCE_HISTORY.bundle，可 git clone SOURCE_HISTORY.bundle restored-reporting
还原全部本地提交。直接解压源码可构建；含git diff的检查应在还原仓库运行。

## 后续范围

当前顺序：文档画布编辑器（S03-I）→ 模板 schema 2.0（ADR 0004：分组、版面区域、换行；S03-J 排版、S03-K 按分组出具、S03-L 区域画布）→
服务端条码/二维码/图片（ADR 0005）→ 嵌入 → S04 其余（RLS、对象存储、配额、清理、备份与容量）。最后接真实发行方/企业身份、
Brant与第二真实产品授权合同并完成数据对账。各项未验能力仍保留其验收门槛。
当前不承诺自动重试、心跳、跨产品关联或跨系统撤权/提交原子性。实际优雅重启证据
不等于崩溃、断电或备份恢复资格。
