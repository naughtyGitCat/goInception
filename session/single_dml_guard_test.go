package session

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/hanchuanchuan/goInception/parser"
)

func singleDMLGuardOptions() *SourceOptions {
	return &SourceOptions{SingleDML: true, Execute: true, WaitTimeout: 300}
}

func singleDMLGuardRecord(t *testing.T, sql string) *Record {
	t.Helper()
	node, err := parser.New().ParseOneStmt(sql, "", "")
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	return &Record{
		Sql:   sql,
		Type:  node,
		Stage: StageCheck,
		Buf:   new(bytes.Buffer),
		TableInfo: &TableInfo{
			Schema: "shop",
			Name:   "t",
			Fields: []FieldInfo{
				{Field: "id", Key: "PRI"},
				{Field: "v"},
			},
		},
	}
}

func TestSingleDMLOptions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SourceOptions)
		dbType int
		valid  bool
	}{
		{"execute", func(*SourceOptions) {}, DBTypeOceanBase, true},
		{"check_only", func(o *SourceOptions) { o.Check, o.Execute = true, false }, DBTypeOceanBase, true},
		{"minimum_timeout", func(o *SourceOptions) { o.WaitTimeout = 1 }, DBTypeOceanBase, true},
		{"maximum_timeout", func(o *SourceOptions) { o.WaitTimeout = 3600 }, DBTypeOceanBase, true},
		{"no_operation", func(o *SourceOptions) { o.Execute = false }, DBTypeOceanBase, false},
		{"backup", func(o *SourceOptions) { o.Backup = true }, DBTypeOceanBase, false},
		{"transaction_one", func(o *SourceOptions) { o.tranBatch = 1 }, DBTypeOceanBase, false},
		{"transaction_batch", func(o *SourceOptions) { o.tranBatch = 50 }, DBTypeOceanBase, false},
		{"negative_transaction", func(o *SourceOptions) { o.tranBatch = -1 }, DBTypeOceanBase, false},
		{"missing_request_timeout", func(o *SourceOptions) { o.WaitTimeout = 0 }, DBTypeOceanBase, false},
		{"negative_timeout", func(o *SourceOptions) { o.WaitTimeout = -1 }, DBTypeOceanBase, false},
		{"timeout_too_large", func(o *SourceOptions) { o.WaitTimeout = 3601 }, DBTypeOceanBase, false},
		{"print", func(o *SourceOptions) { o.Print = true }, DBTypeOceanBase, false},
		{"masking", func(o *SourceOptions) { o.Masking = true }, DBTypeOceanBase, false},
		{"split", func(o *SourceOptions) { o.split = true }, DBTypeOceanBase, false},
		{"mysql", func(*SourceOptions) {}, DBTypeMysql, false},
		{"mariadb", func(*SourceOptions) {}, DBTypeMariaDB, false},
		{"tidb", func(*SourceOptions) {}, DBTypeTiDB, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opt := singleDMLGuardOptions()
			tc.mutate(opt)
			err := validateSingleDMLOptions(opt, tc.dbType)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, got error %v", tc.valid, err)
			}
		})
	}
}

func TestSingleDMLOptions_DisabledModeKeepsLegacySettings(t *testing.T) {
	for _, dbType := range []int{DBTypeMysql, DBTypeMariaDB, DBTypeTiDB, DBTypeOceanBase} {
		t.Run(fmt.Sprint(dbType), func(t *testing.T) {
			opt := &SourceOptions{
				Check: true, Backup: true, WaitTimeout: int(^uint(0) >> 1),
				Print: true, Masking: true, split: true, tranBatch: 50,
			}
			if err := validateSingleDMLOptions(opt, dbType); err != nil {
				t.Fatalf("disabled mode must not impose new restrictions: %v", err)
			}
		})
	}
}

