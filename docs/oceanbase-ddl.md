# OceanBase DDL 原理与 goInception 适配详解

> 路由边界澄清（2026-11-09）：下面分类图是算法说明，不是启用 OSC 的
> 证明。`checkDDLOffline` 现在对每条 OceanBase ALTER 都做在线/离线判定并记录；
> 只有 Online 时关闭 pt-osc，Offline 时保持原有 OSC 分流。大表 + 离线 DDL
> 由大小与行数 OR 门槛共同控制（大小未配置时仅在 OSC/Ghost 开启时回退
> `osc_min_table_size`）。低估计行数和合法落盘 0 都不是小表证明；小表必须经
> 每条 ALTER 的 STRONG 限量读取确认。未知子句一律按 Offline 处理。
> 新增显式 single-DDL 路径在离线且大表时拒绝，不改变全局默认或阈值，
> 不因为工具分支存在就声称 OceanBase 上 pt-osc/gh-ost 实迁已验证。

## 一、OceanBase DDL 基础概念

### Online DDL vs Offline DDL

OceanBase 基于 **LSM-Tree** 存储引擎，与 MySQL 的 B-Tree 更新就地写入不同，增量数据先写 MemTable，后台 Compaction 时合并到 SSTable。这个架构决定了 DDL 的行为：

```
MySQL DDL 三种算法：
┌──────────┐  ┌──────────┐  ┌──────────┐
│ INSTANT   │  │ INPLACE  │  │  COPY    │
│ 只改元数据 │  │ 引擎内重建 │  │ 建新表拷贝│
│ 不阻塞DML │  │ 短暂阻塞  │  │ 全程阻塞  │
└──────────┘  └──────────┘  └──────────┘

OceanBase DDL 两种类型：
┌──────────────────────────┐  ┌──────────────────────────┐
│  Online DDL               │  │  Offline DDL              │
│ 只改 schema 元数据         │  │ 需要重建表（table_id 变）  │
│ 数据补全推迟到 Compaction   │  │ 创建影子表 → 加表锁       │
│ DML 不受影响               │  │ → 数据重组 → 表交换       │
│                           │  │ DML 全程阻塞（TPS=0）     │
└──────────────────────────┘  └──────────────────────────┘
```

**Online DDL 的巧妙之处**：比如 `ADD COLUMN`，OceanBase 只修改 schema 元数据，不立即改已有数据。新写入的行用新 schema（带默认值），旧行在下次 Compaction 时自然补全。这是 LSM-Tree 架构的天然优势。

**Offline DDL 的代价**：相当于 MySQL 的 COPY 算法。创建影子表 → 加**表锁**（阻塞所有 DML）→ 数据重组写入影子表 → 替换原表。整个过程中 TPS/QPS 降到 0。

### 操作分类表

| 操作 | 类型 | 原因 |
|------|------|------|
| ADD COLUMN（末尾） | **Online** | 只改元数据，Compaction 补全 |
| ADD COLUMN（FIRST/AFTER）| OB ≥4.2.5 **Online**，<4.2.5 **Offline** | 4.2.5 优化了中间插列 |
| DROP COLUMN | **Offline** | 需要物理删除数据 |
| RENAME COLUMN | **Online** | 纯元数据 |
| MODIFY COLUMN 改类型（如 CHAR→VARCHAR） | **Offline** | 需要数据转换重写 |
| MODIFY COLUMN 扩类型（如 INT→BIGINT） | **Online** | 兼容扩展，不需重写 |
| CREATE INDEX | **Online** | 后台异步构建，DML 不阻塞 |
| ALTER PARTITION | **Offline** | 需要数据重分布 |
| DROP/TRUNCATE PARTITION | **Online** | 不需要数据重组 |
| ADD COMMENT | **Online** | 纯元数据 |
| 改主键 | **Offline** | 全表重建 |

### 与 MySQL 的关键差异

