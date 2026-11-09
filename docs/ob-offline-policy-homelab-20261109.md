# OceanBase 离线 DDL 策略：homelab 验证

日期：2026-11-09。范围：原生 single-DDL、在线/离线分类、大表策略与既有规则分级。未发布到生产，未变更账号授权。

> 本文为较早的仅MB门槛历史记录，保留当时的冻结及失败事实；不是当前工具的执行说明。后续工具已移除自动租户冻结，改用大小/行数联合策略，最新验收见 [行数门槛验收](ob-offline-rows-homelab-20261109.md)。

## 环境与方法

- 前端：dba-app 独立 4005 / 4007 实例，分别配置 `check_offline_ddl_large_table_as_error=false/true`。
- 后端：OceanBase CE 4.5.0.0，dba 业务租户（tenant_id=1002），test 库；不连接 sys 租户。
- 专用门槛：1 MB；pt-osc / gh-ost 均关闭。
- 驱动：`tools/ob_offline_policy_homelab/main.go`，随机新建自有大小表，结束删除。
- 大表写入 500000 行，业务租户内 minor freeze 后，通过与审核端相同的视图查询确认达到门槛。冻结/合并异步，未达到门槛的运行不构成大表策略验收。

## 最终两轮结果

每轮 13 个记录：12 个非阻塞场景通过，1 个账号权限场景 environmentBlocked。不是 13 个可用能力均已通过。

| 场景 | warn 实例 | error 实例 |
|---|---|---|
| 大表夹具达到 1 MB 门槛 | 通过 | 通过 |
| 普通 CHECK：大表 ADD PRIMARY KEY | level 1 | level 2 |
| 普通 CHECK：小表 ADD PRIMARY KEY | level 0 | level 0 |
| 普通 CHECK：大表尾部 ADD COLUMN | level 0 | level 0 |
| 原生执行：大表 ADD PRIMARY KEY | 明确拒绝，主键未新增 | 同左 |
| 原生执行：大表尾部 ADD COLUMN | EXECUTED，列真实新增 | 同左 |
| 原生执行：小表 ADD PRIMARY KEY | EXECUTED，主键真实新增 | 同左 |
| 普通 CHECK：小表 DROP COLUMN | level 1 | level 1 |
| 普通 CHECK：大表 DROP COLUMN | level 2 | level 2 |
| 普通 CHECK：小表 VARCHAR 收窄 | level 1 | level 1 |
| 普通 CHECK：大表 VARCHAR 收窄 | level 2 | level 2 |
| 原生执行：小表 DROP COLUMN | EXECUTED，列真实删除 | 同左 |
| 固定 inception 账号审核已有表 | environmentBlocked | environmentBlocked |

权限阻塞为 `SELECT command denied ... dba_ob_table_locations`。成功执行场景使用业务租户管理身份，不证明固定 inception 账号具有同等能力；未用 GRANT 掩盖差异。

error 实例可能先被大表总策略短路，因此错误包含离线大表文案而不一定包含后续类型转换文案。

测试过程中曾有一轮冻结后仍未达到 1 MB，导致所谓“大表”实际按小表执行。该轮失败不计为通过；最终重新运行时确认夹具达到门槛。此现象也说明当前策略依赖视图中的落盘大小，并不覆盖所有内存中尚未落盘的数据。

收尾核验：本轮前缀表残留数为 0，4005 / 4007 无监听，临时前端目录已删除。

## 跨家族类型转换的直接执行核验

此前“跨家族类型转换在 OceanBase 上始终报错”的描述不正确。2026-11-09 15:30:35 在上述业务端点新建随机表，写入一行可转换数据，直接执行以下变更均成功：

| 转换 | 输入数据 | 实际结果 |
|---|---|---|
| TEXT → BLOB | `hello` | blob；HEX=68656C6C6F |
| BLOB → TEXT | `world` | text；HEX=776F726C64 |
| INT → VARCHAR(20) | 123 | varchar(20)；值 123 |
| VARCHAR(20) → INT | `'456'` | int；值 456 |

通过 `SHOW CREATE TABLE` 与变更后 SELECT 核验；测试表已删除。这仅证明该版本、这些定义和可转换数据下支持执行，不证明任意数据/约束/版本都可转换，也不是大表锁影响测试。

必须区分：OceanBase 语法/数据转换支持、在线/离线执行分类、goInception 审核级别是三个不同维度。`ER_CHANGE_COLUMN_TYPE` 默认是 level 1；不能把审核端策略误写成 OceanBase 不支持该 ALTER。

## 提交前本地验证

- `go build ./...`：通过。
- 93 个独立 session 测试（排除 gocheck 聚合入口）：441 个含子测试记录通过。
- `go test -race ./session -run 'Test(OceanBase|OfflineDDL|SingleDDL)' -count=1`：通过。
- `go test ./tools/single_ddl_homelab -count=1`：通过。
- 本地二进制 `--help`：退出码 0。
- 旧完整 session 套件此前出现依赖 localhost MySQL 的 setup 失败及 panic。本轮不重跑这些入口，不将其算作通过；不修改本地数据库环境来掩盖失败。
