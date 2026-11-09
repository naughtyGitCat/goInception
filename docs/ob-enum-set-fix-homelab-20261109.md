# ENUM/SET 保守分类修复与 homelab 验收

日期：2026-11-09。**修复和隔离 Go 验收完成，未提交/推送/发布，未启用生产开关。** 本文是 [行数策略验收](ob-offline-rows-homelab-20261109.md) 后续，不将两次结果或 11-08 共享 Runner 历史证据混为最新端到端验收。

## 修复

旧实现将整个 COLUMN_TYPE 与新类型字符串转为小写，包含引号内的 ENUM/SET 成员；因此 `ENUM('A','B') → ENUM('a','b')` 被错误判为 Online，BIN/CS 排序规则下存在漏拦。

现实现复用 SQL parser 将旧元数据类型解析为 FieldType，逐项比较旧/新 Elems：类型必须相同、成员数量相同、顺序和值逐字一致。关键字大小写和等价引号写法不影响成员比较；不自行按逗号拆分，不借 CompactStr 的反斜杠表示比较值。解析失败按 Offline。成员大小写、数量、顺序或值变化一律保守按 Offline，包括 CI，不在审核端模拟排序规则语义。

这不是断言“所有 ENUM/SET 修改都会重建”：此前 [直接 OB 实测](ob-table-size-enum-homelab-20261109.md) 已证 CI 的部分 case-only 和尾部追加可不换 table_id。这里选择保守分类，不把这些结果扩大为通用 Online 能力。归 Offline 也不保证数据一定可转换；BIN/CS 已有不匹配数据仍可能由 OB 以 1265 拒绝。

离线分类继续复用大小/行数门槛：达到任一门槛则 native 拒绝；证明小表后才可继续。忽略告警或关闭大表提示不会绕过 native 的大表拒绝。

## 先红后绿与本地回归

- 新增 `session/oceanbase_enum_set_test.go`。修复前用 `go test ./session -run '^TestOceanBaseEnumSet' -count=1` 真实失败，日志 `/tmp/ob-enum-set-red.log`：case-only 在 CI/BIN/CS、ENUM/SET、MODIFY/CHANGE 都被误放 Online；同值反斜杠表示还存在错误 Offline。修复后同命令通过。
- 分类覆盖大/小写原定义、部分/全部 case 变化、顺序/增删/替换、逗号/括号/引号/反斜杠、反斜杠数量变化、Unicode、空成员和 ENUM↔SET；MODIFY/CHANGE 与三种 collation 交叉覆盖。
- fake 原生保护确认 case-only 会进入行数证明：小表取得豁免；达到门槛即使关闭大表提示仍零下发拒绝。
- 106 个独立 session 顶层测试，含子测试 **720 项通过、0失败、0跳过**。明确排除 12 个旧 gocheck/环境聚合入口（含 TestExpression），不运行 localhost 数据库套件。证据 `/tmp/ob-enum-set-standalone.jsonl`。
- focused session race、config 与两个 homelab 工具 race、工具 vet、`go build ./...`、Darwin CLI `--help`、两仓 `git diff --check` 均通过。未修改 C# 源码，未借历史 C#843项成绩冒充本次复跑。

## 当前真实 homelab 结果

UTC **09:35:08–09:35:45**（北京时间17:35），warn/error 隔离实例依次启动与停止；OB `10.136.38.71:2883`，`5.7.25-OceanBase_CE-v4.5.0.0`，dba 业务租户1002/test。OBProxy 的 @@hostname 为空，额外核验 observer 地址 `10.136.38.71,10.75.169.71`。只测试随机自有新表，无 sys、GRANT、密码修改、冻结、合并或统计刷新。

工具新增 `tools/ob_offline_policy_homelab/enum_set.go`；配置仅隔离实例允许 ENUM/SET，MB=1、rows=1000、OSC/Ghost关闭。

每轮 **79条记录全部通过，0阻塞、0失败**；其中42条为 ENUM/SET 夹具/CHECK/native 记录，另有其清理记录，并非79种独立DDL。

