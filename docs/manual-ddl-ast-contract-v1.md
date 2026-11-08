# Manual DDL AST contract v1

This opt-in PRINT contract is a structural precheck, not permission to execute SQL. It uses the existing goInception parser; CHECK must remain a separate request with the exact same input, wrapper and trusted target, with execute and backup disabled.

## Opt-in and compatibility

`--enable-query-print-summary;` (normalized `queryPrintSummary`) enables PRINT and selects the stable AST summary. `--query-print-summary=1;` is equivalent. The format version is carried by `contractVersion: 1`, not the option name. Conflicting execute and backup flags are forced false before target initialization. Invalid explicit summary booleans fail closed. Existing `--enable-query-print` / `--query-print=1` still return the legacy raw AST JSON and no terminal row.

The pre-deployment option name `--enable-query-print-v1` in tag `v1.3.8-manual-ddl-ast.1` was replaced, not retained as an alias. Use Go `v1.3.8-manual-ddl-ast.2` with Orchestrator `0.1.9` for the summary name; Orchestrator `0.1.8` still sends the superseded option and must not be paired with this producer. Publish the matching Go producer first, verify summary output, then publish the matching consumer. Legacy raw PRINT and ordinary CHECK remain compatible.

The five existing PRINT columns remain **id, statement, errlevel, query_tree, errmsg**. IDs are contiguous and start at 1. `query_tree` is a JSON **string column**, encoded by standard Go `encoding/json`; it is not a persisted object blob. Successful `errmsg` remains SQL NULL for legacy shape compatibility. Source/parse error rows may have an empty `query_tree`; their real error must be preserved, never approved.

## Required JSON fields

Every successful v1 statement summary has exactly these camelCase fields, with no omissions:

| Field | Type | Meaning |
|---|---|---|
| contractVersion | integer | Always 1 |
| statementType | string | Stable AST root type: createTable, alterTable, dropTable, truncateTable, use, set, dropStats, unknown; terminal is batchEnd |
| sqlSha256 | string | Lowercase SHA-256 of the exact UTF-8 `statement` / currentSql, without reformatting or extra normalization |
| complete | boolean | All safety-relevant AST features fall within the conservative supported structural subset |
| database | string | Exact UseStmt DBName for use; otherwise empty |
| tables | array | Each entry has exactly role, schema, name, schemaExplicit |
| actions | string array | Ordered stable action names; never internal numeric enums |

Table entries contain `role` (target, reference, renameTarget), original `schema` string or empty, original `name`, and boolean `schemaExplicit` (`schema != ''`). No session default database is substituted. Unknown roots are always incomplete even if their table shape resembles supported DDL: **DROP STATS is dropStats, not truncateTable**. References found beneath subqueries are references; aliases/CTEs are never authorized targets. Unsupported roots may include reference entries without granting scope.

The supported action vocabulary is:

- createDefinition, dropTable, truncateTable
- addColumn, addConstraint, dropColumn, dropIndex, dropPrimaryKey, modifyColumn, changeColumn, alterColumn, algorithm, lock

Rename emits meaningful `renameTable` but is incomplete. Other unsupported actions emit `unknown` and are incomplete. Multiple ALTER specs retain their original action order, including duplicates.

## Conservative producer subset

Normal explicit CREATE columns and ordinary primary/unique/key/index definitions are supported. Column charset/collation and literal defaults/comments, including signed numeric literals, remain supported. In particular, `MODIFY COLUMN ... MEDIUMTEXT CHARACTER SET utf8mb4 COLLATE ...` is complete. Table options are narrowly supported for InnoDB, charset, collation, comment and autoIncrement; all other engines/options are incomplete, including routing/storage/connection/tablegroup/union/partition/placement features. Normal indexes allow default/BTREE/HASH without parser, partition-index, storage, visibility or clustered-primary-key extensions.

Supported ALTER actions are the narrow vocabulary above with ordinary column/index definitions and literal SET DEFAULT or DROP DEFAULT. This parser represents ALTER SET DEFAULT using ColumnOptionNoOption; only that action-specific context accepts the otherwise unknown option.

CREATE LIKE/AS SELECT/TEMPORARY, views, FK (including column-level REFERENCES), generated/check/function expressions, subqueries, partitions, rename/exchange/import/discard, unknown nodes/actions/options and unrepresented safety-relevant extensions make `complete=false`. The visitor additionally traverses **ColumnOption.Refer** and **TableOption.TableNames** because this parser's Accept methods omit those fields. Unknown nodes are not presumed safe. SQL syntax rejected by the parser produces an error rather than a fabricated complete summary.

