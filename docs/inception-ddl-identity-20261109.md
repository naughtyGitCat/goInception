# 固定 inception DDL 身份只读调查（2026-11-09）

## 最新状态：补权后共享HTTP/Runner已复验（18:19 +08）

2026-11-09 UTC10:18:58–10:19:14，协调线程以最新Go+当前C#、产品实际固定身份，从普通/人工真实HTTP经共享Runner到OB实际执行与NPoco任务落库通过：两1行表Offline新增主键成功；两1000行表native明确拒绝、任务Failed/后项Skipped、schema/全部值不变；响应丢失Unknown与新进程零重放通过。31个协议envelope在内存核对产品身份，0不匹配，不保存凭据。五终态snapshot/revision重启前后字节一致。C#非环境843项本轮重跑全绿，Gobuild/focused race通过。本次未新增授权或改密码，夹具/任务/meta及隔离服务清理。

验收文档在Orchestrator根 `docs/ob-native-ddl-homelab-20261109.md`，原始证据 `/tmp/ob-native-ddl-shared-1109-2447aa/`。此前“Offline/真实ALTER未证”描述为其各轮结束时历史状态，不是当前剩余阻塞。未提交/发布/启用生产，未证明真实MySQL/backup/OSC兼容。

## homelab 权限同步记录（17:53 +08）

用户明确要求给 homelab 同步这两个视图的读取权限，并补充线上已有对应权限。09:53:00 UTC先重新核对：目标仍为 `10.136.38.71:2883`、dba业务租户1002，固定身份对两个视图各1142。09:53:57–09:53:58 UTC在同一业务租户用既有管理账号，仅执行：

```sql
GRANT SELECT ON oceanbase.`DBA_OB_TABLE_LOCATIONS` TO 'inception'@'%';
GRANT SELECT ON oceanbase.`DBA_OB_TABLET_REPLICAS` TO 'inception'@'%';
```

两条均成功。随后用产品实际固定身份新连接，`SHOW GRANTS`确认仅新增上述两个对象级SELECT；两个视图分别`SELECT 1 LIMIT 1`及按svr_ip/ls_id/tablet_id的JOIN读取均成功返回1。保留原有test.*授权，不授予oceanbase.*、全局SELECT或GRANT OPTION，不改密码、不连接生产/sys、不执行业务ALTER或启动前端。

原始脱敏证据：`/tmp/ob-homelab-inception-view-grants-20261109.log`。**此前缺权结论仅为homelab补权前状态，不是生产缺权证据；线上已有权限为用户提供的信息，本次未重新核验生产。** 以下保留此前只读调查及Go验收历史；固定身份Offline完整CHECK/EXECUTE和最新共享Runner验收仍未由本次GRANT替代。

## 补权前调查结论

1. **产品实际业务库身份来源是 C# 固定常量，不是 runner 的记忆值，也不是连接 goInception 前端的账号。** 普通工单和手工 DDL 均组装 `inception@{tenant}#{cluster}`，并使用 `GoInceptionClient.InceptionPasswordForBiz`。homelab 对应 `inception@dba#homelab_ocean_test`，目标 `10.136.38.71:2883/test`。
2. **11-09 runner 注入密码与产品实际常量一致性为 `false`；用户名一致性为 `true`。** 外部 runner 与实际 stage 内 runner 内容完全一致。这是当前证据中测试身份偏离产品的具体原因，不能将其 1045 归因为“产品固定账号坏了”。本文不记录任何密码、哈希、长度或凭据原文。
3. **产品实际常量认证成功；补充核验直接证明两个大小视图均不可读。** 首次产品连接于 2026-11-09 09:23:20 UTC 成功认证；按补充要求于 09:29:20 UTC（17:29:20 +08）再次用同一产品常量建立单次连接，逐项独立处理查询错误。返回 `CURRENT_USER()=inception@%`、`VERSION()=5.7.25-OceanBase_CE-v4.5.0.0`、`DATABASE()=test`；两个视图各自 SELECT 均报 **1142 / SELECT command denied**，而现有 `test` 业务表 SELECT 成功。均禁重连，未猜密码或做认证失败重试。
4. **11-08 和 11-09 原失败不是同一个阶段。** 11-08 固定身份已认证，CREATE/DROP 成功，既有表 ALTER 的 CHECK 被 `dba_ob_table_locations` SELECT 权限拒绝；11-09 runner 则在认证阶段报 1045，尚未执行到 ALTER 或大小视图权限校验。补充直连验证已独立确认当前两个视图缺读权限，不能把 runner 的认证错误与权限错误混为一谈。
5. **当前授权与执行证明须分开。** 产品身份自身的 `SHOW GRANTS` 得到 `USAGE ON *.*` 和 `ALL PRIVILEGES ON test.*`；直接 SELECT 证明固定身份不能读两个大小视图，此结论不再依赖管理员授权列表推断。固定身份既有 ALTER 的成功 CHECK/执行及修正凭据后的行数策略结果仍未证。