| 场景（ENUM/SET × CI/BIN/CS） | warn / error 结果 |
|---|---|
| 原定义完全不变，1000行表 CHECK | 无大表离线提示，不误归 Offline |
| 成员 A/B→a/b，1000行表 CHECK | 大表离线提示 level1 / level2 |
| 同变更，1000行 native | level2 `single_ddl_offline_ddl_not_supported`，未下发，schema及全部行值不变 |
| 同变更，空表 CHECK/native | CHECK无level2；实际执行成功，COLUMN_TYPE变为小写成员，全部空表数据保持不变 |

三种 collation 各有一张空表和一张1000行表，每张同时含 ENUM/SET；先验证 CHECK，再尝试 native。没有在已装入大写不匹配值的 BIN/CS 小表执行此变更，不宣称能消除 OB 数据转换错误。其余原行数边界与小表跨家族转换一并回归通过。

## 产品固定身份：纠正 runner 与当前边界

子 agent 只读调查见 [固定身份调查](inception-ddl-identity-20261109.md)：旧 runner 注入值与 C# 产品常量不相等；产品实际凭据认证正常，自身授权是 USAGE *.* 与 ALL test.*，直接读取 locations/replicas 视图分别报1142，业务表限量读取成功。

本轮修正**临时测试 runner 的注入来源**，从可信产品源码私下读取，通过 stdin/进程内存传递，不打印或写入凭据原文；没有修改产品常量或数据库密码。

本轮两个实例均以实际产品固定身份对自有1000行表做 Online ADD COLUMN 的 **CHECK-only，USE/ALTER均level0通过**。完整日志各202行，各有一条大小采集1142 warning，说明当前大小未知处理不再错误拒绝 Online。该列没有实际添加；这不证明固定身份 Offline+MB策略或真实 ALTER 执行已通过。除该固定身份 CHECK，成功 native 夹具仍使用业务租户管理身份，不能替代产品权限验收。

## 证据、清理与剩余门槛

- `/tmp/ob-row-policy-7d133055ee/{warn-results.json,error-results.json,summary.json,warn.log,error.log}`；日志完整扫描且时间与 JSON 对应。
- warn 表前缀 `ddl_policy_1109_dc1ebdd3b6`，error 前缀 `ddl_policy_1109_1261f005e5`；每轮11张表。清理后独立连接核验两个前缀残留均0；guest运行目录已删除，4005/4007结束后无监听。
- Linux frontend SHA256：`9612128478222ab5da4cab6bb16048592dc3e2853eb3f51fba2efdc2c19b4436`。
- Linux probe SHA256：`7f00bfecb93ed31494e2fe54b34cc1a4c345f39c372f9a2135aacb54c42d4626`。
- 上传采用压缩后1MiB独立分片、对端合并并校验大小/SHA256；原始证据只保留本机与homelab，不向外发布内部日志。

ENUM/SET 分类漏洞已收掉，固定身份的认证与视图权限边界已查明。本轮09:35测试没有补授权；后续17:53 +08按用户明确要求，已在homelab dba租户仅补两个视图的对象级SELECT，并由产品固定身份分别SELECT及JOIN验证通过，详见 [身份记录最新状态](inception-ddl-identity-20261109.md)。此前缺权不是生产缺权证据，用户补充线上已有权限；本次未访问生产。

后续18:19 +08，最新Go+当前C#、补权后固定产品身份的普通/人工HTTP→共享Runner→OB执行→NPoco落库已通过：两小表Offline执行成功、两大表明确拒绝、Unknown停止余项和重启零重放。209主驱动+39恢复断言通过，C#843非环境回归本轮重跑绿。详见Orchestrator根 `docs/ob-native-ddl-homelab-20261109.md`、`/tmp/ob-native-ddl-shared-1109-2447aa/`。不再将这两项列为未验。

仍未提交/发布/启用生产，共享MySQL/备份/OSC真实执行兼容和完整生产Startup等边界不由本轮代替。`EnableGoInceptionNative` 是全局 admission 开关而不是租户白名单，旧任务继续冻结后端。不自动切流。