func TestSingleDMLStatementShapes(t *testing.T) {
	cases := []struct {
		name  string
		sql   string
		valid bool
	}{
		{"use", "USE shop", true},
		{"update", "UPDATE t SET v = v + 1 WHERE id = 1", true},
		{"update_schema_alias", "UPDATE shop.t AS item SET item.v = item.v + 1 WHERE item.id BETWEEN 1 AND 50000", true},
		{"slow_update", "UPDATE t SET v = v + SLEEP(12) + 1 WHERE id = 1", true},
		{"delete", "DELETE FROM t WHERE id BETWEEN 1 AND 50000", true},
		{"delete_schema", "DELETE FROM shop.t WHERE v = 0", true},
		{"insert_values", "INSERT INTO t(id, v) VALUES (1, 2), (2, 3)", true},
		{"update_no_where", "UPDATE t SET v = 1", false},
		{"delete_no_where", "DELETE FROM t", false},
		{"update_join", "UPDATE t JOIN u ON t.id = u.id SET t.v = 1 WHERE t.id > 0", false},
		{"update_multiple_tables", "UPDATE t, u SET t.v = 1 WHERE t.id = u.id", false},
		{"delete_multiple_tables", "DELETE t FROM t JOIN u ON t.id = u.id WHERE t.id > 0", false},
		{"update_limit", "UPDATE t SET v = 1 WHERE id > 0 LIMIT 1", false},
		{"update_order", "UPDATE t SET v = 1 WHERE id > 0 ORDER BY id", false},
		{"delete_limit", "DELETE FROM t WHERE id > 0 LIMIT 1", false},
		{"delete_order", "DELETE FROM t WHERE id > 0 ORDER BY id", false},
		{"update_cte", "WITH c AS (SELECT id FROM u) UPDATE t SET v = 1 WHERE id IN (SELECT id FROM c)", false},
		{"delete_cte", "WITH c AS (SELECT id FROM u) DELETE FROM t WHERE id IN (SELECT id FROM c)", false},
		{"insert_select", "INSERT INTO t(id, v) SELECT id, v FROM u", false},
		{"insert_set", "INSERT INTO t SET id = 1, v = 2", false},
		{"replace", "REPLACE INTO t(id, v) VALUES (1, 2)", false},
		{"ddl", "CREATE TABLE t(id INT PRIMARY KEY)", false},
		{"grant", "GRANT SELECT ON shop.* TO 'probe'@'%'", false},
		{"session_set", "SET SESSION ob_query_timeout = 300000000", false},
		{"show", "SHOW TABLES", false},
		{"select", "SELECT 1", false},
		{"begin", "BEGIN", false},
		{"commit", "COMMIT", false},
		{"rollback", "ROLLBACK", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := singleDMLGuardRecord(t, tc.sql)
			err := validateSingleDMLStatement(record.Type)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, got error %v for %q", tc.valid, err, tc.sql)
			}
		})
	}
}

func TestSingleDMLStatement_NilRejected(t *testing.T) {
	if err := validateSingleDMLStatement(nil); err == nil {
		t.Fatal("nil AST must be rejected")
	}
}

func TestSingleDMLRecords_OneMutation(t *testing.T) {
	for _, sql := range []string{
		"UPDATE t SET v = v + 1 WHERE id = 1",
		"DELETE FROM t WHERE id = 1",
		"INSERT INTO t(id, v) VALUES (1, 2)",
	} {
		t.Run(sql, func(t *testing.T) {
			for _, withUse := range []bool{false, true} {
				mutation := singleDMLGuardRecord(t, sql)
				records := []*Record{mutation}
				if withUse {
					records = append([]*Record{singleDMLGuardRecord(t, "USE shop")}, records...)
				}
				got, err := validateSingleDMLRecords(records)
				if err != nil || got != mutation {
					t.Fatalf("withUse=%v: expected exact mutation record, got %p, error %v", withUse, got, err)
				}
				if mutation.Stage != StageCheck || mutation.ExecComplete || mutation.AffectedRows != 0 {
					t.Fatal("validation must not mark the zero-row CHECK record as executed")
				}
			}
		})
	}
}