## 范围与基线

- Go 仓库：`/Users/zhangruizhi/Github/goInception`，分支 `feat/ob-native-ddl-timeout`，HEAD `dd88b8a4`，存在本次调查之前的未提交修改。
- 产品仓库：`/Users/zhangruizhi/Projects/DBAWorkflowOrchestrator`，同名分支，HEAD `d38d500`，存在本次调查之前的未提交修改。以下源码结论基于调查时工作树，不将 HEAD 当作当前全部代码。
- 先读项目总览第 878–900 行；只核对给定调用链、直接相关 Go 大小采集实现及两日证据，未展开大历史或 transcript。
- 本次只新增本文；未修改源码、runner、配置、凭据或其他文档，未 commit/push，未启动 goInception/HTTP 前端或其他服务。
- DB 访问仅 homelab 业务租户 dba；未连接生产或 sys 租户，未执行 CREATE/ALTER/DROP/GRANT/REVOKE/改密码。`oceanbase.DBA_OB_*` 是业务租户内视图名，不代表连接 sys 租户。

## 产品认证来源与调用链

产品源码根目录：`/Users/zhangruizhi/Projects/DBAWorkflowOrchestrator/src/DBAWorkflowOrchestrator`。

| 位置 | 只读核对结果 |
|---|---|
| `Services/Inception/GoInceptionClient.cs:79–85` | `InceptionUserForBiz` 定义业务审核用户名；`InceptionPasswordForBiz` 定义固定业务密码。注释明确它与连接 goInception 本身的账号无关。密码值不摘录。 |
| `Services/DDL/WorkflowDDLTaskService.cs:139–155` | 普通执行前 CHECK 与 `ExecuteItem` 的 goinception 分支都显式传入 `inception@{TenantName}#{ClusterName}` 和上述密码常量。 |
| `Services/OceanBase/ManualDDL/ManualDDLTaskService.cs:206–215,239–243` | 手工执行的 goinception 分支使用同一身份和密码常量；预检调用 `CheckManualDDLAsync`，后者内部使用该常量。 |
| `Services/Inception/GoInceptionClient.cs:94–124,149–183` | 手工 AST PRINT/CHECK 使用固定密码；单 DDL 能力探针和 EXECUTE 使用调用方传入的目标密码，未传时回退固定常量。上述产品调用方均显式传常量。 |
| `Services/Inception/GoInceptionClient.cs:426–436` | `BuildInceptionSql` 将业务目标用户、密码、host、port 写入 goInception 协议的 source 注释选项，并生成 USE 和 magic envelope；`quoteSource` 路径使用 JSON 引号处理，不是另一份密码来源。不记录完整 source 注释。 |
| `Services/Inception/GoInceptionClient.cs:345–358,468–484` | C# 到 goInception 的前端连接使用 `_settings.Host/Port` 和空的前端 UserID/Password；业务固定凭据通过协议传给目标 OB，二者不可混为一谈。 |

上述身份用于审核，且在新任务冻结 `ExecutionBackend=goinception` 时也用于原生 DDL 执行。若任务仍冻结为 Rhizobium，两个服务均调用 Rhizobium 客户端，执行身份不是这里的固定 inception。不能据本地分支将生产既有 Rhizobium 路由改称 inception 执行。

## runner 与产品的一致性

仅在内存读取/比较敏感值；不打印值或整行凭据来源。

