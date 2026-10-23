# naughtyGitCat fork 变更历史

本文档记录 `naughtyGitCat/goInception` 相对于上游 `hanchuanchuan/goInception` 的变更，包含：
- Fork 分支模型与 cherry-pick 来源
- naughtyGitCat 独有的原创 commit
- 从其他活跃 fork 选择性引入的功能

> 各来源 fork 自己的内部变更，分别由各 fork 自行维护。例如 zmix999 相对 hanchuanchuan 的改造详见 [`zmix999-changelog.md`](./zmix999-changelog.md) —— 那是 zmix999 团队的工作记录，不是本 fork 的工作。本文件仅记录 **naughtyGitCat 这一层主动做的事**。

---

## 2026-10-23 — v1.3.6-ob-lob-type.1（production `f950cc18` 上线）

用户完成构建并替换线上 goInception。`production` 现为 `1a17ede4`，其中代码提交 `f950cc18`，`83c6c8e9` 为测试补充、`1a17ede4` 为文档，两者不改变行为。

- 代码 `f950cc18`：`checkModifyColumn` 的列类型转换审核为 text/blob 家族补上方向判断——同家族扩容（tinytext < text < mediumtext < longtext，blob 同理）放行；缩容、跨家族（text ↔ blob，字符集 vs 二进制不等价）以及与非 LOB 类型互转仍报 `ER_CHANGE_COLUMN_TYPE`（errlevel=1）。动机是 `text -> mediumtext` 这类扩容被误判，导致下游把合法加长 DDL 判为失败（工单 469651190）。
- 上线前验证（Homelab，真实 MySQL 8.0.45，inception 块经 MySQL 协议整块发送）：`text -> mediumtext`、`blob -> longblob` 告警消失且 `--execute=1` 执行成功；`mediumtext -> text`、`text -> blob`、`varchar(100) -> text` 仍 errlevel=1。另在 `upstream/master` 基线上编译了改动前后的两个二进制做同款对照，结果一致。
- 线上尚未复验：失败工单 469651190 重新提交后的审核结果、以及缩容/跨家族方向在线上配置下的实际表现。
- 配置前提：`enable_blob_type` 必须为 true（默认 false），否则 text/blob 家族在更早的 `initDisableTypes()` 阶段就被 `ER_INVALID_DATA_TYPE` 拦下，走不到本次的类型转换审核。
- 版本：上线构建时尚未打 tag，二进制内 `git describe` 为 `v1.3.5-ob-single-dml.1-5-g1a17ede4`（随构建点变化）；随后在记录提交上补打注解 tag `v1.3.6-ob-lob-type.1` 作为该版本标记。同时向上游 `hanchuanchuan/goInception` 提了 PR #721（分支 `fix/text-blob-type-change-order`，基于 `upstream/master`），与线上这版是两条独立的线。

## 2026-10-16 — v1.3.5-ob-single-dml.1

用户批准将 `test/ob-session-timeout` 快进合入实际工作分支 `production`，推送目标为 `origin`（`naughtyGitCat/goInception`），不修改参考分支 `master`。

- 代码 `200751ad`：默认关闭的 `--single-dml` 安全模式，单 mutation、专用连接、会话超时、真实 COUNT 保护与无自动重发；COUNT 使用 WEAK/follower_first，mutation 使用 STRONG。
- 证据 `a28ca756`：Homelab 验收记录。用户确认 #30 结项；真实调用方暂停/DBA通知及 HTTP DDL 完整链留到线上测试，结项不代表这两项已通过。
- 本次发布前回归：`go test -vet=off ./session -run 'Test(SingleDML|OBCount)' -count=1`、`go test ./parser ./ast` 和 `go build` 通过。session 默认 vet 的历史 unreachable 问题未在本次修改，故该轮显式关闭 vet，不能宣称全包 vet 通过。
- 本次仅合并、版本标记和推送，不替换生产服务、不启用 Orchestrator 灰度；旧 MySQL、DDL 及非 single-dml 路径保持原契约。
- 版本沿用 Makefile 的 `git describe --tags` 注入机制，以 `v1.3.5-ob-single-dml.1` 标记此候选版本，历史 tag 保留。

## Linux amd64 编译与版本核验

在仓库根目录、实际工作分支 `production` 上编译。推荐使用已有 Makefile，自动注入版本、commit、分支、构建时间和 Go 版本：

