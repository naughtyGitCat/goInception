# Parse an ALTER TABLE target

Available since `v1.3.10-parse-alter-target.1`. Use this command to identify the original table named in a MySQL `ALTER TABLE` before comparing it with a change request's declared target. For example, autowork can use it before looking up a shadow table. This is not a replacement for the legacy parser's other DDL or DML callers.

`INCEPTION PARSE_ALTER_TARGET` is a standalone query on the existing goInception MySQL listener, not on the target MySQL instance. It accepts one single-quoted SQL string. Do not send a source options comment, `USE`, or an `inception_magic_start` / `inception_magic_commit` envelope.

```sql
INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE Shop.Orders ADD INDEX i_user (user_id), ALGORITHM=INPLACE, LOCK=NONE';
```

A successful query returns one row with three columns:

| Column | Type | Meaning |
| --- | --- | --- |
| `schema` | String | Decoded schema identifier, or an empty string if unqualified. |
| `table` | String | Decoded original target table identifier. For `ALTER TABLE ... RENAME TO ...` this is the table before the rename. |
| `schemaExplicit` | BIGINT | `1` if the SQL names a schema, otherwise `0`. |

The returned identifiers preserve case. SQL quote delimiters are removed and escaped identifier characters are decoded.

## Scenarios and expected results

Pass the SQL in the first column as the single string argument. Success rows below are shown in column order: `(schema, table, schemaExplicit)`.

| Input SQL | Expected result |
| --- | --- |
| `ALTER TABLE Shop.Orders ADD INDEX i_user (user_id), ALGORITHM=INPLACE, LOCK=NONE` | `('Shop', 'Orders', 1)` |
| `ALTER TABLE Orders ADD COLUMN x INT, ADD COLUMN y INT` | `('', 'Orders', 0)`. Multiple changes in one statement are allowed. |
| `ALTER TABLE Shop.Orders RENAME TO Archive.OrdersOld` | `('Shop', 'Orders', 1)`. The rename destination is not returned. |
| `ALTER TABLE tab_a ADD COLUMN x INT; ALTER TABLE tab_b ADD COLUMN y INT` | Error `PARSE_ALTER_TARGET: expected exactly one ALTER TABLE`; no row. |
| `ALTER TABLE Orders ADD COLUMN x INT; ALTER TABLE Orders ADD COLUMN y INT` | Same error and no row, even though both statements name the same table. |
| `DROP TABLE Orders` | Error `PARSE_ALTER_TARGET: expected exactly one ALTER TABLE`; no row. |
| `ALTER TABLE Orders ADD COLUMN` | Error `PARSE_ALTER_TARGET: invalid SQL syntax`; no row. |

The command also rejects an empty string, NUL bytes, parser warnings, and any root node other than `AlterTableStmt`. The outer request cannot include another statement either: appending `; SELECT 1` after the closing quote returns `PARSE_ALTER_TARGET: must be a standalone command`. These are MySQL protocol error responses, not rows with error flags. Older servers do not implement this command; a failure must not be treated as a parsed target.

## Client usage

The command uses the local parser with its default syntax mode, not the target database's `sql_mode`. It does not fill in an omitted schema from the session. It never opens a source or backup connection and does not audit or execute the input. The caller must still check the result against the selected schema and declared table and perform any normal audit before execution. `PARSE_ALTER_TARGET` is still available as an ordinary SQL identifier outside this command.

Send the request as one MySQL text-protocol query. Use the driver's string escaping rather than concatenating DDL directly:

```python
cursor.execute("INCEPTION PARSE_ALTER_TARGET %s", (alter_sql,))
schema, table, schema_explicit = cursor.fetchone()
```

## Compare with autowork's legacy parser

Run the comparison against an isolated homelab goInception listener, exposed only via loopback or an SSH tunnel. The comparison driver needs Python 3 and PyMySQL. Point `--legacy` at the original Linux `autowork-platform/lib/get_sql_table_name` executable, or pass a command that runs it over SSH. The driver sends the same SQL to the legacy binary's standard input and the new MySQL command; it never executes the input on a target.

```text
python3 -I tools/parse_alter_target_homelab/compare.py --port <local-test-port> --legacy <legacy-binary>
```

The fixtures assert common table-name results as well as the intentional differences: separate schema output, support for `ALGORITHM=INPLACE, LOCK=NONE`, and rejection of non-ALTER or multiple statements rather than returning a misleading target.
