# IDP Reporting Platform

IDP 产品的独立报表平台：一个 Go 服务端和它自带的网页控制台。来源产品负责业务事实、授权、单据编号与确认；平台通过适配器读取，在当前授权下做查询、快照、报表布局、看板、文档模板、Excel/PDF 导出与单据预览，正式单据在来源产品确认编号后出具（ADR 0003）。平台不修改来源的事实，也不自己编号。

## 现状（2026-11-06）

- 最近一轮是 ADR 0011：三个设计器统一版本与发布（S07-G），报表工作台成为首页、数据中心并入数据抽屉（S07-H）。数据库迁移到 010。历次切片见 docs/HISTORY.md。
- 数据只来自合同模拟器 `adapters/contractsim`：应收账龄与运单两套合成数据，数据与授权规则写死在代码里（docs/TEST_SCENARIOS.md）。仓库里没有任何真实来源产品的适配器。
- 生产服务端 `reporting-server` 已有配置、密钥、健康与就绪、日志、重载、优雅停机与可选的 `/metrics`，但这不是上线准入：`GET /capabilities` 的 `production_approved` 恒为 false。真实发行方（IdP）、真实适配器、数据对账与准入决定仍未完成，见 docs/DEPLOYMENT.md 上线清单第 16–19 项。
- 当前不承诺自动重试、心跳、跨产品关联或跨系统撤权/提交原子性；出具改为保证每个结果都能与来源产品对账（ADR 0003）。本机的重启、kill -9 与备份恢复演练是一台机器上一次运行的证据，不是资格。

## 控制台

服务端在 `/console/` 提供控制台，不需要前端构建（docs/CONSOLE.md）。应用栏有六个入口：报表工作台（首页：查询、预览、快照、导出与报表布局）、看板工作室、模板中心（自己的模板与 37 个只读样例）、文档工作室（文档模板的画布、预览与出具）、任务面板、能力与环境；“帮助”打开随服务提供的用户手册 `/console/manual/`。三个设计器共用一个版本与发布模型（ADR 0011）。产品页面可以用框架嵌入同一组视图（ADR 0006，docs/EMBEDDING.md）。

## 代码结构

| 目录 | 内容 |
|---|---|
| `reporting/` | 领域中立的核心：查询、任务、快照与私有资产；只依赖标准库（CI 检查） |
| `adapters/contractsim/` | 合同模拟器，即合成的来源产品；`pgsource` 在单独的数据库里持久保存来源版本与授权 |
| `adapters/httpsource/` | 通用来源适配器 `http`（S19-A）：来源产品实现固定的 HTTPS 协议（docs/SOURCE_HTTP_PROTOCOL.md；1.1 版加入可选的公司目录路由，ADR 0015，S19-B）即可接入；`sourcetest` 是协议的参考实现，`conformance` 与 `cmd/reporting-source-check` 是产品方自检工具 |
| `server/`、`cmd/reporting-server/` | 生产组合：配置文件、密钥、健康与就绪、日志、重载、停机、指标（docs/DEPLOYMENT.md） |
| `cmd/reporting-demo/` | 本机演示：合同模拟器、回环地址、随机令牌 |
| `cmd/reporting-migrate/` | 运维的存储工具：迁移、角色、profile、保留期清理、`verify-objects`（docs/OPERATIONS.md） |
| `storage/` | 平台存储：内存、PostgreSQL（迁移 001–010）、对象存储 |
| `transport/httpapi/` | HTTP API（docs/API.md） |
| `identity/accessjwt/` | RS256 访问令牌（docs/ACCESS_JWT.md） |
| `tracecontext/` | W3C Trace Context：`traceparent` 的严格解析、请求的 span、适配器发给来源产品的头（docs/S14A_SCOPE.md）；只依赖标准库 |
| `export/` | XLSX、PDF（固定版本的 Typst，PDF/A-2b）与条码；`present` 是两种导出共用的表现层（布局的列、标签、格式、小计与标记，ADR 0024） |
| `doctemplates/`、`issuance/`、`privatedocs/` | 文档模板与样例包、出具的渲染、私有版本目录 |
| `layouts/`、`dashboards/` | 报表列布局、看板 |
| `embedding/` | 谁可以在框架里显示平台的页面（ADR 0006） |
| `console/` | 控制台与嵌入页的静态文件，DOM 测试 |
| `integration/`、`internal/` | 跨包测试，测试夹具与工具 |
| `scripts/` | 检查、证据与真实浏览器脚本 |

`cmd/` 另有 `reporting-samples`（把原型模板转换成样例）与 `reporting-recovery-probe`（本地夹具的恢复验收客户端）。

## 构建与运行

Go 固定为 1.26.10（`go.mod` 的 toolchain 行，CI 检查两者一致），依赖由 `go.mod` 与 `go.sum` 固定。本机演示：

```bash
go build -o bin/reporting-demo ./cmd/reporting-demo
export REPORTING_DEMO_TOKEN="$(openssl rand -hex 32)"
./bin/reporting-demo -profile contract-simulator -memory
```

