# OceanBase 单步 DML 模式与 Homelab 验证

> 2026-10-10｜任务 #28：本地实现与单步能力验证完成，未提交、未合并、未发布生产。  
> 这是测试工程记录，不是生产运行报告。Orchestrator 接入、持久化 Checkpoint 全链路与生产切流仍待后续任务。

## 2026-10-14：内部真实 COUNT 保护（仅本地实现，未验收/发布）

本次不是 Go 规划/AST/JSON 协议：只给既存 OB single-DML 内部 `realRowCount` 增加显式、默认关闭的保护。旧 `getRealRowCount`、raw/exec、全局默认、MySQL 旧请求和 13 列结果保持不变。

- 新 Go `SourceOptions`：`OBCountProtection bool`、`OBCountTimeout int`、`OBCountCapability bool`；SQL 对应 `--enable-ob-count-protection`、`--ob-count-timeout=30`、`--enable-ob-count-capability`。API timeout=0 表示省略；SQL 显式 timeout=0 拒绝。timeout 为 1..3600 秒，与 mutation wait-timeout 独立；省略默认 30。显式 timeout 即使保护关闭也拒绝；MySQL 请求新功能拒绝。
- 正常 CHECK/EXECUTE 保持既有 `--single-dml=1;--real-row-count=1;--disable-remote-backup;--trans=0;--wait-timeout=...`，只追加 protection 和 count timeout，**不带 capability**。每次请求的内部 UPDATE/DELETE COUNT 都做保护审核；仍保留 max_update_rows 与 single-DML 80,000 硬拒绝（80,000 可审核，80,001 拒绝），不接受外部计数授权替代。
- COUNT 的 USE/autocommit/OB query+trx timeout/`ob_read_consistency='WEAK'`/`@proxy_route_policy='follower_first'`/Query/Scan 绑定专用 `sql.Conn` 与请求 context；预算包含建连和设置，COUNT 下发前再次截断服务端预算。follower 优先，但允许 OBProxy 在 follower 不可用时路由 leader；不是 follower-only，也不保证 COUNT 永不访问主副本。应用不做 retry/重连/第二次 COUNT，代理内部路由不等于应用重试。设置、取消、连接、Scan、NULL、负数、空/多行、多列、额外结果集、rows.Err/Close 错误均关闭，无旧 raw/exec 重连或重探测。fingerprint 不跳过受保护 COUNT。
- 读 Conn 用 Raw 返回 ErrBadConn 物理丢弃，不把 WEAK session 还池；mutation 使用另一专用连接并显式 STRONG，不继承读路由。

### 最小版本化能力证明

在上述保护选项上添加 `--enable-ob-count-capability;--enable-check;`（禁止 EXECUTE），正文只有 `USE \`已知存在数据库\``，再 commit。不依赖业务表，不执行 COUNT/mutation；旧实现忽略新选项不会生成该 marker。数据库/已有审核失败、空正文、非 USE 或多语句不返回证明。

唯一结果行的旧 13 列契约（单测已验证，仍待真实 wire 联调）：

| 列 | 精确值 |
|---|---|
| order_id | 1 |
| stage | CHECKED |
| error_level | 2（故意拒绝，不是执行成功） |
| stage_status | Audit Completed |
| error_message | `ob_single_dml_count_protection_v1`（精确匹配，无句点） |
| sql | 调用方的唯一 USE 原文（按既有前端规范化后验证身份） |
| affected_rows | 0 |
| sequence | `0_0_00000000` |
| backup_dbname / sqlsha1 | SQL NULL，不是空串 |
| execute_time / backup_time | `0` |
| needMerge | 0 |

### 本轮自动化与发布门槛

先失败单测再实现；额外结果集缺口另经红测试复现（原先 level0 且 mutation=1），再补 NextResultSet 拒绝。`go test ./session -run 'TestOBCount|TestSingleDML|Test.*OceanBase' -count=1`：40 顶层测试 + 113 子测试通过，0 失败；同过滤的 `go test -race -vet=off ./session ... -count=1` 通过；`go build ./...`、`go vet -unreachable=false ./session`、`git diff --check` 通过；`go test ./parser ./ast -count=1` 全套通过。`go vet ./...` 的 session_inception_exec_test.go:787 unreachable code 已在未改 HEAD 临时归档复现，未修改旧测试。

旧全 session suite 自带 test@localhost 配置，SetUpSuite 因 MySQL 1045 失败；本轮误运行后不再执行，未改账号或配置、**没有真实 MySQL CHECK/EXECUTE/旧 realRowCount/事务/备份成功证据**，不能将该次运行算兼容验收。

**不可发布共享制品**：当前 follower_first 契约允许代理回主，原 follower_only 实现的既有测试不构成当前策略的运行时验收；仍需实际 OBProxy follower 优先/不可用时 leader 路由审计、取消后服务端停止证据、真实 MySQL 回归与新 capability wire 联调。单 Conn 只证明客户端连接绑定，不证明 OBProxy 后端固定或 OB 引擎永不转发。新功能保持默认关闭。本轮无 commit/push/部署；保留下面历史实验，不把历史 single-DML 证据当新 COUNT 保护验收。

