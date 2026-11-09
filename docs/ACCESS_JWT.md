# Access JWT 资源端认证模块

状态：S02-A，本地协议及合成数据 HTTP 验证。未连接真实 IdP，未实现浏览器
登录、动态发现/JWKS 轮询、令牌交换、代客或生产认证控制面。

## 组合方式

```go
verifier, err := accessjwt.New(accessjwt.Config{
    Issuer: trustedIssuer, Audience: reportingAudience,
    RequiredScope: approvedScope, JWKS: provisionedPublicJWKS,
    MaxTokenLifetime: approvedLifetime, Bindings: provisionedBindings,
})
// On err, fail startup. Never fall back to DemoBearer.
handler, err := httpapi.Handler(service, verifier.Authenticate)
```

此代码展示组合点，不提供虚构生产 issuer 或密钥。宿主需提供 TLS、超时/请求
头预算及受保护配置来源；现有 reporting-demo 仍是显式演示认证配置，没有
自动切换。通过模块验证不会改变 API 的 contract-simulator 数据标记。

## 验证规则

RS256；kid 只索引配置中的公钥；typ 为 at+jwt 或 application/at+jwt（媒体类型
大小写不敏感）。JWT 必含 iss/sub/aud/exp/iat/jti/client_id，签名、精确 issuer/
audience、整数秒日期、有效期及显式最大寿命均检查。零时钟宽容；配置最大
寿命为 1–3600 秒，测试使用 900 秒上限。这些是当前支持范围，不代表企业
生产寿命或时钟同步策略已选定。

需精确包含配置的 OAuth scope；测试采用 reporting.read 示例值，它不是全球
注册标准。缺少该 scope 的已验签、已映射身份得到 403 insufficient_scope；
格式、签名或身份无效得到 401 invalid_token。响应包含 Bearer challenge，
错误体不包含 token/claims/配置。角色、邮箱、tenant 与自填 integration 不
能扩大绑定；来源业务授权仍逐次生效。

仅支持单个 Authorization Bearer 头；不接受 query/cookie/转发身份头作认证。
顶层重复 JSON 键（含转义同名）、额外根、非 UTF-8、超限输入都拒绝。JOSE
仅支持 alg/kid/typ；不支持 crit、jku、x5u、内嵌 jwk、b64 扩展或 JWE。
含 act/cnf 的委托或持钥证明令牌明确拒绝，不能降级为普通 Bearer。

JWKS 由运营方从可信发行方带外提供，最多 64 KiB/16 把公钥；RSA 2048–4096 位、
e=65537。若包含 use/alg/key_ops，则只允许 sig/RS256/[verify]；不接收私钥或
远程 key URL。最多 4096 条主体+client_id 绑定、每条 32 个明确产品；不支持
通配产品。OAuth scope 最多 4096 字节/64 个词；令牌整体最多 32 KiB。

## 更新与撤销边界

Replace 完整验证新配置后原子替换；失败保留旧配置并返回错误。调用方必须
处置该错误，不能把失败误认为撤销已生效。合法的空 Bindings 配置用于全部
拒绝。先前已开始的认证可使用原配置完成；后续认证用新配置。这不构成
身份、来源授权与业务提交之间的分布式原子撤销。

映射只确定平台入口与 owner；多个客户端经明确绑定到同一 issuer+subject+
tenant 时拥有相同 owner，当前客户端的产品许可仍限制操作。一个身份如需在
同一客户端选择多个租户，必须另定可信租户选择合同，不能由 body tenant
绕过当前唯一映射。此版本不实现该选择流程。

不保存或输出 Bearer JWT；jti 存在性检查不是一次性令牌或重放黑名单。JWT 是
可重复使用的 Bearer 凭据。平台绑定更新与真实组织成员权威、用户会话/退出、
机器身份委托均须后续真实 IdP 合同验证。签名密钥的轮换办法与两个暴露窗口见
下节（S04-H）；其中自动发现（动态 JWKS）与紧急撤销仍等真实 IdP 合同。

## 签名密钥轮换与两个暴露窗口（S04-H）

`Verifier.KeyIDs()` 返回当前生效配置里受信任的 `kid`，已排序，只有 id，不含模数
和指数；轮换后用它核对，宿主也可在启动时记录。重叠轮换（`docs/OPERATIONS.md`
“Keys and credentials”给出完整步骤）：

