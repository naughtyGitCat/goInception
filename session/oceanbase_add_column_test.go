package session

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hanchuanchuan/goInception/ast"
	"github.com/hanchuanchuan/goInception/config"
	"github.com/hanchuanchuan/goInception/parser"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const oceanbaseDoubleAddSQL = "ALTER TABLE `TB_COMPETITOR_SOURCE_LIST` " +
	"ADD COLUMN `brand` VARCHAR(128) NOT NULL DEFAULT '' " +
	"COMMENT '品牌（sheet「品牌」列，2026-11-05 新增；三业务表品牌优先来源）' AFTER `series`, " +
	"ADD COLUMN `one_id` VARCHAR(64) NOT NULL DEFAULT '' " +
	"COMMENT '对标严选ONEID（sheet 同名列，2026-11-05 新增，当前全空预留）' AFTER `remark`"

func oceanbaseAddStatement(t *testing.T, sql string) *ast.AlterTableStmt {
	t.Helper()
	node, err := parser.New().ParseOneStmt(sql, "", "")
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	return node.(*ast.AlterTableStmt)
}

func oceanbaseAddSession() (*session, *TableInfo) {
	table := &TableInfo{Schema: "test", Name: "TB_COMPETITOR_SOURCE_LIST", Collation: "utf8mb4_general_ci", Fields: []FieldInfo{
		{Field: "id", Type: "bigint(20)", Key: "PRI"},
		{Field: "series", Type: "varchar(128)"},
		{Field: "remark", Type: "varchar(128)"},
		{Field: "tail", Type: "int(11)"},
	}}
	s := &session{
		dbType: DBTypeOceanBase, dbVersion: 4, obVersion: oceanbaseVersion{4, 5, 0, 0},
		inc:   config.Inc{CheckOfflineDDL: true, EnableIdentiferKeyword: true},
		stage: StageCheck, opt: &SourceOptions{Check: true},
		myRecord: &Record{Buf: new(bytes.Buffer)}, recordSets: &MyRecordSets{},
		tableCacheList: make(map[string]*TableInfo),
	}
	return s, table
}

type oceanbaseVersionTestDriver struct {
	*singleDMLTestDriver
	rows    [][]driver.Value
	queries int
}

func (d *oceanbaseVersionTestDriver) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := d.singleDMLTestDriver.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &oceanbaseVersionTestConn{singleDMLTestConn: conn.(*singleDMLTestConn), state: d}, nil
}

// Only adapt SHOW's two-column rows; reuse the existing in-memory driver.
type oceanbaseVersionTestConn struct {
	*singleDMLTestConn
	state *oceanbaseVersionTestDriver
}