| 维度 | MySQL | OceanBase |
|------|-------|-----------|
| 存储引擎 | B-Tree，就地更新 | LSM-Tree，追加写入 |
| Online DDL 锁行为 | INPLACE 算法开头/结尾短暂 MDL 锁 | 完全不加表锁 |
| Offline DDL 锁行为 | COPY 算法全程锁 | 全程表锁，TPS=0 |
| 索引构建 | 并行排序 | 分布式排序 + 旁路写入（绕过 MemTable 直接写 SSTable），比 MySQL 快 3-4 倍 |
| Schema 协调 | 单节点，无需协调 | 多版本 Schema，落后节点限制 DDL 事务但不杀进程 |
| 前缀索引列 | 可修改 | **不可修改** |
| 主键 | 可后加 | 部分版本**必须建表时指定** |
| 触发器 | 支持 | **不支持**（导致 pt-osc 原版不可用） |
| SEQUENCE | 不支持 | 支持 Oracle 风格序列 |

### OceanBase 4.x DDL 改进

- **旁路写入机制（V4.0+）**：DDL 期间数据绕过 MemTable，通过 Paxos 复制直接写入 SSTable，性能比 V3.x 提升约 5 倍
- **分布式执行计划重设计**：采样+扫描和排序+扫描两个子计划跨多节点并行流水线执行
- **V4.3+ 增强**：行列存在线转换、快速中间列添加、扩展并行 DDL 操作

---

## 二、goInception 中的 OceanBase DDL 审计流程

### 整体判断逻辑

```
ALTER TABLE 进来
       │
       ▼
① checkAlterUseOsc(table)
   表大小 >= osc_min_table_size 且 osc 开关打开？
       │
   是 → useOsc = true        否 → useOsc = false（小表直接执行）
       │
       ▼
② checkDDLInstant(node, table)              ← 只对 MySQL 生效
   MySQL 5.7/8.0 能用 INSTANT 算法？
       │
   是 → useOsc = false（MySQL 原生秒级完成）
       │
       ▼
③ checkDDLOffline(node, table)              ← 只对 OceanBase 生效
   fail-closed 白名单判定每个子句是否在线
       │
   包含任意 Offline 子句 → record.obOfflineAlter = true（useOsc 保持原分流）
   全部子句 Online       → record.obOfflineAlter = false（关闭 pt-osc）
       │
   Offline 且大小或估计行数 >= 门槛 → record.obOfflineLargeAlter = true
   否则 STRONG 限量读取: 行数 >= 门槛为大表; 低于且大小有效才授予小表豁免
   大小未知/探测失败 → level 2 拒绝; Online 不做行数探测
       │
       ▼
④ 大表离线 DDL（check_offline_ddl_large_table_size / rows）
   as_error = false → 告警；as_error = true → 报错
       │
       ▼
⑤ 最终执行
   useOsc == true  → 走 pt-osc 或 gh-ost
   useOsc == false → 直接执行 DDL
```

### Offline DDL 分类逻辑（isOBAlterOnline）

分类采用白名单 + fail-closed：只有能确认的在线子句才返回 Online，未知子句、
未知选项、无法稳定判定的类型变更一律按 Offline 处理。

| 子句 | 判定 |
|------|------|
| ADD INDEX / UNIQUE / KEY | Online |
| ADD PRIMARY KEY / FOREIGN KEY | Offline |
| DROP INDEX / RENAME INDEX | Online |
| ADD COLUMN（末尾；FIRST/AFTER 需 OB ≥ 4.2.5 对应版本线） | 版本感知，见 `classifyOceanBaseAddColumn` |
| ADD COLUMN 带 AUTO_INCREMENT / STORED 生成列 | Offline（OB 3.x 除外） |
| MODIFY / CHANGE COLUMN | 同符号整型扩宽、字符串/大对象同家族扩容、VARCHAR(x≤255)↔TINYTEXT、字符集/排序规则不变、无 FIRST/AFTER、无其他选项 → Online；其余 Offline |
| DROP COLUMN | Offline |
| RENAME COLUMN / ALTER COLUMN / RENAME TABLE | Online |
| ALTER TABLE OPTION | AUTO_INCREMENT / ROW_FORMAT / COMPRESSION / BLOCK_SIZE / COMMENT / COLLATE，以及不带 CONVERT 的 CHARSET → Online；其余 Offline |
| ADD / DROP / TRUNCATE PARTITION | Online |
| ALTER PARTITION（仅自动分区属性、无显式分区定义） | Online |
| EXCHANGE / REORGANIZE / REMOVE PARTITIONING 等 | Offline |
| ALGORITHM / LOCK 提示 | 不改变 OceanBase 执行方式，视作 Online |

