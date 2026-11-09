package session

import (
	"strings"
	"testing"

	"github.com/hanchuanchuan/goInception/ast"
)

func TestOceanBaseAlterOnlineClassification(t *testing.T) {
	cases := []struct {
		sql     string
		version oceanbaseVersion
		online  bool
	}{
		{"ALTER TABLE t ADD COLUMN brand INT NOT NULL DEFAULT 0", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t ADD COLUMN brand INT AFTER series", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t ADD COLUMN brand INT AFTER series", oceanbaseVersion{4, 3, 0, 0}, false},
		{"ALTER TABLE t ADD COLUMN brand INT BEFORE series", oceanbaseVersion{4, 5, 0, 0}, false},
		{"ALTER TABLE t ADD COLUMN seq BIGINT AUTO_INCREMENT", oceanbaseVersion{3, 2, 4, 0}, true},
		{"ALTER TABLE t ADD COLUMN seq BIGINT AUTO_INCREMENT", oceanbaseVersion{4, 5, 0, 0}, false},
		{"ALTER TABLE t DROP COLUMN brand", oceanbaseVersion{4, 5, 0, 0}, false},
		{"ALTER TABLE t RENAME COLUMN series TO series_new", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t ADD INDEX idx_tail(tail)", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t ADD UNIQUE KEY uk_tail(tail)", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t ADD PRIMARY KEY(tail)", oceanbaseVersion{4, 5, 0, 0}, false},
		{"ALTER TABLE t DROP INDEX idx_tail", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t RENAME INDEX idx_tail TO idx_tail2", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t MODIFY COLUMN tail BIGINT", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t MODIFY COLUMN id INT", oceanbaseVersion{4, 5, 0, 0}, false},
		{"ALTER TABLE t MODIFY COLUMN tail BIGINT UNSIGNED", oceanbaseVersion{4, 5, 0, 0}, false},
		{"ALTER TABLE t MODIFY COLUMN series VARCHAR(255)", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t MODIFY COLUMN series VARCHAR(64)", oceanbaseVersion{4, 5, 0, 0}, false},
		{"ALTER TABLE t MODIFY COLUMN series TEXT", oceanbaseVersion{4, 5, 0, 0}, false},
		{"ALTER TABLE t MODIFY COLUMN series VARCHAR(255) AFTER remark", oceanbaseVersion{4, 5, 0, 0}, false},
		{"ALTER TABLE t CHANGE COLUMN series series VARCHAR(255)", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t CHANGE COLUMN series series VARCHAR(64)", oceanbaseVersion{4, 5, 0, 0}, false},
		{"ALTER TABLE t AUTO_INCREMENT = 100", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t COMMENT = 'x'", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t CONVERT TO CHARACTER SET utf8mb4", oceanbaseVersion{4, 5, 0, 0}, false},
		{"ALTER TABLE t MODIFY COLUMN tail BIGINT, ALGORITHM=INPLACE, LOCK=NONE", oceanbaseVersion{4, 5, 0, 0}, true},
		{"ALTER TABLE t MODIFY COLUMN series VARCHAR(255), DROP COLUMN tail", oceanbaseVersion{4, 5, 0, 0}, false},
	}
	for _, tc := range cases {
		s, table := oceanbaseAddSession()
		s.obVersion, s.dbVersion = tc.version, tc.version.major
		node := oceanbaseAddStatement(t, tc.sql)
		if got := s.isOBAlterOnline(node, table); got != tc.online {
			t.Errorf("version=%+v sql=%s online=%v want=%v", tc.version, tc.sql, got, tc.online)
		}
	}
	s, table := oceanbaseAddSession()
	if s.isOBAlterOnline(nil, table) {
		t.Fatal("nil ALTER must be offline")
	}
}

func TestOceanBaseAlterOnlineTypeChange(t *testing.T) {
	table := &TableInfo{Schema: "test", Name: "t", Collation: "utf8mb4_general_ci", Fields: []FieldInfo{
		{Field: "id", Type: "int(11)"},
		{Field: "u", Type: "int(11) unsigned"},
		{Field: "s", Type: "varchar(128)", Collation: "utf8mb4_general_ci"},
		{Field: "s255", Type: "varchar(255)"},
		{Field: "body", Type: "text"},
		{Field: "big", Type: "mediumtext"},
		{Field: "tiny", Type: "tinytext"},
		{Field: "amount", Type: "decimal(10,2)"},
		{Field: "e", Type: "enum('a','b')"},
	}}
	cases := []struct {
		sql    string
		online bool
	}{
		{"ALTER TABLE t MODIFY COLUMN id BIGINT", true},
		{"ALTER TABLE t MODIFY COLUMN u BIGINT UNSIGNED", true},
		{"ALTER TABLE t MODIFY COLUMN id BIGINT UNSIGNED", false},
		{"ALTER TABLE t MODIFY COLUMN id INT(10)", true},
		{"ALTER TABLE t MODIFY COLUMN s VARCHAR(255)", true},
		{"ALTER TABLE t MODIFY COLUMN s VARCHAR(64)", false},
		{"ALTER TABLE t MODIFY COLUMN body MEDIUMTEXT", true},
		{"ALTER TABLE t MODIFY COLUMN big TEXT", false},
		{"ALTER TABLE t MODIFY COLUMN body BLOB", false},
		{"ALTER TABLE t MODIFY COLUMN s TINYTEXT", true},
		{"ALTER TABLE t MODIFY COLUMN s255 TINYTEXT", true},
		{"ALTER TABLE t MODIFY COLUMN body VARCHAR(255)", false},
		{"ALTER TABLE t MODIFY COLUMN body VARCHAR(100)", false},
		{"ALTER TABLE t MODIFY COLUMN tiny VARCHAR(255)", true},
		{"ALTER TABLE t MODIFY COLUMN tiny VARCHAR(100)", false},
		{"ALTER TABLE t MODIFY COLUMN amount DECIMAL(12,2)", true},
		{"ALTER TABLE t MODIFY COLUMN amount DECIMAL(12,0)", false},
		{"ALTER TABLE t MODIFY COLUMN e ENUM('a','b')", true},
		{"ALTER TABLE t MODIFY COLUMN e ENUM('a','c')", false},
		{"ALTER TABLE t MODIFY COLUMN s VARCHAR(255) COLLATE utf8mb4_bin", false},
		{"ALTER TABLE t MODIFY COLUMN s VARCHAR(255) NOT NULL DEFAULT '' COMMENT 's'", true},
		{"ALTER TABLE t MODIFY COLUMN s VARCHAR(255) AUTO_INCREMENT", false},
	}
	for _, tc := range cases {
		s, _ := oceanbaseAddSession()
		node := oceanbaseAddStatement(t, tc.sql)
		if got := s.isOBAlterOnline(node, table); got != tc.online {
			t.Errorf("sql=%s online=%v want=%v", tc.sql, got, tc.online)
		}
	}
}

func TestOfflineDDLLegacyCheckSeverity(t *testing.T) {
	newSession := func(dbType int, threshold, size uint) (*session, *TableInfo) {
		s, table := oceanbaseAddSession()
		s.dbType = dbType
		s.inc.CheckColumnTypeChange = true
		s.inc.EnableNullable = true
		// 与原生执行路径一致: 忽略前置的类型转换告警, 才能走到后续级别判定
		s.opt.IgnoreWarnings = true
		s.inc.CheckOfflineDDLLargeTableSize = threshold
		table.TableSize = size
		return s, table
	}
	cases := []struct {
		name      string
		dbType    int
		sql       string
		threshold uint
		size      uint
		level     uint8
		expect    string
	}{
		{"drop column small warning", DBTypeOceanBase, "ALTER TABLE t DROP COLUMN tail", 100, 1, 1, "Can't drop column"},
		{"drop column large error", DBTypeOceanBase, "ALTER TABLE t DROP COLUMN tail", 100, 100, 2, "Can't drop column"},
		{"drop column threshold unset", DBTypeOceanBase, "ALTER TABLE t DROP COLUMN tail", 0, 1, 2, "Can't drop column"},
		{"narrowing small warning", DBTypeOceanBase, "ALTER TABLE t MODIFY COLUMN series VARCHAR(64)", 100, 1, 1, "varchar(128) -> varchar(64)"},
		{"narrowing large error", DBTypeOceanBase, "ALTER TABLE t MODIFY COLUMN series VARCHAR(64)", 100, 100, 2, "varchar(128) -> varchar(64)"},
		{"signedness small warning", DBTypeOceanBase, "ALTER TABLE t MODIFY COLUMN tail BIGINT UNSIGNED", 100, 1, 1, "Cannot change column type"},
		{"signedness large error", DBTypeOceanBase, "ALTER TABLE t MODIFY COLUMN tail BIGINT UNSIGNED", 100, 100, 2, "Cannot change column type"},
		// 其他数据库不受门槛影响, 保持上游的告警级别。
		{"mysql narrowing unchanged", DBTypeMysql, "ALTER TABLE t MODIFY COLUMN series VARCHAR(64)", 100, 100, 1, "varchar(128) -> varchar(64)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, table := newSession(tc.dbType, tc.threshold, tc.size)
			table.TableSizeKnown = true
			offlineTableDB(t, s, &offlineTableTestDriver{probeRows: probeTestRows(1)})
			node := oceanbaseAddStatement(t, tc.sql)
			s.checkDDLOffline(node, table)
			spec := node.Specs[0]
			switch spec.Tp {
			case ast.AlterTableDropColumn:
				s.checkDropColumn(table, spec)
			default:
				s.checkModifyColumn(table, spec)
			}
			if s.myRecord.ErrLevel != tc.level {
				t.Fatalf("level=%d want=%d message=%s", s.myRecord.ErrLevel, tc.level, s.myRecord.Buf)
			}
			if !strings.Contains(s.myRecord.Buf.String(), tc.expect) {
				t.Fatalf("missing %q: %s", tc.expect, s.myRecord.Buf)
			}
		})
	}
}

func TestOceanBaseDecimalCapacityGrowOnly(t *testing.T) {
	for _, tc := range []struct {
		oldType, newType string
		online           bool
	}{
		{"decimal(10,2)", "decimal(10,1)", false},
		{"decimal(10,2)", "decimal(12,1)", false},
		{"decimal(10,2)", "decimal(10,3)", false},
		{"decimal(10,2)", "decimal(11,4)", false},
		{"decimal(10,2)", "decimal(12,3)", true},
		{"decimal(10,2)", "decimal(10,2)", true},
		{"decimal", "decimal(10,0)", true},
		{"decimal(10)", "decimal(10,0)", true},
		{"decimal(10,2)", "decimal(12)", false},
	} {
		if got := obTypeLengthGrowOnly(tc.oldType, tc.newType); got != tc.online {
			t.Errorf("%s -> %s growOnly=%v want=%v", tc.oldType, tc.newType, got, tc.online)
		}
	}
}

func TestOceanBaseDecimalNarrowingSeverity(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		size       uint
		mysql      bool
		checkTypes bool
		level      uint8
	}{
		{"scale small", "MODIFY COLUMN amount DECIMAL(10,1)", 99, false, true, 1},
		{"scale at threshold", "MODIFY COLUMN amount DECIMAL(10,1)", 100, false, true, 2},
		{"scale above threshold", "MODIFY COLUMN amount DECIMAL(10,1)", 101, false, true, 2},
		{"precision grows scale shrinks", "MODIFY COLUMN amount DECIMAL(12,1)", 100, false, true, 2},
		{"integer capacity shrinks", "MODIFY COLUMN amount DECIMAL(10,3)", 100, false, true, 2},
		{"precision and scale grow integer capacity shrinks", "MODIFY COLUMN amount DECIMAL(11,4)", 100, false, true, 2},
		{"change scale", "CHANGE COLUMN amount amount DECIMAL(10,1)", 100, false, true, 2},
		{"precision shrinks", "MODIFY COLUMN amount DECIMAL(9,2)", 100, false, true, 2},
		{"capacity grows", "MODIFY COLUMN amount DECIMAL(12,3)", 100, false, true, 0},
		{"unchanged", "MODIFY COLUMN amount DECIMAL(10,2)", 100, false, true, 0},
		{"mysql scale unchanged policy", "MODIFY COLUMN amount DECIMAL(10,1)", 100, true, true, 0},
		{"type check disabled", "MODIFY COLUMN amount DECIMAL(10,1)", 100, false, false, 1},
	}
	for _, tc := range cases {
		for mode, ignoreWarnings := range map[string]bool{"stop on warnings": false, "ignore warnings": true} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				s, table := oceanbaseAddSession()
				if tc.mysql {
					s.dbType = DBTypeMysql
				}
				s.inc.EnableNullable = true
				s.inc.CheckColumnTypeChange = tc.checkTypes
				s.inc.CheckOfflineDDLLargeTableSize = 100
				s.inc.CheckOfflineDDLLargeTableAsError = false
				s.opt.IgnoreWarnings = ignoreWarnings
				table.TableSize = tc.size
				table.TableSizeKnown = true
				offlineTableDB(t, s, &offlineTableTestDriver{probeRows: probeTestRows(1)})
				table.Fields = append(table.Fields, FieldInfo{Field: "amount", Type: "decimal(10,2)", Null: "YES"})
				node := oceanbaseAddStatement(t, "ALTER TABLE t "+tc.sql)
				s.checkDDLOffline(node, table)
				s.checkModifyColumn(table, node.Specs[0])
				if s.myRecord.ErrLevel != tc.level {
					t.Fatalf("level=%d want=%d offline=%v message=%s", s.myRecord.ErrLevel, tc.level, s.myRecord.obOfflineAlter, s.myRecord.Buf)
				}
			})
		}
	}
}