func (c *oceanbaseVersionTestConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.HasPrefix(strings.ToLower(query), "show variables where variable_name in") {
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
	c.state.queries++
	return &oceanbaseVersionTestRows{obCountTestRows: obCountTestRows{rows: c.state.rows}}, nil
}

type oceanbaseVersionTestRows struct{ obCountTestRows }

func (*oceanbaseVersionTestRows) Columns() []string { return []string{"Variable_name", "Value"} }

func TestOceanBaseServerVersionRowOrderAndInitReset(t *testing.T) {
	for _, commentFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("commentFirst=%v", commentFirst), func(t *testing.T) {
			versionRow := []driver.Value{"version", "5.7.25-OceanBase_CE-v4.5.0.0"}
			commentRow := []driver.Value{"version_comment", "OceanBase 4.3.0.0"}
			rows := [][]driver.Value{versionRow, commentRow}
			if commentFirst {
				rows = [][]driver.Value{commentRow, versionRow}
			}
			state := &oceanbaseVersionTestDriver{singleDMLTestDriver: &singleDMLTestDriver{}, rows: rows}
			db := sql.OpenDB(state)
			t.Cleanup(func() { _ = db.Close() })
			gormDB, err := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{
				DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
			})
			if err != nil {
				t.Fatal(err)
			}
			s := NewInception()
			s.db = gormDB
			s.stage = StageCheck
			s.myRecord = &Record{Buf: new(bytes.Buffer)}
			s.recordSets = NewRecordSets()
			s.mysqlServerVersion()
			if state.queries != 1 || s.dbVersion != 4 || s.dbType != DBTypeOceanBase || s.obVersion != (oceanbaseVersion{4, 5, 0, 0}) || s.myRecord.ErrLevel != 0 {
				t.Fatalf("row order altered result: queries=%d type=%d major=%d version=%+v error=%s", state.queries, s.dbType, s.dbVersion, s.obVersion, s.myRecord.Buf)
			}
			s.mysqlServerVersion()
			if state.queries != 1 {
				t.Fatal("same session must reuse version state")
			}
			s.init()
			if s.dbVersion != 0 || s.dbType != DBTypeMysql || s.obVersion != (oceanbaseVersion{}) {
				t.Fatal("init retained prior server version")
			}
			state.rows = [][]driver.Value{{"version_comment", "MySQL Community Server"}, {"version", "5.7.21"}}
			s.mysqlServerVersion()
			if state.queries != 2 || s.dbVersion != 50721 || s.dbType != DBTypeMysql || s.obVersion != (oceanbaseVersion{}) || s.myRecord.ErrLevel != 0 {
				t.Fatalf("init failed to re-probe next target: queries=%d type=%d major=%d version=%+v error=%s", state.queries, s.dbType, s.dbVersion, s.obVersion, s.myRecord.Buf)
			}
		})
	}
}

func TestOceanBaseServerVersion(t *testing.T) {
	cases := []struct {
		version, comment string
		dbType, legacy   int
		full             oceanbaseVersion
	}{
		{"5.7.25-OceanBase_CE-v4.5.0.0", "OceanBase 4.5.0.0", DBTypeOceanBase, 4, oceanbaseVersion{4, 5, 0, 0}},
		{"5.7.25-OceanBase_CE-v4.5.0.0", "OceanBase", DBTypeOceanBase, 4, oceanbaseVersion{4, 5, 0, 0}},
		{"5.7.25-OceanBase_CE-v4.5.0.0", "OceanBase 4.3.0.0", DBTypeOceanBase, 4, oceanbaseVersion{4, 5, 0, 0}},
		{"5.7.25-OceanBase_CE-v4.5.0", "OceanBase", DBTypeOceanBase, 4, oceanbaseVersion{4, 5, 0, 0}},
		{"5.7.25", "OceanBase 4.3.5.1", DBTypeOceanBase, 4, oceanbaseVersion{4, 3, 5, 1}},
		{"5.7.25-OceanBase_CE-v3.2.4.0", "OceanBase", DBTypeOceanBase, 3, oceanbaseVersion{3, 2, 4, 0}},
		{"5.7.25-OceanBase", "OceanBase", DBTypeOceanBase, 0, oceanbaseVersion{}},
		{"5.7.25-OceanBase_CE-v4", "OceanBase 4.", DBTypeOceanBase, 0, oceanbaseVersion{}},
		{"5.7.25-OceanBase_CE-v4.5.0.bad", "OceanBase", DBTypeOceanBase, 0, oceanbaseVersion{}},
		{"5.7.25-OceanBase_CE-v999999999999999999999.5.0.0", "OceanBase", DBTypeOceanBase, 0, oceanbaseVersion{}},
		{"5.7.21", "MySQL Community Server", DBTypeMysql, 50721, oceanbaseVersion{}},
		{"8.0.30", "MySQL Community Server", DBTypeMysql, 80030, oceanbaseVersion{}},
		{"10.3.32-MariaDB", "MariaDB Server", DBTypeMariaDB, 100332, oceanbaseVersion{}},
		{"5.7.25-TiDB-v4.0.0", "TiDB Server", DBTypeTiDB, 50725, oceanbaseVersion{}},
	}
	for _, tc := range cases {
		t.Run(tc.version+"/"+tc.comment, func(t *testing.T) {
			s, _ := oceanbaseAddSession()
			s.setServerVersion(tc.version, tc.comment)
			if s.dbType != tc.dbType || s.dbVersion != tc.legacy || s.obVersion != tc.full {
				t.Fatalf("type=%d legacy=%d full=%+v, want type=%d legacy=%d full=%+v", s.dbType, s.dbVersion, s.obVersion, tc.dbType, tc.legacy, tc.full)
			}
			wantLevel := uint8(0)
			if tc.dbType == DBTypeOceanBase && tc.full.major == 0 {
				wantLevel = 2
			}
			if s.myRecord.ErrLevel != wantLevel {
				t.Errorf("invalid OceanBase version must fail closed: level=%d want=%d message=%s", s.myRecord.ErrLevel, wantLevel, s.myRecord.Buf)
			}
		})
	}
	s, _ := oceanbaseAddSession()
	s.init()
	if s.obVersion != (oceanbaseVersion{}) || s.dbVersion != 0 || s.dbType != DBTypeMysql {
		t.Fatalf("init retained server state: type=%d legacy=%d full=%+v", s.dbType, s.dbVersion, s.obVersion)
	}
}