```bash
GOOS=linux GOARCH=amd64 make server TARGET=goInception
```

Makefile 的 `GOBUILD` 已设置 `CGO_ENABLED=0`。产物为仓库根目录的 `goInception`，此命令只编译，不上传、不重启服务。

如需直接调用 `go build`，必须显式传入版本注入参数；裸 `go build` 不会自动填写 `-V` 展示的变量，输出会是 `None`：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "\
-X 'github.com/hanchuanchuan/goInception/mysql.TiDBReleaseVersion=$(git describe --tags --dirty)' \
-X 'github.com/hanchuanchuan/goInception/util/printer.TiDBGitHash=$(git rev-parse HEAD)' \
-X 'github.com/hanchuanchuan/goInception/util/printer.TiDBGitBranch=$(git branch --show-current)' \
-X 'github.com/hanchuanchuan/goInception/util/printer.TiDBBuildTS=$(date -u '+%Y-%m-%d %H:%M:%S')' \
-X 'github.com/hanchuanchuan/goInception/util/printer.GoVersion=$(go version)'" \
  -o goInception tidb-server/main.go
```

直接编译命令的构建时间使用 UTC；现有 Makefile 使用构建机本地时间，若需要 UTC 可在 Makefile 命令前加 `TZ=UTC`。

将 Linux 二进制复制到目标 Linux 机器后核验（macOS 不能直接运行此交叉编译产物）：

```bash
./bin/goInception -V
```

确认五项均非 `None`，commit 与候选源码一致、分支为 `production`。版本由 `git describe --tags --dirty` 生成：精确 tag 上是 `v1.3.5-ob-single-dml.1`；其后提交会带提交距离/hash，工作区有修改会带 `-dirty`，不得将 dirty 制品误记为干净 tag 制品。临时回归中的裸 `go build` 仅证明可编译，不是带发布元数据的制品。

## Fork 模型

本 fork 是 `hanchuanchuan/goInception` 的下游，**不是任何其他 fork 的下游**。`production` 分支基于 hanchuanchuan/master，按需从多个活跃 fork 挑选 commit 加上自己的 patch。

```
hanchuanchuan/goInception (remote: upstream)   ← 真正的上游，原作者，已基本停更
        │
        └──fork──► naughtyGitCat/goInception (remote: origin)
                     ├── master      与 upstream/master 同步参考
                     └── production  ← 实际工作分支
                            │
                            ├── 自己的原创 patch
                            ├── 从 zmix999/goInception 挑 (OB 适配、binlog 备份、parser 增强等)
                            ├── 从 oceanbase/goInception (Integration 分支) 挑 (OB Offline DDL、版本识别等)
                            └── 从其他 fork (xqiljkxw / young0 等) 按需挑
