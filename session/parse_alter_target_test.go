package session

import (
	"strings"
	"testing"

	"github.com/hanchuanchuan/goInception/mysql"
	"github.com/hanchuanchuan/goInception/parser"
	"golang.org/x/net/context"
)

func TestParseAlterTargetCommand(t *testing.T) {
	for _, tc := range []struct {
		name, query, schema, table string
		explicit                   int64
	}{
		{"unqualified", "INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE Orders ADD COLUMN k INT'", "", "Orders", 0},
		{"algorithm and lock", "  InCePtIoN /* utility */ parse_alter_target 'ALTER TABLE `Shop`.`Orders`\n ADD INDEX i (uid),\n ALGORITHM=INPLACE, LOCK=NONE;';", "Shop", "Orders", 1},
		{"original not rename destination", "INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE `OldDB`.`OldTable` RENAME TO `NewDB`.`NewTable`'", "OldDB", "OldTable", 1},
		{"quoted literal", "INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE `测试库`.`订.单` ADD COLUMN c VARCHAR(20) COMMENT ''line1;\nline2''';", "测试库", "订.单", 1},
		{"nonreserved identifier", "INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE parse_alter_target ADD COLUMN parse_alter_target INT'", "", "parse_alter_target", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewInception()
			s.sessionVars.SQLMode = mysql.ModeANSIQuotes | mysql.ModeNoBackslashEscapes
			s.sessionVars.CurrentDB = "not_the_target_schema"
			sets, err := s.ExecuteInc(context.Background(), tc.query)
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

func TestParseAlterTargetRejectsUnsupportedRequests(t *testing.T) {
	for _, tc := range []struct {
		name, query, want string
	}{
		{"empty", "INCEPTION PARSE_ALTER_TARGET ''", "expected exactly one ALTER TABLE"},
		{"missing argument", "INCEPTION PARSE_ALTER_TARGET", "expected one quoted SQL argument"},
		{"non-ALTER", "INCEPTION PARSE_ALTER_TARGET 'DROP TABLE t'", "expected exactly one ALTER TABLE"},
		{"invalid SQL", "INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE t ADD COLUMN'", "invalid SQL syntax"},
		{"parser warning", "INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE t LAST PARTITION LESS THAN (10) NO_WRITE_TO_BINLOG'", "parser warning"},
		{"nested command", "INCEPTION PARSE_ALTER_TARGET 'INCEPTION PARSE_ALTER_TARGET ''ALTER TABLE t ADD c INT'''", "invalid SQL syntax"},
		{"inner batch", "INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE t ADD a INT; ALTER TABLE t ADD b INT'", "expected exactly one ALTER TABLE"},
		{"outer suffix", "INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE t ADD a INT';\nSELECT 1", "must be a standalone command"},
		{"outer prefix", "/*--enable-execute;*/\ninception_magic_start;\nINCEPTION PARSE_ALTER_TARGET 'ALTER TABLE t ADD a INT'", "must be a standalone command"},
		{"unclosed outer comment", "INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE t ADD a INT' /*", "must be a standalone command"},
		{"decoded NUL", `INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE t ADD a INT\0'`, "invalid SQL syntax"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewInception()
			sets, err := s.ExecuteInc(context.Background(), tc.query)
			if len(sets) != 0 || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %d result sets, %v", tc.want, len(sets), err)
			}
			if s.db != nil || s.opt != nil || s.recordSets != nil {
				t.Fatal("parse-only failure entered ordinary Inception setup")
			}
		})
	}
}

func TestParseAlterTargetNameRemainsOrdinaryIdentifier(t *testing.T) {
	for _, sql := range []string{
		"SELECT parse_alter_target FROM parse_alter_target",
		"SELECT 1 AS parse_alter_target",
		"ALTER TABLE parse_alter_target ADD COLUMN parse_alter_target INT",
		"INCEPTION SHOW VARIABLES",
	} {
		if _, err := parser.New().ParseOneStmt(sql, "", ""); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
}