| 核对项 | 布尔结果 |
|---|---|
| 产品 `InceptionPasswordForBiz` 与 `/tmp/obpolicy/run-row-policy-host.py` 注入 `SINGLE_DDL_OB_PASSWORD` 的值相等 | `false` |
| 产品固定密码非空 | `true` |
| runner 注入值非空 | `true` |
| 外部 runner 与 `/tmp/ob-row-policy-7f2f4da342/runner.py` 文件内容相等 | `true` |
| 外部/stage runner 注入密码相等 | `true` |
| probe 的审核用户名与产品在该 homelab 目标组装的用户名相等 | `true` |

直接链路：runner 第 54 行通过 Incus 环境注入审核凭据；`tools/ob_offline_policy_homelab/main.go:337–340` 读取 `SINGLE_DDL_OB_PASSWORD`，第 574–576 行以该值和 `inception@dba#homelab_ocean_test` 做既有 ALTER 的 CHECK。该测试工具并未自动读取产品 C# 常量；因此不能仅凭用户名相同就称其完全复现产品固定身份。

管理员凭据另由 `SINGLE_DDL_OB_ADMIN_PASSWORD` 注入，不能把管理员场景成功或结果总览当成固定 inception 场景成功。

## 两日证据与真实差异

### 2026-11-08：认证已通过，既有 ALTER 的审核存在权限缺口

来源：产品仓库 `docs/ob-native-ddl-homelab-20261108.md` 和 `docs/ob-native-ddl-evidence-20261108/protocol.json`。

- protocol 时间：`2026-11-08T09:04:05Z`；目标 `10.136.38.71:2883`，业务租户 ID 1002，版本与本次一致。
- `audit_identity_create_7200`：CREATE 为 `EXECUTED / Execute Successfully`，`passed=true`。
- `audit_identity_drop`：DROP 为 `EXECUTED / Execute Successfully`，`passed=true`。
- `audit_identity_existing_alter`：USE 的 CHECK 为 level 0；ALTER 为 level 2，报 `SELECT command denied to user 'inception'@'%' for table 'dba_ob_table_locations'`，`environmentBlocked=true`。
- 该 ALTER case 的 `passed=true` 不能解释为 ALTER 成功；它同时显式标记环境阻塞，SQL 行本身是审核拒绝。历史文档明确未补 GRANT、未借管理员替代产品权限。

### 2026-11-09：runner 身份值偏离产品，认证先失败

来源：`/tmp/ob-row-policy-7f2f4da342/warn-results.json`、`error-results.json`，及同目录两份完整日志。

| phase | JSON 时间（UTC） | 固定身份 case | 实际阻塞 |
|---|---|---|---|
| warn | `2026-11-09T09:00:43Z` | `passed=false, environmentBlocked=true` | `Error 1045 (42000): Access denied`，发生在 `inception_magic_start`，仅一个 CHECKED 结果行 |
| error | `2026-11-09T09:00:52Z` | `passed=false, environmentBlocked=true` | 同上，尚未到 USE/ALTER 和视图读取 |

两份结果各 31 cases，30 passed、1 environmentBlocked，总体 `allPassed=true`。此总标记允许环境阻塞，**不证明固定账号场景通过**。

完整扫描 `warn.log` 和 `error.log`，各 93 行，各出现一次 1045，无大小视图 command-denied 记录；时间范围分别为日志标记的 `2026/11/09 09:00:34.995–09:00:43.466`、`09:00:43.528–09:00:52.807`，与 JSON 同轮时间对应，不以旧日志尾部冒充当前证据。

**可成立的因果结论：** 本轮 runner 使用的密码不是产品值，而同一目标上产品值本次认证成功，故本轮 1045 不能用于判断产品账号失效，更不能作为大小视图权限检查结果。11-08 已证的视图权限问题与 11-09 的 runner 注入偏差是两个独立层次。未检查账号历史审计，不声称期间绝无外部改密/授权操作；本次没有必要猜测或修改账号。

## 本次 homelab 只读核验

路径：SSH 到 `mechrevo-mini-07c.lan`，通过 Incus 在 `db-ocean-lxc1` 使用既有 `/usr/bin/obclient`，所有连接显式指定 `-h10.136.38.71 -P2883`，从未采用配置的默认 loopback。宿主未安装 pymysql，因此未尝试依赖该模块。

### 首次产品常量连接：认证成功，后续查询曾被无关变量阻断

时间 `2026-11-09T09:23:20.642099+00:00`：