Parser warnings are captured for the entire parse chunk: every v1 row in that chunk becomes incomplete. This covers ignored syntax such as WITH PARSER and avoids interpreting warnings through SQL keyword matching. Legacy behavior is unchanged. DROP TABLE RESTRICT/CASCADE is parsed but discarded without warnings by this parser; these modifiers do not add table objects or expand authorized targets and the structural DROP summary remains supported. CHECK still determines target-server semantics. Future parser changes must revalidate these boundaries.

`complete=true` means structural coverage, **not** safety approval: explicit-schema DDL and multi-target DROP may be completely represented but must still be rejected by the consumer's single-unqualified-target scope.

## Terminal row

In v1 only, successfully reaching the genuine final magic commit appends one last successful PRINT row:

- statement: `inception_magic_commit`
- contractVersion: 1
- statementType: batchEnd
- sqlSha256: `8f56319cc97e33b897875c55d82afb6427cf3ed0d95679e578ebed1f49b3bb26`
- complete: true
- database: empty
- tables: []
- actions: []

Source/parse failure, missing commit, duplicate start, cancellation or trailing content after commit does not synthesize a successful terminal. A warned commit chunk also fails closed with an error rather than a successful terminal. Unsupported but successfully parsed body statements may be followed by batchEnd: consumers must reject their incomplete summaries, not mistake completion for approval. Legacy PRINT has no batchEnd.

## Consumer validation requirements

Permit only complete=true and the four table DDL roots, exactly one table object with role=target, empty schema and schemaExplicit=false, a safe trusted identifier, and no other table objects. Require exact actions `[createDefinition]`, `[dropTable]`, `[truncateTable]` for their corresponding roots. ALTER actions must be nonempty and all from the supported ALTER list.

Remove only the generated first USE, verifying its exact expected SQL, use/database contract and error level 0. Preserve user USE/SET and reject them semantically. Require exactly one final batchEnd, contiguous IDs, no extra rows/result sets, strict JSON fields/types and no duplicate/missing/unknown fields. Bind hashes to exact returned statement text. Match PRINT and CHECK rows by exact canonical SQL order/count/IDs, allowing only Trim/terminal-semicolon normalization, from the same original input and target. Consume the entire response before exposing a successful list. Old servers lacking v1 or batchEnd fail closed with an actionable error. Source startup rejection retains the real CHECK error (for example 1045) but never approves it.

Raw wrapper protection (comments, magic directives and escaping) stays before submission; do not infer SQL scope from keywords or JSON shape. Both initial precheck and execution-wide fresh locked recheck use this contract. Schema snapshots and post-mutation refresh use the freshly validated table. Preserve identity, target, capacity, fingerprint, requestId/start CAS, instance locks, durable InFlight, failure/unknown-stop and no automatic replay. API DTOs remain separate from persistent records.

## Executable fixtures and local verification

`docs/manual-ddl-ast-contract-v1-fixtures.json` is a shared, credential-free fixture array with `statement` and expected `queryTree`. It is not a sequence of executable DDLs and is verified directly against the actual Go parser by `TestQueryPrintSummarySharedFixtures`. A consumer can encode each queryTree into its PRINT string column to build positive/negative response fixtures; rows/IDs/errors/terminal placement must still be checked separately.

From the repository root:

```text
go test ./session -run '^TestQueryPrintSummary' -count=1
go test -race ./session -run '^(TestQueryPrintSummary|TestSingleDML|TestOBCount|TestOceanBase|TestJoinOceanBase|TestParseOceanBase|TestPlanSnippet|TestLobType|TestBlobText|Test_checkDDL)' -count=1
go test ./ast ./parser -count=1
```

Tests cover charset/collation MEDIUMTEXT, explicit CREATE definitions, supported ALTER order, exact UTF-8 hashes, DROP STATS distinction, qualified schemas, FK and extra references, unknown/hidden options, expression/subquery defaults, legacy PRINT, parser warnings, option conflicts, and successful/missing/interrupted/error/trailing batch termination. Existing DB-dependent suites must not be repointed to localhost or a production target to claim validation. The full `go test -short ./session` is not green in this environment: existing `session/core_test.go` testInceptionSuite ignores short, retains a hardcoded external DB dependency and panics in the unchanged audit error defer when initialization fails. `testCommon` suites honor short and skip; use the explicit offline selection above without starting or reconfiguring a DB. Before release, a no-network empty-host probe reproduced the same audit-defer nil panic for Audit/RunExecute with the four suite entry paths on both baseline `7b1fd580` and the current implementation. This confirms the initialization-error panic predates v1, but is not a full-suite same-environment baseline rerun. Evidence: `/tmp/manual-ddl-ast-integration-20261108/go-baseline-rejection.log` and `go-current-rejection.log`.