func TestOfflineDDLLargeTablePolicy(t *testing.T) {
	cases := []struct {
		name      string
		threshold uint
		oscOn     bool
		oscMin    uint
		size      uint
		asError   bool
		useOsc    bool
		sql       string
		large     bool
		level     uint8
	}{
		{"large offline", 100, false, 0, 100, false, true, "ALTER TABLE t MODIFY COLUMN series VARCHAR(64)", true, 1},
		{"small offline", 100, false, 0, 99, false, true, "ALTER TABLE t MODIFY COLUMN series VARCHAR(64)", false, 0},
		{"osc fallback", 0, true, 50, 60, false, true, "ALTER TABLE t MODIFY COLUMN series VARCHAR(64)", true, 1},
		{"disabled", 0, false, 0, 10000, false, false, "ALTER TABLE t MODIFY COLUMN series VARCHAR(64)", false, 0},
		{"large offline as error", 100, false, 0, 100, true, true, "ALTER TABLE t MODIFY COLUMN series VARCHAR(64)", true, 2},
		{"large online", 100, false, 0, 1000, false, true, "ALTER TABLE t MODIFY COLUMN series VARCHAR(255)", false, 0},
		{"large drop column", 100, false, 0, 1000, false, true, "ALTER TABLE t DROP COLUMN tail", true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, table := oceanbaseAddSession()
			s.inc.CheckOfflineDDLLargeTableSize = tc.threshold
			s.inc.CheckOfflineDDLLargeTableAsError = tc.asError
			s.osc.OscOn, s.osc.OscMinTableSize = tc.oscOn, tc.oscMin
			s.myRecord.useOsc = tc.useOsc
			table.TableSize = tc.size
			table.TableSizeKnown = true
			offlineTableDB(t, s, &offlineTableTestDriver{probeRows: probeTestRows(1)})
			node := oceanbaseAddStatement(t, tc.sql)
			s.checkDDLOffline(node, table)
			if s.myRecord.obOfflineLargeAlter != tc.large {
				t.Fatalf("large=%v want=%v (offline=%v size=%d)", s.myRecord.obOfflineLargeAlter, tc.large, s.myRecord.obOfflineAlter, table.TableSize)
			}
			if s.myRecord.ErrLevel != tc.level {
				t.Fatalf("level=%d want=%d message=%s", s.myRecord.ErrLevel, tc.level, s.myRecord.Buf)
			}
			if tc.large && !strings.Contains(s.myRecord.Buf.String(), "offline DDL on a large table") {
				t.Fatalf("missing explicit message: %s", s.myRecord.Buf)
			}
			if tc.name == "large online" && s.myRecord.useOsc {
				t.Fatal("online DDL must leave the OSC path")
			}
			if tc.useOsc && s.myRecord.obOfflineAlter && !s.myRecord.obOfflineLargeAlter && !s.myRecord.useOsc {
				t.Fatal("offline classification must not clear useOsc")
			}
		})
	}
}