- `--no-defaults --skip-reconnect`；用户为产品组装的 homelab 身份，库 `test`；敏感值仅通过进程内存/标准输入送入客户端环境，不在命令参数或文档显示。
- `SELECT CURRENT_USER(), USER(), VERSION(), DATABASE()` 返回：
  - `inception@%`
  - `inception@10.136.38.71`
  - `5.7.25-OceanBase_CE-v4.5.0.0`
  - `test`
- 随后额外的 `SELECT @@ob_tenant_id` 返回 `ERROR 1193 (HY000): Unknown system variable 'ob_tenant_id'`。客户端退出码 1 来自该 SQL，不是认证失败。
- 首次 `-e` 批次在错误处终止；该批原计划同批的 SHOW GRANTS 和两个视图 SELECT **实际未执行**，没有把它们写成成功/权限拒绝。其后按补充要求另作一次产品连接收口，见下文。当前租户由显式登录用户名绑定 dba；ID 1002 来自两日已有证据，不冒充本次变量回读。

### 业务管理员：一次独立只读连接补查授权和视图存在性

时间 `2026-11-09T09:24:22.068506+00:00`：

- 私下读取既有 `/root/.obclient_dba.cnf` 校验配置用户属于业务 dba、非 sys；未打印配置或密码，未修改配置。用该配置、显式远程 host/port 和 `test` 连接，`CURRENT_USER()=root@%`，版本同上，退出码 0。
- `SHOW GRANTS FOR 'inception'@'%'` 只保留权限和作用域，丢弃任何认证子句：
  - `GRANT USAGE ON *.*`
  - `GRANT ALL PRIVILEGES ON test.*`
- `SELECT 1 FROM oceanbase.DBA_OB_TABLE_LOCATIONS LIMIT 1` 返回一行 `1`。
- `SELECT 1 FROM oceanbase.DBA_OB_TABLET_REPLICAS LIMIT 1` 返回一行 `1`。
- 这仅证明视图存在且管理员可读，并显示固定身份的授权范围；**不是以管理员代替固定身份通过权限测试**。产品身份对两个视图的直接结果由后续补充连接取得。

### 补充产品常量连接：逐项独立查询，直接收口视图读取边界

按补充要求，于 `2026-11-09T09:29:20.130701+00:00` 建立一次新的产品身份连接；全调查共两次产品连接、一次管理员连接，均为只读、均禁重连，没有认证失败后猜测/重试。补充连接不读取管理员配置、不查询 `@@ob_tenant_id`。

使用持久 `obclient --no-defaults --skip-reconnect --batch --unbuffered --force` 会话，Python 对每项查询单独 try/except；每条 SQL 后发一个唯一结束标记，将成功行和该项错误码独立收集。客户端 SQL 错误由 `--force` 继续处理，单项失败不阻断后续查询，所有项目均在同一连接中完成。大小视图和业务表查询均 LIMIT 1，并带三秒 QUERY_TIMEOUT 提示。

| 独立项目 | 产品身份直接结果 |
|---|---|
| `SELECT VERSION(),CURRENT_USER(),DATABASE()` | 成功；`5.7.25-OceanBase_CE-v4.5.0.0`、`inception@%`、`test`。 |
| `SHOW GRANTS` | 成功；`USAGE ON *.*`、`ALL PRIVILEGES ON test.*`；认证子句不输出。 |
| `SELECT 1 FROM oceanbase.DBA_OB_TABLE_LOCATIONS LIMIT 1` | **1142 / SQLSTATE 42000**；`SELECT command denied`，表名 `dba_ob_table_locations`，零成功行。 |
| `SELECT 1 FROM oceanbase.DBA_OB_TABLET_REPLICAS LIMIT 1` | **1142 / SQLSTATE 42000**；`SELECT command denied`，表名 `dba_ob_tablet_replicas`，零成功行。 |
| 从 `information_schema.TABLES` 选取 test 一张既有 BASE TABLE，LIMIT 1 | 成功返回一个候选；表名仅用于内存构造下一条限量查询，不另行输出。 |
| 对所选既有 `test` 表执行 `SELECT 1 ... LIMIT 1` | 成功，返回一行 `1`；未建对象。 |

进程退出码 0 表示 force 会话正常结束，**不表示两个视图查询成功**；两条 1142 已分别记录。至此，“产品身份当前是否可读这两个视图”的边界已由直接执行证据闭合：**两者均不可读，但业务库现有表可读**。并未验证真实 ALTER 或改变任何授权。

