# OceanBase 空/小表大小与 ENUM 排序规则实测

## 范围

- 日期：2026-11-09，15:51–16:03（数据库显示时间）。
- 端点：homelab OceanBase `10.136.38.71:2883`，CE 4.5.0.0；dba 业务租户，tenant_id=1002；test 库。
- 连接后实际核验 VERSION、CURRENT_USER、DATABASE、tenant_id。
- 仅新建/操作/删除随机前缀测试表；未连接 sys，未修改授权、租户参数或生产配置。
- **未执行 MINOR FREEZE 或其他租户级维护**。非严格模式仅设在探针自己的数据库会话上。
- 本轮测试直连数据库，未启动 goInception 服务，也未修改分类器实现。

## 一、空表、小表是否自然触发 NULL/扫描失败

两批独立随机新表，每批：3 张空表、3 张 1 行表、3 张 1000 行表、1 张 20000 行表。每表从装载完成后按 0 / 1 / 3 / 10 / 30 / 60 秒计划采样，查询耗时会使实际时点略晚。

复用当前审核端的大小查询：

```sql
SELECT /*+ READ_CONSISTENCY(WEAK) */
       ROUND(SUM(t2.data_size/1024/1024), 2)
FROM oceanbase.DBA_OB_TABLE_LOCATIONS t1,
     oceanbase.DBA_OB_TABLET_REPLICAS t2
WHERE t1.svr_ip=t2.svr_ip
  AND t1.ls_id=t2.ls_id
  AND t1.tablet_id=t2.tablet_id
  AND t1.database_name='test'
  AND t1.table_name=?;
```

分别扫描为 sql.NullFloat64 和当前实现使用的 float64，并查询匹配行数。第二批额外核验实际 COUNT(*)、SUM(OCTET_LENGTH(pad)) 和视图原始 SUM(data_size)，排除“只是 MB 四舍五入”的混淆。

| 每表实际行数 | 第二批核验的每表 payload 字节数 | 两批样本数 | 结果 |
|---:|---:|---:|---|
| 0 | 0 | 36 | 数值 0，无扫描错误 |
| 1 | 256 | 36 | 数值 0，无扫描错误 |
| 1000 | 256000 | 36 | 数值 0，无扫描错误 |
| 20000 | 5120000 | 12 | 数值 0，无扫描错误 |

- 共 120 个存在表样本，**没有 NULL/Scan 失败**；各样本视图关联匹配数为 2。
- 第二批 60 个样本的原始 data_size 总和也全部为 0，不是整数 MB 截断导致。
- 每批各有 1 个明确不存在的随机表名作对照：关联匹配数为 0，SUM 返回 NULL，float64 扫描报 `converting NULL to float64 is unsupported`。
- 因而不能声称“正常空表/小表会稳定触发未知大小异常”。本轮稳定观察到的是：新建未手动冻结的表，即使已确认有 5120000 字节 payload，该视图也可在 60 秒观察窗内持续返回合法 0。
- 5120000 字节是逻辑 payload，不是应占用的落盘空间；受 memtable、压缩及统计口径影响，二者不能等同。若策略仅按落盘 MB 定义大表，它是该口径的正常零值；若要用它保障总数据重建成本，就存在覆盖盲区。
- 不存在表的 NULL 对照只证明扫描失败分支真实存在，不证明正常表上的自然发生频率。网络中断/结果流错误本轮未注入。

## 二、ENUM 与 CI/CS

三种排序规则：`utf8mb4_general_ci`、`utf8mb4_bin`、`utf8mb4_0900_as_cs`。默认会话 SQL mode 实际为：

```text
STRICT_ALL_TABLES,NO_ZERO_IN_DATE,NO_AUTO_CREATE_USER
```

基础矩阵 9 种操作 × 3 种排序规则 = 27 组，独立新表重复两轮；成功/错误码、表 ID 是否变化和行值/枚举序号两轮一致。额外 9 组覆盖空表与非严格模式，共执行 63 次 ALTER，36 种组合。

