# naughtyGitCat fork 变更历史

本文档记录 `naughtyGitCat/goInception` 相对于上游的变更，包含：
- Fork 分支模型说明
- naughtyGitCat 独有的原创 commit
- 来自其他 fork 的 cherry-pick / rebase 内容

> 关于 `zmix999/goInception` 相对于 `hanchuanchuan/goInception` 的变更，请看 [`zmix999-changelog.md`](./zmix999-changelog.md)。本文件仅记录 naughtyGitCat 这一层的增量。

---

## Fork 链路

```
hanchuanchuan/goInception        ← 原作者，已基本停更
        │
        ├──fork──►
        │
zmix999/goInception              ← 接力维护，做大量 OceanBase / binlog 适配
        │
        ├──历史 cherry-pick / rebase──►
        │
naughtyGitCat/goInception        ← 本 fork
   ├── master      跟原始上游 hanchuanchuan/master 同步参考
   └── production  ← 实际工作分支，所有自定义改造在这里
```

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

## 历史 cherry-pick（来自 zmix999）

production 含有约 **1300 个**来自 zmix999 的 commit，是历史上某个时间点把 zmix999 的工作 cherry-pick / rebase 到 production 形成的。这些 commit 的 patch-id 与 zmix999/master 上对应 commit 相同，但 git hash 不同。

代表性内容（具体见 [zmix999-changelog.md](./zmix999-changelog.md)）：

- OceanBase 适配：版本号解析、序列支持、DDL 冲突检测、pt-osc 适配等
- 加密 binlog 备份：parser、session_backup 等
- parser 增强：column before、partition 检查、AS OF SNAPSHOT 等
- 测试 / 元数据 / 依赖升级

由于 hash 错位，这些 commit 在 `git log zmix999/master..production` 里会重复显示，不代表 naughtyGitCat 自己写的。

---

## 后续维护策略

### 加新 patch

1. 在 `production` 上提交（git config 已固定 `naughtyGitCat <psyduck007@outlook.com>`）
2. cherry-pick 到 `fix/<topic>` 分支，PR 到 `zmix999/goInception:master`
3. 等 merge

### rebase production 与 zmix999 对齐

PR 合并堆积一段时间后，做一次：

```bash
git fetch zmix999
git checkout production
git rebase zmix999/master
```

git 会自动识别 patch-id 重复的 commit 并 drop，理想情况下 production 变成 `zmix999/master + 少量未合并 patch`，clean 干净。

如果冲突太多，备选方案是新切一个 branch：

```bash
git checkout -b production-new zmix999/master
git cherry-pick <未合并的几个 patch>
git branch -m production production-old
git branch -m production-new production
git push -f origin production
```
