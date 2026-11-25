package session

import (
	"strings"
	"testing"

	"github.com/hanchuanchuan/goInception/mysql"
	"github.com/hanchuanchuan/goInception/parser"
	"golang.org/x/net/context"
)

func TestMergeAlterTableCommand(t *testing.T) {
	for _, tc := range []struct {
		name, payload, suffix, schema, table, actions string
	}{
		{
			name: "four ordered after columns",
			payload: "ALTER TABLE `Shop`.`Orders` ADD COLUMN `c1` INT NOT NULL DEFAULT 0 AFTER `base`;" +
				"ALTER TABLE `Shop`.`Orders` ADD COLUMN `c2` VARCHAR(20) DEFAULT 'a;b' COMMENT '备注一' AFTER `c1`;" +
				"ALTER TABLE `Shop`.`Orders` ADD COLUMN `CamelCase` DECIMAL(10,2) DEFAULT 1.5 AFTER `c2`;" +
				"ALTER TABLE `Shop`.`Orders` ADD COLUMN `c4` DATETIME DEFAULT CURRENT_TIMESTAMP AFTER `CamelCase`",
			schema:  "Shop",
			table:   "Orders",
			actions: "ADD COLUMN `c1` INT NOT NULL DEFAULT 0 AFTER `base`, ADD COLUMN `c2` VARCHAR(20) DEFAULT 'a;b' COMMENT '备注一' AFTER `c1`, ADD COLUMN `CamelCase` DECIMAL(10,2) DEFAULT 1.5 AFTER `c2`, ADD COLUMN `c4` DATETIME DEFAULT CURRENT_TIMESTAMP() AFTER `CamelCase`",
		},
		{
			name: "add column then index and modify another column",
			payload: "ALTER TABLE Shop.Orders ADD COLUMN RefID BIGINT NOT NULL DEFAULT -1;" +
				"ALTER TABLE Shop.Orders ADD INDEX idx_ref (RefID);" +
				"ALTER TABLE Shop.Orders MODIFY COLUMN ChannelID VARCHAR(64) NOT NULL DEFAULT ''",
			schema:  "Shop",
			table:   "Orders",
			actions: "ADD COLUMN `RefID` BIGINT NOT NULL DEFAULT -1, ADD INDEX `idx_ref`(`RefID`), MODIFY COLUMN `ChannelID` VARCHAR(64) NOT NULL DEFAULT ''",
		},
		{
			name:    "unqualified ignores the session schema",
			payload: "ALTER TABLE Orders ADD COLUMN a INT; ALTER TABLE Orders ADD COLUMN b INT",
			schema:  "",
			table:   "Orders",
			actions: "ADD COLUMN `a` INT, ADD COLUMN `b` INT",
		},
		{
			name: "multiple specs and outer trailing semicolon",
			payload: "ALTER TABLE `Sales`.`Orders` ADD COLUMN `a` INT, ADD COLUMN `b` VARCHAR(10) DEFAULT 'x,y' AFTER `a`;" +
				"ALTER TABLE `Sales`.`Orders` ADD COLUMN `c` BIGINT UNSIGNED AFTER `b`",
			suffix:  ";",
			schema:  "Sales",
			table:   "Orders",
			actions: "ADD COLUMN `a` INT, ADD COLUMN `b` VARCHAR(10) DEFAULT 'x,y' AFTER `a`, ADD COLUMN `c` BIGINT UNSIGNED AFTER `b`",
		},
		{
			name:    "prior command names are ordinary columns",
			payload: "ALTER TABLE T ADD COLUMN parse_alter_target INT; ALTER TABLE T ADD COLUMN parse_dml_target INT",
			schema:  "",
			table:   "T",
			actions: "ADD COLUMN `parse_alter_target` INT, ADD COLUMN `parse_dml_target` INT",
		},
		{
			name:    "executable comment marker in a literal",
			payload: "ALTER TABLE T ADD COLUMN a VARCHAR(20) DEFAULT '/*!40101'; ALTER TABLE T ADD COLUMN b INT",
			schema:  "",
			table:   "T",
			actions: "ADD COLUMN `a` VARCHAR(20) DEFAULT '/*!40101', ADD COLUMN `b` INT",
		},
		{
			name:    "alter ignore phrase in a literal",
			payload: "ALTER TABLE T ADD COLUMN a INT COMMENT 'ALTER IGNORE TABLE'; ALTER TABLE T ADD COLUMN b INT",
			schema:  "",
			table:   "T",
			actions: "ADD COLUMN `a` INT COMMENT 'ALTER IGNORE TABLE', ADD COLUMN `b` INT",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewInception()
			s.sessionVars.SQLMode = mysql.ModeANSIQuotes | mysql.ModeNoBackslashEscapes
			s.sessionVars.CurrentDB = "not_the_target_schema"
			query := "  iNcEpTiOn /* local */ merge_alter_table " + quoteMergeAlterTablePayload(tc.payload) + tc.suffix
			sets, err := s.ExecuteInc(context.Background(), query)
			if err != nil || len(sets) != 1 {
				t.Fatalf("expected one result set, got %d, %v", len(sets), err)
			}
			rs := sets[0]
			defer rs.Close()
			fields := rs.Fields()
			if len(fields) != 3 ||
				fields[0].ColumnAsName.O != "schema" ||
				fields[1].ColumnAsName.O != "table" ||
				fields[2].ColumnAsName.O != "alterActions" {
				t.Fatalf("unexpected result columns: %v", fields)
			}
			for i, field := range fields {
				if field.Column.FieldType.Tp != mysql.TypeVarString {
					t.Fatalf("column %d is not VarString: %d", i, field.Column.FieldType.Tp)
				}
			}
			chk := rs.NewChunk()
			if err := rs.Next(context.Background(), chk); err != nil || chk.NumRows() != 1 {
				t.Fatalf("expected one row, got %d, %v", chk.NumRows(), err)
			}
			row := chk.GetRow(0)
			if row.GetString(0) != tc.schema || row.GetString(1) != tc.table || row.GetString(2) != tc.actions {
				t.Fatalf("unexpected merge (%q, %q, %q)", row.GetString(0), row.GetString(1), row.GetString(2))
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

func TestMergeAlterTableRejectsUnsupportedRequests(t *testing.T) {
	const (
		command           = "INCEPTION MERGE_ALTER_TABLE "
		twoAlt            = "MERGE_ALTER_TABLE: expected at least two ALTER TABLE statements"
		onlyAlter         = "MERGE_ALTER_TABLE: expected only ALTER TABLE statements"
		mixedSchema       = "MERGE_ALTER_TABLE: mixed schema qualification is not supported"
		sameTarget        = "MERGE_ALTER_TABLE: expected the same schema and table"
		oneTable          = "MERGE_ALTER_TABLE: expected a single named table"
		addOnly           = "MERGE_ALTER_TABLE: only ADD COLUMN, ADD INDEX or MODIFY COLUMN actions are supported"
		oneColumn         = "MERGE_ALTER_TABLE: expected exactly one column per column action"
		invalid           = "MERGE_ALTER_TABLE: invalid SQL syntax"
		standalone        = "MERGE_ALTER_TABLE: must be a standalone command"
		oneArgument       = "MERGE_ALTER_TABLE: expected one quoted SQL argument"
		warning           = "MERGE_ALTER_TABLE: input produced a parser warning"
		executable        = "MERGE_ALTER_TABLE: executable comments are not supported"
		alterIgnore       = "MERGE_ALTER_TABLE: ALTER IGNORE TABLE is not supported"
		unsupportedColumn = "MERGE_ALTER_TABLE: unsupported column definition"
	)
	for _, tc := range []struct {
		name, query, want string
	}{
		{"empty", command + "''", twoAlt},
		{"single statement", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT"), twoAlt},
		{"missing argument", command, oneArgument},
		{"wrong argument delimiter", command + `"ALTER TABLE t ADD COLUMN a INT; ALTER TABLE t ADD COLUMN b INT"`, oneArgument},
		{"insert in batch", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; INSERT INTO t VALUES (1)"), onlyAlter},
		{"use in batch", command + quoteMergeAlterTablePayload("USE Shop; ALTER TABLE t ADD COLUMN a INT"), onlyAlter},
		{"drop column", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; ALTER TABLE t DROP COLUMN b"), addOnly},
		{"unique index", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD UNIQUE INDEX i (a); ALTER TABLE t MODIFY COLUMN b INT"), "MERGE_ALTER_TABLE: expected a named nonunique column index"},
		{"repeated column change", command + quoteMergeAlterTablePayload("ALTER TABLE t MODIFY COLUMN A VARCHAR(64); ALTER TABLE t MODIFY COLUMN a INT"), "MERGE_ALTER_TABLE: a column may only be changed once"},
		{"index before column change", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD INDEX i (a); ALTER TABLE t MODIFY COLUMN A INT"), "MERGE_ALTER_TABLE: column changes must precede indexes on that column"},
		{"repeated index name", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD INDEX I (a); ALTER TABLE t ADD INDEX i (b)"), "MERGE_ALTER_TABLE: an index name may only be used once"},
		{"after later changed column", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT AFTER B; ALTER TABLE t ADD COLUMN b INT"), "MERGE_ALTER_TABLE: column changes must precede AFTER references to that column"},
		{"after self", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT AFTER A; ALTER TABLE t ADD COLUMN b INT"), "MERGE_ALTER_TABLE: a column cannot refer to itself in AFTER"},
		{"algorithm", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; ALTER TABLE t ADD COLUMN b INT, ALGORITHM=INPLACE"), addOnly},
		{"lock", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; ALTER TABLE t ADD COLUMN b INT, LOCK=NONE"), addOnly},
		{"rename", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; ALTER TABLE t RENAME TO u"), addOnly},
		{"multiple columns in one action", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN (a INT, b INT); ALTER TABLE t ADD COLUMN c INT"), oneColumn},
		{"qualified column name", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a.b INT; ALTER TABLE t ADD COLUMN c INT"), unsupportedColumn},
		{"qualified after column", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT AFTER x.y; ALTER TABLE t ADD COLUMN c INT"), unsupportedColumn},
		{"qualified then unqualified", command + quoteMergeAlterTablePayload("ALTER TABLE Shop.t ADD COLUMN a INT; ALTER TABLE t ADD COLUMN b INT"), mixedSchema},
		{"unqualified then qualified", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; ALTER TABLE Shop.t ADD COLUMN b INT"), mixedSchema},
		{"cross table", command + quoteMergeAlterTablePayload("ALTER TABLE Shop.t ADD COLUMN a INT; ALTER TABLE Shop.u ADD COLUMN b INT"), sameTarget},
		{"cross schema", command + quoteMergeAlterTablePayload("ALTER TABLE Shop.t ADD COLUMN a INT; ALTER TABLE Other.t ADD COLUMN b INT"), sameTarget},
		{"wildcard schema", command + quoteMergeAlterTablePayload("ALTER TABLE *.t ADD COLUMN a INT; ALTER TABLE *.t ADD COLUMN b INT"), oneTable},
		{"executable comment", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; /*!40101 ALTER TABLE t ADD COLUMN b INT */"), executable},
		{"alter ignore", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; ALTER IGNORE TABLE t ADD COLUMN b INT"), alterIgnore},
		{"backslash in text", command + quoteMergeAlterTablePayload(`ALTER TABLE t ADD COLUMN a INT COMMENT 'path\\name'; ALTER TABLE t ADD COLUMN b INT`), "MERGE_ALTER_TABLE: ambiguous SQL quoting is not supported"},
		{"double quoted literal", command + quoteMergeAlterTablePayload(`ALTER TABLE t ADD COLUMN a INT COMMENT "a note"; ALTER TABLE t ADD COLUMN b INT`), "MERGE_ALTER_TABLE: ambiguous SQL quoting is not supported"},
		{"inline unique key", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT UNIQUE; ALTER TABLE t ADD COLUMN b INT"), unsupportedColumn},
		{"enum charset", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a ENUM('x','y') CHARACTER SET utf8mb4; ALTER TABLE t ADD COLUMN b INT"), unsupportedColumn},
		{"before column", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT BEFORE z; ALTER TABLE t ADD COLUMN b INT"), unsupportedColumn},
		{"discarded storing option", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD INDEX i (a) COMMENT 'x' STORING (b); ALTER TABLE t ADD COLUMN c INT"), "MERGE_ALTER_TABLE: unsupported index syntax"},
		{"discarded global option", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD INDEX i (a) USING BTREE GLOBAL; ALTER TABLE t ADD COLUMN c INT"), "MERGE_ALTER_TABLE: unsupported index syntax"},
		{"parser warning", command + quoteMergeAlterTablePayload("ALTER TABLE t LAST PARTITION LESS THAN (10) NO_WRITE_TO_BINLOG; ALTER TABLE t ADD COLUMN b INT"), warning},
		{"malformed second statement", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; ALTER TABLE t ADD COLUMN"), invalid},
		{"nested command", command + quoteMergeAlterTablePayload("INCEPTION MERGE_ALTER_TABLE 'ALTER TABLE t ADD COLUMN a INT'"), invalid},
		{"decoded NUL", command + `'ALTER TABLE t ADD COLUMN a INT\0; ALTER TABLE t ADD COLUMN b INT'`, invalid},
		{"outer suffix", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; ALTER TABLE t ADD COLUMN b INT") + ";\nSELECT 1", standalone},
		{"outer prefix", "inception_magic_start;\n" + command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; ALTER TABLE t ADD COLUMN b INT"), standalone},
		{"unclosed outer comment", command + quoteMergeAlterTablePayload("ALTER TABLE t ADD COLUMN a INT; ALTER TABLE t ADD COLUMN b INT") + " /*", standalone},
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

func TestMergeAlterTablePriorCommandsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, query, schema, table string
		explicit                   int64
	}{
		{"parse alter target", "INCEPTION PARSE_ALTER_TARGET 'ALTER TABLE Shop.Orders ADD COLUMN k INT'", "Shop", "Orders", 1},
		{"parse dml target", "INCEPTION PARSE_DML_TARGET 'DELETE FROM Shop.Orders WHERE id = 10'", "Shop", "Orders", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewInception()
			sets, err := s.ExecuteInc(context.Background(), tc.query)
			if err != nil || len(sets) != 1 {
				t.Fatalf("expected one result set, got %d, %v", len(sets), err)
			}
			rs := sets[0]
			defer rs.Close()
			chk := rs.NewChunk()
			if err := rs.Next(context.Background(), chk); err != nil || chk.NumRows() != 1 {
				t.Fatalf("expected one row, got %d, %v", chk.NumRows(), err)
			}
			row := chk.GetRow(0)
			if row.GetString(0) != tc.schema || row.GetString(1) != tc.table || row.GetInt64(2) != tc.explicit {
				t.Fatalf("unexpected target (%q, %q, %d)", row.GetString(0), row.GetString(1), row.GetInt64(2))
			}
		})
	}
}

func TestMergeAlterTableNameRemainsOrdinaryIdentifier(t *testing.T) {
	for _, sql := range []string{
		"SELECT merge_alter_table FROM merge_alter_table",
		"SELECT 1 AS merge_alter_table",
		"ALTER TABLE merge_alter_table ADD COLUMN merge_alter_table INT",
		"SELECT 'INCEPTION MERGE_ALTER_TABLE'",
		"/* INCEPTION MERGE_ALTER_TABLE */ INCEPTION SHOW VARIABLES",
	} {
		if _, err := parser.New().ParseOneStmt(sql, "", ""); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
		if _, matched, err := parser.ReadMergeAlterTableCommand(sql); matched || err != nil {
			t.Errorf("ordinary SQL matched MERGE_ALTER_TABLE: %q (%v)", sql, err)
		}
	}
}

func quoteMergeAlterTablePayload(sql string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(sql) + "'"
}
