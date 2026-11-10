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
  - 绑定里的每个产品必须有该租户的连接（ADR 0016）：启动与 `check-config` 拒绝，重载以 `ISSUER_BINDING_UNSERVED` 拒绝；`GET /session` 只列出有连接的产品。启动行与重载行记录绑定文件与连接的 SHA-256 和增删改数目，不记任何标识（`server/connections_test.go`）。
- 测试：`identity/accessjwt` 的 18 个测试，尤其 `TestRejectJOSEAndCompactTokenAttacks`、`TestRejectInvalidAccessTokenClaims`、`TestAnUntrustedKidIsRefusedWithoutNamingAKey`、`TestTokenWithoutReportingScopeIsDenied`、`TestOnlyAuthorizationHeaderProvidesCredentials`、`TestFailedReplaceLeavesThePreviousConfigurationInForce`、`TestAStolenTokenIsBoundedByItsLifetimeAndNotRevocable`；`server` 的 `TestReloadAppliesRotatedKeysAndBindingsAndKeepsTheOldOnesOnFailure`、`TestSessionOverTLSWithRealTokens`；`transport/httpapi` 的 `TestHTTPAuthenticationFailureChallengesWithoutLeakingCredentials`。
- 审查时看：这是只用标准库手写的 JWT 校验，没有用现成的库，值得逐行看——每个解析都有上限、比较都是精确的、每条失败路径都走 `denied()`。
- 已知不覆盖：没有吊销，被盗的令牌只受寿命限制；JWKS 不自动发现，靠文件与重载；真实发行方未接入（docs/ACCESS_JWT.md，上线清单第 11、16 项）。
- 浏览器登录（S15-A，ADR 0019）：要成立的是“登录只是拿到令牌的另一种方式，不新增信任”。代码：`identity/signin/signin.go`（`Check`、`Exchange`）、`transport/httpapi/signin.go`（`WithSignIn`）、`console/assets/app.js` 的 `takeSignInReturn`、`setupSignIn`、`beginSignIn`。要点：两个路由不认证；授权码只在服务端向配置的令牌端点兑换（HTTPS、可配 CA、不跟随重定向、超时 10 秒、回答 ≤ 64 KiB、同时 ≤ 16 个）；换到的访问令牌先经 `Authenticate` 判过才交给页面，之后每个请求照旧验证；ID 令牌只比较 nonce，不用于授权，与刷新令牌一起丢弃；同一 code 在本进程内十分钟内只兑换一次；回调地址是唯一配置值（`…/console/`），服务端从不重定向；拒绝只回固定代码，日志只记结果词。浏览器端：PKCE 校验串、state 与 nonce 只在一次跳转期间存在本标签页 sessionStorage 的一项里，页面加载时先读出并删除，code 立即从地址去掉；state 不符、无记录或超时则不发请求。机密客户端的密钥只来自环境变量 `REPORTING_SIGN_IN_CLIENT_SECRET`，配置文件拒绝像密钥的字段名。测试：`identity/signin` 的 `TestAGoodCodeIsExchangedOnce`、`TestRefusalsOfTheProvider`、`TestAnswersThatAreNotATokenAnswer`、`TestTimeoutsAndUnreachableProviders`、`TestAConcurrentReplayAndTheBoundOnExchanges`、`TestConfigurationRules`；`transport/httpapi` 的 `TestSignInExchangeAnswersFixedCodes`、`TestSignInExchangeEndToEndWithAFakeProvider`；`server` 的 `TestServerSignInWithAFakeProvider`、`TestConfigSignInSection`；DOM 测试中以 `S15-A:` 开头的五个。审查时看：`Exchange` 的每条失败路径是否只回固定错误、`nonceMatches` 只做比较；控制台在发出兑换请求前的 state 检查；`WithSignIn` 判令牌时用的是部署自己的 `Authenticate`。

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
- 令牌：只在内存，不进 URL、浏览器存储、日志或任务；退出或 401 清空页面数据（docs/CONSOLE.md）。浏览器登录（S15-A）只在跳转到身份提供方的那一次导航期间把 PKCE 校验串、state 与 nonce 存在 sessionStorage 的一项里，页面加载时先删除；登录后存储为空（DOM 测试断言）。下载的文件先在浏览器里用 Web Crypto 核对 SHA-256，再交给用户（`console/assets/app.js`）。
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
- 追踪（S14-A，`tracecontext/`）：只读一个 `traceparent` 头，按规范严格解析（小写十六进制、长度、全零 ID、版本 `ff`），只保留两个 ID 与 sampled 位；无效或多个值就开始新的追踪。`tracestate` 不读、不记、不回、不转发（调用方可放任意内容，来源产品在信任边界之后）。调用方能选 `trace_id`，所以它只用于关联，不证明什么；`request_id` 始终由服务端生成。测试：`tracecontext` 包的解析测试、`TestTheServerSpanContinuesAValidTraceparentAndReachesTheAdapter`、`TestAJobRunsInATraceOfItsOwnAndItsFailureNamesIt`。
- 审查时看：SigV4 与 JWT 校验一样是手写的；新加的日志行是否只带代码。

