# IDP Reporting Platform · S01-B

独立 Go 报表后端，2026-10-26（上海时间）。S01-B 增加持久来源合同、真实服务进程/PG
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

固定 Go 1.26.5、pgx 5.10.0、Excelize 2.11.0；依赖由 go.mod/go.sum 固定。

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

普通/race 平台库为 reporting_test_s01 / reporting_test_s01_race，来源库为
reporting_source_test_s01 / reporting_source_test_s01_race。生产库名及非回环地址拒绝。
再次初始化不会重授已撤权限。-pause-workers 只用于恢复夹具，使任务排队而不执行；
没有 HTTP 调度或授权管理后门。游标连续性验证保留同一演示令牌；生产密钥生命周期属S04。

资产目前在 PostgreSQL 内。测试平台/来源共用一个 PG 实例、分别使用两个库，
不宣称独立主机故障域、生产角色/RLS 或对象存储已验收。

## 测试与证据

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./...
go mod verify
GO_BIN=go python3 scripts/verify_independence.py
```

主机缺少 DSN 时，两个真实数据库入口明确 SKIP，不能计为数据库通过。设置
REPORTING_S01_REQUIRE_PG=1 后缺少必需 DSN 直接失败。

通过 Service、四个 Adapter 操作、Repository、HTTP 与 XLSX 接口验证。故障夹具只在
受保护测试库注入最终更新失败、发布期间失租约和字节损坏。scripts/run_pg16_vm.py
执行真实PG普通/race和真实HTTP进程/PG停止启动；工具变量见docs/VALIDATION.md。
cmd/reporting-recovery-probe 是本地验收客户端，显式 bootstrap 会清空受保护夹具；
业务服务不导入它，也不在 HTTP 上开放其控制能力。

当前逐条状态见 docs/ACCEPTANCE_S01B.json；原始日志、文件与摘要在 evidence/，
S01-A 历史证据保留在 evidence/s01a_baseline/，审查在docs/CODE_REVIEW_S01A.md。
压缩包附 SOURCE_HISTORY.bundle，可 git clone SOURCE_HISTORY.bundle restored-reporting
还原全部本地提交。直接解压源码可构建；含git diff的检查应在还原仓库运行。

## 后续范围

S02：真实身份、Brant与第二真实产品授权合同和数据对账。S03：独立控制台、嵌入、
三个设计器/PDF。S04：生产迁移/回滚、最小权限/RLS、对象存储、配额、清理、备份与容量。
当前不承诺自动重试、心跳、跨产品关联或跨系统撤权/提交原子性。实际优雅重启证据
不等于崩溃、断电或备份恢复资格。
