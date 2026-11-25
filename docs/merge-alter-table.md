# Merge same-table ALTER TABLE statements

Available since [`v1.3.12-merge-alter-table.1`](https://github.com/naughtyGitCat/goInception/releases/tag/v1.3.12-merge-alter-table.1). The command merges supported column and index actions without running ordinary Inception review.

`INCEPTION MERGE_ALTER_TABLE` is a standalone query on the existing goInception MySQL listener, not on the target MySQL instance. It accepts one single-quoted SQL string containing two or more `ALTER TABLE` statements. It runs before Inception initialization: it never opens a source or backup connection, never audits, and never executes the input. The caller still performs its normal audit before execution.

```sql
INCEPTION MERGE_ALTER_TABLE 'ALTER TABLE `Shop`.`Orders` ADD COLUMN `c1` INT AFTER `base`; ALTER TABLE `Shop`.`Orders` ADD COLUMN `c2` VARCHAR(20) DEFAULT ''a;b'' AFTER `c1`';
```

A successful query returns exactly one row with three String (`VarString`) columns:

| Column | Meaning |
| --- | --- |
| `schema` | Decoded schema identifier, or an empty string when every statement is unqualified. |
| `table` | Decoded target table identifier with its original spelling and case. |
| `alterActions` | Comma-separated, ordered ALTER actions rendered by the Go AST printer. It has no `ALTER TABLE` prefix, table name, or trailing SQL terminator. |

For the example above the row values are:

```text
schema = Shop
table = Orders
alterActions = ADD COLUMN `c1` INT AFTER `base`, ADD COLUMN `c2` VARCHAR(20) DEFAULT 'a;b' AFTER `c1`
```

The Python caller does not need to reassemble statement text. It renders the final statement with quoted identifiers:

```python
cursor.execute("INCEPTION MERGE_ALTER_TABLE %s", (batch_sql,))
schema, table, actions = cursor.fetchone()

def quote_ident(value):
    return "`" + value.replace("`", "``") + "`"

target = f"{quote_ident(schema)}.{quote_ident(table)}" if schema else quote_ident(table)
alter_sql = f"ALTER TABLE {target} {actions}"
```

The actions are the canonical AST rendering: keywords are uppercase, identifiers are back-quoted with their case preserved, and string literals are single-quoted with their content preserved (including Unicode and semicolons). Type and expression spelling follows the parser's canonical form, for example `CURRENT_TIMESTAMP` renders as `CURRENT_TIMESTAMP()`.

## Accepted batch

Every statement must:

- be a plain `ALTER TABLE` root; `ALTER IGNORE TABLE` is rejected because the parser does not retain `IGNORE`;
- name the same table with the same qualification. Mixed qualification, such as `Shop.Orders` followed by `Orders`, is rejected explicitly; the session's current database is never used to complete an omitted schema;
- contain only `ADD COLUMN`, `MODIFY COLUMN`, or named nonunique `ADD INDEX`/`ADD KEY` on named columns;
- not add key or other constraints through column definitions, add special indexes or table options, drop or rename objects, or set `ALGORITHM` or `LOCK`;
- not use executable comments, double-quoted literals, or backslash escapes in literals. The command uses the parser's default SQL mode, not the target or session mode.

Each column action defines one column; each column and index name can be changed or added only once, case-insensitively. A change to a column must come before any `AFTER` or index reference to it in the batch. An `AFTER` cannot refer to the same column. Plain types, literal defaults, `CURRENT_TIMESTAMP`, column comments, `FIRST`/`AFTER`, and identifier case are preserved through canonical rendering; explicit character sets on ENUM/SET are rejected. Named indexes can use plain column parts, including prefix lengths, optional order, comments and BTREE; no expressions or vendor-specific options. Index order is honored according to the target MySQL version. Actions are merged in input order. Type changes pass through normal review rules, not this command.

## Errors

Failures return no result row. They are MySQL protocol errors whose message starts with `MERGE_ALTER_TABLE:`. The complete current set is:

| Message | Cause |
| --- | --- |
| `MERGE_ALTER_TABLE: expected one quoted SQL argument` | The command is not followed by exactly one single-quoted string. |
| `MERGE_ALTER_TABLE: must be a standalone command` | Another outer statement, prefix, enclosing comment, or trailing request is present. |
| `MERGE_ALTER_TABLE: invalid SQL syntax` | The payload does not parse, contains a decoded NUL, or is not valid `ALTER TABLE` SQL. |
| `MERGE_ALTER_TABLE: executable comments are not supported` | The payload contains an active `/*! ... */` comment. |
| `MERGE_ALTER_TABLE: ALTER IGNORE TABLE is not supported` | The payload uses `ALTER IGNORE TABLE`. |
| `MERGE_ALTER_TABLE: input produced a parser warning` | Parsing succeeded with a warning, so the parse is not trusted. |
| `MERGE_ALTER_TABLE: expected at least two ALTER TABLE statements` | Zero or one statement was supplied. |
| `MERGE_ALTER_TABLE: expected only ALTER TABLE statements` | A root node is not a plain `ALTER TABLE`, such as `USE` or `INSERT`. |
| `MERGE_ALTER_TABLE: expected a single named table` | The target has an empty name, a wildcard schema, partition names, index hints, table sampling, or an `AS OF` clause. |
| `MERGE_ALTER_TABLE: mixed schema qualification is not supported` | Some statements name a schema and others do not. |
| `MERGE_ALTER_TABLE: expected the same schema and table` | Qualified statements name different schemas or tables. Comparison is exact and case-sensitive. |
| `MERGE_ALTER_TABLE: expected ALTER actions` | A statement has no actions. |
| `MERGE_ALTER_TABLE: only ADD COLUMN, ADD INDEX or MODIFY COLUMN actions are supported` | The batch contains a different action. |
| `MERGE_ALTER_TABLE: table-level constraints are not supported` | A column action carries an attached table constraint. |
| `MERGE_ALTER_TABLE: expected exactly one column per column action` | A specification defines several columns. |
| `MERGE_ALTER_TABLE: unsupported column definition` | A column definition is missing required fields, or has an unsupported type attribute, expression, constraint or position. |
| `MERGE_ALTER_TABLE: ambiguous SQL quoting is not supported` | The SQL contains double-quoted or backslash-escaped literals. |
| `MERGE_ALTER_TABLE: unsupported index syntax` | The batch includes `STORING`, `LOCAL` or `GLOBAL` outside quoted tokens. |
| `MERGE_ALTER_TABLE: expected a named nonunique column index` | An index is unnamed, unique, expression-based or uses unsupported options. |
| `MERGE_ALTER_TABLE: a column may only be changed once` | The same column is added or modified more than once. |
| `MERGE_ALTER_TABLE: column changes must precede indexes on that column` | An index references a column changed later in the batch. |
| `MERGE_ALTER_TABLE: column changes must precede AFTER references to that column` | An `AFTER` references a column changed later in the batch. |
| `MERGE_ALTER_TABLE: a column cannot refer to itself in AFTER` | A column names itself as the preceding column. |
| `MERGE_ALTER_TABLE: an index name may only be used once` | The same index name occurs more than once. |
| `MERGE_ALTER_TABLE: cannot render ALTER actions` | The AST printer rejected a retained action. |

Ordinary non-executable comments are allowed. Because the command returns only parsed structure, a successful response does not prove that the table or columns exist, that the columns do not already exist, or that the resulting `ALTER TABLE` is safe to run.

## Scope

This command does not modify `PARSE_ALTER_TARGET`, `PARSE_DML_TARGET`, the ordinary PRINT/CHECK/EXEC paths, or autowork's current parser selection. It does not add a global reserved keyword, port, or connection. `merge_alter_table` remains an ordinary SQL identifier outside this command.
