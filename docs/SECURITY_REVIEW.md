# 安全审查指引

2026-11-06。本文给人工安全审查用：按信任边界列出每处要成立什么、由哪段代码保证、哪些测试证明、审查时该看什么，以及已知没有覆盖的地方。它不是审查结论。上线准入（docs/DEPLOYMENT.md 上线清单第 19 项）之前，建议由人按本文逐项看过，结论另写一份记录。

文中的函数名与测试名比行号稳定；行号指 2026-11-06 的 main。

## 先读

- docs/s00/ARCHITECTURE.md：来源产品负责事实与授权，平台只存自己的资产。
- ADR 0003（出具由来源产品确认）、ADR 0006（嵌入）、ADR 0007（对象存储）、ADR 0008（控制台的安全姿态）、CODING_STANDARDS.md。
- 平台的四条立场，下面各节都是它们的落实：
  1. 结果在读取时授权：查看、翻页与下载时服务端都再问来源产品当前授权；撤权、过期、授权不可用或完整性失败都拒绝披露。
  2. 存储里的每次披露都限定到所有者；PostgreSQL 再用行级安全兜底。
  3. 调用方不能在请求里提供身份、权限或预算。
  4. 日志、拒绝信息与指标只带计数和代码，不带凭据、令牌或数据值。

## 1. 访问令牌

要成立的：只有配置的发行方签发、发给本平台、带所需 scope、仍在有效期内的访问令牌能换成身份；身份来自配置里的绑定表，不来自令牌里的任意字段。

- 代码：`identity/accessjwt/verifier.go`。认证在 `Authenticate`（122 行起），JWKS 解析在 `publicKeys`，原子替换在 `Replace`。
- 规则（审查时逐条对照代码）：
  - 凭据只取 `Authorization: Bearer`，长度有上限；三段式，头部恰好 `alg`、`typ`、`kid` 三项；`alg` 只认 RS256，`typ` 只认 `at+jwt`；`kid` 必须是已配置的密钥。
  - 先验签名（RSA PKCS#1 v1.5、SHA-256），再解析载荷。
  - 载荷带 `act` 或 `cnf` 即拒绝；`iss` 精确相等，`aud` 含本平台，`jti` 必须有。
  - `iat ≤ 现在 < exp`，`exp - iat ≤ max_token_lifetime`，`nbf` 若有须已到。没有时钟偏差容忍：发行方的时钟快一秒，令牌就被拒绝。
  - 身份 = 绑定表里 (`sub`, `client_id`) 对应的那一项；scope 不够时回答 `insufficient_scope`，不当作未认证。
  - JWKS 只认 RSA，模长 2048–4096 位，指数只认 65537，最多 16 把，未知字段拒绝。
  - 重载（SIGHUP）原子替换整套配置，失败时旧配置继续生效，进行中的认证按旧配置完成。
- 测试：`identity/accessjwt` 的 18 个测试，尤其 `TestRejectJOSEAndCompactTokenAttacks`、`TestRejectInvalidAccessTokenClaims`、`TestAnUntrustedKidIsRefusedWithoutNamingAKey`、`TestTokenWithoutReportingScopeIsDenied`、`TestOnlyAuthorizationHeaderProvidesCredentials`、`TestFailedReplaceLeavesThePreviousConfigurationInForce`、`TestAStolenTokenIsBoundedByItsLifetimeAndNotRevocable`；`server` 的 `TestReloadAppliesRotatedKeysAndBindingsAndKeepsTheOldOnesOnFailure`、`TestSessionOverTLSWithRealTokens`；`transport/httpapi` 的 `TestHTTPAuthenticationFailureChallengesWithoutLeakingCredentials`。
- 审查时看：这是只用标准库手写的 JWT 校验，没有用现成的库，值得逐行看——每个解析都有上限、比较都是精确的、每条失败路径都走 `denied()`。
- 已知不覆盖：没有吊销，被盗的令牌只受寿命限制；JWKS 不自动发现，靠文件与重载；真实发行方未接入（docs/ACCESS_JWT.md，上线清单第 11、16 项）。

## 2. 演示服务

要成立的：演示只在本机用，不能变成生产入口。

- 代码：`cmd/reporting-demo/main.go` 只接受回环监听地址（230 行）；`transport/httpapi/api.go` 开头的演示认证要求恰好一个 `Authorization` 头，用 `hmac.Equal` 常数时间比较；PostgreSQL 夹具只接受固定库名与回环主机。生产服务端拒绝自称模拟器的适配器，除非配置写明 `allow_simulator`（docs/DEPLOYMENT.md，`server/start_test.go`）。
- 测试：`TestFixtureRefusesOtherDatabaseAndRemoteHost`、`TestTheDemosLogCarriesNoTokenAndNoKey`、`TestDemoCursorKeysAreDerivedFromTheTokensAndNeverNamedInARefusal`、`TestStartRefusalsNameTheStepAndCarryNoSecret`。
- 已知不覆盖：演示令牌是一个共享密钥，不是 OIDC。

