# S00 接口与隔离合同 V1.0

状态：供 S01 实现的最小目标合同，不是已发布或已运行的生产 API。
现有 /r01 合成测试配置保留；不能仅更改路由就声明完成独立平台接口。

## 信任上下文

认证、接入产品与租户映射由服务端可信配置及认证结果生成。用户可选择 integration_id 与
company_ids，但它们只是选择器，必须在服务端映射到当前主体获准范围；不能作为授权依据。
请求 JSON 不接受自填 tenant/actor/roles/permissions/budget。
主体以受信 issuer＋subject 标识，不能以邮箱或不同系统中的同名 user_id 合并身份。
平台资源至少由 platform_tenant_id＋integration_id＋resource_id 隔离；原产品租户 ID 显式映射，
不能因两个产品都使用 tenant_a 而自动合并。共享平台租户内的跨产品访问也需显式授权。
OIDC 登录结果与访问令牌分开处理；平台资源接口验证签名、可信发行方、目标受众、有效期与
令牌用途，不能用 ID Token 替代资源访问授权。来源产品继续确认当前业务权限。

## 产品适配器最小接口

| 接口 | 行为与不变量 |
|---|---|
| DescribeCatalog | 提供已发布、版本化的数据集、字段、粒度、指标、维度和支持组合；不能暴露任意 SQL |
| PrepareRead | 按可信主体和批准 QuerySpec 授权，返回稳定 source_snapshot_ref、query_hash、scope_handle、source_watermark、TTL |
| ReadPage | 仅按同一来源快照读取；绑定租户、产品、主体、查询、版本和水位；禁止翻页变成新的实时结果 |
| AuthorizeCurrent | 在准入、最终发布、结果访问和下载时确认当前权限覆盖原范围；未知范围关系或授权源不可用时拒绝披露 |

适配器部署在来源产品一侧或其可信接入层。其业务查询可以使用原有 Bento/事务设施；
平台不会将 Go 事务上下文经网络传给另一个进程，也不会直接调用产品的私有数据库适配器。
服务账号用于接口连接，不自动继承任意最终用户的数据权限；必须绑定真实委托主体和范围。
正式单据的 PrepareDocument/ConfirmIssue 是后续独立能力，必须由来源产品授权，未纳入 S01。

## 平台最小接口面（目标）

| 路径 | S01 目标 |
|---|---|
| GET /api/reporting/v1/catalog | 返回当前主体可用目录；未知产品或版本拒绝 |
| POST /api/reporting/v1/queries | 对批准数据集执行有界预览；返回 columns、typed rows、totals、completeness、issues、evidence |
| POST /api/reporting/v1/snapshot-jobs | 严格请求＋Idempotency-Key；202；提交不执行数据查询 |
| GET /api/reporting/v1/jobs/{id} | 私有任务状态；成功结果引用仍需当前授权及 TTL 校验 |
| POST /api/reporting/v1/jobs/{id}/cancel | 幂等取消；取消后不能发布；清理动作不披露报表内容 |
| GET /api/reporting/v1/snapshots/{id}/rows | 冻结分页；游标绑定主体、产品、租户、查询与版本 |
| POST /api/reporting/v1/exports | 同一不可变快照的有界 XLSX 任务 |
| GET /api/reporting/v1/artifacts/{id}/content | 当前授权下私有下载；校验类型、文件名、长度与内容摘要 |

目录管理/审批、Report/Dashboard/Template CRUD 等完整接口在对应阶段定义，不在 S00
把未实现的接口标为可用。JSON 示例是接口草案，尚不构成完整 OpenAPI/JSON Schema。

## 数据与运行不变量

- 通用核心不得硬编码 customer.id、aging.bucket、ar.open_balance 或任何产品业务字段。
- 结果使用 columns 定义字段及类型；精确金额/小数与超出前端安全整数范围的整数以字符串传输；币种、精度、计量单位显式声明。
- S01 每次查询只处理一个已批准数据集及其来源快照。多源水位不构成分布式事务；跨源强一致在合同与实现未成立前拒绝。
- 幂等键作用域至少含租户、接入产品、主体、操作与 key；同 key 异规范化 payload 冲突。
- 数据源只返回已授权字段/行；平台不依赖先取全量敏感数据再在前端过滤。
- 原范围冻结，授权扩大不扩大旧任务；撤权或原币种/时区政策不再满足时，整份结果/文件拒绝。
- 当前权限覆盖由可信求值器确认，不能以两个不透明策略 hash 相同代替通用范围覆盖证明。
- 任务领取使用数据库时间、租约、递增 generation；取消、失租约与旧代次不得发布；快照/任务结果原子发布。
- 下载/发布前授权源不可用时，保持不可披露并记录可区分的暂时故障，不能退化为仅凭 owner 放行。
- 查询/导出限额由平台配置及服务端策略决定。R01-D 的1000分组、1MiB快照等仅是旧测试配置预算，不是独立平台生产 SLO。