## 9. 依赖与构建

- Go：`go.mod` 与 `go.sum` 固定，CI 执行 `go mod verify`；工具链固定为 1.26.10（`GOTOOLCHAIN=local`）；核心 `reporting/` 只依赖标准库（CI 检查）。
- Node 只用于开发与测试：版本精确固定，`package-lock.json` 锁定，`npm audit` 为 0（`minimatch` 用 `overrides` 固定在已修复的版本，原因写在 `eslint.config.mjs`）。控制台运行时不加载第三方代码。
- Typst 与字体按 SHA-256 固定（`scripts/fetch_pdf_tools.py`），服务端启动时再核对一次可执行文件。
- CI：第三方 action 按完整提交 SHA 固定，令牌只读，不用仓库密钥；容器镜像（PostgreSQL、S3 兼容服务、veraPDF）按摘要固定；PDF 检查的 Python 依赖用 `--require-hashes` 安装。

## 审计（S12-B，ADR 0018）

- 要成立的：发布、撤回与正式出具在执行前写入审计，写不进就拒绝（503 `AUDIT_UNAVAILABLE`）；其余操作执行后尽力记录结果；审计只增不改。
- 代码：`transport/httpapi/api.go`（`authenticated` 中的 `AuditAction` 与记录）、`storage/postgres/audit.go`、迁移 012（行级安全：本身份读写自己的事件；`reporting.audit = 'tenant'` 时只读同租户全部事件，服务端仅为管理员设置）。
- 测试：`TestAuditRecordsActionsAndListsThem`、`TestPostgresAuditContract`、行级安全测试 `the_audit_is_read_by_its_identity_or_an_administrator_and_never_rewritten`。
- 已知不覆盖：审计不记录读取查询与预览；审计的保留期由运营方在平台外决定。

## 团队共享空间（S12-C，ADR 0018）

- 要成立的：团队模板只在本租户内可见；保存者不能发布自己保存的版本（审批）；没有人能以团队身份登录。
- 做法：共享空间是保留主体 `~team` 的所有者，行级安全仍按租户与发行方隔离（与任何身份的行相同）；租户内谁能读、改、发布由服务端角色决定。每个版本记录保存者的摘要（`reporting.ActorDigest`），接口只回答 `saved_by_me`。
- 代码：`transport/httpapi/private_documents.go`（`docSpace`、`mountSpacedDocuments`）、`privatedocs.PublishAs`、`issuance/issuer.go`（团队模板的发布核对）。
- 测试：`TestTeamSpaceIsSharedWithinTheTenantAndPublicationIsAnApproval`、`TestPublicationOfASharedVersionIsAnotherPersonsApproval`、DOM S12-C。
- 已知不覆盖：只覆盖单据模板；看板与报表布局的团队空间、团队成员分组在后续切片。
## 定时报表与投递（S16-A，ADR 0020）

- 要成立的：定时运行以所有者当时生效的绑定提交与本人相同的快照/导出作业（同一准入、配额与来源产品授权）；绑定被移除、产品被移除、缺少 `viewer` 角色或来源拒绝时运行被拒绝；同一触发时刻在多台服务器上只运行一次；投递只含编号与结果，不含报表数据，用环境变量中的密钥做 HMAC-SHA256 签名，只投 https（测试可用回环 http），不跟随重定向；投递端点由部署配置，没有任何接口可以读写它；定时任务从不正式出具。
- 代码：`reporting/schedule.go`、`reporting/scheduler.go`、`delivery/webhook/webhook.go`、`identity/accessjwt`（`Bound`）、`storage/postgres/schedules.go`、迁移 013（行级安全同 007）、`transport/httpapi/schedules.go`。
- 测试：`TestAScheduledRunActsAsItsOwnerNow`、`TestSchedulersOnOneStoreRunAnOccurrenceOnce`、`TestPostgresScheduleContract`、行级安全测试 `schedules_and_runs_are_their_identitys_and_the_schedulers`、`delivery/webhook` 的签名与重定向测试、`TestTheServerRunsSchedulesAndDeliversThem`（日志中不出现端点、密钥与报表名称）。
- 已知不覆盖：投递至少一次（接收方按运行编号去重）；运行记录的保留期（S16-B）；邮件与按租户的端点（S16-B）。

## 管理员只读视图（S18-B）

- 要成立的：只有本租户的管理员能读；只读；不出现任何密钥、适配器选项、密钥标识以外的密钥材料，推送地址只出现主机。
- 做法：`/admin/` 下的每条路由（含读取）都要求 administrator 角色（`RequiredRole`），拒绝记入审计；视图按调用者的租户构造，返回别的租户即完整性失败；
  服务器在重新加载锁下读取生效的绑定。
- 代码：`transport/httpapi/admin.go`、`server/admin.go`、`cmd/reporting-demo`（`demoAdminView`）。
- 测试：`TestTheAdministratorsViewIsTheAdministratorsAlone`、`TestTheServerShowsTheConfigurationInForceToAnAdministrator`（推送地址的路径、查询与签名密钥、
  适配器选项都不出现）、DOM S18-B。
