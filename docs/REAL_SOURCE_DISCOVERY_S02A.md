# S02-A 真实来源与身份接入核对

日期：2026-10-26（Asia/Shanghai）。本文件记录实际读到的来源，不把设计、mock、
本地协议测试升级为真实运行验收。来源摘要见 evidence/s02a/source_discovery.json。

## 1. Brant：最新业务源码，仍缺报表所需的真实应收账款台账

- 当前选取第四十批《核价身份隔离完整统一工程》，Library ID
  libfile_0cc1462dcc6c81919d677bdb2d460f06；完整 ZIP SHA-256：
  1253d572b74738b9f0e0dd3793df455a90d4f3851db8b61fda3c4f3c0e2920c9。
- 同批审查明确：正式候选迁移 000001–000006 仍没有生产候选 T02 record/result
  表；不能声称 PostgreSQL SelectCost 已完成，G00 NOT APPROVED。
- 核价、基础数据和当前授权候选实现已经存在；Foundation AuthorizationStore
  按 tenant/company/actor 查询并锁定当前授权，但源码明确它不构成认证边界，
  也不替代历史证据或记录级授权。
- 未在本次读取的业务源码中定位到可接入的真实应收未清项、收款核销、贷项及
  账龄数据集发布接口。合同模拟器的 receivables.aging 不能映射为成本计算结果。
- T02 历史交付的同身份文档已更新为 V0.5；最新读取确认其通过范围为 Python/
  SQLite 本地核心，企业真实认证及完整运行态仍未通过。不能用旧版本文件名
  或本地阶段通过替代当前 Go 业务数据及真实认证。

待补齐：应收事实来源/粒度/状态、核销与贷项规则、日期与币种政策、source actor
映射、记录/公司读权、稳定读取及撤权接口、可核对样本。需要真实应收源，而非
在报表平台内部另建财务真值。

## 2. Parcel：查到真实设计文档，当前可访问实现以客户端及 mock 为主

核对了两个现有包：V3.0 RELEASE FINAL V37 与 Commercial Party UX V1.1。
V37 的 prototype/db/schema.ts 明确为空；app/page.tsx 以 sessionStorage 保存
演示状态，app/data.ts/enterprise-model.ts 提供演示订单与业务数据。
这些包可供界面及业务字段参考，不能作为服务端运单与权限权威。

另外通过已连接 GitHub 只读核对 idpxyz/idp-parcel，main 固定提交：
1428542bc8a5cf609ff70528ddf5e4d0697b58c7（仓库返回日期 2026-03-27）。
完整递归树未被截断；选取并读取以下文件：

| 文件 | 核对结论 |
| --- | --- |
| docs/0327-07-shipment.md | 运单头与每物理件分表、创建/详情/WMS 设计合同；物理 DDL 与具体枚举以实现为准 |
| docs/0327-08-account-entitlement.md | 账户/合约/权益及禁止信任未校验 account_id；具体 IAM/JWT claim 不在该文档范围 |
| apps/parcel-web/src/mocks/shipments.ts | SAMPLE_SHIPMENTS/SAMPLE_PARCELS 是固定演示数组 |
| apps/parcel-web/src/shared/api/client.ts | 客户端 HTTP 封装；不能证明后端台账、快照或当前授权端点已经部署 |
| apps/parcel-web/README.md | 客户端工程说明，不是报表来源运行验收 |

两个接入前必须澄清的差异：

1. 运单设计 §3 写 parcel.sequence 从 1 开始；mock 的 PARCEL_DEMO_001 为 0。
2. 设计 §5 的建议履约状态为 shipped/delivered 等；mock 使用 in_transit。
   建议状态机允许按产品裁剪，因此这是需要明确版本映射的差异，不能直接
   宣称业务实现违反了最终运行合同。

其他尚未确立的报表合同：运单数与包裹件数分别计数；创建/确认/交接的日期
语义；取消/退件/重复事件规则；原成交金额、后续调整与成本的分离；跨币种
规则；账户、企业及委托主体范围。不得把 mock 的整票总价与多件 JOIN 后重复
求和，也不能把创建时间当发运时间。设计中的计价小单位与 g/mm 字段需要原
产品明确 scale、currency、unit 和缺失值策略后再发布到目录。

本轮未调用承运商、修改 Parcel 仓库、写入真实业务或发送外部消息。上述核对
仅针对实际访问的包及固定提交，不推断用户不存在其他后端代码或部署。

## 3. 身份：有 Keycloak PoC，尚无 Reporting 生产发行方合同

读取 IDP_MCP_P2-02C3A_Keycloak_OIDC_MCP_PoC_V1.0。其 README 明确只有静态/
单元验证，作者环境未跑外部 Keycloak/agentgateway，不能声称 Gateway PASS。
该 PoC 的 client_credentials 是机器身份，不能转换成 Reporting 人类委托
权限，也不能据此认定 Reporting 已选择或部署 Keycloak。

本轮实现 identity/accessjwt，按 RFC 9068/RFC 8725 的基础验证规则与 RFC 6750
错误语义验证配置化访问令牌。一个实例固定一个 HTTPS issuer、精确 audience、
公钥集合、所需 scope 与 subject+client_id 映射；不使用邮箱合并身份。
这只是资源端认证模块；真实发行方发现、租户成员权威、用户登录、回调及
实际 IdP 令牌兼容仍需实测。真实服务账号委托合同仍未实现。

## 4. 本轮关闭与保留的门槛

| 门槛 | 当前结论 |
| --- | --- |
| 本地签名访问令牌验证与 HTTP 私有资产链路 | 以本轮实际测试记录为准；可独立验证 |
| I14：Brant + 第二真实产品 | BLOCKED_SOURCE_CONTRACT；未完成真实对账 |
| I15：真实 OIDC/SSO | PARTIAL_LOCAL_PROTOCOL；未完成真实发行方验收 |
| I16：服务账号及真实来源当前授权 | PARTIAL_FIXTURE；不得继承机器账户全量权限 |
| S01-B 数据库恢复基线 | 保留原验收；本轮未改数据库实现、未重跑 VM |

下一步先提供并核对真实后端/API 与身份发行方合同，再实现来源侧只读适配器
并跑两产品对账。不得用新建的随机服务地址、服务账户、客户数据或 mock 补齐
这些外部事实。平台仍独立部署与演进；原产品继续拥有业务真值和最终读权。
