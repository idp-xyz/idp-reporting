# 真实产品只读报表接入合同（S02 工作稿）

状态：待来源产品确认与实现；不是已经部署的 API。沿用 docs/s00/INTERFACES.md
四个操作与现有 Go Adapter，不引入产品私有包或从平台跨库执行产品 SQL。

S19-A 起，来源产品**不再需要平台为它写 Go 适配器**：产品在自己的服务里实现固定的
HTTPS 协议——报表来源协议第 1 版（docs/SOURCE_HTTP_PROTOCOL.md，机器可读的
docs/source-openapi.json），平台用已发行、已审查的通用适配器 `http` 接入
（docs/DEPLOYMENT.md“Connecting a source product over HTTP”）。协议把下面四个操作
（及可选的出具授权）原样搬到 JSON over HTTPS：每个请求都显式带来源上下文
（平台租户、issuer、subject、integration、source tenant），产品在服务端把
subject 映射为自己的 actor，除此之外请求里的任何东西都不能作为授权依据；
错误是封闭的错误码集合；数值一律是精确字符串。产品实现后，用
`reporting-source-check`（协议第 7 节）对平台将要连接的端点自检，全部通过才配置连接。
`adapters/httpsource/sourcetest` 是协议的 Go 参考实现。

## 接入前须交付的事实

| 项目 | 必须说明及提供的证据 |
| --- | --- |
| 来源定位 | 服务端仓库/固定提交、实际运行配置与受控测试数据；说明与现有设计和 UI 包的关系 |
| 可信身份 | issuer、资源 audience、访问令牌 typ/算法/claim/profile；主体到来源 actor/account 的服务端映射及撤销方式 |
| 租户/产品映射 | platform tenant + integration 对应哪个 source tenant；issuer+subject 对应谁；不得按同名 ID/邮箱自动合并 |
| 数据目录 | 数据集/指标版本、行粒度、字段与缺失值、金额精度/币种、数量单位、支持维度/筛选/排序与日期政策。产品在目录中为每个数据集版本声明允许筛选的字段与运算符（`filters`，可带封闭取值表与必填）和允许排序的字段与方向（`sorts`，ADR 0023）；未声明则不开放。产品在自己的事务里应用它们，筛选值一律作为参数绑定，绝不拼入语句文本；未声明的字段、运算符或取值回答 `unsupported`，合计必须是筛选后范围的合计；`reporting-source-check` 的规则 F1–F6 逐一核对 |
| 稳定读取 | 准备读取的事务/版本/水位/TTL；分页如何维持同一事实与原授权范围；过期、清理与更新后的行为 |
| 当前授权 | 原冻结范围如何被当前资格完整覆盖；公司/账户/记录/敏感字段；撤权/币种政策变化及授权源故障如何拒绝 |
| 公司目录（可选能力，ADR 0015，S19-B） | 来源能否列出当前主体可选的公司及其名称（`CompanyDirectory`；协议 1.1 版的可选路由 `POST /reporting-source/v1/companies`，docs/SOURCE_HTTP_PROTOCOL.md "companies"）；列表多新；超过 1000 项时怎么截断并标记 `partial`；每个 id 与 name 1–128 字节、无控制字符、id 不重复。不实现则不要在连接上声明 `companies: true`，控制台保持手输公司 ID；实现了也不改变 PrepareRead / AuthorizeCurrent 的授权（列表只是建议，读取仍由来源逐次授权），平台不存、不缓存、不记录名称。声明前先通过 `reporting-source-check -companies`（规则 D1–D2） |
| 单据作废（可选能力，ADR 0025，S24-A） | 产品能否应平台的请求作废一张它已确认的正式单据（`DocumentCanceller`；协议的可选路由 `POST /reporting-source/v1/issue-cancellations`，docs/SOURCE_HTTP_PROTOCOL.md "The cancellation of an issued document"）。须说明：谁可以作废（由产品按请求里的 context 映射出的 actor 与**产品自己的规则**决定，例如已结算、已关账则拒绝；平台只转达，不替产品判断）；只作废已确认的单据（未确认的预留、已作废的任务回答 `conflict`，没预留过的任务回答 `not_found`，请求所指的预留号/确认号/编号与产品所持不一致回答 `conflict`）；**编号不再使用**，更正的单据是另一次出具、另一个编号；按任务 ID 幂等——同一张单据再次请求，无论这次的理由写什么，都回答同一张回执（作废号与时间），平台靠这一点在回执丢失或自己没记下时对账；回执时间为整秒且不早于确认时间；理由是人写的自由文本（1–200 个字符、单行），按数据绑定，不写入日志。不实现则不要在连接上声明 `cancellation: true`：平台对该产品的作废路由回答 422，单据仍在产品里作废，平台的出具记录不知情。声明前先在**非生产端点**通过 `reporting-source-check -issue -cancellation`（规则 X1–X4；它会确认并作废一张单据，另预留并作废两个编号） |
| 服务身份 | 平台到来源的连接凭据（协议第 1 节：Bearer 服务凭据和/或 mTLS 客户端证书，CA 由平台固定）、真实委托主体及用途/受众；机器账号不能自动代表任意用户——服务凭据只证明“这是平台”，代表谁只看请求里的 context，由产品自己映射与授权 |
| 对账 | 匿名化但可复核样本、来源独立总量/金额、负值/大数/多件/跨日/取消及权限边界案例 |

