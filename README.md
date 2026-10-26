# IDP Reporting Platform · S01-A

独立 Go 报表后端的首个工程切片，日期 2026-10-26（上海时间）。
提供两个**合同模拟器**：应收账龄、非财务运单统计。它们使用合成的预汇总数据，
不连接真实 Brant/Parcel，也不证明真实财务算法、权限映射或单据出具已接入。

## 当前可运行链路

版本化目录 → 有界预览 → 异步快照 → 冻结分页 → 异步 XLSX → 私有下载。
任务支持取消、幂等键、租约代次与原子发布。原授权范围冻结；每次结果披露及发布前
重新确认当前来源权限。权限收缩、币种/时区变化、TTL 和授权服务不可用均拒绝披露。

`reporting/` 只依赖标准库；没有 Brant 私有导入、本地 replace 或来源业务表。
`storage/postgres` 只拥有平台任务/快照/产物；`adapters/contractsim` 是来源合同模拟器。
`transport/httpapi` 绑定可替换认证口；`export/xlsx` 使用原有固定版本 Excelize。
模块路径 `idp.local/reporting` 是本地工程命名，尚未注册外部仓库或发布模块。

## 本地启动

需要 Go **1.26.5**。依赖版本由 go.mod/go.sum 固定；首次构建需要公共 Go 模块缓存或网络。

```bash
go build -o bin/reporting-demo ./cmd/reporting-demo
export REPORTING_DEMO_TOKEN="$(openssl rand -hex 32)"
./bin/reporting-demo -profile contract-simulator -memory
```

仅监听 `127.0.0.1:7810`。所有接口需要 `Authorization: Bearer $REPORTING_DEMO_TOKEN`。
`-memory` 必须显式选择，重启丢失全部资产。没有生产模式、默认密码或远程监听开关。
启动不打印令牌；本演示令牌不是 OIDC/SSO。

PG 模式仅允许数字回环地址和指定隔离库 `reporting_test_s01` / `reporting_test_s01_race`：

```bash
export REPORTING_S01_TEST_DSN='postgresql://postgres@127.0.0.1:55432/reporting_test_s01?sslmode=disable'
./bin/reporting-demo -profile contract-simulator -initialize-fixture
```

数据库须预先存在且专用于隔离测试。不要在真实业务库运行 fixture。
SQL 为测试配置，尚未提供生产角色/RLS、升级回滚、私有对象存储或备份策略。
模拟器的来源句柄和授权状态在进程内，**PG 资产持久化不等于端到端重启恢复已通过**；
服务重启后旧来源句柄可能失效并拒绝访问，S01-B 将验证可恢复的来源合同。

## API 与复现

真实实现范围见 [docs/API.md](docs/API.md)，两个请求示例在
`docs/s00/contracts/query-examples.json`。固定公司为 `company_demo`。
请求不得提交 tenant、actor、roles、permissions 或 budget；未知字段/重复键/null 拒绝。

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./...
go mod verify
GO_BIN=go python3 scripts/verify_independence.py
```

普通主机测试未提供 PG DSN 时，真实数据库入口明确 SKIP，不能据此宣称 PG 通过。
设置 `REPORTING_S01_REQUIRE_PG=1` 后，缺少 DSN 会直接失败。
`scripts/run_pg16_vm.py` 在预置工具下运行无网络的完整 Linux 客体和真实 PG16 普通/race 测试。
工具变量见 [docs/VALIDATION.md](docs/VALIDATION.md)；工具是独立输入，不需要任何来源产品代码。

## 后续验收

S01-B：补齐真实重启、故障与完整存储验收。S02：真实身份及两个真实产品授权数据对账。
S03：独立控制台/嵌入与三个设计器。S04：生产迁移、容量、最小权限、对象存储和恢复。
本轮没有 PDF、正式单据、通用 SQL/表达式、跨产品关联、自动重试或心跳续租。

实现范围与限制以 `docs/S01A_SCOPE.md`、`docs/VALIDATION.md` 和原始证据为准，
`docs/s00/acceptance.json` 保留原设计状态，当前逐项进度另记 `docs/ACCEPTANCE_S01A.json`。

交付压缩包另附 SOURCE_HISTORY.bundle。直接解压的项目可构建/运行；要复核提交差异或
运行含 git diff 的 scripts/verify_host.py，请先用 bundle 还原仓库：
`git clone SOURCE_HISTORY.bundle reporting-history`。历史包含规格基点和第二适配器零核心改动的提交。