func TestOceanBaseServerVersionRetainsDropProtection(t *testing.T) {
	for _, version := range []string{"5.7.25-OceanBase_CE-v4.5.0.0", "5.7.25-OceanBase_CE-v4.5.0.bad"} {
		s, table := oceanbaseAddSession()
		s.setServerVersion(version, "OceanBase")
		node := oceanbaseAddStatement(t, "ALTER TABLE t DROP COLUMN series")
		s.checkDropColumn(table, node.Specs[0])
		if s.myRecord.ErrLevel != 2 {
			t.Fatalf("DROP must be blocked by valid OB4 protection or invalid-version error: version=%s level=%d message=%s", version, s.myRecord.ErrLevel, s.myRecord.Buf)
		}
		if s.obVersion.major == 4 && !strings.Contains(s.myRecord.Buf.String(), s.getErrorMessage(ER_CANT_DROP_COLUMN)) {
			t.Fatalf("legacy major-based DROP protection lost: %s", s.myRecord.Buf)
		}
	}
}

func TestOceanBaseAddClassifier(t *testing.T) {
	versions := []oceanbaseVersion{{}, {3, 2, 4, 0}, {4, 2, 4, 0}, {4, 2, 5, 0}, {4, 2, 6, 0}, {4, 3, 0, 0}, {4, 3, 5, 0}, {4, 3, 6, 0}, {4, 5, 0, 0}, {4, 5, 1, 0}, {5, 0, 0, 0}}
	for _, version := range versions {
		for _, suffix := range []string{"", " FIRST", " AFTER series", " BEFORE series"} {
			for _, column := range []string{"brand INT NOT NULL DEFAULT 0", "brand INT AS (id + 1) VIRTUAL", "brand INT AS (id + 1) STORED", "brand BIGINT UNSIGNED NOT NULL AUTO_INCREMENT"} {
				node := oceanbaseAddStatement(t, "ALTER TABLE t ADD COLUMN "+column+suffix)
				spec := node.Specs[0]
				want := oceanbaseAddOnline
				if suffix != "" && (suffix == " BEFORE series" || !version.supportsAddPosition()) {
					want = oceanbaseAddUnknownPosition
				}
				if version.major != 3 {
					if strings.Contains(column, "STORED") {
						want = oceanbaseAddStored
					}
					if strings.Contains(column, "AUTO_INCREMENT") {
						want = oceanbaseAddAutoIncrement
					}
				}
				if got := classifyOceanBaseAddColumn(version, spec.NewColumns[0], spec.Position); got != want {
					t.Errorf("version=%+v SQL=%s class=%d want=%d", version, column+suffix, got, want)
				}
				if suffix == "" && classifyOceanBaseAddColumn(version, spec.NewColumns[0], nil) != want {
					t.Errorf("nil position differs from append for %s", column)
				}
			}
		}
	}
	for _, version := range []oceanbaseVersion{{4, 2, 5, 0}, {4, 3, 5, 0}, {4, 5, 0, 0}} {
		if !version.supportsAddPosition() {
			t.Errorf("documented release line rejected: %+v", version)
		}
	}
	for _, version := range []oceanbaseVersion{{}, {4, 2, 4, 0}, {4, 2, 6, 0}, {4, 3, 0, 0}, {4, 3, 6, 0}, {4, 5, 1, 0}, {5, 0, 0, 0}} {
		if version.supportsAddPosition() {
			t.Errorf("unconfirmed release allowed: %+v", version)
		}
	}
}