```

各 cherry-pick 来源在 git 中配置为独立 remote：

| Remote | 仓库 | 用途 |
|:--|:--|:--|
| `upstream` | `hanchuanchuan/goInception` | 真上游，跟踪 master |
| `origin` | `naughtyGitCat/goInception` | 本仓库 |
| `zmix999` | `zmix999/goInception` | cherry-pick 来源 |
| `oceanbase` | `oceanbase/goInception` | cherry-pick 来源 (Integration 分支) |
| `xqiljkxw` / `young0` 等 | 同名 fork | 按需 cherry-pick |

---

## naughtyGitCat 原创 commit

按时间倒序，作者 `naughtyGitCat <psyduck007@outlook.com>`：

| Commit | 日期 | 类型 | 说明 |
|:--|:--|:--|:--|
| `2f0a1fe5` | 2026-04-30 | docs | README Contact 段改为 fork 维护者邮箱 |
| `507a5e7f` | 2026-04-30 | docs | README 链接到 fork 自定义文档；删除 Financial Contributors |
| `fee67392` | 2026-04-30 | fix | OceanBase 允许 varchar/decimal 同类型扩长（之前一刀切禁止） |
| `dfcbff57` | 2026-04-11 | fix | result set 列定义的默认 charset 修复（OceanBase 字符集兼容） |
| `4fd89843` | 2026-04-11 | fix | skipSqlList 匹配时去掉末尾分号（避免漏匹配） |
| `7c311f48` | 2026-04-14 | fix | OceanBase online DDL 跳过 pt-osc（OB 原生支持，不需要外部工具） |

### 已合并到上游的 commit

以下 PR 已合并到 `zmix999/goInception:master`，未来 rebase production 时会被识别为重复并去重：

- `dfcbff57` + `4fd89843` → [zmix999/goInception#3](https://github.com/zmix999/goInception/pull/3) (merged)
- `7c311f48` → [zmix999/goInception#1](https://github.com/zmix999/goInception/pull/1) (merged)
- `fee67392` → [zmix999/goInception#4](https://github.com/zmix999/goInception/pull/4) (待审核)

---

## 历史 cherry-pick 内容

production 在 `upstream/master`（hanchuanchuan）的基础上叠加了多个 fork 的功能。从 git 历史看，相对 `upstream/master` 大约 ahead 1500 + commit（数字会随上游 / cherry-pick 增长而变）。

按来源分类：

### 从 zmix999/master cherry-pick

代表性内容（详见 [zmix999-changelog.md](./zmix999-changelog.md)）：

- OceanBase 适配：版本号解析、序列支持、DDL 冲突检测、pt-osc 适配
- 加密 binlog 备份：parser、session_backup
- parser 增强：column before、partition 检查、AS OF SNAPSHOT
- 测试 / 元数据 / 依赖升级

cherry-pick 进来的 commit patch-id 与 zmix999/master 上对应 commit 相同但 hash 不同。

### 从 oceanbase/Integration cherry-pick

OceanBase 团队在自己 fork 的 `Integration` 分支上做的 OB 增强，按主题挑选 + 主动跳过的方式同步。**当前累积在 `ob-integration-sync` 分支**（基于最新 production，含 production 全部内容 + OB Integration 9 个 commit）。需要 OB 列存 / Offline DDL 这套功能时，可以 fast-forward `production` 到 `ob-integration-sync` 把内容合进来。

已 cherry-pick 主题（截至 2026-05-28 第四轮）：

| 轮次 | 主题 |
|:--:|:--|
| Round 1 | Offline DDL 检查（`CheckOfflineDDL` 配置 / 7 个 OB DDL 错误码 / `session/oceanbase_check.go`） |
| Round 1 | OB 3.x / 4.x 大版本识别（`select version()` 解析 + 后续逻辑分流） |
| Round 1 | unsigned ↔ signed 整型转换支持 |
| Round 2 | `TABLE_MODE` 选项值校验 + `ALTER TABLE SET` 前缀 |
| Round 2 | OB 专属 table option：`REPLICA_NUM` / `BLOCK_SIZE` / `USE_BLOOM_FILTER` / `TABLET_SIZE` / `PCTFREE` |
| Round 2 | partition：`select from t partition(p)` 测试 |
| Round 3 | partition：SUBPARTITION BY RANGE/LIST + `VALUES IN (DEFAULT)`（partial — 跳过 TEMPLATE 变体） |
| Round 3 | partition：8 / 17 条非 TEMPLATE 测试用例 |
| **Round 4** | **OB 4.x storage options：`ORGANIZATION INDEX/HEAP` / `DELTA_FORMAT` / `ENABLE_MACRO_BLOCK_BLOOM_FILTER` / `MERGE_ENGINE` / `SKIP_INDEX_LEVEL` / `DYNAMIC_PARTITION_POLICY`** |

主动跳过：

- **SUBPARTITION TEMPLATE** —— 与 production 的 `parser.y` 中 `Expression` / `PredicateExpr` 优先级链产生 shift/reduce 冲突，goyacc 默认 `AllowConflicts: false` 直接拒绝。要 port 得整体同步 OB 的 expression 优先级声明，工作量超 cherry-pick 范畴。
- **WITH COLUMN GROUP 的部分测试** —— Round 4 中 5 条纯 column group 测试，因 HEAD 已有自己的 column group 实现（`ColumnGroupOption{Tp: ColumnGroupOptionType}` + `WithColumnGroupOpt`）与 OB 的（`ColumnGroupOption{Items: []ColumnGroupType}` + `ColumnGroupOpt`）AST/grammar 都冲突，且 HEAD Restore 输出 `WITH COLUMN GROUP (...)` 比 OB 期待多一个空格，未引入。功能上 HEAD 能 parse 该语法，仅 round-trip 格式不一致。

### 上游状态复查（2026-10-23）

以 `git ls-remote oceanbase` 为准（不看本地缓存）：`Integration` 仍是 `95c8f822`（2026-05-27），即 Round 4 的同步点，无新增提交；`oceanbase/master` 是纯上游 `cfb88db9`，不含 OB 内容。

唯一有新增的是旁支 `check-offline-ddl-err-message`：从共同祖先 `b8c391f4` 分出，落后 Integration 一个提交，另有 2 个自己未并入 Integration 的提交。

| 提交 | 日期 | 内容 |
|:--|:--|:--|
| `77602d78` | 2026-04-20 | 新增配置 `ob_online_ddl_skip_osc`（默认 false），OB 开启后统一跳过 pt-osc/gh-ost |
| `76080f70` | 2026-04-21 | 改为保守白名单分类器 `session/oceanbase_alter_classifier.go`：仅确认在线的 ALTER 才跳过 OSC，未识别语法保留 OSC；与 `check_offline_ddl` 解耦，另带约 320 行免数据库单测 |

**结论：暂不引入。** 这两个提交上游自己没并进 Integration（4 月提交、5 月的 Integration 更新未包含），开关默认关闭；且与本 fork 的 `7c311f48`（OB online DDL 跳过 pt-osc）语义重叠——我们的 `checkDDLOfflineOperation` 对未知语法是"跳过 OSC"，他们是"保留 OSC"，要采纳就得改线上 DDL 走向。可行性已验证：cherry-pick 到 production 时 `77602d78` 仅 `config/config.go` 一处插入点冲突，`76080f70` 干净合入，`go build` / `go vet` / `gofmt` 均通过。待上游把这两个提交并进 Integration 时再复核。

### 方法论沉淀

四轮 cherry-pick 过程中提炼的几条经验：

1. **多源 cherry-pick 模型下，"早期 cherry-pick 留下的同名实现"是最大的坑**。看到同名 struct / enum / grammar rule 时，先 grep 一下 HEAD 是不是已有不兼容版本（典型例子：Round 4 的 `ColumnGroupOption`）。
2. **`parser.y` / `parser.go` 关系**：`parser.go` 是 yacc 生成的状态机，由 `make parser` 自动生成。两份 fork 的 `parser.y` 即使只差几行，重新生成的 `parser.go` 状态编号会全部偏移 → 看起来几千行 diff，绝大部分是噪音。cherry-pick 时**只关心 `parser.y` 冲突**，`parser.go` 用 `git checkout --ours` 丢掉后重新 `make parser` 即可。
3. **`git cherry-pick` 在 AST 不兼容的语义重叠场景下必崩**，goyacc 会 panic 且 y.output 为空。这时改用**手工 Edit 加最小变更集**（不走 cherry-pick）比修 conflict 更省（典型例子：Round 4）。
4. **拆 commit 看价值**：上游 commit 标题往往涵盖多个改动，单独 cherry-pick 时只挑真正净增的部分。例如 95c8f822 标题是"column group + table options"，但 column group 部分 HEAD 已有，**真正的净价值是 6 个新 table options**。
5. **危险操作前打备份分支**：rebase / reset 前 `git branch ob-integration-sync-pre-rebase-<日期>`，确认一周无问题再删。

---

## 后续维护策略

### 加新 patch

1. 在 `production` 上提交（git config 已固定 `naughtyGitCat <psyduck007@outlook.com>`）
2. 如果想回馈某个上游 fork（zmix999 / oceanbase），cherry-pick 到 `fix/<topic>` 分支 PR 过去；合并后从 production 移除等价 commit 即可
3. 否则就留在 production，作为本 fork 的功能

### 持续从其他 fork 引入功能

```bash
# 例：从 zmix999 引入新功能
git fetch zmix999
git log --oneline production..zmix999/master    # 看 zmix999 有什么新东西
git cherry-pick <hash>                            # 挑想要的
```

### 不要做的事

❌ **不要 `git rebase upstream/master` 或 `git rebase zmix999/master` 整体换 base**。production 是 hanchuanchuan 下游 + 多源 cherry-pick 的形态，rebase 换 base 会丢失这个模型的语义（即使功能等价）。

✅ 想拉上游新东西就**单独 cherry-pick** 想要的 commit，保留 production 的混合 base 结构。

### 备份分支保留约定

任何会重写 production 历史的危险操作前（如 rebase / reset），先打备份分支：

```bash
git branch production-pre-<原因>-<日期>
```

确认无问题一周后删除。
