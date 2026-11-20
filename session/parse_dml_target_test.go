package session

import (
	"strings"
	"testing"

	"github.com/hanchuanchuan/goInception/mysql"
	"github.com/hanchuanchuan/goInception/parser"
	"golang.org/x/net/context"
)

func TestParseDMLTargetCommand(t *testing.T) {
	for _, tc := range []struct {
		name, payload, schema, table string
		explicit                     int64
	}{
		{"update", "UPDATE Orders SET state = 1 WHERE id = 10", "", "Orders", 0},
		{"qualified aliased update", "UPDATE `Shop`.`Orders` AS o SET o.state = 1 WHERE o.id = 10", "Shop", "Orders", 1},
		{"qualified delete", "DELETE FROM Shop.Orders WHERE id = 10 ORDER BY id LIMIT 1;", "Shop", "Orders", 1},
		{"unqualified delete", "DELETE FROM Orders WHERE id = 10", "", "Orders", 0},
		{"aliased delete", "DELETE FROM Shop.Orders AS o WHERE o.id = 10", "Shop", "Orders", 1},
		{"parse is not a safety audit", "DELETE FROM Orders", "", "Orders", 0},
		{"read subquery is not a write target", "UPDATE Shop.Orders SET state = (SELECT state FROM Staging WHERE id = 10) WHERE id = 10", "Shop", "Orders", 1},
		{"delete with subquery", "DELETE FROM Orders WHERE id IN (SELECT id FROM ArchivedOrders)", "", "Orders", 0},
		{"quoted identifiers and text", "UPDATE `测试库`.`订``单` SET note = 'line1;\nline2 \\ test' WHERE id = 10", "测试库", "订`单", 1},
		{"ordinary identifier", "UPDATE parse_dml_target SET parse_dml_target=1 WHERE id=10", "", "parse_dml_target", 0},
		{"other command name in SQL", "DELETE FROM parse_alter_target WHERE id=10", "", "parse_alter_target", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewInception()
			s.sessionVars.SQLMode = mysql.ModeANSIQuotes | mysql.ModeNoBackslashEscapes
			s.sessionVars.CurrentDB = "not_the_target_schema"
			sets, err := s.ExecuteInc(context.Background(), "  iNcEpTiOn /* local */ parse_dml_target "+quoteDMLTargetPayload(tc.payload)+";")
			if err != nil || len(sets) != 1 {
				t.Fatalf("expected one result set, got %d, %v", len(sets), err)
			}
			rs := sets[0]
			defer rs.Close()
			fields := rs.Fields()
			if len(fields) != 3 || fields[0].ColumnAsName.O != "schema" || fields[1].ColumnAsName.O != "table" || fields[2].ColumnAsName.O != "schemaExplicit" {
				t.Fatalf("unexpected result columns: %v", fields)
			}
			chk := rs.NewChunk()
			if err := rs.Next(context.Background(), chk); err != nil || chk.NumRows() != 1 {
				t.Fatalf("expected one row, got %d, %v", chk.NumRows(), err)
			}
			row := chk.GetRow(0)
			if row.GetString(0) != tc.schema || row.GetString(1) != tc.table || row.GetInt64(2) != tc.explicit {
				t.Fatalf("unexpected target (%q, %q, %d)", row.GetString(0), row.GetString(1), row.GetInt64(2))
			}
			if err := rs.Next(context.Background(), chk); err != nil || chk.NumRows() != 0 {
				t.Fatalf("unexpected extra rows: %d, %v", chk.NumRows(), err)
			}
			if s.db != nil || s.opt != nil || s.recordSets != nil {
				t.Fatal("parse-only command entered ordinary Inception setup")
			}
		})
	}
}