- 已知不覆盖：没有写入（ADR 0021 提议，未决定）；管理员看得到本租户每条绑定的主体与客户端标识，这是有意的。

## 通用来源适配器与来源协议（S19-A，docs/SOURCE_HTTP_PROTOCOL.md）

- 要成立的：来源产品通过固定协议接入后，平台的任何保证都不变——来源决定每次读取；来源的回答在验证通过之前不进入平台；来源不可用、超时或越出协议都关闭披露；凭据、来源地址与回答内容不进日志、错误与响应。
- 做法：只用 HTTPS（TLS ≥ 1.2；`ca_file` 替换系统根证书即固定 CA；可选 mTLS），`http://` 只允许回环 IP 且需 `loopback_http`；不跟随重定向，不读环境代理；每个请求有超时（默认 10 秒，至多 60 秒）、请求 1 MiB、回答至多 1 MiB 的上限；回答用 `reporting.DecodeStrict` 严格解码（未知字段、大小写不同、`null`、重复键、尾随数据都拒绝）。每个回答都对照所问逐项验证：目录形状（`ValidateQuery`）、读取的查询摘要、数据集、公司、时区、币种政策、水位与 15 分钟内的到期、页的引用/摘要/水位、行宽与每格类型、合计（`ValidateSourcePage`）、游标长度与前进、授权与作废只认 `true`、预留号码与时间。验证不过即 `INTEGRITY_FAILURE`，不转交；超时、5xx、401、重定向即 `AUTHORITY_UNAVAILABLE`；错误码是封闭集合，码与状态不符即完整性失败。请求总带完整来源上下文，别人上下文的读取在本地就拒绝、不发出。错误文本固定，只含操作、原因与请求编号。凭据只来自 `auth_env` 指名的环境变量，选项里不能出现密钥字段（配置的密钥名检查覆盖 `options`）。
- 代码：`adapters/httpsource/httpsource.go`（`call`、`refusal`、`validDataset`、`PrepareRead`、`ReadPage`、`Authority`）、`adapters/httpsource/protocol`、`reporting/validation.go`（`ValidateSourcePage`）、`server/registry.go`（`DefaultRegistry`，`http` 不是模拟器）。
- 测试：`TestEveryStatusMapsToItsPlatformError`、`TestAnswersThatDoNotVerifyAreIntegrityFailures`、`TestARedirectIsNeverFollowed`、`TestAnAnswerOverTheBoundIsRefused`、`TestASlowSourceIsUnavailableWithinTheTimeout`、`TestMutualTLSAndAPinnedCA`（含 TLS 1.1 被拒）、`TestTheConfigurationIsCheckedAndNeverShown`、`TestAReadOfAnotherContextIsNeverSent`、`TestTheRequestCarriesTheContextTheCredentialAndARequestID`、`TestMemoryContractOverHTTP` 与 `TestPostgresContract/over_http`（整套合同经协议运行）、`TestDocumentAuthorityOverHTTP`（出具授权一致性套件经协议运行）、`TestTheShippedRegistryServesAnHTTPSourceEndToEnd` 与 `TestABadHTTPConnectionRefusesTheStartAndIsNeverShown`（日志与拒绝中不出现凭据与地址）、`cmd/reporting-source-check` 的测试。
- 审查时看：协议只证明“这是平台”，代表谁看 context——来源产品必须在服务端映射 subject 并以自己的当前授权判断（`reporting-source-check` 的 C3、C4、R7–R10 检查，但只能抽查）；服务凭据泄露等于可以以平台身份为任意 context 发请求，所以凭据只给这一个产品、按产品分开、经 mTLS 更好。
- 已知不覆盖：没有对真实产品端点测过；没有请求签名或防重放（依赖 TLS 与短时读取）；公司目录（ADR 0015）不在第 1 版（S19-B）；来源侧的限速与配额是产品自己的；`Evidence.DataClass` 仍是 `contract-simulator`（核心固定值，与适配器无关，另行处理）。

## 已知没有覆盖的

- 真实发行方、各产品对来源协议的实现与自检、数据对账与准入决定（上线清单第 16–19 项）。
- 令牌吊销与 JWKS 自动发现；时钟偏差容忍为零。
- 浏览器登录只对假的身份提供方测过；没有发现文档、提供方登出、静默续期与刷新令牌；`private_key_jwt`、mTLS 客户端认证与不透明访问令牌不支持；防重放记录只在单个进程内（跨服务器依赖提供方的一次性规则）；未认证的兑换路由没有按来源限速，靠 `max_in_flight`、16 个并发槽与负载均衡器（ADR 0019，docs/S15A_SCOPE.md）。
- 追踪只到日志：请求按 W3C Trace Context 带 `trace_id`/`span_id`（S14-A），但没有 span 导出器；后台任务的追踪不是受理它的请求的追踪（存储里没有这个字段）。TLS 证书自动续期；systemd 单元示例未经测试（上线清单第 13–15 项）。
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