func TestSingleDMLRecords_CountProbeBeforeMetadata(t *testing.T) {
	cases := []struct {
		name string
		sqls []string
	}{
		{"empty", nil},
		{"use_only", []string{"USE shop"}},
		{"two_updates", []string{"UPDATE t SET v = 1 WHERE id = 1", "UPDATE t SET v = 2 WHERE id = 2"}},
		{"mixed_mutations", []string{"USE shop", "DELETE FROM t WHERE id = 1", "INSERT INTO t(id, v) VALUES (1, 2)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var records []*Record
			for _, sql := range tc.sqls {
				r := singleDMLGuardRecord(t, sql)
				r.TableInfo = nil
				r.ErrLevel = 2
				r.ErrorMessage = "table metadata unavailable"
				records = append(records, r)
			}
			_, err := validateSingleDMLRecords(records)
			if err == nil || !strings.Contains(err.Error(), "single_dml_requires_one_mutation") {
				t.Fatalf("missing stable count-probe rejection before metadata checks: %v", err)
			}
			for _, r := range records {
				if r.ErrLevel != 2 || r.Stage != StageCheck || r.ExecComplete {
					t.Fatal("count validation must not clear audit failure or simulate execution")
				}
			}
		})
	}
}

func TestSingleDMLRecords_RejectsExtraStatements(t *testing.T) {
	cases := []struct {
		name string
		sqls []string
	}{
		{"use_after_mutation", []string{"UPDATE t SET v = 1 WHERE id = 1", "USE shop"}},
		{"multiple_use", []string{"USE shop", "USE other", "UPDATE t SET v = 1 WHERE id = 1"}},
		{"set_before_mutation", []string{"SET SESSION ob_query_timeout = 300000000", "UPDATE t SET v = 1 WHERE id = 1"}},
		{"ddl_after_mutation", []string{"UPDATE t SET v = 1 WHERE id = 1", "CREATE TABLE u(id INT PRIMARY KEY)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var records []*Record
			for _, sql := range tc.sqls {
				records = append(records, singleDMLGuardRecord(t, sql))
			}
			if _, err := validateSingleDMLRecords(records); err == nil {
				t.Fatal("extra or misplaced statement must be rejected")
			}
		})
	}
}

func TestSingleDMLRecords_RejectsMalformedRecord(t *testing.T) {
	mutation := singleDMLGuardRecord(t, "UPDATE t SET v = 1 WHERE id = 1")
	for _, bad := range []*Record{nil, {Sql: "unparsed SQL", Stage: StageCheck}} {
		if _, err := validateSingleDMLRecords([]*Record{bad, mutation}); err == nil {
			t.Fatal("nil record or missing AST must fail closed")
		}
	}
}

func TestSingleDMLRecords_UpdatePrimaryKeyGuard(t *testing.T) {
	cases := []struct {
		name   string
		sql    string
		fields []FieldInfo
		valid  bool
	}{
		{"value_update", "UPDATE t SET v = v + 1 WHERE id = 1", []FieldInfo{{Field: "id", Key: "PRI"}, {Field: "v"}}, true},
		{"pk_update", "UPDATE t SET id = id + 1 WHERE id = 1", []FieldInfo{{Field: "id", Key: "PRI"}, {Field: "v"}}, false},
		{"pk_case_alias", "UPDATE t AS item SET item.ID = item.id + 1 WHERE item.id = 1", []FieldInfo{{Field: "id", Key: "PRI"}, {Field: "v"}}, false},
		{"composite_pk_second_column", "UPDATE t SET tenant_id = tenant_id + 1 WHERE id = 1", []FieldInfo{{Field: "id", Key: "PRI"}, {Field: "tenant_id", Key: "PRI"}, {Field: "v"}}, false},
		{"later_pk_assignment", "UPDATE t SET v = 2, id = id + 1 WHERE id = 1", []FieldInfo{{Field: "id", Key: "PRI"}, {Field: "v"}}, false},
		{"no_pk_value_update", "UPDATE t SET v = 2 WHERE id = 1", []FieldInfo{{Field: "id"}, {Field: "v"}}, true},
		{"missing_fields", "UPDATE t SET v = 1 WHERE id = 1", nil, false},
		{"empty_fields", "UPDATE t SET v = 1 WHERE id = 1", []FieldInfo{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutation := singleDMLGuardRecord(t, tc.sql)
			mutation.TableInfo.Fields = tc.fields
			_, err := validateSingleDMLRecords([]*Record{mutation})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, got error %v", tc.valid, err)
			}
		})
	}
	t.Run("missing_table", func(t *testing.T) {
		mutation := singleDMLGuardRecord(t, "UPDATE t SET v = 1 WHERE id = 1")
		mutation.TableInfo = nil
		if _, err := validateSingleDMLRecords([]*Record{mutation}); err == nil {
			t.Fatal("UPDATE needs metadata to rule out primary-key assignments")
		}
	})
}

