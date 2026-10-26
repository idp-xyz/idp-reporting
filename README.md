# IDP Reporting Platform · S03-B2

2026-10-27。新增当前身份私有布局服务端保存、不可变历史、版本冲突保护与重新打开。
显式增加启动参数 -layout-directory ../reporting-layouts 才启用；目录必须私有0700。
布局文件独立于-memory查询/任务存储，普通关闭/重开后仍可读取。
本切片是可信本地文件目录/单进程，不是生产模板数据库；崩溃可留.lock，必须确认无写进程后由管理员清理。
不含业务数据/公司/身份权限，不替代来源授权。25项软件DOM及Go普通/race通过；
双轴复审通过。浏览器、生产、断电/分布式恢复未验，真实接入最后。
运行和边界见docs/S03B2_SCOPE.md，当前证据evidence/s03b2。

以下保留此前阶段说明作为历史基线：

# IDP Reporting Platform · S03-B1

2026-10-27。新增屏幕报表列布局编辑：列选择/顺序/左右对齐、元数据 JSON 导出和粘贴重新打开。
只作用于当前授权结果；不保存数据、身份或公司范围。Excel仍导出完整冻结结果。
这是本地文件布局切片，不是完整三类设计器或服务端模板发布。23项软件DOM及Go普通/race通过，
双轴复审通过。浏览器验收未完成，未重跑PG；真实接入最后。详见docs/S03B_SCOPE.md及evidence/s03b。

以下为S03-A历史基线说明：

# IDP Reporting Platform · S03-A

本轮新增独立控制台与基础报表查看器，由 Go 服务直接提供 /console/。目录、预览、
快照分页、任务跟踪/取消、恢复私有任务与 Excel 下载均调用既有同源 API。用户已明确
真实接入放在最后；后续先推进三类设计器、模板/PDF与平台运行能力。

实现通过 API 和21项软件DOM回归，但浏览器预览路由被当前环境/URL策略阻断，真实
浏览器/移动视觉/下载验收尚未完成。见 docs/CONSOLE.md、docs/VALIDATION.md。
首次运行后在本机打开 http://127.0.0.1:7810/console/ 并输入当前配置的令牌。

S02-A已交付配置化 RS256 访问令牌认证模块 identity/accessjwt，以及 Brant/Parcel 真实
接入资料核对。模块通过现有 httpapi.Authenticate 入口组合；须由可信宿主提供
issuer/audience/公钥/scope/主体映射，不自动替换下方 reporting-demo 的演示身份。
本地签名令牌及 HTTPS/合成来源验证不等于真实 IdP、两个真实业务来源或生产准入。
组合与限制见 docs/ACCESS_JWT.md，真实接入缺口见 docs/REAL_SOURCE_DISCOVERY_S02A.md，
后续真实接入合同见 docs/REAL_SOURCE_ONBOARDING.md。

独立 Go 报表后端，2026-10-26（上海时间）。已通过的 S01-B 基线增加持久来源合同、真实服务进程/PG
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
npm ci --ignore-scripts --no-audit --no-fund
python3 scripts/run_s03a_checks.py --go /path/to/go1.26.5/bin/go
python3 scripts/verify_s03a_workbooks.py
```

主机缺少 DSN 时，两个真实数据库入口明确 SKIP，不能计为数据库通过。设置
REPORTING_S01_REQUIRE_PG=1 后缺少必需 DSN 直接失败。

通过 Service、四个 Adapter 操作、Repository、HTTP 与 XLSX 接口验证。故障夹具只在
受保护测试库注入最终更新失败、发布期间失租约和字节损坏。scripts/run_pg16_vm.py
执行真实PG普通/race和真实HTTP进程/PG停止启动；历史工具变量见docs/VALIDATION_S01B.md。
cmd/reporting-recovery-probe 是本地验收客户端，显式 bootstrap 会清空受保护夹具；
业务服务不导入它，也不在 HTTP 上开放其控制能力。

当前逐条状态见 docs/ACCEPTANCE_S03A.json；本轮原始日志、文件与摘要在 evidence/s03a/。
S02-A历史证据在 evidence/s02a_baseline/；此前审查见 docs/CODE_REVIEW_S02A.md。
S01-B 历史证据完整保留在 evidence/s01b_baseline/，其中含原 S01-A 历史证据；
历次审查见 docs/CODE_REVIEW_S01A.md、docs/CODE_REVIEW_S01B.md。本轮不重跑 PG/VM。
压缩包附 SOURCE_HISTORY.bundle，可 git clone SOURCE_HISTORY.bundle restored-reporting
还原全部本地提交。直接解压源码可构建；含git diff的检查应在还原仓库运行。

## 后续范围

当前顺序：控制台/查看器后继续三类设计器、模板/PDF及嵌入，再推进生产迁移/回滚、
最小权限/RLS、对象存储、配额、清理、备份与容量。最后接真实发行方/企业身份、
Brant与第二真实产品授权合同并完成数据对账。各项未验能力仍保留其验收门槛。
当前不承诺自动重试、心跳、跨产品关联或跨系统撤权/提交原子性。实际优雅重启证据
不等于崩溃、断电或备份恢复资格。
