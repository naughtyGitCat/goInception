package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/hanchuanchuan/goInception/ast"
	"github.com/hanchuanchuan/goInception/model"
	"github.com/hanchuanchuan/goInception/parser"
	"golang.org/x/net/context"
)

func printSummaryNode(t *testing.T, sql string) ast.StmtNode {
	t.Helper()
	n, err := parser.New().ParseOneStmt(sql, "", "")
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	return n
}

func TestQueryPrintSummarySharedFixtures(t *testing.T) {
	data, err := os.ReadFile("../docs/manual-ddl-ast-contract-v1-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Statement string            `json:"statement"`
		QueryTree queryPrintSummary `json:"queryTree"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Statement, func(t *testing.T) {
			got := summarizeQueryPrintSummary(printSummaryNode(t, fixture.Statement), fixture.Statement)
			if !reflect.DeepEqual(got, fixture.QueryTree) {
				t.Fatalf("shared fixture drift: got %+v want %+v", got, fixture.QueryTree)
			}
		})
	}
}

func TestQueryPrintSummary(t *testing.T) {
	cases := []struct {
		sql, kind string
		complete  bool
		actions   []string
	}{
		{"ALTER TABLE Orders MODIFY COLUMN Body MEDIUMTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL COMMENT '正文'", "alterTable", true, []string{"modifyColumn"}},
		{"CREATE TABLE Orders (id bigint NOT NULL AUTO_INCREMENT, body varchar(20) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT 'x', PRIMARY KEY (id), UNIQUE KEY idx_body (body)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin COMMENT='orders'", "createTable", true, []string{"createDefinition"}},
		{"ALTER TABLE Orders ADD COLUMN v int DEFAULT -1 AFTER id, ADD KEY ix_v(v), DROP COLUMN old_v, DROP INDEX ix_old, DROP PRIMARY KEY, CHANGE COLUMN a b varchar(20), ALTER COLUMN v SET DEFAULT 2, ALGORITHM=INPLACE, LOCK=NONE", "alterTable", true, []string{"addColumn", "addConstraint", "dropColumn", "dropIndex", "dropPrimaryKey", "changeColumn", "alterColumn", "algorithm", "lock"}},
		{"ALTER TABLE Orders ALTER COLUMN v DROP DEFAULT", "alterTable", true, []string{"alterColumn"}},
		{"DROP TABLE Orders", "dropTable", true, []string{"dropTable"}},
		{"TRUNCATE TABLE Orders", "truncateTable", true, []string{"truncateTable"}},
		{"DROP STATS Orders", "dropStats", false, []string{}},
		{"USE Shop", "use", true, []string{}},
		{"SET sql_mode = ''", "set", false, []string{}},
		{"CREATE TABLE Orders LIKE other_db.seed", "createTable", false, []string{"createDefinition"}},
		{"CREATE TABLE Orders AS SELECT * FROM other_db.seed", "createTable", false, []string{"createDefinition"}},
		{"CREATE TEMPORARY TABLE Orders (id int)", "createTable", false, []string{"createDefinition"}},
		{"CREATE VIEW Orders AS SELECT * FROM seed", "unknown", false, []string{}},
		{"DROP VIEW Orders", "unknown", false, []string{}},
		{"CREATE TABLE Orders (id int, FOREIGN KEY (id) REFERENCES other_db.seed(id))", "createTable", false, []string{"createDefinition"}},
		{"CREATE TABLE Orders (id int REFERENCES other_db.seed(id))", "createTable", false, []string{"createDefinition"}},
		{"ALTER TABLE Orders ADD COLUMN id int REFERENCES other_db.seed(id)", "alterTable", false, []string{"addColumn"}},
		{"ALTER TABLE Orders ADD CONSTRAINT fk FOREIGN KEY (id) REFERENCES other_db.seed(id)", "alterTable", false, []string{"addConstraint"}},
		{"CREATE TABLE Orders (id int DEFAULT (uuid()))", "createTable", false, []string{"createDefinition"}},
		{"ALTER TABLE Orders ALTER COLUMN id SET DEFAULT (uuid())", "alterTable", false, []string{"alterColumn"}},
		{"CREATE TABLE Orders (id int, v int GENERATED ALWAYS AS (id+1) STORED)", "createTable", false, []string{"createDefinition"}},
		{"CREATE TABLE Orders (id int, CHECK (id > 0))", "createTable", false, []string{"createDefinition"}},
		{"CREATE TABLE Orders (id int) PARTITION BY HASH(id) PARTITIONS 2", "createTable", false, []string{"createDefinition"}},
		{"CREATE TABLE Orders (id int) ENGINE=FEDERATED CONNECTION='remote'", "createTable", false, []string{"createDefinition"}},
		{"CREATE TABLE Orders (id int) TABLEGROUP='remote'", "createTable", false, []string{"createDefinition"}},
		{"CREATE TABLE Orders (id int) ROW_FORMAT=COMPRESSED", "createTable", false, []string{"createDefinition"}},
		{"ALTER TABLE Orders ENGINE=FEDERATED", "alterTable", false, []string{"unknown"}},
		{"ALTER TABLE Orders RENAME TO other_db.dest", "alterTable", false, []string{"renameTable"}},
		{"ALTER TABLE Orders EXCHANGE PARTITION p0 WITH TABLE other_db.dest", "alterTable", false, []string{"unknown"}},
		{"SELECT * FROM Orders", "unknown", false, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			got := summarizeQueryPrintSummary(printSummaryNode(t, tc.sql), tc.sql)
			if got.StatementType != tc.kind || got.Complete != tc.complete || !reflect.DeepEqual(got.Actions, tc.actions) {
				t.Fatalf("summary=%+v, want %s complete=%v actions=%v", got, tc.kind, tc.complete, tc.actions)
			}
			if got.ContractVersion != 1 || got.SqlSha256 != fmt.Sprintf("%x", sha256.Sum256([]byte(tc.sql))) {
				t.Fatalf("invalid version/hash: %+v", got)
			}
			if got.Tables == nil || got.Actions == nil {
				t.Fatal("arrays must not be null")
			}
			if tc.kind == "use" && got.Database != "Shop" {
				t.Fatalf("use database=%q", got.Database)
			}
			if tc.kind != "use" && got.Database != "" {
				t.Fatalf("database must not be default-filled: %q", got.Database)
			}
		})
	}
}

func TestQueryPrintSummaryReferencesAndSchema(t *testing.T) {
	cases := []struct {
		sql    string
		tables []queryPrintTable
	}{
		{"ALTER TABLE `Shop`.`Orders` MODIFY id int", []queryPrintTable{{"target", "Shop", "Orders", true}}},
		{"CREATE TABLE Orders (id int REFERENCES Other.Seed(id))", []queryPrintTable{{"target", "", "Orders", false}, {"reference", "Other", "Seed", true}}},
		{"ALTER TABLE Orders RENAME TO Other.Dest", []queryPrintTable{{"target", "", "Orders", false}, {"renameTarget", "Other", "Dest", true}}},
		{"ALTER TABLE Orders EXCHANGE PARTITION p0 WITH TABLE Other.Dest", []queryPrintTable{{"target", "", "Orders", false}, {"reference", "Other", "Dest", true}}},
		{"CREATE TABLE Orders AS SELECT * FROM (SELECT * FROM Other.Seed) AS x", []queryPrintTable{{"target", "", "Orders", false}, {"reference", "Other", "Seed", true}}},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			got := summarizeQueryPrintSummary(printSummaryNode(t, tc.sql), tc.sql)
			if !reflect.DeepEqual(got.Tables, tc.tables) {
				t.Fatalf("tables=%+v want %+v", got.Tables, tc.tables)
			}
		})
	}
}

func TestQueryPrintSummaryUnknownAndHiddenFeatures(t *testing.T) {
	mutations := []func(*ast.AlterTableStmt){
		func(n *ast.AlterTableStmt) { n.Specs[0].Tp = ast.AlterTableType(99999) },
		func(n *ast.AlterTableStmt) {
			n.Specs[0].Options = []*ast.TableOption{{Tp: ast.TableOptionUnion, TableNames: []*ast.TableName{{Schema: model.NewCIStr("Other"), Name: model.NewCIStr("Seed")}}}}
		},
		func(n *ast.AlterTableStmt) {
			n.Specs[0].NewColumns[0].Options = append(n.Specs[0].NewColumns[0].Options, &ast.ColumnOption{Tp: ast.ColumnOptionType(99999)})
		},
		func(n *ast.AlterTableStmt) {
			n.Specs[0].NewColumns[0].Options = append(n.Specs[0].NewColumns[0].Options, &ast.ColumnOption{Tp: ast.ColumnOptionNotNull, Expr: printSummaryNode(t, "SELECT (SELECT id FROM Other.Seed)").(*ast.SelectStmt).Fields.Fields[0].Expr})
		},
		func(n *ast.AlterTableStmt) {
			n.Specs[0].NewColumns[0].Options = append(n.Specs[0].NewColumns[0].Options, &ast.ColumnOption{Tp: ast.ColumnOptionNotNull, Refer: &ast.ReferenceDef{Table: &ast.TableName{Schema: model.NewCIStr("Other"), Name: model.NewCIStr("Seed")}}})
		},
	}
	for i, mutate := range mutations {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			n := printSummaryNode(t, "ALTER TABLE Orders ADD COLUMN id int").(*ast.AlterTableStmt)
			mutate(n)
			got := summarizeQueryPrintSummary(n, "exact sql")
			if got.Complete {
				t.Fatalf("hidden feature accepted: %+v", got)
			}
			if (i == 1 || i >= 3) && len(got.Tables) != 2 {
				t.Fatalf("hidden references missing: %+v", got)
			}
		})
	}
}

func TestQueryPrintSummaryOptions(t *testing.T) {
	for _, options := range []string{"--enable-query-print-summary;--enable-execute;--enable-remote-backup;", "--query-print-summary=1;--execute=1;--backup=1;"} {
		s := NewInception()
		s.init()
		s.recordSets = NewRecordSets()
		s.myRecord = &Record{Buf: new(bytes.Buffer)}
		// Invalid/missing source deliberately prevents any network connection.
		s.parseOptions("/*" + options + "*/inception_magic_start")
		if s.opt == nil || !s.opt.QueryPrintSummary || !s.opt.Print || s.opt.Execute || s.opt.Backup {
			t.Fatalf("PRINT summary did not disable execution/backup: %+v", s.opt)
		}
	}
}

func TestQueryPrintSummaryDisabledAndLegacyOptions(t *testing.T) {
	for _, options := range []string{
		"--disable-query-print-summary;--enable-query-print;",
		"--query-print-summary=0;--enable-query-print;",
		"--enable-query-print-v1;--enable-query-print;",
	} {
		s := NewInception()
		s.init()
		s.recordSets = NewRecordSets()
		s.myRecord = &Record{Buf: new(bytes.Buffer)}
		// Missing source stops before connecting; the superseded name is not an alias.
		s.parseOptions("/*" + options + "*/inception_magic_start")
		if s.opt == nil || s.opt.QueryPrintSummary || !s.opt.Print || s.opt.Execute || s.opt.Backup {
			t.Fatalf("disabled/superseded summary option changed legacy PRINT: %+v", s.opt)
		}
	}
}

func TestQueryPrintSummaryDirectOptionsDisableExecution(t *testing.T) {
	s := NewInception()
	s.opt = &SourceOptions{QueryPrintSummary: true, Execute: true, Backup: true}
	// Validate normalization before the empty-source rejection; no network is used.
	if err := s.checkOptions(); err == nil {
		t.Fatal("missing source must be rejected")
	}
	if !s.opt.Print || s.opt.Execute || s.opt.Backup || s.opt.Check {
		t.Fatalf("direct summary option did not remain print-only: %+v", s.opt)
	}
}

func TestQueryPrintSummaryBatchCompletion(t *testing.T) {
	cases := []struct {
		name, sql      string
		begin, v1, end bool
	}{
		{"success", "USE Shop;\nALTER TABLE Orders MODIFY Body MEDIUMTEXT;\ninception_magic_commit;", true, true, true},
		{"legacy", "USE Shop;\nTRUNCATE Orders;\ninception_magic_commit;", true, false, false},
		{"parseFailure", "USE Shop;\nALTER TABLE Orders ADD COLUMN;\ninception_magic_commit;", true, true, false},
		{"missingCommit", "USE Shop;\nTRUNCATE Orders;", true, true, false},
		{"sourceFailure", "/*--enable-query-print-summary;*/inception_magic_start;\nTRUNCATE Orders;\ninception_magic_commit;", false, true, false},
		{"duplicateStart", "/*--enable-query-print-summary;*/inception_magic_start;\ninception_magic_commit;", true, true, false},
		{"trailingSameChunk", "TRUNCATE Orders; inception_magic_commit; DROP TABLE Orders;", true, true, false},
		{"trailingNextChunk", "TRUNCATE Orders;\ninception_magic_commit;\nDROP TABLE Orders;", true, true, false},
		{"warningSameChunk", "CREATE TABLE Orders (id int, FULLTEXT KEY ix(id) WITH PARSER ngram); inception_magic_commit;", true, true, false},
		{"emptyBatch", "inception_magic_commit;", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewInception()
			s.init()
			s.recordSets = NewRecordSets()
			s.printSets = NewPrintSets()
			s.opt = &SourceOptions{Print: true, QueryPrintSummary: tc.v1}
			s.haveBegin = tc.begin
			// Remote PRINT never needs TiDB metadata. Avoid storage bootstrap without mocking the parser/control flow.
			s.txn.txnFuture = &txnFuture{}
			s.sessionVars.CommonGlobalLoaded = true
			_, err := s.executeInc(context.Background(), tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			ended := false
			for i, row := range s.printSets.rc.data {
				if row[0].GetInt64() != int64(i+1) {
					t.Fatal("noncontiguous ids")
				}
				if row[1].GetString() == "inception_magic_commit" && row[2].GetInt64() == 0 {
					ended = true
					if i != len(s.printSets.rc.data)-1 || row[2].GetInt64() != 0 {
						t.Fatal("terminal must be successful and final")
					}
					var got queryPrintSummary
					if err := json.Unmarshal([]byte(row[3].GetString()), &got); err != nil {
						t.Fatal(err)
					}
					if got.StatementType != "batchEnd" || !got.Complete || got.Database != "" || len(got.Tables) != 0 || len(got.Actions) != 0 || got.SqlSha256 != fmt.Sprintf("%x", sha256.Sum256([]byte(row[1].GetString()))) {
						t.Fatalf("invalid terminal: %+v", got)
					}
				}
			}
			if ended != tc.end {
				t.Fatalf("ended=%v want %v", ended, tc.end)
			}
			if tc.name == "sourceFailure" && (s.printSets.rc.count != 1 || s.printSets.rc.data[0][2].GetInt64() != 2) {
				t.Fatal("source error lost")
			}
		})
	}
}

func TestQueryPrintSummaryParserRejectsUnsupportedSyntax(t *testing.T) {
	for _, sql := range []string{"CREATE TABLE Orders (id int) UNION=(Other.Seed)", "CREATE TABLE Orders (id int) DATA DIRECTORY='/tmp/orders'", "ALTER TABLE Orders IMPORT TABLESPACE", "ALTER TABLE Orders DISCARD TABLESPACE"} {
		if _, err := parser.New().ParseOneStmt(sql, "", ""); err == nil {
			t.Fatalf("fixture now parses; add summary coverage: %s", sql)
		}
	}
}

func TestQueryPrintSummaryCreateHiddenOptions(t *testing.T) {
	for _, tp := range []ast.TableOptionType{ast.TableOptionUnion, ast.TableOptionDataDirectory, ast.TableOptionConnection, ast.TableOptionType(99999)} {
		n := printSummaryNode(t, "CREATE TABLE Orders (id int)").(*ast.CreateTableStmt)
		n.Options = append(n.Options, &ast.TableOption{Tp: tp, TableNames: []*ast.TableName{{Schema: model.NewCIStr("Other"), Name: model.NewCIStr("Seed")}}})
		got := summarizeQueryPrintSummary(n, "original")
		if got.Complete || len(got.Tables) != 2 || got.Tables[0].Role != "target" || got.Tables[1].Role != "reference" {
			t.Fatalf("hidden create option accepted/lost: %+v", got)
		}
	}
	for _, tp := range []ast.AlterTableType{ast.AlterTableImportTablespace, ast.AlterTableDiscardTablespace, ast.AlterTableImportPartitionTablespace, ast.AlterTableDiscardPartitionTablespace} {
		n := printSummaryNode(t, "ALTER TABLE Orders ADD id int").(*ast.AlterTableStmt)
		n.Specs[0].Tp = tp
		got := summarizeQueryPrintSummary(n, "original")
		if got.Complete {
			t.Fatalf("storage action accepted: %+v", got)
		}
	}
}

func TestQueryPrintSummaryInvalidOptionFailsClosed(t *testing.T) {
	s := NewInception()
	s.init()
	s.recordSets = NewRecordSets()
	s.myRecord = &Record{Buf: new(bytes.Buffer)}
	s.parseOptions("/*--query-print-summary=maybe;*/inception_magic_start")
	if s.myRecord.ErrLevel != 2 || !strings.Contains(s.myRecord.Buf.String(), "query_print_summary_invalid_option") {
		t.Fatalf("invalid summary mode must fail closed: %s", s.myRecord.Buf.String())
	}
}

func TestQueryPrintSummaryParserWarningsFailClosed(t *testing.T) {
	sql := "CREATE TABLE Orders (id int, FULLTEXT KEY ix (id) WITH PARSER ngram)"
	s := NewInception()
	s.init()
	s.opt = &SourceOptions{Print: true, QueryPrintSummary: true}
	s.printSets = NewPrintSets()
	nodes, err := s.ParseSQL(context.Background(), sql, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !s.queryPrintSummaryParseWarnings {
		t.Fatal("parser warning was discarded")
	}
	if _, err := s.printCommand(context.Background(), nodes[0], sql); err != nil {
		t.Fatal(err)
	}
	var got queryPrintSummary
	if err := json.Unmarshal([]byte(s.printSets.rc.data[0][3].GetString()), &got); err != nil {
		t.Fatal(err)
	}
	if got.Complete {
		t.Fatal("warning must fail closed")
	}
	sql = "CREATE TABLE Orders (id int)"
	nodes, err = s.ParseSQL(context.Background(), sql, "", "")
	if err != nil {
		t.Fatal(err)
	}
	s.printCommand(context.Background(), nodes[0], sql)
	json.Unmarshal([]byte(s.printSets.rc.data[1][3].GetString()), &got)
	if !got.Complete {
		t.Fatal("warning leaked into next parse")
	}
}

func TestQueryPrintSummaryWireAndLegacy(t *testing.T) {
	sql := "ALTER TABLE Orders MODIFY Body MEDIUMTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"
	n := printSummaryNode(t, sql)
	for _, v1 := range []bool{false, true} {
		s := NewInception()
		s.opt = &SourceOptions{Print: true, QueryPrintSummary: v1}
		s.printSets = NewPrintSets()
		if _, err := s.printCommand(context.Background(), n, sql); err != nil {
			t.Fatal(err)
		}
		row := s.printSets.rc.data[0]
		if len(row) != 5 || row[1].GetString() != sql || row[2].GetInt64() != 0 {
			t.Fatalf("old PRINT shape changed: %v", row)
		}
		var tree map[string]json.RawMessage
		if err := json.Unmarshal([]byte(row[3].GetString()), &tree); err != nil {
			t.Fatal(err)
		}
		if v1 {
			for _, key := range []string{"contractVersion", "statementType", "sqlSha256", "complete", "database", "tables", "actions"} {
				if _, ok := tree[key]; !ok {
					t.Fatalf("missing key %s: %s", key, row[3].GetString())
				}
			}
			if len(tree) != 7 {
				t.Fatalf("unexpected fields: %s", row[3].GetString())
			}
		} else if _, ok := tree["contractVersion"]; ok {
			t.Fatal("legacy PRINT changed")
		}
	}
}