func TestOceanBaseAddOfflineGateAndClassification(t *testing.T) {
	cases := []struct {
		sql     string
		version oceanbaseVersion
		message string
		offline bool
	}{
		{"ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' AFTER series", oceanbaseVersion{4, 5, 0, 0}, "", false},
		{"ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' FIRST", oceanbaseVersion{4, 5, 0, 0}, "", false},
		{"ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' AFTER series", oceanbaseVersion{}, "AFTER on OceanBase 0.0.0.0", true},
		{"ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' BEFORE series", oceanbaseVersion{4, 5, 0, 0}, "BEFORE on OceanBase 4.5.0.0", true},
		{"ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' AFTER series", oceanbaseVersion{4, 3, 0, 0}, "AFTER on OceanBase 4.3.0.0", true},
		{"ALTER TABLE t ADD COLUMN generated_value INT AS (id + 1) STORED COMMENT 'stored' AFTER series", oceanbaseVersion{5, 0, 0, 0}, "stored generated column 'generated_value'", true},
		{"ALTER TABLE t ADD COLUMN generated_value INT AS (id + 1) VIRTUAL COMMENT 'virtual'", oceanbaseVersion{4, 5, 0, 0}, "", false},
		{"ALTER TABLE t ADD COLUMN generated_value INT AS (id + 1) STORED COMMENT 'stored'", oceanbaseVersion{3, 2, 4, 0}, "", false},
	}
	for _, tc := range cases {
		for _, enabled := range []bool{false, true} {
			s, table := oceanbaseAddSession()
			s.obVersion, s.dbVersion = tc.version, tc.version.major
			s.inc.CheckOfflineDDL = enabled
			if strings.Contains(tc.sql, "AUTO_INCREMENT") {
				table.Fields = table.Fields[1:]
			}
			node := oceanbaseAddStatement(t, tc.sql)
			if got := s.checkDDLOfflineOperation(node, table); got != tc.offline {
				t.Errorf("gate=%v sql=%s offline=%v want=%v", enabled, tc.sql, got, tc.offline)
			}
			s.checkAddColumn(table, node.Specs[0])
			message := s.myRecord.Buf.String()
			wantLevel := uint8(0)
			if enabled && tc.offline {
				wantLevel = 2
			}
			if s.myRecord.ErrLevel != wantLevel || (wantLevel != 0 && !strings.Contains(message, tc.message)) || strings.Contains(message, "%s") {
				t.Errorf("gate=%v sql=%s level=%d want=%d message=%s", enabled, tc.sql, s.myRecord.ErrLevel, wantLevel, message)
			}
		}
	}
	mixed := oceanbaseAddStatement(t, "ALTER TABLE t ADD COLUMN brand INT AFTER series, ADD COLUMN generated_value INT AS (id + 1) STORED")
	s, table := oceanbaseAddSession()
	if !s.checkDDLOfflineOperation(mixed, table) {
		t.Fatal("mixed Online/Offline ADD must retain offline classification")
	}
}