## 3. 授权与隔离

要成立的：一个身份只能看到、改动自己的任务、快照、产物、模板、看板、布局与图片，而且只能看到来源产品此刻仍授权给它的数据。

- 来源授权：查询、翻页（`Rows`）、下载（`Download`）、提交导出与出具、查看任务都经 `reporting/service.go` 的 `current` 调用适配器的 `AuthorizeCurrent`，再问来源产品；worker 执行任务前也再问一次；文档预览与检查在渲染之后、回答之前再确认一次。测试：`TestDocumentPreviewPrivateFrozenAndReauthorizedAfterRender`、`TestDocumentChecksAuthorizeAgainBeforeAnswering`、`TestBandedTemplatesAreAuthorizedAgainstTheDataset`、`TestEveryReferenceOfACodedTemplateIsAuthorized`。
- 所有者限定：测试 `TestImagesAreUploadedListedAndServedToTheirOwnerOnly`、`TestTemplateMetadataCurrentTargetRightsAndOwner`、`TestDashboardRoutesPrivateCurrentCatalogAndSeparateFromLayouts`、`TestQueriesInFlightBoundOneIdentityAndLeaveAnotherAlone`。
- 调用方不能提供权限：严格 JSON 拒绝权限字段、`null`、重复键与多余的根。测试：`TestStrictJSONRejectsAuthorityNullDuplicatesAndExtraRoots`、`TestHTTPRejectsUntrustedAuthorityAndAmbiguousJSON`。
- 行级安全（迁移 007）：每个事务设置身份上下文，没有上下文就没有行；清理只在 worker 范围里跨身份删除。启动与就绪检查（`storage/postgres/production.go` 的 `rlsProblems`、`RoleProblems`）拒绝被关掉的行级安全、被删改或新增的策略，以及带 `BYPASSRLS` 的平台角色。测试：`integration/rls_test.go`，由 `TestPostgresMigrations` 的 `row_level_security` 子测试运行；其中 `the_mistake_on_purpose_leaks_nothing_under_a_context`（故意漏写所有者条件也不泄露）最关键。
- 角色：迁移角色、应用角色（SELECT、INSERT、UPDATE，不能删除）、可选的清理角色；授权语句在 `Grants` 与 `GrantSweeper`。
- 审查时看：新增的仓库方法是否都带所有者条件；新增的表是否有策略，并且被 `rlsProblems` 检查到。
- 已知不覆盖：数据库超级用户与表的所有者天然绕过行级安全（docs/OPERATIONS.md “Row-level security”）。

## 4. HTTP 边界

- 请求体：读取前用 `http.MaxBytesReader` 限制。测试：`TestEveryRouteThatReadsABodyRefusesAnOversizedOneBeforeParsingIt`。
- 并发与配额：超过 `max_in_flight` 回答 503 `OVERLOADED`（`transport/httpapi/inflight.go`）；每身份的配额在数据库时钟与身份锁下计数。测试：`TestMaxInFlight*`、`TestQuota*`、`TestAdmissionCountsByTheDatabasesClockUnderTheIdentitysLock`。
- 响应头：每个 API 回答都带 `Cache-Control: no-store`、`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY` 与 `frame-ancestors 'none'`（`transport/httpapi/api.go` 的 `secured`）；上传的图片以 `default-src 'none'; sandbox` 提供（`transport/httpapi/document_assets.go`）。测试：`TestEveryRouteAnswersWithTheSecurityHeaders`。
- 拒绝信息只带代码与位置，不回显值。测试举例：`TestIssueByGroupRefusalsAreThePreviewsAndCarryNoValue`、`TestIssuerRefusesAGroupWhoseFieldDiffersWithoutNamingTheValue`。
- 分页游标：由密钥环 HMAC-SHA256 签名；验证时环里每把密钥都算一遍、不提前退出，耗时不透露是哪把签的（`reporting/service.go`）。快照过期后游标失效。测试：`TestCursorRing*`、`TestPaginationContinuesAcrossARestartThatRotatesTheCursorKey`、`TestACursorStopsWorkingWhenItsSnapshotExpires`。

## 5. 控制台与嵌入页