func TestSingleDMLRecords_RowEstimateBoundaries(t *testing.T) {
	for _, estimate := range []int64{-1, 79999, 80000, 80001} {
		t.Run(fmt.Sprint(estimate), func(t *testing.T) {
			mutation := singleDMLGuardRecord(t, "UPDATE t SET v = 1 WHERE id = 1")
			mutation.AffectedRows = estimate
			_, err := validateSingleDMLRecords([]*Record{mutation})
			valid := estimate >= 0 && estimate <= 80000
			if (err == nil) != valid {
				t.Fatalf("estimate=%d valid=%v error=%v", estimate, valid, err)
			}
			if mutation.Stage != StageCheck || mutation.ExecComplete || mutation.AffectedRows != estimate {
				t.Fatal("row validation must not mark execution or rewrite estimates")
			}
		})
	}
}

func TestSingleDMLRecords_PreservesAuditFailure(t *testing.T) {
	use := singleDMLGuardRecord(t, "USE shop")
	mutation := singleDMLGuardRecord(t, "UPDATE t SET v = 1 WHERE id = 1")
	use.ErrLevel, use.ErrorMessage = 2, "database preparation failed"
	mutation.ErrLevel, mutation.ErrorMessage, mutation.AffectedRows = 2, "audit rejected", 123456
	got, err := validateSingleDMLRecords([]*Record{use, mutation})
	if err == nil && got != mutation {
		t.Fatal("successful validation must return the original mutation record")
	}
	if use.ErrLevel != 2 || use.ErrorMessage != "database preparation failed" ||
		mutation.ErrLevel != 2 || mutation.ErrorMessage != "audit rejected" ||
		mutation.AffectedRows != 123456 || mutation.Stage != StageCheck || mutation.ExecComplete {
		t.Fatal("validation must preserve native audit errors and estimates without confirming mutation")
	}
}

func TestSingleDMLTimeoutSQL(t *testing.T) {
	for _, seconds := range []int{1, 15, 300, 3600} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			got, err := singleDMLTimeoutSQL(seconds)
			if err != nil {
				t.Fatalf("valid timeout rejected: %v", err)
			}
			microseconds := int64(seconds) * 1000000
			want := fmt.Sprintf("set session wait_timeout=%d, ob_query_timeout=%d, ob_trx_timeout=%d;", seconds, microseconds, microseconds)
			if strings.ToLower(strings.TrimSpace(got)) != want {
				t.Fatalf("incorrect timeout or microsecond conversion: got %q, want %q", got, want)
			}
		})
	}
}

func TestSingleDMLTimeoutSQL_Invalid(t *testing.T) {
	for _, seconds := range []int{-1, 0, 3601, int(^uint(0) >> 1)} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			got, err := singleDMLTimeoutSQL(seconds)
			if err == nil || got != "" {
				t.Fatalf("invalid timeout must produce no executable SQL: got %q, error %v", got, err)
			}
		})
	}
}
