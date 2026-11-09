# OceanBase 离线 DDL：大小与行数联合策略验收

日期：2026-11-09。状态：**本地实现与隔离 homelab 验证完成，未提交、推送或发布**。本文保留当轮结果；当时未包含的 ENUM/SET 修复后续已完成，见 [分类修复与最新验收](ob-enum-set-fix-homelab-20261109.md)。固定身份旧 runner 的1045后来证实为注入值与产品不一致，详见 [只读调查](inception-ddl-identity-20261109.md)，不代表产品凭据失效。

## 策略

- 大表 = 有效大小达到 MB 门槛，**或**估计/限量实读行数达到行数门槛，边界使用 `>=`。
- `TABLE_ROWS` 只能证明大表，低估计值/0 不能证明小表。
- 未判大时，用 `SELECT /*+ QUERY_TIMEOUT(3000000) READ_CONSISTENCY(STRONG) */ 1 FROM 表 LIMIT 行数门槛+1` 确认，最多返回门槛+1行，客户端/服务端查询均限时3秒。不执行全表 COUNT。
- 小表豁免仅属于本条 ALTER：实读行数低于门槛，且启用 MB 条件时大小必须有效且低于门槛。合法落盘0不再单独构成豁免。
- 尚未证明大表时，启用 MB 条件但大小未知，或行数探测失败/超时/结果异常，报 level 2，不能因忽略告警继续执行。若任一指标已经证明大表，不需要另一指标来证明小表；native 仍拒绝。
- `check_offline_ddl_large_table_rows` 默认0；有效 MB 门槛启用且 rows=0 时采用100000行安全上限。两项均关闭时不做新行数探测，保持 opt-in 默认。
- Online、关闭策略、rows-only 不因无关大小视图 NULL 被拒绝。大小采集保持未知标志，实际拒绝由 Offline 策略决定。
- 不缓存上一条 ALTER 的小表证明，不使用 WEAK/follower 读证明小表，不改连接池会话路由，不进行租户维护。
- 这是审核时点的快照，不锁住后续业务写入，也不保证执行时数据量不变。

## 范围与端点

- 前端：dba-app，隔离4005 warn / 4007 error，依次启动并停止；OSC/Ghost关闭。
- 后端：`10.136.38.71:2883`，`5.7.25-OceanBase_CE-v4.5.0.0`，dba业务租户，tenant_id=1002，test库。
- 实际核验 VERSION / DATABASE / CURRENT_USER / tenant_id。OBProxy返回的 `@@hostname` 为空，额外从本次夹具的 `DBA_OB_TABLE_LOCATIONS` 核验实际 observer 地址：`10.136.38.71,10.75.169.71`。
- 大小门槛1 MB，行数门槛1000；仅本次随机新建的自有表。
- **无 MINOR FREEZE、合并、统计刷新、sys连接、账号或授权变更，无生产变更**。
- 工具：`tools/ob_offline_policy_homelab/main.go`，先只读核对隔离前端的四个策略配置，再允许创建夹具及发送请求。

## 最终真实结果

最终两轮 UTC 09:00:34–09:00:52（北京时间17:00:34–17:00:52）。每轮31条记录：**30条非阻塞记录通过，1条固定身份认证阻塞**。30包含夹具核验、审核/执行及清理记录，不是30种独立DDL。

四个边界夹具的大小均为有效0 MB，`TABLE_ROWS`均为0，STRONG限量实读分别为0/999/1000/1001：

| 实际行数 | 普通CHECK：ADD PRIMARY KEY（warn实例） | 普通CHECK（error实例） | native执行结果 |
|---:|---|---|---|
| 0 | level 0 | level 0 | 成功加主键，数据不变 |
| 999 | level 0 | level 0 | 成功加主键，数据不变 |
| 1000 | level 1大表离线提示 | level 2 | `single_ddl_offline_ddl_not_supported`；无执行、无主键，结构/所有行值不变 |
| 1001 | level 1大表离线提示 | level 2 | 同上 |

两实例还均验证：