func TestOceanBaseAddAutoIncrementErrorNamesColumn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s, table := oceanbaseAddSession()
		s.inc.CheckOfflineDDL = enabled
		table.Fields = table.Fields[1:]
		node := oceanbaseAddStatement(t, "ALTER TABLE t ADD COLUMN id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY COMMENT 'id'")
		s.checkAddColumn(table, node.Specs[0])
		message := s.myRecord.Buf.String()
		if s.myRecord.ErrLevel != 2 || strings.Contains(message, "%s") || (enabled && !strings.Contains(message, "AUTO_INCREMENT column 'id'")) || (!enabled && strings.Contains(message, "AUTO_INCREMENT")) {
			t.Errorf("gate=%v: expected named AUTO_INCREMENT protection (or independent PK protection with gate off), level=%d message=%s", enabled, s.myRecord.ErrLevel, message)
		}
	}
}

func TestOceanBaseAddOfflineProtectionAfterPositionWarning(t *testing.T) {
	for _, second := range []string{
		"ADD COLUMN generated_value INT AS (id + 1) STORED COMMENT 'stored'",
		"ADD COLUMN new_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY COMMENT 'id'",
		"ADD COLUMN another INT NOT NULL DEFAULT 0 COMMENT 'another' BEFORE series",
	} {
		s, table := oceanbaseAddSession()
		s.inc.CheckColumnPositionChange = true
		node := oceanbaseAddStatement(t, "ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' AFTER series, "+second)
		s.checkAddColumn(table, node.Specs[0])
		if s.myRecord.ErrLevel != 1 {
			t.Fatalf("expected preceding position warning, got %d: %s", s.myRecord.ErrLevel, s.myRecord.Buf)
		}
		table = s.tableCacheList["test.TB_COMPETITOR_SOURCE_LIST"]
		s.checkAddColumn(table, node.Specs[1])
		message := s.myRecord.Buf.String()
		want := "stored generated column 'generated_value'"
		if strings.Contains(second, "AUTO_INCREMENT") {
			want = "AUTO_INCREMENT column 'new_id'"
		}
		if strings.Contains(second, "BEFORE") {
			want = "BEFORE on OceanBase 4.5.0.0"
		}
		if s.myRecord.ErrLevel != 2 || !strings.Contains(message, want) {
			t.Errorf("preceding warning hid offline protection for %s: level=%d message=%s", second, s.myRecord.ErrLevel, message)
		}
	}
}

func TestOceanBaseDoubleAddAST(t *testing.T) {
	node := oceanbaseAddStatement(t, oceanbaseDoubleAddSQL)
	if len(node.Specs) != 2 {
		t.Fatalf("expected two ADD specs, got %d", len(node.Specs))
	}
	for i, spec := range node.Specs {
		if spec.Tp != ast.AlterTableAddColumns || len(spec.NewColumns) != 1 || spec.Position.Tp != ast.ColumnPositionAfter {
			t.Fatalf("spec %d is not an ordinary ADD AFTER: %#v", i, spec)
		}
		for _, option := range spec.NewColumns[0].Options {
			if option.Tp == ast.ColumnOptionGenerated {
				t.Fatalf("ordinary column %s unexpectedly has generated option", spec.NewColumns[0].Name.Name)
			}
		}
	}
}

func TestOceanBaseDoubleAddAudit(t *testing.T) {
	s, table := oceanbaseAddSession()
	for _, spec := range oceanbaseAddStatement(t, oceanbaseDoubleAddSQL).Specs {
		s.checkAddColumn(table, spec)
		table = s.tableCacheList["test.TB_COMPETITOR_SOURCE_LIST"]
	}
	if s.myRecord.ErrLevel != 0 {
		t.Fatalf("ordinary ADD AFTER rejected at level %d: %s", s.myRecord.ErrLevel, s.myRecord.Buf)
	}
	var fields []string
	for _, field := range table.Fields {
		fields = append(fields, field.Field)
	}
	want := []string{"id", "series", "brand", "remark", "one_id", "tail"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("snapshot columns=%v, want %v", fields, want)
	}
}

