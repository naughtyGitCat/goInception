# goInception

[![travis-ci](https://img.shields.io/travis/hanchuanchuan/goInception.svg)](https://travis-ci.org/hanchuanchuan/goInception)
[![CircleCI Status](https://circleci.com/gh/hanchuanchuan/goInception.svg?style=shield)](https://circleci.com/gh/hanchuanchuan/goInception)
[![GitHub release](https://img.shields.io/github/release-pre/hanchuanchuan/goInception.svg?style=brightgreen)](https://github.com/hanchuanchuan/goInception/releases)
[![codecov](https://codecov.io/gh/hanchuanchuan/goInception/branch/master/graph/badge.svg)](https://codecov.io/gh/hanchuanchuan/goInception)
[![](https://img.shields.io/badge/go-1.22.1-brightgreen.svg)](https://golang.org/dl/)
[![TiDB](https://img.shields.io/badge/TiDB-v2.1.1-brightgreen.svg)](https://github.com/pingcap/tidb)
![](https://img.shields.io/github/downloads/hanchuanchuan/goInception/total.svg)
![](https://img.shields.io/github/license/hanchuanchuan/goInception.svg)


**[[English]](README.md)**
**[[Chinese]](README.zh.md)**


goInception is a MySQL maintenance tool, which can be used to review, implement, backup, and generate SQL statements for rollback. It parses SQL syntax and returns the result of the review based on custom rules.

**Documentation:**
**[[Document]](https://hanchuanchuan.github.io/goInception/)**
**[[中文文档]](https://hanchuanchuan.github.io/goInception/zh/)**

**[[Upstream Changelog]](https://hanchuanchuan.github.io/goInception/changelog.html)**

**Fork-specific changes:**
- [[naughtyGitCat fork 变更历史]](docs/naughtygitcat-changelog.md) — 本 fork 的原创 commit、PR 状态、维护流程
- [[zmix999 Fork 改造详解]](docs/zmix999-changelog.md) — 上游 zmix999 相对 hanchuanchuan 的改动详解（OceanBase 适配等）
- [[OceanBase DDL 设计文档]](docs/oceanbase-ddl.md) — pt-osc 适配、Online/Offline DDL 判定等


----

### Quick start


#### Binary

[goInception Download](https://github.com/hanchuanchuan/goInception/releases)


#### Docker Image
```
docker pull hanchuanchuan/goinception
```


#### Source code compilation

***go version 1.14+ (go mod)***

```bash
git clone https://github.com/hanchuanchuan/goInception.git
cd goInception
go build -o goInception tidb-server/main.go

./goInception -config=config/config.toml
```

----

#### Associated SQL audit platform

* [Archery](https://github.com/hhyo/Archery) `Query support (MySQL/MsSQL/Redis/PostgreSQL), MySQL optimization (SQLAdvisor|SOAR|SQLTuning), slow log management, table structure comparison, session management, Alibaba Cloud RDS management, etc.`


#### Acknowledgments
    GoInception reconstructs from the Inception which is a well-known MySQL auditing tool and uses TiDB SQL parser.

- [Inception](https://github.com/hanchuanchuan/inception)
- [TiDB](https://github.com/pingcap/tidb)

#### Sponsorship and support
- [Sponsorship and support](https://hanchuanchuan.github.io/goInception/support.html)

#### Contact

e-mail: `psyduck007@outlook.com`

### Contributing

Welcome and thank you very much for your contribution. For the process of submitting PR, please refer to [CONTRIBUTING.md](CONTRIBUTING.md)。


## Contributors

### Code Contributors

This project exists thanks to all the people who contribute. [[Contribute](CONTRIBUTING.md)].
<a href="https://github.com/hanchuanchuan/goInception/graphs/contributors"><img src="https://opencollective.com/goInception/contributors.svg?width=890&button=false" /></a>