注意：`DROP COLUMN`、收窄列类型、整型有符号/无符号互转等还有独立的
`check_offline_ddl` 规则。这些规则与离线分类共用同一门槛：小表降级为告警，
大表（或门槛未配置、无法判定大小时）保持报错。

### 大表离线 DDL 告警/报错

Offline DDL 会重建表并在整个数据重组期间持有 `EXCLUSIVE` 表锁，DML 全程阻塞。
有效落盘 MB **或行数**任一达到门槛，即为大表：

```toml
# MB。0 仅在 OSC/Ghost 开启时回退到 osc_min_table_size
check_offline_ddl_large_table_size = 1024
# 0（默认）: 有效 MB 门槛开启时使用100000行，否则关闭行数检查
check_offline_ddl_large_table_rows = 100000
# false（默认）大表提示仅告警；true 直接报错
check_offline_ddl_large_table_as_error = false
```

- `TABLE_ROWS >= 行数门槛` 可直接判大；小于门槛或 0 不作为小表证明。
- 否则读取 `SELECT /*+ QUERY_TIMEOUT(3000000) READ_CONSISTENCY(STRONG) */ 1 FROM 表 LIMIT 门槛+1`，最多返回门槛+1行，不做全表 COUNT。客户端/服务端查询均限时3秒。
- 只有行数低于门槛，且启用了 MB 门槛时该大小结果有效且低于门槛，才授予**本条 ALTER**的小表豁免。仅启用行数门槛时不要求 MB 证明。
- NULL/扫描失败/结果流失败不能当作0；尚未证明大表时，启用大小检查但大小未知，或行数探测失败/超时，均 level 2 拒绝。仅存在审核缓存、尚未真实创建的表也不授予豁免。若任一指标已证明大表，不依赖另一指标，小表豁免仍不成立。
- 有效 MB 门槛与显式行数门槛都关闭时不做新行数探测，保留既有 opt-in 默认。开启 MB 门槛会自动启用10万行安全上限。
- 每条 ALTER 重新采集大小并独立确认行数，不复用上一条的小表结论。不做租户级冻结或维护，不改连接池会话路由。

`check_offline_ddl = false` 只关闭大表提示，不关闭分类、已启用的门槛或未知结果拒绝；single-DDL 仍拒绝大表离线 DDL，没有 pt-osc/gh-ost 兜底。探测是审核时点的快照，不锁住后续写入，也不承诺 DDL 执行时数据量不变。

同一门槛也决定既有独立规则的级别：

| 场景 | 级别 |
|------|------|
| 本条 ALTER 已证明大小、行数均低于启用门槛 | 告警（level 1），DDL 可继续 |
| 大小或行数达到门槛 | 报错（level 2） |
| 启用门槛但大小未知或探测失败 | 报错（level 2），不授予豁免 |
| 门槛均关闭 | 保持既有独立规则报错 |

当前改动覆盖 OceanBase 4.x 的 `DROP COLUMN`、VARCHAR/CHAR 长度收窄、
DECIMAL 容量缩小与整型有符号/无符号互转。DECIMAL(p,s) 分别比较整数位
`p-s` 与小数位 `s`，任意一侧缩小均按收窄处理；普通 CHECK 与忽略告警的
执行路径使用相同门槛，MySQL 的既有审核行为不变。