### 当前代码与权限解释的边界

`session/oceanbase_offline_table.go:85–129` 的大小采集 JOIN 两个 `oceanbase.DBA_OB_*` 视图，因此业务库 `test.*` 的 ALL 并不等于拥有这些视图的 SELECT。

但当前 11-09 工作树 `session/session_inception.go:3400–3408` 已将大小采集失败保留为未知并告警；`oceanbase_offline_table.go:15–41` 在策略需要大小证明时按未知处理。不能将 11-08 旧场景的视图缺权推广为“当前所有 ALTER 必然阻塞”。具体 Online/Offline、MB/rows 门槛路径是否可在该固定身份完成，仍需修正测试身份后的真实 CHECK 证明。

## 已证权限与未证执行能力

| 项目 | 证据强度与边界 |
|---|---|
| 当前产品固定值可认证、可进入 test | 首次与补充产品连接均直接证明；无认证失败。 |
| 当前固定身份对 test 的授权和读取 | 产品自身 SHOW GRANTS 直接证明 `ALL PRIVILEGES ON test.*`，且既有业务表限量 SELECT 返回一行；包含库级 ALTER 授权不等于具体 ALTER 已成功执行。 |
| 固定身份 CREATE/DROP 执行 | 11-08 既有原始 protocol 成功；本次没有再执行 DDL。 |
| 固定身份读取 locations 视图 | 补充产品连接直接 SELECT，错误 **1142**，当前不可读；与 11-08 已有拒绝证据一致。 |
| 固定身份读取 replicas 视图 | 补充产品连接独立 SELECT，错误 **1142**，当前不可读；不是从首个视图错误推断。 |
| 当前两视图存在且管理员可查询 | 管理员 SELECT 各一行证明；与产品身份的两条直接拒绝证据分开。 |
| 固定身份既有表 ALTER 的完整 CHECK / 原生执行 | 未证；11-08 CHECK 被拒，11-09 runner 认证失败，本次只读未执行。 |
| 11-09 rows-only / MB 组合策略在产品身份下的结果 | 未证；管理员场景不能填补这一空白，修正身份后可能暴露下一层权限/策略阻塞。 |
| 生产权限、生产切流及 OSC 工具执行 | 不在本次范围，不作通过结论。 |

## 脱敏运行命令记录

以下是实际命令的脱敏结构，不包含可直接复原密码的内容。敏感文件只在内存解析，stdout/stderr 在返回前去除敏感值；SHOW GRANTS 仅输出权限与 scope，认证子句全部舍弃。

```text
# 本地只读定位与基线
pwd
git -C <Go仓库> status --short --branch
git -C <产品仓库> status --short --branch
git -C <各仓库> rev-parse --short HEAD
python3 -I - <内存解析指定常量/runner、比较布尔值、汇总完整JSON与日志>

# SSH只读客户端能力/时间确认
ssh -i <既有SSH密钥> -o BatchMode=yes -o ConnectTimeout=10 root@mechrevo-mini-07c.lan \
  '<检查宿主python模块；incus exec db-ocean-lxc1 -- which obclient；date -u>'

# 产品身份：密码由本地源码私下读取，经SSH标准输入传给远端Python；
# 再经标准输入传入容器shell的MYSQL_PWD，不把敏感值拼到argv。
incus exec db-ocean-lxc1 -- sh -c \
  '<从stdin读取并export MYSQL_PWD；exec obclient --no-defaults --skip-reconnect \
    --connect-timeout=5 --batch --skip-column-names --force \
    -h10.136.38.71 -P2883 -u<产品homelab用户> --database=test -e <只读SQL>>'
# 上述为首次产品连接：SELECT身份/版本/库；SELECT @@ob_tenant_id（1193后终止）。
# 仅首次批次的SHOW GRANTS、两个视图SELECT未执行；已由下述补充连接收口。

# 补充产品连接：沿用上述安全注入方式，但不使用-e；
# 一个持久obclient会话接收独立SQL；Python逐项try/except、结束标记分隔结果。
incus exec db-ocean-lxc1 -- sh -c \
  '<从stdin读取并export MYSQL_PWD；exec obclient --no-defaults --skip-reconnect \
    --connect-timeout=5 --batch --unbuffered --skip-column-names --force \
    -h10.136.38.71 -P2883 -u<产品homelab用户> --database=test>'
# stdin实际依次执行：SELECT VERSION(),CURRENT_USER(),DATABASE(); SHOW GRANTS;
# SELECT /*+ QUERY_TIMEOUT(3000000) */ 1 FROM oceanbase.DBA_OB_TABLE_LOCATIONS LIMIT 1;
# SELECT /*+ QUERY_TIMEOUT(3000000) */ 1 FROM oceanbase.DBA_OB_TABLET_REPLICAS LIMIT 1;
# SELECT TABLE_NAME FROM information_schema.TABLES
#   WHERE TABLE_SCHEMA='test' AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME LIMIT 1;
# SELECT /*+ QUERY_TIMEOUT(3000000) */ 1 FROM `test`.`<既有表名>` LIMIT 1;
# 每项后另发唯一结束标记SELECT，不把上一项的1142当作连接失败。

# 管理员业务租户：先私下检查cnf用户非sys；不打印cnf内容。
incus exec db-ocean-lxc1 -- obclient \
  --defaults-extra-file=/root/.obclient_dba.cnf --skip-reconnect --connect-timeout=5 \
  --batch --skip-column-names --force -h10.136.38.71 -P2883 --database=test
# stdin依次：SELECT身份/版本/库；SHOW GRANTS FOR 'inception'@'%';
# SELECT 1 FROM oceanbase.DBA_OB_TABLE_LOCATIONS LIMIT 1;
# SELECT 1 FROM oceanbase.DBA_OB_TABLET_REPLICAS LIMIT 1;
```

