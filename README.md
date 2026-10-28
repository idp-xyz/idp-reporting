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

以下为此前阶段历史说明：

# IDP Reporting Platform · S03-G

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

当前顺序：控制台/查看器后继续三类设计器、模板/PDF及嵌入，再推进生产迁移/回滚、
最小权限/RLS、对象存储、配额、清理、备份与容量。最后接真实发行方/企业身份、
Brant与第二真实产品授权合同并完成数据对账。各项未验能力仍保留其验收门槛。
当前不承诺自动重试、心跳、跨产品关联或跨系统撤权/提交原子性。实际优雅重启证据
不等于崩溃、断电或备份恢复资格。