每例记录 ALTER 错误、SHOW WARNINGS、SHOW CREATE TABLE、变更前后 TABLE_ID、e / HEX(e) / e+0 / e='a' / e='A'。**TABLE_ID 变化用作换表重建证据；没有并发 DML 锁等待测量，不能据此声称完全无锁。**

### 有数据、严格模式

| 操作 | CI | BIN / CS |
|---|---|---|
| 原定义不变 | 成功，表 ID 不变 | 同左 |
| `('A','B') → ('a','b')` | 成功，表 ID 不变；返回值变成小写，序号不变 | 错误 1265，数据/原定义保留 |
| 末尾追加 C | 成功，表 ID 不变 | 同左 |
| 重排为 `('B','A')` | 成功，表 ID 改变；文本值不变，序号互换 | 同左 |
| 已用 A/B 替换成 C/D | 错误 1265 | 同左 |
| 删除未使用 C | 成功，表 ID 改变 | 同左 |
| 删除已使用 B | 错误 1265 | 同左 |
| 在 A 与 B 之间插入小写 a | 错误 1291（重复成员） | 成功，表 ID 改变；B 的序号从 2 变成 3 |
| SET 成员仅改大小写 | 成功，表 ID 不变；文本变小写，位掩码不变 | 错误 1265 |

### 大小写修改的额外对照

| 场景 | CI | BIN / CS |
|---|---|---|
| 空表，严格模式 | 成功，表 ID 不变 | 成功，**表 ID 改变** |
| 有数据，sql_mode=''，ENUM | 成功，表 ID 不变；值变小写 | 成功，**表 ID 改变，原值变成空串、枚举序号 0** |
| 有数据，sql_mode=''，SET | 成功，表 ID 不变；值变小写 | 成功，**表 ID 改变，原值/组合变成空串、位掩码 0** |

非严格 BIN/CS 两组完成后 SHOW WARNINGS 未返回记录；不能依赖警告结果保证未丢失枚举语义。

## 结论对当前分类器的影响

1. 用户提出的 CI/CS 区别被实测证实。不能把所有大小写变更一概认定为重建；CI 下本轮走的是表 ID 不变的路径。
2. 当前分类器对整个类型字符串 ToLower，且不根据旧列实际 collation 判定，会把 BIN/CS 的大小写变更也当作相同。空表与非严格模式下的成功换表重建说明：它不仅是“最后被数据库报错拦住”的问题。
3. 不能用 EqualFold 或任意后缀匹配代替 OceanBase 排序规则比较。若后续支持 CI 下的特定元数据变更，需同时约束成员顺序、排序规则不变等条件；或者先采用精确成员匹配的保守策略。这里只记录依据，未修改实现。
4. 末尾追加成员与重排/删除成员的实际路径不同；本轮没有验证跨 255 成员等存储宽度边界，不外推至所有 ENUM 扩容。

## 证据及收尾

本地原始 JSON：

- `/tmp/obpolicy/evidence/enum-size-results-20261109.json`：第一批 60 个存在表样本、NULL 对照、基础 27 组。
- `/tmp/obpolicy/evidence/enum-size-repeat-20261109.json`：第二批实际行数/字节数、60 个存在表样本、NULL 对照、基础矩阵独立重复。
- `/tmp/obpolicy/evidence/enum-extra-results-20261109.json`：额外 9 组。
- 临时 Go 探针源文件：`/tmp/obpolicy/enum-size-probe.go`。

三个随机前缀分别为 `ddl_probe_1109_fb9e4a21e4`、`ddl_probe_1109_fe90d68323`、`ddl_probe_1109_442c4ccbf7`。共新建 83 张表；各轮 cleanupErrors 为空、remainingTables=0。结束后数据库再次查询残留数为 0。dba-app 内本轮 3 个探针二进制已删除；未启动后台前端实例。未提交/推送/部署。