未运行旧 runner，未启动其前端，未执行 probe 的 `--run`，未创建/清理数据库夹具。调查没有补造新的 JSON/日志证据文件；本节保留本次工具返回的脱敏事实与命令结构。

## 后续最小步骤（仅建议，未施工）

1. 后续测试运行时，从可信产品凭据来源安全注入审核密码，保留“与产品常量相等”的布尔断言；不再使用记忆值，也不将真实密码硬编码到 runner、报告或命令参数。
2. 产品固定身份的认证、SHOW GRANTS、两个视图 SELECT 和既有业务表限量读取已在补充连接完成，无需为此重复登录。保留两条独立 1142 作为当前权限边界，不以管理员权限或客户端总体退出码覆盖该结果。
3. 先对已有业务测试表做 CHECK-only，分清认证、元数据读取、大小/行数策略的实际阻塞。不要直接重跑含 CREATE/ALTER/DROP 的旧测试工具来填补只读调查证据。
4. 若确认需补视图权限，由用户另行审批最小权限方案；本次不 GRANT、不改密码、不借管理员替代固定身份宣称通过。只有再次明确授权 DDL 后，才验证固定身份既有 ALTER 的实际执行，并分别记录 CHECK 与 EXECUTE 结果。

当前固定身份可认证、可读业务表，且对两个大小视图均被直接 SELECT 以 1142 拒绝，权限边界已查明。调查完成时，剩余未证项是修正 runner 身份后的策略 CHECK 及真实 ALTER 链路，不是账号是否有效。本调查不触发生产路由、权限或配置变更。

## 调查后协调线程的独立验收补记

本节不属于上述 subagent 只读调查。09:35:08–09:35:45 UTC，协调线程在既有授权的新建自有对象测试范围内，修正临时 runner 的注入来源，以产品源码常量私下安全注入，未修改产品或数据库凭据。最新 Go 构建在 warn/error 隔离实例均以实际固定身份对自有1000行表完成 **Online ADD COLUMN CHECK，USE/ALTER均level0**；未实际执行该固定身份 ALTER。两份完整日志各有一条 locations 大小采集1142 warning，验证当前采集失败不再误拒绝 Online。

因此，“固定身份既有表完整CHECK未证”是上表描述的调查结束时边界；后续已增加上述一个Online场景。仍不能声称固定身份Offline+MB策略或真实ALTER执行通过。历史runner注入值比较仅针对09:00轮原文件；临时runner现已修正，不再描述其当前内容为旧值。详见 [ENUM/SET修复与最新验收](ob-enum-set-fix-homelab-20261109.md)，证据 `/tmp/ob-row-policy-7d133055ee/`。