浏览器打开 http://127.0.0.1:7810/console/ ，输入令牌。演示只监听回环地址，演示令牌不是 OIDC；`-memory` 重启即清空。PDF 导出与预览要加 `-pdf-typst`、`-pdf-typst-sha256` 与 `-pdf-fonts`（固定版本的 Typst 与字体，`scripts/fetch_pdf_tools.py` 下载并校验）；模板、看板、布局与底图要在重启后保留，加四个私有目录参数（`-template-directory` 等，见 `-h`）。测试模式与造数脚本见 docs/TEST_SCENARIOS.md。

演示也可以用两个受保护的 PostgreSQL 测试库代替 `-memory`（`REPORTING_S01_TEST_DSN`、`REPORTING_S01_SOURCE_DSN`，首次加 `-initialize-fixture`）；它只接受固定的夹具库名与回环地址，拒绝生产库。

生产用 `reporting-server`（docs/DEPLOYMENT.md）与 `reporting-migrate`（docs/OPERATIONS.md）。发行的命令只注册通用来源适配器 `http`（S19-A）：来源产品实现 docs/SOURCE_HTTP_PROTOCOL.md 并通过 `reporting-source-check`；有自己 Go 适配器的宿主仍可用三行 `main` 注册。

## 测试

每个拉取请求和 main 上的每次推送跑五个 CI 作业（`.github/workflows/ci.yml`）：

| 作业 | 内容 |
|---|---|
| Go | gofmt、vet、build、测试与 race；核心 `reporting/` 只依赖标准库 |
| PostgreSQL | 合同与持久化测试（普通与 race），对一次性的 PostgreSQL 16 与 S3 兼容服务 |
| Console | `node --check`、ESLint、DOM 测试（jsdom，经真实 Go 服务） |
| PDF | 固定的 Typst 与字体：Go 的 Typst 测试、整套 DOM 测试、PDF 检查器（含反例与 37 个样例）、veraPDF（PDF/A-2b）。PR 只在改动触及 PDF 路径（渲染、出具、预览、控制台、检查器及其钉定）时全量运行，否则直接通过；推送到 main 与每日 03:30 的定时运行永远全量（race 版 Typst 测试也只在这两处跑） |
| Browser | 真实 Chrome：控制台各页、模板中心，以及钉在 docs/UX_BASELINE_S07.md 的任务基线 |

本机（Node 24.22.0，与 CI 相同）：

```bash
go vet ./... && go test ./...
npm ci --ignore-scripts --no-audit --no-fund
npm run lint
node --test console/dom.test.mjs console/embed.dom.test.mjs   # 会构建并启动真实的 Go 演示服务
npm run format:check                                           # Prettier（ADR 0014）；npm run format 写回
```

缺少数据库、对象存储或 Typst 时，相应测试明确 SKIP，不能算作通过；`REPORTING_S01_REQUIRE_PG=1`、`REPORTING_OBJECT_STORE_REQUIRE=1` 与 `REPORTING_PDF_REQUIRE=1` 让缺少的环境直接失败。CI 用 `scripts/go_test_strict.py` 跑 Go 测试，跳过的测试必须恰好是预期的那些。真实浏览器脚本也可以对本机 Chrome 运行：`CHROME_RUNNING=1 GO=$REPORTING_GO scripts/ci_browser_evidence.sh OUT`（docs/CONSOLE.md）。

## 文档

| 想知道 | 看 |
|---|---|
| 架构基线与接口合同 | docs/s00/ARCHITECTURE.md、docs/s00/INTERFACES.md |
| 设计决定 | docs/ADR_0001_PDF_ENGINE.md … docs/ADR_0024_TABLE_REPORT_FORMATTING.md（0021 为提案） |
| API | docs/API.md |
| 控制台与设计器 | docs/CONSOLE.md、docs/STUDIO.md，用户手册 docs/manual/ |
| 嵌入 | docs/EMBEDDING.md |
| 部署与上线清单 | docs/DEPLOYMENT.md |
| 存储、迁移、角色、行级安全、配额、对象存储、备份与恢复 | docs/OPERATIONS.md |
| 容量 | docs/CAPACITY.md |
| 访问令牌 | docs/ACCESS_JWT.md |
| 安全审查：每条信任边界的代码、测试与已知缺口 | docs/SECURITY_REVIEW.md |
| 接入真实来源产品 | docs/REAL_SOURCE_ONBOARDING.md，协议 docs/SOURCE_HTTP_PROTOCOL.md（docs/source-openapi.json） |
| 本机测试场景与造数 | docs/TEST_SCENARIOS.md |
| 任务基线 | docs/UX_BASELINE_S07.md |
| 样例包 | docs/SAMPLES.md |
| 历次切片 | docs/HISTORY.md；各切片的 docs/S0x_SCOPE.md、docs/CODE_REVIEW_*.md 与 evidence/ |
| 工程规则 | CODING_STANDARDS.md |