- 达到行数门槛的表，Online ADD COLUMN仅CHECK，level 0，不产生离线大表提示；本轮不在该表执行修改。
- 小表 DROP COLUMN、VARCHAR收窄：level 1；大表相同审核：level 2。
- 小表实际 native DROP COLUMN成功，所有保留列的行值一致。
- 独立1行表，**完整goInception审核+native执行**三种跨家族转换均成功，核对实际COLUMN_TYPE、SHOW CREATE及所有行值：
  - TEXT→BLOB，结果blob；
  - BLOB→TEXT，结果text；
  - INT→VARCHAR(20)，结果varchar(20)。
- TEXT/BLOB NOT NULL仍出现既有level 1告警；native请求显式忽略告警，level 2仍拒绝。测试没有关闭错误保护。

这些结果仅证明当前版本、定义和可转换数据；不保证任意跨类型转换都成功，也未测量并发DML锁等待。

## 未通过与修正记录

1. 首轮warn/error各有3条跨类型CHECK断言失败：实际均level 1，告警字段是“表名.列名”，驱动错误地期待只有“列名”。native三种转换当轮均已成功。修正测试的限定列名匹配后，用独立新表重跑，最终两轮全部非阻塞记录通过。首轮失败原始证据保留，不冒充通过。
2. 固定 `inception` 身份本轮使用的既定凭据在**认证阶段1045 Access denied**，没有进入表元数据审核；这与此前已认证后大小视图权限不足是不同阻塞。本轮不能再次推断其catalog授权范围，也不能证明该身份可执行这些DDL。成功链路使用dba业务租户管理身份，未修改凭据或GRANT。
3. 独立审查发现大小采集过早写level 2会误拒绝Online/关闭策略/rows-only。用完整`checkAlterTable` fake复现后，改为采集层保留未知、由Offline策略决定是否拒绝；新增完整调用链回归，四场景通过。

## 本地验证

最终代码验证：

- `go build ./...`通过。
- 104个独立session顶层测试，含子测试510条通过、0失败、0跳过。
- `go test -race ./session -run '^(TestOceanBase|TestOfflineDDL|TestSingleDDL|TestSingleDML|TestOBCount)' -count=1`通过。
- config、两个homelab工具的测试及race通过；工具vet通过。
- Darwin CLI重新构建并运行`--help`，退出码0；Linux amd64交叉编译并实际启动/驱动如上。
- `git diff --check`通过。
- `config`测试曾揭示样例缺少已有的`check_offline_ddl=true`，与程序默认不一致；已在样例补齐，完整config测试通过。新行数门槛默认仍为0。

失败及排除边界：扩大测试筛选时误纳入`helper_test.go`的`TestExpression`（实际上调用gocheck `TestingT`，并非独立表达式单测），触发localhost旧套件1045及panic。未修改本地数据库、未重跑该聚合入口。最终筛选明确排除它与其余11个gocheck聚合入口；**不是完整旧session套件全部通过**。失败记录`/tmp/ob-offline-standalone-tests.jsonl`保留，安全筛选结果为`/tmp/ob-offline-safe-standalone-tests.jsonl`。

## 证据与清理

- 首轮失败证据：`/tmp/ob-row-policy-723dd7fab1/`。
- 最终证据：`/tmp/ob-row-policy-7f2f4da342/{warn-results.json,error-results.json,summary.json,warn.log,error.log}`。
- 最终表前缀：`ddl_policy_1109_64de755f3e`、`ddl_policy_1109_c2240940bd`；每轮5张表。工具清理后通过独立连接再次查询，各前缀残留数0。
- 首轮另10张表也核验残留0。两个运行目录内guest二进制/配置/临时存储均删除；结束后再次确认4005/4007均无监听。
- 原始证据保留本机及homelab宿主机，不把包含内部细节的日志发布到外部服务。
- 最终homelab前端Linux二进制SHA256：`75654c8b1d70e0e86868df008786714744a3220830238dda0a3138f42cec8e15`。
- 最终探针SHA256：`fb4f8c46c9b6b41d2828018cfb4b11fba63aa48b4eb1225b86baad7ef9865688`。

**当轮未提交/推送/发布；ENUM/SET 当时未修。后续已修并复验，见文首链接；最新共享线路和固定身份 Offline 策略尚待验收，整体上线结论仍不成立。**
