# Parse an UPDATE or DELETE target

Available since `v1.3.11-parse-dml-target.1`.

`INCEPTION PARSE_DML_TARGET` identifies the table named by one single-table MySQL `UPDATE` or `DELETE`. It runs on the existing goInception MySQL listener, does not connect to the target or a backup server, and does not review or execute the SQL. A successful parse does not establish that the target exists, is a physical table rather than a view, or that the statement is safe to execute.

```sql
INCEPTION PARSE_DML_TARGET 'UPDATE Shop.Orders AS o SET o.state = 1 WHERE o.id = 10';
```

This returns one row, `('Shop', 'Orders', 1)`, in the following column order:

| Column | Type | Meaning |
| --- | --- | --- |
| `schema` | String | Decoded schema identifier, or empty if not stated. |
| `table` | String | Decoded written table name, not an alias or a table read in a subquery. |
| `schemaExplicit` | BIGINT | `1` when the statement names a schema; otherwise `0`. |

Identifiers preserve spelling and case after quotes are decoded. An unqualified `DELETE FROM Orders WHERE id = 10` returns `('', 'Orders', 0)` even if the client has a default database. A caller using the result for metadata lookup must validate the names against its selected target. For `UPDATE`, a qualified assignment must use the alias if one is declared, otherwise the table name, with the same spelling as the target declaration. A schema-qualified assignment must match an explicit target schema and cannot qualify an alias.

| Argument SQL | Result |
| --- | --- |
| `DELETE FROM Shop.Orders WHERE id IN (SELECT id FROM ArchivedOrders)` | `('Shop', 'Orders', 1)`. `ArchivedOrders` is read, not written. |
| `UPDATE Orders JOIN Other ON Orders.id=Other.id SET Orders.state=1` | Error: outer JOINs are out of scope, even if only one table is assigned to. |
| `DELETE o FROM Orders AS o WHERE o.id=10` | Error: this is the multi-table DELETE form, even though it lists only one target. `DELETE ... USING` is also out of scope. |
| `INSERT INTO Orders VALUES (10)` | Error: only `UPDATE` and `DELETE` are accepted. |
| `UPDATE Orders SET state=1; DELETE FROM Orders` | Error: pass one statement, even when both statements name the same table. |

Other unsupported targets include comma-separated outer tables, derived tables, top-level CTEs, wildcard schemas, and table sampling or historical reads on the target. Invalid syntax, NUL bytes, and parser warnings are rejected. The parser uses its default syntax mode rather than any target `sql_mode`. Errors are MySQL protocol errors with no result row; the command never guesses a target from an unsupported statement.

Send a standalone text-protocol query and use the driver's quoting. Do not add a source options comment, `USE`, an Inception execution envelope, or a second command.

```python
cursor.execute("INCEPTION PARSE_DML_TARGET %s", (dml_sql,))
schema, table, schema_explicit = cursor.fetchone()
```

## Comparison with the legacy Vitess parser

This command is not a drop-in replacement for the legacy `get_sql_table_name` executable. It returns structured identifiers, while the legacy command prints a rendered table expression for `UPDATE` and `DELETE`.

A Linux AMD64 test used the MySQL protocol against an isolated goInception instance and ran the original Vitess-based executable on the same 16 synthetic inputs. In nine supported cases both identified the same target; in the other seven goInception returned error `1105` with no result row.

| Tested input | Legacy Vitess stdout or outcome | `PARSE_DML_TARGET` outcome |
| --- | --- | --- |
| Unqualified `UPDATE` or `DELETE` on `Orders` or `_SHADOW_Orders` | Bare table name | Same name in `table`; empty schema, `schemaExplicit=0` |
| `UPDATE Shop.Orders AS o SET o.state=1 WHERE o.id=10` | `Shop.Orders as o` | `('Shop', 'Orders', 1)` |
| Qualified `DELETE` with `ORDER BY` and `LIMIT`, or with a read subquery | `Shop.Orders` | `('Shop', 'Orders', 1)`; the read-side table is not the target |
| Quoted Chinese schema and table | `` `测试库`.`订单` `` | `('测试库', '订单', 1)` |
| `UPDATE` with an outer JOIN, or `DELETE` with an explicit target list (one or more targets) | Rendered table expression, including JOIN or alias where present | Rejected: expected a single named table |
| `INSERT INTO Orders (id) VALUES (10)` | `Orders` | Rejected: expected exactly one `UPDATE` or `DELETE` |
| Two DML statements on the same or different tables | Syntax error | Rejected: expected exactly one `UPDATE` or `DELETE` |
| Malformed `UPDATE` | Syntax error | Rejected: invalid SQL syntax |

The MySQL protocol test also rejected two requests that appended a second command, with multi-statement support enabled on the client. The existing [`PARSE_ALTER_TARGET`](./parse-alter-target.md) and `INCEPTION SHOW VARIABLES` commands passed regression checks. No target database was connected to and none of the input statements was executed.