要成立的：页面上的任何数据（列名、单元格、模板名）都只作为文本显示；令牌只在内存；只有配置的产品来源能把嵌入页放进框架、与它交换消息。

- 响应头：`console/handler.go` 的 `policy` 是 `default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; object-src 'none'`，`frame-ancestors` 平时是 `'none'`，只有嵌入页对配置的来源放开；另有 `X-Frame-Options: DENY`、`nosniff` 与 `Referrer-Policy: no-referrer`。测试：`console` 包的 7 个测试。
- 不写标记：控制台只用 DOM API 建节点（ADR 0008）；ESLint 拒绝 `innerHTML`、`outerHTML`、`insertAdjacentHTML`、`eval` 与 `new Function`（`eslint.config.mjs`，CI 执行）；DOM 测试用 `<img src=x onerror=…>` 这样的名称检查它们只显示为文本。
- 令牌：只在内存，不进 URL、浏览器存储、日志或任务；退出或 401 清空页面数据（docs/CONSOLE.md）。下载的文件先在浏览器里用 Web Crypto 核对 SHA-256，再交给用户（`console/assets/app.js`）。
- 嵌入：允许的来源是精确的序列化来源，http 只允许回环地址且要显式打开（`embedding/embedding.go`；`TestAcceptsOnlyExactSerializedOrigins`、`TestLoopbackHTTPOnlyWhenAskedAndOnlyForLoopbackAddresses`、`TestNilAndEmptyAllowNothing`）。嵌入页只处理来自父窗口、来源正确的消息（`console/assets/embed.js` 的 `onMessage`，65 行）；发给父窗口的消息是封闭列表、带精确的目标来源、不带值；不在框架里或地址不对时不通信（`console/embed.dom.test.mjs`）。
- 审查时看：`console/assets/app.js` 里每一处把服务端回答写进页面的地方；`console/assets/embed.js` 的消息处理。

## 6. 渲染与导出

要成立的：模板与数据都来自用户，渲染它们不能执行代码、读不该读的文件或访问网络。

- Typst 进程（`export/pdf/run_linux.go`）：新的用户、网络与 IPC 命名空间（没有网络）；父进程退出即 SIGKILL；内存、文件大小与 core 有上限；`--root` 是每个任务自己的临时目录；字体只用指定目录（`--ignore-system-fonts`、`--ignore-embedded-fonts`）。可执行文件在 `export/pdf/writer.go` 的 `New` 里按 SHA-256 核对，执行的是已打开的那个文件（`/proc/self/fd/3`），之后被改动或替换即拒绝；他人可替换的目录也拒绝。测试：`TestNewChecksTheBinaryAndTheLimits`、`TestUnchangedDetectsModifiedOrReplacedFiles`、`TestDirectoriesOthersCouldReplaceAreRefused`、`TestNewChecksTheHostBeforeAnythingElse`、`TestTypstFailsClosed`、`TestTypstWithAHardCoreLimitOfZero`、`TestAcquireLimitsConcurrentRenders`。
- 没有模板注入：Typst 源码是平台内置的固定文件（`export/pdf/template.typ`、`export/pdf/document.typ`），用户的模板与数据只写进 `data.json`，由 `json("data.json")` 读取；图片在临时目录里的文件名由平台按摘要生成（`image-<摘要前 8 字节>.<扩展名>`，`export/pdf/coded.go`），不来自调用方。审查时看：`.typ` 文件里没有 `eval`，`data.json` 的值只作为文本、数字与平台生成的文件名使用。
- 图片（PNG 或 JPEG）与底图（PNG）：都先按大小拒绝（各 512 KiB），再只读图片头取尺寸；图片每边最多 2000 像素，渲染前按模板记下的 SHA-256 核对（`doctemplates/images.go` 的 `ImageInfo`，`doctemplates/assets.go`；`TestAnImageIsContainedAndCheckedAgainstItsDigest`）。
- XLSX：每个值都写成文本单元格（`export/xlsx` 用 `SetCellStr`），不生成公式；没有 CSV 导出。
- CI 用 `scripts/check_pdf_export.py`（含反例）与 veraPDF 检查生成的 PDF。

## 7. 正式出具

要成立的：编号与确认只来自来源产品；一个任务最多对应一个确认的编号；任何结果都能与来源产品对账（ADR 0003）。