func TestParseDMLTargetRejectsUnsupportedRequests(t *testing.T) {
	const (
		oneDML      = "PARSE_DML_TARGET: expected exactly one UPDATE or DELETE"
		singleTable = "PARSE_DML_TARGET: expected a single named table"
		invalid     = "PARSE_DML_TARGET: invalid SQL syntax"
		standalone  = "PARSE_DML_TARGET: must be a standalone command"
		oneArgument = "PARSE_DML_TARGET: expected one quoted SQL argument"
		command     = "INCEPTION PARSE_DML_TARGET "
	)
	for _, tc := range []struct {
		name, query, want string
	}{
		{"empty", command + "''", oneDML},
		{"missing argument", command, oneArgument},
		{"wrong argument delimiter", command + `"DELETE FROM t"`, oneArgument},
		{"insert", command + "'INSERT INTO t VALUES(1)'", oneDML},
		{"replace", command + "'REPLACE INTO t VALUES(1)'", oneDML},
		{"alter", command + "'ALTER TABLE t ADD c INT'", oneDML},
		{"select", command + "'SELECT * FROM t'", oneDML},
		{"invalid SQL", command + "'UPDATE t SET'", invalid},
		{"parser warning", command + "'UPDATE /*+ no_such_hint() */ t SET c=1'", "PARSE_DML_TARGET: input produced a parser warning"},
		{"decoded NUL", command + `'UPDATE t SET c=1\0'`, invalid},
		{"inner batch", command + "'UPDATE t SET c=1; DELETE FROM t'", oneDML},
		{"same-target inner batch", command + "'UPDATE t SET c=1; UPDATE t SET c=2'", oneDML},
		{"joined update with one written table", command + "'UPDATE t JOIN u ON t.id=u.id SET t.c=1'", singleTable},
		{"comma update", command + "'UPDATE t, u SET t.c=1, u.c=2'", singleTable},
		{"parenthesized join update", command + "'UPDATE (t JOIN u ON t.id=u.id) SET t.c=1'", singleTable},
		{"derived table", command + "'UPDATE (SELECT id FROM t) AS a SET id=1'", singleTable},
		{"wrong assignment alias", command + "'UPDATE t AS a SET b.c=1'", singleTable},
		{"wrong assignment schema", command + "'UPDATE Shop.t SET Other.t.c=1'", singleTable},
		{"wildcard schema update", command + "'UPDATE *.t SET c=1'", singleTable},
		{"wildcard schema delete", command + "'DELETE FROM *.t'", singleTable},
		{"empty table name", command + "'UPDATE Shop.`` SET c=1'", singleTable},
		{"CTE update", command + "'WITH t AS (SELECT 1) UPDATE t SET c=1'", singleTable},
		{"CTE delete", command + "'WITH t AS (SELECT 1) DELETE FROM t'", singleTable},
		{"single written table with multi-table delete syntax", command + "'DELETE t FROM t JOIN u ON t.id=u.id'", singleTable},
		{"explicit delete target list without a join", command + "'DELETE t FROM t WHERE id=1'", singleTable},
		{"using delete syntax", command + "'DELETE FROM t USING t WHERE id=1'", singleTable},
		{"multiple delete targets", command + "'DELETE t,u FROM t JOIN u ON t.id=u.id'", singleTable},
		{"outer suffix", command + "'DELETE FROM t';\nSELECT 1", standalone},
		{"outer prefix", "inception_magic_start;\n" + command + "'DELETE FROM t'", standalone},
		{"other parse command first", "INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE t ADD c INT';" + command + "'DELETE FROM t'", "PARSE_ALTER_TARGET: must be a standalone command"},
		{"other parse command last", command + "'DELETE FROM t';INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE t ADD c INT'", "PARSE_ALTER_TARGET: must be a standalone command"},
		{"unclosed outer comment", command + "'DELETE FROM t' /*", standalone},
		{"executable outer comment", "/*! " + command + "'DELETE FROM t' */", standalone},
		{"USE plus DML", command + "'USE Shop; UPDATE t SET c=1'", oneDML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewInception()
			sets, err := s.ExecuteInc(context.Background(), tc.query)
			if len(sets) != 0 || err == nil || err.Error() != tc.want {
				t.Fatalf("expected %q, got %d result sets, %v", tc.want, len(sets), err)
			}
			if s.db != nil || s.opt != nil || s.recordSets != nil {
				t.Fatal("parse-only failure entered ordinary Inception setup")
			}
		})
	}
}

func TestParseDMLTargetNameRemainsOrdinaryIdentifier(t *testing.T) {
	for _, sql := range []string{
		"SELECT parse_dml_target FROM parse_dml_target",
		"SELECT 1 AS parse_dml_target",
		"UPDATE parse_dml_target SET parse_dml_target=1",
		"SELECT 'INCEPTION PARSE_DML_TARGET'",
		"/* INCEPTION PARSE_DML_TARGET */ INCEPTION SHOW VARIABLES",
	} {
		if _, err := parser.New().ParseOneStmt(sql, "", ""); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
		if _, matched, err := parser.ReadParseDMLTargetCommand(sql); matched || err != nil {
			t.Errorf("ordinary SQL matched PARSE_DML_TARGET: %q (%v)", sql, err)
		}
	}
}

func quoteDMLTargetPayload(sql string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(sql) + "'"
}