func TestOceanBaseAddCacheDependenciesAndSameAnchor(t *testing.T) {
	cases := []struct {
		name, sql string
		want      []string
	}{
		{"after_new_column", "ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' AFTER series, ADD COLUMN one_id INT NOT NULL DEFAULT 0 COMMENT 'one_id' AFTER brand", []string{"id", "series", "brand", "one_id", "remark", "tail"}},
		{"same_anchor", "ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' AFTER series, ADD COLUMN one_id INT NOT NULL DEFAULT 0 COMMENT 'one_id' AFTER series", []string{"id", "series", "one_id", "brand", "remark", "tail"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, table := oceanbaseAddSession()
			for _, spec := range oceanbaseAddStatement(t, tc.sql).Specs {
				s.checkAddColumn(table, spec)
				table = s.tableCacheList["test.TB_COMPETITOR_SOURCE_LIST"]
			}
			var fields []string
			for _, field := range table.Fields {
				fields = append(fields, field.Field)
			}
			if s.myRecord.ErrLevel != 0 || !reflect.DeepEqual(fields, tc.want) {
				t.Fatalf("cache order=%v want=%v level=%d error=%s", fields, tc.want, s.myRecord.ErrLevel, s.myRecord.Buf)
			}
		})
	}
}

func TestOceanBaseAddDuplicateStillRejected(t *testing.T) {
	for _, duplicateNew := range []bool{false, true} {
		t.Run(fmt.Sprintf("duplicateNew=%v", duplicateNew), func(t *testing.T) {
			s, table := oceanbaseAddSession()
			sql := "ALTER TABLE t ADD COLUMN series INT NOT NULL DEFAULT 0 COMMENT 'duplicate' AFTER remark"
			if duplicateNew {
				sql = "ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' AFTER series, ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'duplicate' AFTER remark"
			}
			for _, spec := range oceanbaseAddStatement(t, sql).Specs {
				s.checkAddColumn(table, spec)
				if cached := s.tableCacheList["test.TB_COMPETITOR_SOURCE_LIST"]; cached != nil {
					table = cached
				}
			}
			column := "series"
			if duplicateNew {
				column = "brand"
			}
			want := fmt.Sprintf(s.getErrorMessage(ER_COLUMN_EXISTED), "TB_COMPETITOR_SOURCE_LIST."+column)
			if s.myRecord.ErrLevel != 2 || !strings.Contains(s.myRecord.Buf.String(), want) || strings.Contains(s.myRecord.Buf.String(), "stored generated") {
				t.Fatalf("duplicate validation lost: level=%d message=%s", s.myRecord.ErrLevel, s.myRecord.Buf)
			}
		})
	}
}

func TestOceanBaseAddIgnoreWarningsRetainsOfflineErrors(t *testing.T) {
	for _, second := range []string{
		"ADD COLUMN generated_value INT AS (id + 1) STORED COMMENT 'stored'",
		"ADD COLUMN new_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY COMMENT 'id'",
	} {
		t.Run(second, func(t *testing.T) {
			s, table := oceanbaseAddSession()
			s.inc.CheckColumnPositionChange = true
			s.opt.IgnoreWarnings = true
			node := oceanbaseAddStatement(t, "ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' AFTER series, "+second)
			s.checkAddColumn(table, node.Specs[0])
			if s.myRecord.ErrLevel != 1 || s.hasError() {
				t.Fatalf("position warning must be ignorable: level=%d error=%s", s.myRecord.ErrLevel, s.myRecord.Buf)
			}
			table = s.tableCacheList["test.TB_COMPETITOR_SOURCE_LIST"]
			s.checkAddColumn(table, node.Specs[1])
			want := "stored generated column 'generated_value'"
			if strings.Contains(second, "AUTO_INCREMENT") {
				want = "AUTO_INCREMENT column 'new_id'"
			}
			if s.myRecord.ErrLevel != 2 || !s.hasError() || !s.hasErrorBefore() || !strings.Contains(s.myRecord.Buf.String(), want) {
				t.Fatalf("ignore warnings must not hide offline error: level=%d error=%s", s.myRecord.ErrLevel, s.myRecord.Buf)
			}
		})
	}
}