1. 发行方发布新密钥后，调用 `Replace`，JWKS 同时含旧钥与新钥（其余配置不变）：两把钥签的令牌都被接受。
2. 发行方改用新钥签发；等到旧钥签发的令牌都已过期（最长 `MaxTokenLifetime`）。
3. 调用 `Replace`，JWKS 只含新钥：旧钥签的令牌从下一次认证起得到 401 `invalid_token`，新钥的被接受；无需重启。

以上由真实 RS256 令牌的测试证明（`identity/accessjwt/rotation_test.go`）：重叠期两把钥都被接受，移除后旧钥被拒；
验证失败的 `Replace`（重复 kid、过短模数、17 把钥、重复绑定、超界寿命、非 https 发行方、无钥）返回 `ErrInvalidConfig`
且不改变任何东西——半途失败的轮换什么也没换，调用方必须把该错误当作“未轮换”；已开始的认证用其读取到的旧配置完成，
所以移除自“下一次认证”起生效，而不是调用的瞬间；令牌指明未受信任的 `kid` 被拒，错误与 HTTP 回答都不点名任何密钥。
`Replace` 是 Go 方法，仓库不提供触发它的端点或文件监视；生产服务器（S05-A，`server` 包与 `docs/DEPLOYMENT.md`）在 SIGHUP 时重读 `jwks_file` 与 `bindings_file` 并调用它：成功后日志列出受信任的 `kid`，失败则保留旧配置并只记录错误代码。其余配置的重新加载仍需重启。

**两个不同的暴露窗口，不可混为一谈：**

- **签名密钥泄露**：暴露到运营方用 `Replace` 把它移除为止；自下一次认证起生效，无需重启；该钥已签发的令牌无论剩余寿命都被拒。立即移除，不要等旧令牌过期。
- **令牌被盗**：暴露到令牌过期，至多 `MaxTokenLifetime`（1–3600 秒，由运营方设定）。不存在对单个令牌的撤销（没有 `jti` 黑名单，S02-A 的边界在此重申；
  同一令牌在寿命内可被反复出示，`TestAStolenTokenIsBoundedByItsLifetimeAndNotRevocable`），所以寿命就是唯一的控制：按组织可接受的被盗窗口来选，
  更短的寿命让发行方多签发令牌。提前终止被盗令牌的唯一办法是移除签发它的密钥，这会同时终止该钥的所有令牌。

仍等真实 IdP 合同的：JWKS 的自动发现与轮询、紧急撤销（按主体/客户端/会话）、与发行方的轮换协调；本模块只验证带外提供的配置。

## 依据

- RFC 9068 §2、§4：访问令牌类型及 claims/资源端检查。
  https://www.rfc-editor.org/rfc/rfc9068
- RFC 8725 §3：固定算法、签名/issuer/audience、显式类型与不同 JWT 用途隔离。
  https://www.rfc-editor.org/rfc/rfc8725
- RFC 6750 §3：Bearer challenge 与 invalid_token/insufficient_scope。
  https://www.rfc-editor.org/rfc/rfc6750

本模块是有意收窄的配置化 profile，不声明支持所有 JOSE/OAuth 扩展或完成
第三方认证符合性测试；具体限制见上文及 S02A_SCOPE.md。

## 角色（S12-A，ADR 0018）

绑定可带 `roles`：`viewer`、`designer`、`publisher`、`issuer`、`administrator` 中的 1–5 个、不重复。未写 `roles` 的绑定保持角色引入前的权利（`viewer, designer, publisher, issuer`），`administrator` 从不默认给予。令牌里的角色声明一律忽略，与产品相同：只有绑定能授予。服务端在每个路由处按其模式要求一个角色（`transport/httpapi.RequiredRole`），不满足时回答 403 `AUTH_ROLE_DENIED`；`GET /session` 列出有效角色。空的 `roles` 列表或未知角色名被拒绝（启动或重载时，重载保持旧绑定）。

团队共享空间（S12-C）：主体 `~team` 保留给每个租户的共享设计空间，任何绑定都不能使用它（绑定文件与访问令牌配置都拒绝）。