大小采集失败与落盘0的行数盲区已补充严格结果校验和限量行数证明。
ENUM/SET 整串转小写的漏洞已修复：复用 SQL parser 比较类型与成员列表，
仅数量、顺序和值逐字一致时允许 Online；大小写变化、重排或增删均保守按
Offline，包括 CI。不能据此声称这些修改在所有 collation 下都必然重建。
修复已通过单测与 CI/BIN/CS 的实际 CHECK/native 大小表验收，见
[ENUM/SET 修复验收](ob-enum-set-fix-homelab-20261109.md)；此前研究和行数边界分别见
[空/小表和 ENUM 实测](ob-table-size-enum-homelab-20261109.md)、
[大小与行数策略验收](ob-offline-rows-homelab-20261109.md)。当前仍未提交/推送/发布，
11-09 18:19 +08补权后固定身份的最新共享HTTP/Runner配对验收已完成，普通/人工小表Offline真实执行、大表拒绝、Unknown和重启零重放通过；详见Orchestrator根 `docs/ob-native-ddl-homelab-20261109.md`。仍未发布启用，不自动切生产。

跨家族类型转换的普通 `ER_CHANGE_COLUMN_TYPE` 审核默认是 level 1 告警，
不是“始终报错”。在 homelab OB 4.5.0.0 的可转换数据下直接执行
`TEXT → BLOB`、`BLOB → TEXT`、`INT → VARCHAR`、`VARCHAR → INT` 均成功。
是否支持转换、转换是否为 Offline DDL、审核是否拒绝是不同问题；
具体结果取决于版本、定义、约束与数据。见 [homelab 验证记录](ob-offline-policy-homelab-20261109.md)。

### DDL 冲突检测（checkAlterOperationConflicts）

OceanBase 不允许在一条 ALTER TABLE 中混合某些操作组合：

```sql
-- MySQL OK，OceanBase 报错：
ALTER TABLE t
  MODIFY COLUMN a BIGINT,
  DROP INDEX idx_b;

-- 必须拆成两条：
ALTER TABLE t MODIFY COLUMN a BIGINT;
ALTER TABLE t DROP INDEX idx_b;
```

goInception 使用位掩码检测以下冲突组合并在审计阶段拦截：
- 改列类型 + 删索引 → 拒绝
- 删列 + 删索引 → 拒绝
- 改列/加列 + 改主键 → 拒绝

### 前缀索引列保护

```sql
-- 假设 col 有前缀索引 INDEX idx(col(10))
ALTER TABLE t MODIFY COLUMN col VARCHAR(200);
-- MySQL: OK
-- OceanBase: 报错！
```

goInception 在审计时扫描表的所有索引，如果目标列参与了前缀索引就拦截。

---

## 三、pt-osc 的 OceanBase 适配

### 为什么需要 pt-osc

OceanBase 的 Offline DDL 会**锁全表**。对于生产大表，可能意味着几分钟甚至更久的业务中断。pt-osc 通过"建新表 → 逐行拷贝 → 交换"的方式避免长时间锁表。

### 适配要点

OceanBase 不是传统主从架构，很多 MySQL 概念不存在：

| MySQL pt-osc 参数 | OceanBase 适配 | 原因 |
|---|---|---|
| `--critical-load Threads_running` | 移除 | OB 无此 status 变量 |
| `--max-lag` | 移除 | OB 无主从复制延迟 |
| `--recurse=1` | 移除 | OB 无从库可检查 |
| `--check-plan` | 加 `--nocheck-plan` | OB 执行计划不兼容 |
| `--analyze-before-swap` | 加 `--noanalyze-before-swap` | OB 不需要 |
| （新增）`--table_mode` | 拷贝时设为 "queuing"，完成后改回 "normal" | 利用 LSM-Tree 优化批量写入 |
| （新增）`--degree` | 控制并行度 | OB 分布式统计收集 |

### TABLE_MODE 优化原理

pt-osc 拷贝数据时，把影子表设为 "queuing" 模式（优化批量写入的 Compaction 策略），拷贝完交换前再改回 "normal"（优化正常读写）。这是利用 OceanBase LSM-Tree 特性的针对性优化。

### 表大小查询适配

MySQL 直接查 `information_schema.tables`，但 OceanBase 的这个视图不准确。改为查 OB 内部视图：

```sql
SELECT /*+ READ_CONSISTENCY(WEAK) */ SUM(data_size)
FROM oceanbase.DBA_OB_TABLE_LOCATIONS l
JOIN oceanbase.DBA_OB_TABLET_REPLICAS r
  ON l.tablet_id = r.tablet_id
WHERE l.table_name = ? AND l.database_name = ?
```