func TestOceanBaseAddStoredErrorNamesColumn(t *testing.T) {
	s, table := oceanbaseAddSession()
	node := oceanbaseAddStatement(t, "ALTER TABLE t ADD COLUMN generated_value INT GENERATED ALWAYS AS (id + 1) STORED COMMENT 'generated'")
	s.checkAddColumn(table, node.Specs[0])
	message := s.myRecord.Buf.String()
	if s.myRecord.ErrLevel != 2 || !strings.Contains(message, "stored generated column 'generated_value'") || strings.Contains(message, "%s") {
		t.Fatalf("expected named level-2 STORED error, got level %d: %s", s.myRecord.ErrLevel, message)
	}
}

func TestOceanBaseAddPositionPolicy(t *testing.T) {
	for _, dbType := range []int{DBTypeMysql, DBTypeOceanBase} {
		for _, enabled := range []bool{false, true} {
			s, table := oceanbaseAddSession()
			s.dbType = dbType
			s.inc.CheckOfflineDDL = false
			s.inc.CheckColumnPositionChange = enabled
			node := oceanbaseAddStatement(t, "ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' AFTER series")
			s.checkAddColumn(table, node.Specs[0])
			wantLevel := uint8(0)
			if enabled {
				wantLevel = 1
			}
			message := s.myRecord.Buf.String()
			if s.myRecord.ErrLevel != wantLevel || strings.Contains(message, "%s") || strings.Contains(message, "stored generated") || (enabled && !strings.Contains(message, "TB_COMPETITOR_SOURCE_LIST.brand")) {
				t.Errorf("db=%d enabled=%v: level=%d message=%s", dbType, enabled, s.myRecord.ErrLevel, message)
			}
		}
	}
}

func TestOceanBaseAddMissingAfterColumn(t *testing.T) {
	s, table := oceanbaseAddSession()
	node := oceanbaseAddStatement(t, "ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0 COMMENT 'brand' AFTER absent_column")
	s.checkAddColumn(table, node.Specs[0])
	message := s.myRecord.Buf.String()
	if s.myRecord.ErrLevel != 2 || !strings.Contains(message, "TB_COMPETITOR_SOURCE_LIST.absent_column") || strings.Contains(message, "stored generated") {
		t.Fatalf("missing AFTER target must retain validation: level=%d message=%s", s.myRecord.ErrLevel, message)
	}
}

func TestOceanBaseExistingModifyChangePositionProtection(t *testing.T) {
	for _, sql := range []string{
		"ALTER TABLE t MODIFY COLUMN series VARCHAR(128) NOT NULL DEFAULT '' COMMENT 'series' AFTER remark",
		"ALTER TABLE t CHANGE COLUMN series series VARCHAR(128) NOT NULL DEFAULT '' COMMENT 'series' AFTER remark",
	} {
		s, table := oceanbaseAddSession()
		s.inc.CheckColumnPositionChange = true
		node := oceanbaseAddStatement(t, sql)
		spec := node.Specs[0]
		if spec.Tp == ast.AlterTableModifyColumn && !s.checkDDLOfflineOperation(node, table) {
			t.Fatalf("existing MODIFY reordering must remain offline: %s", sql)
		}
		if spec.Tp == ast.AlterTableChangeColumn {
			s.checkChangeColumn(table, spec)
		} else {
			s.checkModifyColumn(table, spec)
		}
		message := s.myRecord.Buf.String()
		if !strings.Contains(message, "Cannot change the position of the column 'TB_COMPETITOR_SOURCE_LIST.series'") || s.myRecord.ErrLevel < 1 {
			t.Fatalf("existing position policy lost for %s: level=%d message=%s", sql, s.myRecord.ErrLevel, message)
		}
	}
}

func TestOceanBaseAddVirtualClassification(t *testing.T) {
	s, table := oceanbaseAddSession()
	node := oceanbaseAddStatement(t, "ALTER TABLE t ADD COLUMN generated_value INT GENERATED ALWAYS AS (id + 1) VIRTUAL")
	if s.checkDDLOfflineOperation(node, table) {
		t.Fatal("VIRTUAL append must not be classified as STORED/offline")
	}
}