### 2026-10-15：follower_first 本地修订

- 仅将显式 OB single-DML + protected realRowCount 的专用 COUNT 连接路由从 follower_only 改为 follower_first。WEAK、专用连接物理丢弃、请求与服务端预算、无应用 retry、80,000 可审核/80,001 拒绝、mutation 独立 STRONG 及 MySQL 旧路径不变。
- TDD：先将隔离测试期望改为精确 follower_first，并补充 mutation 显式 STRONG/不得设置读路由的断言；运行 `TestOBCountConnectionAndMutationIsolation` 红，实际下发仍为 follower_only。再改唯一生产路由 SQL，focused `go test ./session -run 'TestOBCount|TestSingleDML|Test.*OceanBase' -count=1` 通过（40 顶层 + 113 子测试）。路由 SET 故障驱动改按 `@proxy_route_policy` 匹配，避免改名后故障注入失效。
- 同过滤的 `go test -race -vet=off ./session ... -count=1`、`go vet -unreachable=false ./session`、`git diff --check` 通过。默认 `go vet ./session` 仍报未修改旧文件 `session/session_inception_exec_test.go:787` unreachable code，与下文已确认基线缺陷一致。
- 本次只运行本地离线测试，无共享服务/网络/生产操作，无 commit/push；**未新增 OBProxy runtime、真实 MySQL 或 capability wire 验收证据**。上文 2026-10-14 自动化与下文 2026-10-10 实测保留为历史记录。

## 接口与边界

注释中显式传 `--single-dml=1`，默认关闭。该模式要求 OceanBase、恰好一个 INSERT/UPDATE/DELETE、最多一个前置 USE、`backup=false`、`trans=0` 和显式 `wait-timeout=1..3600` 秒。

- 支持单表 UPDATE/DELETE + WHERE（无 JOIN/CTE/LIMIT/ORDER）及 INSERT VALUES；不接受 DDL/DCL、额外 SET/SHOW/事务控制、INSERT SELECT/REPLACE/INSERT SET。
- UPDATE 必须有可用字段元数据，禁止对 PRI 列赋值，包括大小写、别名和复合主键中的任意一列。
- UPDATE/DELETE 的估算超过 80,000、任何负估算或其他原生审核错误都不会被放行。
- CHECK-only 请求包含两个 mutation 时返回稳定标记 `single_dml_requires_one_mutation`。旧版本会忽略未知 flag，调用方必须验证此标记，而不是只探活 TCP。
- 新模式独立获取一个 `sql.Conn`，USE/autocommit/timeout/执行身份校验与 mutation 绑定同连接；准备失败时零 mutation；执行阶段不调用旧连接池重连/1046 重发分支。
- 已执行但读取响应失败仍返回 EXECUTED/Execute failed，提交状态须由上层按 unknown 人工处理；不允许自动重试。失败行的 affected_rows 仍可能是审核估算，不能当作已提交行数。
- 原始 13 列结果集不增加字段；执行前模式/设置错误可挂在 USE 行，上层不能无条件过滤该错误。

单步调用示意（凭据使用现有固定内部账号，不写入日志或示例）：

```sql
/*--user={tenantScopedUser};--password={password};--host={host};--port={port};--enable-execute;--enable-ignore-warnings;--disable-remote-backup;--trans=0;--wait-timeout=300;--single-dml=1;*/
inception_magic_start;
USE `{database}`;
UPDATE t SET v=v+1 WHERE id=1;
inception_magic_commit;
```

## 验证摘要

验证分支 `test/ob-session-timeout`，基线 `57d51c71`，本地未提交实现。测试 binary 标识 `v1.3.3-single-dml-homelab.20261010`；不是生产版本，也没有创建对应 git tag。

### 自动化

- 先写测试并确认缺实现时失败，再实现。
- focused session 回归：**31 个顶层测试 + 94 个子用例通过**；包含新增模式及既有 OceanBase EXPLAIN 回归。
- `go test -race ./session -run '^TestSingleDML' -count=1` 通过。
- 模拟驱动证明 USE/SET/identity/mutation 同连接；SET 错误零 mutation；ErrBadConn、ErrInvalidConn、取消及 `wait-timeout=900` 下 1046 错误均仅一次 mutation 调用，没有新连接重发。
- 注释参数非法值、超时整秒边界、请求优先级、旧 global wait_timeout 不覆盖 OB query/trx、关闭模式保留旧参数等均有测试。
- 配置错误不再 dump 原始参数/密码，语法错误与参数失败均有防日志泄漏测试。

### 真机