用 `READ_CONSISTENCY(WEAK)` hint 避免强一致性读的开销。

---

## 四、gh-ost 的 OceanBase 适配情况

**当前未做任何 OceanBase 适配。**

gh-ost 代码中所有参数都按 MySQL 主从架构硬编码：

| 参数 | 问题 |
|------|------|
| `--max-lag-millis` | OB 无主从延迟概念 |
| `--assume-master-host` | OB 无主从概念 |
| `--critical-load Threads_running` | OB 无此变量 |
| `--replication-lag-query` | OB 无复制延迟查询 |

gh-ost 理论上可以适配 OceanBase（因为它用 binlog 解析而非 trigger，OB 的 Binlog Service 兼容 MySQL binlog 协议），但实际适配工作尚未完成。如需使用 gh-ost，需要补一套类似 pt-osc 的参数适配逻辑。

---

## 五、OceanBase 特有语法支持

### 序列（SEQUENCE）

OceanBase 支持 Oracle 风格的序列，goInception 支持两种语法：

```sql
INSERT INTO t VALUES (seq.NEXTVAL, 'data');      -- 点号风格
INSERT INTO t VALUES (NEXTVAL('seq'), 'data');    -- 函数调用风格
```

审计时会查询 `DBA_SEQUENCES` 确认引用的序列是否存在。

### 版本检测

OceanBase 版本格式：`OceanBase_CE-v4.2.5.0` → 解析为整数 `42500`

关键版本阈值：
- `< 42500`（4.2.5 之前）：ADD COLUMN 带 FIRST/AFTER 是 offline DDL
- `< 43100`（4.3.1 之前）：不支持全文索引

---

## Six: native single-DDL local implementation