- 代码：`issuance/issuer.go` 只渲染已发布的冻结版本；worker 以租约与代际围栏发布结果，旧代际的写入失败。
- 测试：`integration/fencing_test.go` 的 `TestLeaseGenerationAndAtomicPublication`；`reporting/issue_test.go`（`TestIssueFailureDoesNotVoidWhatAnotherOutcomeOwns`、`TestIssueAdapterAnsweringWithTheLeaseErrorIsAnIntegrityFailure`）；`TestIssueRefusedByTheLayoutLeavesNothingReservedAtTheProduct`、`TestIssuerRendersOnlyThePublishedFrozenVersion`；PostgreSQL 上的 `TestDurablePostgresIssues`。
- 已知不覆盖：还没有任何真实产品的出具合同与对账（上线清单第 17、18 项）。

## 8. 存储、密钥与日志

- 数据库连接：生产存储要求 `sslmode=verify-full`；连接串解析失败时不回显它（可能含密码）。代码：`storage/postgres/production.go`；测试：`TestOpenProductionRefusalsCarryNoPassword`。
- 对象存储（`storage/objects/`）：只接受 https，可以指定只信任某个 CA；http 只允许回环地址且需显式参数；密钥只从环境变量读，给密钥的参数被拒绝；不跟随重定向，每个请求有超时；取回的对象先按库里记录的长度与摘要核对，再返回。签名（SigV4）是手写的，用规范公开的示例测试。测试：`TestSigV4MatchesTheSpecificationsPublishedExamples`、`TestHTTPSIsVerifiedAgainstTheNamedCAAlone`、`TestRedirectsAreNotFollowed`、`TestGetChecksTheObjectAgainstTheRowBeforeReturningAByte`、`TestErrorsCarryNoBodyKeyEndpointOrCredential`、`TestTheKeysAreTheEnvironmentsAndAFlagForEitherIsRefusedWithoutRepeatingIt`。
- 密钥文件：必须是私有的普通文件，不能是符号链接；公开的文件不能被他人写（`server/files.go`）；私有目录要求 0700。测试：`TestSecretFilesMustBePrivateRegularAndNotSymlinks`、`TestPublicFilesMustNotBeWritableByOthers`。
- 日志、拒绝信息与指标只带计数与代码。测试：`TestConfigRefusalsCarryNoSecret`、`TestCheckConfigPrintsCountsAndNoValue`、`TestWorkerFailuresAreLoggedByCodeOnly`、`TestAPanicInAHandlerIsLoggedWithoutItsValue`。`/metrics` 默认关闭，只能监听回环地址、不能与 API 同端口，标签只有服务端选定的值（`server/metrics.go`，`TestMetricsListener`）。
- 审查时看：SigV4 与 JWT 校验一样是手写的；新加的日志行是否只带代码。

## 9. 依赖与构建

- Go：`go.mod` 与 `go.sum` 固定，CI 执行 `go mod verify`；工具链固定为 1.26.10（`GOTOOLCHAIN=local`）；核心 `reporting/` 只依赖标准库（CI 检查）。
- Node 只用于开发与测试：版本精确固定，`package-lock.json` 锁定，`npm audit` 为 0（`minimatch` 用 `overrides` 固定在已修复的版本，原因写在 `eslint.config.mjs`）。控制台运行时不加载第三方代码。
- Typst 与字体按 SHA-256 固定（`scripts/fetch_pdf_tools.py`），服务端启动时再核对一次可执行文件。
- CI：第三方 action 按完整提交 SHA 固定，令牌只读，不用仓库密钥；容器镜像（PostgreSQL、S3 兼容服务、veraPDF）按摘要固定；PDF 检查的 Python 依赖用 `--require-hashes` 安装。

## 已知没有覆盖的

- 真实发行方、真实适配器、数据对账与准入决定（上线清单第 16–19 项）。
- 令牌吊销与 JWKS 自动发现；时钟偏差容忍为零。
- 追踪（tracing）；TLS 证书自动续期；systemd 单元示例未经测试（上线清单第 13–15 项）。
- 容量数字是一台机器上的合成测量，不是生产规划（docs/CAPACITY.md）。
- 演示只有一个可登录的身份；跨身份隔离只在 Go 测试里验证（docs/TEST_SCENARIOS.md）。
- 仓库里没有渗透测试或第三方安全审查的记录。

## 怎么跑这些测试

```bash
go test ./...                                    # 没有数据库、对象存储或 Typst 的测试会明确 SKIP
REPORTING_S01_REQUIRE_PG=1 go test -p 1 ./...    # 再设置两个测试库的 DSN（README“构建与运行”），行级安全、出具与围栏才会运行
npm run lint
node --test console/dom.test.mjs console/embed.dom.test.mjs
```

对象存储与 Typst 的测试另需 `REPORTING_OBJECT_STORE_TEST_*` 与 `REPORTING_TYPST*` 环境，写法照 `.github/workflows/ci.yml`。