最终验证时间 **2026-10-10 11:25:51 UTC**，仅 `homelab_ocean_test` / 业务租户 `dba` / 数据库 `test`；独立 goInception 4002，保留共享 4001 与 Orchestrator 5002。

配置明确 `max_update_rows=80000`、`er_udpate_too_much_rows=2`。探针每次创建随机命名的专属 fixture，并在结束后 DROP，仅操作该 fixture，不连接 sys 或生产。

最终 25 个检查中：**24 项功能/安全检查通过；1 项旧 DDL 基线一致性通过但成功执行被环境权限阻塞**。不能把这 1 项写成 DDL 正向验收。

| 项目 | 实测证据 |
|---|---|
| 新旧版本能力 | 旧4001忽略single-dml，双mutation CHECK不返回标记；新4002返回稳定拒绝，零执行 |
| 安全拒绝 | 双mutation、修改PK、SET、DDL、非法mode/timeout及多USE均CHECKED/level2；目标值保持0 |
| 普通 DML | INSERT VALUES、UPDATE、DELETE、确认零行成功，真实影响行数与状态一致 |
| 同连接设置与执行 | mutation内写入 query/trx=45,000,000微秒、CONNECTION_ID=3221701242（每次测试ID不同）；与当次响应sequence的线程ID一致 |
| 12秒长 SQL | `wait-timeout=300`，约 **12.021s** 成功，跨过默认10秒 |
| 指定截断 | `wait-timeout=15`、SLEEP(18)，约 **15.031s** 以15,000,000微秒限制截断（具体以原始JSON为准） |
| 并发隔离 | 两条独立请求SLEEP(4)，T=2约2.023s截断，T=10约4.023s成功 |
| SET失败 | 同目标业务OB的协议故障代理注入SET错误；mutation转发数 **0**，USE保留错误，mutation仍CHECKED |
| 响应丢失 | 代理转发完整mutation后切断响应；mutation转发数 **1**，返回unknown提示，无重发；事后数据虽生效，也不能倒推当时已知成功 |
| 旧模式 DML | 不带single-dml的普通UPDATE仍成功 |
| 旧模式 DDL | 新4002与旧4001均在CHECKED阶段因 `dba_ob_table_locations` SELECT权限缺失拒绝；行为一致，**未验证DDL执行成功**，未擅自授权 |
| 清理 | fixture已DROP，临时实例限时退出，4002释放，共享4001/5002 PID未变 |

实际同连接ID应取最终JSON的 `mutation_reads_same_session_timeouts_and_connection_id`，不要把表中示例ID当固定值。

测试制品 SHA-256：

- 服务 binary：`375fa9a612f1ca2c49d670a53acefa017987ec8fed44356584b60190ed3c7815`
- 最终探针 binary：`e853cbd847cc68faecdc34078be261e488df05a9f62d7e864e2b58cef15ffbc8`

探针改进基线对拍后会重新生成其hash；服务binary的上述hash为本轮最终服务制品。以保留制品重新计算的hash为准。

## 可重复执行的工具

`tools/single_dml_homelab/` 是Go工具，无新增Shell脚本：

```text
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o probe ./tools/single_dml_homelab
```

将其放到专用Homelab测试目录，在 `dba-app` 提供环境变量 `SINGLE_DML_OB_PASSWORD`，然后运行 `./probe --run --frontend-port=4002`。不会输出密码；不允许把4000/4001当作待测新实例。目标OB地址、业务租户、库和代理地址故意固定在homelab，拒绝在其他主机运行。

- 不带 `--run` 只提示使用约束，不执行写入。
- 原有4001只做能力CHECK和旧DDL基线对拍，后者只针对工具自己的fixture。
- 故障代理不记录认证包，向同一个业务OB转发；分别拒绝timeout SET、或在完整mutation已转发后丢响应。
- DDL权限不足时输出 `environmentBlocked=true`，只记新旧基线一致性，明确不是DDL执行通过。
- 具体配置/部署和实例启动仍需使用授权的测试环境，不提供生产替换命令。

## 静态检查与未覆盖范围

默认 `go vet ./session` **不是全绿**：`session/session_inception_exec_test.go:787` 的旧 unreachable code 报错。已将未修改 `HEAD` 源码完整归档到独立临时目录复跑，同位置同错误，确认为基线问题；未越界改旧测试。

`go vet -unreachable=false ./session`、探针自身 `go vet`、focused tests、race、Linux构建和 `git diff --check` 均通过。此处显式说明禁用的检查，不掩盖默认vet失败。

仍待后续：Orchestrator的 long?/USE/阶段解析与能力探针、逐批durable全链路、15万3批、严格行数边界的真实大表、生产兼容/灰度、真实MySQL与binlog backup/pt-osc验收。旧OB DDL成功路径须在合法补齐权限的环境验证，本轮没有更改账号权限。

原始JSON和完整日志保存在本会话job临时目录；可重复探针与本记录在仓库中持久保留。没有commit/push/merge、没有生产操作。