`--single-ddl=1` 是默认关闭的新请求级 opt-in；仅 OceanBase，必须显式指定
`--wait-timeout=1..7200`、恰好 CHECK 或 EXECUTE、backup=false/trans=0。
支持单条表 DDL 与可选一个前置 USE；AST 检查所有目标/引用/改名表及列引用
schema 必须与当前库精确匹配（区分大小写，未知 lower_case_table_names 时保守拒绝 case drift），
拒绝系统库及显式 sys 租户身份（user@sys[#cluster] / cluster:sys:user），
后者在连接初始化前拦截。拒绝 CREATE SELECT、多表 DROP/RENAME、临时表、parser warning、尾随语句。
EXCHANGE PARTITION 涉及第二张 staging 表，即使同库也硬拒绝，不依赖可忽略的 partition warning
及与 DML/COUNT/PRINT/masking/split/middleware 混用。既有 CHECK 规范仍是审核闸门。
`record.obOfflineLargeAlter` 为 true（大表 + 离线 DDL）时，新路径在 CHECK/EXECUTE
都报 `single_ddl_offline_ddl_not_supported`，不隐式改走原生或 OSC；小表离线 DDL
仍由 `--wait-timeout` 预算约束。

### 能力与结果契约

仅 `--single-ddl=1;--enable-check;--enable-single-ddl-capability;--wait-timeout=T`
加一个有效业务库 USE（无 DDL）可返回 marker `ob_single_ddl_native_v1`。
普通 13 列不变：order_id=1、CHECKED、error_level=2、Audit Completed、
error_message 为精确 marker、sql 为原始 USE、affected_rows=0、
sequence=`0_0_00000000`、backup_dbname/sqlsha1=NULL、execute_time/backup_time='0'、
needMerge=0。错误数据库/审核错误/非法形态/EXECUTE 不提供能力证明。

执行先获取专用 `sql.Conn`，在同一物理连接上 USE、autocommit=1、
wait_timeout=T、ob_query_timeout=ob_trx_timeout=T*1000000（int64，7200 秒为
7200000000 微秒），回读三个 timeout、autocommit 与 connection_id，严格比对。
一次 T context 覆盖获取连接、setup/readback、DDL；父 context 可以缩短预算。
SET/状态不符/下发前取消均零 DDL，返回 CHECKED + error。
DDL 唯一一次 `Conn.ExecContext`，不经过 `s.exec`/initConnection/OSC/backup 重试分支；
提交后的失败为 EXECUTED/Execute failed，调用方应按 unknown 核对，禁止重放。
成功 USE 与 DDL 均 EXECUTED/Execute Successfully，并保留线程号/sequence。
旧 13 列错误文案仍不提供结构化 driver code/SQLSTATE，不据裸错误号推断未应用。

所有退出与 session.clear 都通过 `Conn.Raw` 返回 `driver.ErrBadConn` 丢弃物理连接，
而不只是归还 sql.Conn 至池，防止 timeout/autocommit 等状态污染后续审核/旧路径。
客户端断连/timeout 不等于已证明服务端 DDL 停止。**native 不是 Online 保证**。

### 本地验证边界（2026-11-08）

新测试先 red（缺符号），后 green；另有整数浮点强转、magic commit 后尾随 SQL、
parser warning、sys source、schema case drift、EXCHANGE PARTITION 均独立 red→green。无数据库 fake driver 验证同连接、7200/int64、
精确回读、setup 失败零下发、获取/setup/DDL context、1046/失联/取消无重试与物理销毁。
当前筛选 `TestSingleDDL|TestSingleDML|TestOBCount|TestBlobTextJson|TestOceanBase|TestQueryPrintSummary`
79 顶层 / 333 子例全部通过（其中新增 SingleDDL 15 顶层 / 141 子例），0 fail/skip，
同筛选 race 亦通过；parser/ast 回归、`go build ./...`、独立 binary build 与 `-V` 冒烟通过。
本机 Go 1.26.2 默认 session vet 通过；旧环境记录的 unreachable baseline 不在本轮重现。
普通 binary build 未注入发布 ldflags，`-V` 中 Release/Git/Build/Go 字段为 None，
不冒充发布身份。未跑旧 localhost 全 session suite、未启服务、未运行 DB SQL，
Homelab 真实 DDL/服务端终态由外部验收工具另行取证；此处不把纯测记作实库通过。
未 commit/push/release、未修改生产路由/配置/权限。

### 协调线程 Homelab 最终验收（同日17:02～17:04 +08）

仅业务dba/test，OB4.5.0、独立Go4005；`tools/single_ddl_homelab`真实协议14项通过，
400万行真实CREATE INDEX T60在15.276s成功，索引存在；T1约1.029s执行失败，保守Unknown。
T7200会话回读query/trx均7,200,000,000微秒，与一次DDL同物理连接；SET注入失败零DDL，
目标响应丢失仅一个DDL包、不重发。固定inception CREATE/DROP成功，但既有ALTER因
缺dba_ob_table_locations SELECT被拒，未GRANT；长索引用已有业务管理员单独验证，不冒充
固定审核账号权限验收。独立Orchestrator实际HTTP/NPoco人工与普通任务Completed，正式执行
frontend响应丢失持久化Unknown/余项Skipped、重复start及新进程不重放。自有对象/表/监听
清理，原业务表清单和授权不变。OSC在仅此原生测试实例关闭，生产共享策略未改；没有实等
300/7200秒或MySQL/backup/OSC兼容执行验收。详细记录及脱敏JSON在Orchestrator根
`docs/ob-native-ddl-homelab-20261108.md`、`docs/ob-native-ddl-evidence-20261108/`。

## 参考资料

- [OceanBase Online/Offline DDL (V4.3.3)](https://en.oceanbase.com/docs/common-oceanbase-database-10000000001784252)
- [OceanBase Online/Offline DDL (V4.2.1)](https://en.oceanbase.com/docs/common-oceanbase-database-10000000001106314)
- [How to Make DDL Execution Efficient in a Distributed Database](https://en.oceanbase.com/blog/4934724608)
- [OceanBase DDL vs MySQL DDL 分析](https://blog.csdn.net/OceanBaseGFBK/article/details/138126035)
- [OceanBase V4 ALTER TABLE DDL](https://blog.csdn.net/OceanBaseGFBK/article/details/139203749)
- [OceanBase Binlog Service](https://blog.csdn.net/OceanBaseGFBK/article/details/140102397)
