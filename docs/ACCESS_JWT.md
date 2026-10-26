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
可重复使用的 Bearer 凭据。平台绑定更新与真实组织成员权威、签名密钥自动
轮换、紧急撤销、用户会话/退出、机器身份委托均须后续真实 IdP 合同验证。

## 依据

- RFC 9068 §2、§4：访问令牌类型及 claims/资源端检查。
  https://www.rfc-editor.org/rfc/rfc9068
- RFC 8725 §3：固定算法、签名/issuer/audience、显式类型与不同 JWT 用途隔离。
  https://www.rfc-editor.org/rfc/rfc8725
- RFC 6750 §3：Bearer challenge 与 invalid_token/insufficient_scope。
  https://www.rfc-editor.org/rfc/rfc6750

本模块是有意收窄的配置化 profile，不声明支持所有 JOSE/OAuth 扩展或完成
第三方认证符合性测试；具体限制见上文及 S02A_SCOPE.md。