## 操作义务

DescribeCatalog 仅返回当前主体可访问的已发布版本与批准组合，不接受客户端
SQL/任意公式。PrepareRead 在来源可信事务中验证身份映射、当前权利与查询，
返回绑定 source tenant、platform owner、integration、query hash、数据集版本、
watermark、原始授权范围及 expires_at 的稳定读取引用。ReadPage 仅沿同一引用
投影；当前数据变化不得让后续页跳到另一个版本。AuthorizeCurrent 比较当前
权利对原范围的完整覆盖；扩权不能扩大旧结果，缩权不能只过滤文件后放行。

不能以不透明策略 hash 相同代替范围覆盖证明。来源应将权限、来源不可用、
过期、参数不支持及损坏区分；平台仍关闭披露。网络超时、授权缓存最大年龄、
重试/撤权竞态与远程快照配额必须在真实合同中确定，不预造跨系统原子性。

平台排队任务保存可信 owner 与原范围，不能把用户 Bearer JWT 写入任务、快照
或文件。任务在用户令牌过期后能否继续执行取决于当前来源资格；读取任务、
分页与下载仍需新的有效请求认证及来源当前授权，不将提交时验签替代以后读权。

## 两个产品的首批对账约束

Brant 应收账龄须由真实未清项及核销/贷项语义产生，不能把核价/报价金额当应收。
账龄基准日期、到期日、未来日、核销截止、冻结水位、币种换算与四舍五入都由
来源发布版本化口径。未提供时不发布 receivables.aging 的真实目录。

Parcel 须分别声明运单头与物理件；一票多件不能放大头表金额。创建、确认、
交接、签收的计数分别定义，状态版本映射、时区日界、重复事件、取消/退件
及补差入账语义由来源确定。display_amount_minor 是成交快照，不自动等于
净收入、已收款或成本。新增报表指标 ID 与公式须经产品确认，不直接沿用 mock。

## 验收次序

1. 固定真实提交、身份/委托及数据合同；将上表未知项逐项变为可执行样本。
2. 来源侧按 docs/SOURCE_HTTP_PROTOCOL.md 实现四个只读操作（出具时再加三个出具路由；要让平台能申请作废，再加作废路由），来源自己的权限与事务设施继续权威。
3. 产品方用 `reporting-source-check` 对预发端点全部通过（用平台将使用的凭据与 CA；`-issue` 与 `-cancellation` 只在非生产端点上跑）。
4. 平台配置 `"adapter": "http"` 的连接并验证 owner、source tenant、版本/水位/TTL 的完整绑定（上线清单第 17 项）。
5. 在两个产品各完成目录、预览、任务、分页、XLSX、撤权/故障与独立业务对账。
6. 再更新 I14–I16 的资格；生产部署与容量准入另行按既定阶段验收。
