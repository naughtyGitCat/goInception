package session

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/hanchuanchuan/goInception/ast"
	"github.com/hanchuanchuan/goInception/parser"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func ddlRecords(t *testing.T, sqlText string) []*Record {
	t.Helper()
	nodes, _, err := parser.New().Parse(sqlText, "utf8mb4", "utf8mb4_bin")
	if err != nil {
		t.Fatal(err)
	}
	var records []*Record
	for _, node := range nodes {
		records = append(records, &Record{Sql: strings.TrimSpace(node.Text()), Type: node, Stage: StageCheck, Buf: new(bytes.Buffer)})
	}
	return records
}

func TestSingleDDLOptions(t *testing.T) {
	valid := func() *SourceOptions { return &SourceOptions{SingleDDL: true, Execute: true, WaitTimeout: 7200} }
	for _, tc := range []struct {
		name   string
		change func(*SourceOptions)
		dbType int
	}{
		{"missing_timeout", func(o *SourceOptions) { o.WaitTimeout = 0 }, DBTypeOceanBase},
		{"negative_timeout", func(o *SourceOptions) { o.WaitTimeout = -1 }, DBTypeOceanBase},
		{"too_long", func(o *SourceOptions) { o.WaitTimeout = 7201 }, DBTypeOceanBase},
		{"backup", func(o *SourceOptions) { o.Backup = true }, DBTypeOceanBase},
		{"trans", func(o *SourceOptions) { o.tranBatch = 1 }, DBTypeOceanBase},
		{"both_modes", func(o *SourceOptions) { o.Check = true }, DBTypeOceanBase},
		{"neither_mode", func(o *SourceOptions) { o.Execute = false }, DBTypeOceanBase},
		{"dml", func(o *SourceOptions) { o.SingleDML = true }, DBTypeOceanBase},
		{"count", func(o *SourceOptions) { o.OBCountProtection = true }, DBTypeOceanBase},
		{"count_timeout", func(o *SourceOptions) { o.OBCountTimeout = 1 }, DBTypeOceanBase},
		{"count_explicit_zero", func(o *SourceOptions) { o.obCountTimeoutSet = true }, DBTypeOceanBase},
		{"count_capability", func(o *SourceOptions) { o.OBCountCapability = true }, DBTypeOceanBase},
		{"print", func(o *SourceOptions) { o.Print = true }, DBTypeOceanBase},
		{"summary", func(o *SourceOptions) { o.QueryPrintSummary = true }, DBTypeOceanBase},
		{"mask", func(o *SourceOptions) { o.Masking = true }, DBTypeOceanBase},
		{"split", func(o *SourceOptions) { o.split = true }, DBTypeOceanBase},
		{"middleware", func(o *SourceOptions) { o.middlewareExtend = "route" }, DBTypeOceanBase},
		{"execute_capability", func(o *SourceOptions) { o.SingleDDLCapability = true }, DBTypeOceanBase},
		{"capability_without_mode", func(o *SourceOptions) { o.SingleDDL = false; o.SingleDDLCapability = true }, DBTypeOceanBase},
		{"system_source_database", func(o *SourceOptions) { o.db = "oceanbase" }, DBTypeOceanBase},
		{"sys_tenant", func(o *SourceOptions) { o.User = "inception@sys" }, DBTypeOceanBase},
		{"sys_tenant_cluster", func(o *SourceOptions) { o.User = "inception@SYS#cluster" }, DBTypeOceanBase},
		{"sys_tenant_colon", func(o *SourceOptions) { o.User = "cluster:sys:inception" }, DBTypeOceanBase},
		{"mysql", func(o *SourceOptions) {}, DBTypeMysql},
		{"tidb", func(o *SourceOptions) {}, DBTypeTiDB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt := valid()
			tc.change(opt)
			if err := validateSingleDDLOptions(opt, tc.dbType); err == nil {
				t.Fatal("unsafe options accepted")
			}
		})
	}
	for _, seconds := range []int{1, 300, 3601, 7200} {
		opt := valid()
		opt.WaitTimeout = seconds
		if err := validateSingleDDLOptions(opt, DBTypeOceanBase); err != nil {
			t.Fatal(err)
		}
		opt.Check, opt.Execute = true, false
		if err := validateSingleDDLOptions(opt, DBTypeOceanBase); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []string{"inception", "inception@dba", "inception@dba#cluster", "cluster:dba:inception", "sys_reader@dba#cluster"} {
		opt := valid()
		opt.User = user
		if err := validateSingleDDLOptions(opt, DBTypeOceanBase); err != nil {
			t.Fatal("business identity rejected", err)
		}
	}
	if err := validateSingleDDLOptions(&SourceOptions{Execute: true, Backup: true, WaitTimeout: 9000}, DBTypeMysql); err != nil {
		t.Fatal("legacy affected", err)
	}
}

func TestSingleDDLSourceOptionsStrictBeforeNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, options, token string
		enabled              bool
	}{
		{"numeric", "--single-ddl=1;--wait-timeout=7200;--enable-execute;", "", true},
		{"boolean", "--enable-single-ddl;--wait-timeout=1;--enable-check;", "", true},
		{"capability", "--single-ddl=1;--wait-timeout=7200;--enable-check;--enable-single-ddl-capability;", "", true},
		{"disabled", "--disable-single-ddl;--wait-timeout=9000;--enable-execute;", "", false},
		{"invalid_boolean", "--single-ddl=maybe;--wait-timeout=1;--enable-execute;", "single_ddl_invalid_option", false},
		{"invalid_capability", "--single-ddl=1;--single-ddl-capability=maybe;--wait-timeout=1;--enable-check;", "single_ddl_invalid_option", true},
		{"fraction", "--single-ddl=1;--wait-timeout=1.5;--enable-execute;", "single_ddl_invalid_integer_option", true},
		{"float_integer", "--single-ddl=1;--wait-timeout=1.0;--enable-execute;", "single_ddl_invalid_integer_option", true},
		{"float_zero_trans", "--single-ddl=1;--wait-timeout=1;--trans=0.0;--enable-execute;", "single_ddl_invalid_integer_option", true},
		{"scientific", "--single-ddl=1;--wait-timeout=1e0;--enable-execute;", "single_ddl_invalid_integer_option", true},
		{"fraction_trans", "--single-ddl=1;--wait-timeout=1;--trans=0.5;--enable-execute;", "single_ddl_invalid_integer_option", true},
		{"both", "--single-ddl=1;--wait-timeout=1;--enable-execute;--enable-check;", "single_ddl_requires_check_or_execute", true},
		{"backup_check", "--single-ddl=1;--wait-timeout=1;--enable-check;--enable-remote-backup;", "single_ddl_requires_no_backup_and_trans_zero", true},
		{"print", "--single-ddl=1;--wait-timeout=1;--enable-execute;--enable-query-print;", "single_ddl_requires_check_or_execute", true},
		{"print_summary", "--single-ddl=1;--wait-timeout=1;--enable-execute;--enable-query-print-summary;", "single_ddl_requires_check_or_execute", true},
		{"bad_check", "--single-ddl=1;--wait-timeout=1;--check=maybe;--enable-execute;", "single_ddl_invalid_option", true},
		{"no_timeout", "--single-ddl=1;--enable-execute;", "single_ddl_timeout", true},
		{"source_database", "--single-ddl=1;--wait-timeout=1;--enable-execute;--db=oceanbase;", "single_ddl_requires_non_system_database", true},
		{"sys_tenant", "--single-ddl=1;--wait-timeout=1;--enable-execute;--user=inception@sys#cluster;", "single_ddl_system_tenant_not_supported", true},
		{"sys_tenant_colon", "--single-ddl=1;--wait-timeout=1;--enable-execute;--user=cluster:sys:inception;", "single_ddl_system_tenant_not_supported", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewInception()
			s.init()
			s.recordSets = NewRecordSets()
			s.myRecord = &Record{Stage: StageCheck, Buf: new(bytes.Buffer)}
			s.parseOptions("/*" + tc.options + "*/inception_magic_start")
			if s.opt == nil || s.opt.SingleDDL != tc.enabled {
				t.Fatalf("mode mismatch: %+v", s.opt)
			}
			if tc.token != "" && !strings.Contains(s.myRecord.Buf.String(), tc.token) {
				t.Fatalf("missing %s: %s", tc.token, s.myRecord.Buf.String())
			}
			if tc.token == "" && strings.Contains(s.myRecord.Buf.String(), "single_ddl_") {
				t.Fatal(s.myRecord.Buf.String())
			}
		})
	}
}

func TestSingleDDLASTScope(t *testing.T) {
	for _, text := range []string{
		"CREATE TABLE t(id int)", "CREATE TABLE t LIKE test.source", "ALTER TABLE test.t ADD COLUMN v int",
		"DROP TABLE test.t", "CREATE INDEX idx ON t(id)", "DROP INDEX idx ON t", "TRUNCATE TABLE t",
		"RENAME TABLE t TO test.t2", "ALTER TABLE t RENAME TO test.t2",
		"CREATE TABLE t(id int, CONSTRAINT fk FOREIGN KEY(id) REFERENCES test.parent(id))",
	} {
		t.Run("accept_"+text, func(t *testing.T) {
			records := ddlRecords(t, "USE test;"+text)
			if _, err := validateSingleDDLRecords(records, "test"); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, text := range []string{
		"USE test", "ALTER TABLE t ADD v int; ALTER TABLE t ADD v2 int", "USE test; USE test; ALTER TABLE t ADD v int",
		"ALTER TABLE t ADD v int; USE test", "CREATE TABLE t AS SELECT 1 AS id", "DROP TABLE t,t2", "DROP VIEW t",
		"CREATE VIEW t AS SELECT 1", "CREATE DATABASE other", "SET autocommit=1", "UPDATE t SET id=1 WHERE id=2",
		"SELECT 1", "RENAME TABLE t TO t2,t3 TO t4", "CREATE TEMPORARY TABLE t(id int)",
		"USE sys; ALTER TABLE t ADD v int", "USE oceanbase; ALTER TABLE t ADD v int", "USE mysql; ALTER TABLE t ADD v int",
		"USE information_schema; ALTER TABLE t ADD v int", "USE performance_schema; ALTER TABLE t ADD v int",
		"ALTER TABLE other.t ADD v int", "RENAME TABLE t TO other.t2", "ALTER TABLE t RENAME TO other.t2",
		"CREATE TABLE t LIKE other.source", "CREATE TABLE t(id int, CONSTRAINT fk FOREIGN KEY(id) REFERENCES other.parent(id))",
		"ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY(id) REFERENCES other.parent(id)",
		"CREATE TABLE t(id int REFERENCES other.parent(id))",
	} {
		t.Run("reject_"+text, func(t *testing.T) {
			if _, err := validateSingleDDLRecords(ddlRecords(t, text), "test"); err == nil {
				t.Fatal("unsafe AST accepted")
			}
		})
	}
	for _, database := range []string{"", "SYS", "OceanBase", "MYSQL", "INFORMATION_SCHEMA", "PERFORMANCE_SCHEMA"} {
		t.Run("context_"+database, func(t *testing.T) {
			if _, err := validateSingleDDLRecords(ddlRecords(t, "ALTER TABLE t ADD v int"), database); err == nil {
				t.Fatal("unsafe context")
			}
		})
	}
}

func TestSingleDDLExchangePartitionRejectsSecondMutationTarget(t *testing.T) {
	for _, text := range []string{
		"ALTER TABLE t EXCHANGE PARTITION p0 WITH TABLE staging",
		"ALTER TABLE t EXCHANGE PARTITION p0 WITH TABLE test.staging WITH VALIDATION",
		"ALTER TABLE t EXCHANGE PARTITION p0 WITH TABLE test.staging WITHOUT VALIDATION",
	} {
		t.Run(text, func(t *testing.T) {
			if _, err := validateSingleDDLRecords(ddlRecords(t, "USE test;"+text), "test"); err == nil {
				t.Fatal("partition exchange mutates a second table")
			}
			for _, checkOnly := range []bool{false, true} {
				d := &singleDDLDriver{}
				s, _ := newSingleDDLSession(t, d)
				s.recordSets = NewRecordSets()
				for _, r := range ddlRecords(t, "USE test;"+text) {
					s.recordSets.Append(r)
					s.myRecord = r
				}
				s.opt.Check, s.opt.Execute = checkOnly, !checkOnly
				// Warnings are intentionally ignored, so the AST guard—not the
				// legacy partition warning level—must provide the hard rejection.
				s.executeCommit(context.Background())
				if d.ddls != 0 || d.opened != 0 || !s.hasErrorBefore() {
					t.Fatal("exchange was not rejected before connection acquisition")
				}
			}
		})
	}
}

func TestSingleDDLSchemaCaseMustMatchExactly(t *testing.T) {
	for _, text := range []string{
		"ALTER TABLE Biz.t ADD v int",
		"CREATE TABLE t LIKE Biz.source",
		"RENAME TABLE t TO Biz.t2",
		"ALTER TABLE t RENAME TO Biz.t2",
		"CREATE TABLE t(id int REFERENCES Biz.parent(id))",
		"ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY(id) REFERENCES Biz.parent(id)",
		"ALTER TABLE t DROP COLUMN Biz.t.v",
	} {
		t.Run(text, func(t *testing.T) {
			records := ddlRecords(t, "USE biz;"+text)
			if _, err := validateSingleDDLRecords(records, "biz"); err == nil {
				t.Fatal("case drift may address a different database on case-sensitive targets")
			}
		})
	}
	for _, text := range []string{"USE Biz; ALTER TABLE Biz.t ADD v int", "USE biz; ALTER TABLE biz.t ADD v int"} {
		t.Run("exact_"+text, func(t *testing.T) {
			if _, err := validateSingleDDLRecords(ddlRecords(t, text), "biz"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A dedicated DDL fake checks physical disposal (not just sql.Conn.Close),
// acquisition/setup/DDL deadlines, and the exact server-state readback.
type singleDDLDriver struct {
	mu                               sync.Mutex
	opened, closed, ddls             int
	calls                            []singleDMLTestCall
	deadlines                        []time.Time
	setupError, queryError, ddlError error
	mismatch                         string
	waitSeconds                      int64
	cancelAt                         string
	cancel                           context.CancelFunc
	blockAt                          string
}

func (d *singleDDLDriver) Driver() driver.Driver            { return d }
func (d *singleDDLDriver) Open(string) (driver.Conn, error) { return d.Connect(context.Background()) }
func (d *singleDDLDriver) Connect(ctx context.Context) (driver.Conn, error) {
	deadline, _ := ctx.Deadline()
	d.mu.Lock()
	d.deadlines = append(d.deadlines, deadline)
	d.mu.Unlock()
	if d.blockAt == "acquire" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opened++
	return &singleDDLConn{d: d, id: int64(d.opened)}, nil
}

type singleDDLConn struct {
	d  *singleDDLDriver
	id int64
}

func (c *singleDDLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c *singleDDLConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (c *singleDDLConn) Close() error              { c.d.mu.Lock(); defer c.d.mu.Unlock(); c.d.closed++; return nil }
func (c *singleDDLConn) call(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	c.d.mu.Lock()
	c.d.calls = append(c.d.calls, singleDMLTestCall{c.id, text})
	c.d.deadlines = append(c.d.deadlines, deadline)
	c.d.mu.Unlock()
	return nil
}
func (c *singleDDLConn) ExecContext(ctx context.Context, text string, _ []driver.NamedValue) (driver.Result, error) {
	if err := c.call(ctx, text); err != nil {
		return nil, err
	}
	lower := strings.ToLower(text)
	ddl := strings.HasPrefix(lower, "alter ") || strings.HasPrefix(lower, "create ") || strings.HasPrefix(lower, "drop ") || strings.HasPrefix(lower, "truncate ") || strings.HasPrefix(lower, "rename ")
	if ddl {
		c.d.mu.Lock()
		c.d.ddls++
		c.d.mu.Unlock()
		if c.d.blockAt == "ddl" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return driver.RowsAffected(0), c.d.ddlError
	}
	if strings.Contains(lower, "ob_query_timeout=") {
		if c.d.cancelAt == "setup" {
			c.d.cancel()
		}
		if c.d.blockAt == "setup" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if c.d.setupError != nil {
			return nil, c.d.setupError
		}
	}
	return driver.RowsAffected(0), nil
}
func (c *singleDDLConn) QueryContext(ctx context.Context, text string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := c.call(ctx, text); err != nil {
		return nil, err
	}
	if c.d.cancelAt == "readback" {
		c.d.cancel()
	}
	if c.d.queryError != nil {
		return nil, c.d.queryError
	}
	if strings.Contains(text, "read_only") {
		return &singleDMLTestRows{columns: []string{"name", "value"}, values: []driver.Value{"read_only", "OFF"}}, nil
	}
	seconds := c.d.waitSeconds
	values := []driver.Value{int64(1), seconds, seconds * 1000000, seconds * 1000000, c.id}
	if c.d.mismatch == "autocommit" {
		values[0] = int64(0)
	}
	if c.d.mismatch == "wait" {
		values[1] = seconds - 1
	}
	if c.d.mismatch == "query" {
		values[2] = seconds*1000000 - 1
	}
	if c.d.mismatch == "trx" {
		values[3] = seconds*1000000 - 1
	}
	if c.d.mismatch == "id" {
		values[4] = int64(0)
	}
	return &singleDMLTestRows{columns: []string{"autocommit", "wait_timeout", "ob_query_timeout", "ob_trx_timeout", "connection_id"}, values: values}, nil
}
func newSingleDDLSession(t *testing.T, d *singleDDLDriver) (*session, *Record) {
	t.Helper()
	if d.waitSeconds == 0 {
		d.waitSeconds = 7200
	}
	db := sql.OpenDB(d)
	t.Cleanup(func() { _ = db.Close() })
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	s := NewInception()
	s.db = gdb
	s.dbType = DBTypeOceanBase
	s.dbName = "test"
	s.stage = StageCheck
	s.opt = &SourceOptions{SingleDDL: true, Execute: true, WaitTimeout: int(d.waitSeconds), IgnoreWarnings: true}
	s.recordSets = NewRecordSets()
	for _, r := range ddlRecords(t, "USE `test`; ALTER TABLE t ADD COLUMN v int") {
		s.recordSets.Append(r)
	}
	r := s.recordSets.All()[1]
	s.myRecord = r
	return s, r
}
func assertDDLDiscarded(t *testing.T, s *session, d *singleDDLDriver) {
	t.Helper()
	if s.singleDDLConn != nil || d.closed != d.opened {
		t.Fatalf("physical connection leaked: open=%d close=%d conn=%v", d.opened, d.closed, s.singleDDLConn != nil)
	}
	if d.opened > 0 {
		db, _ := s.db.DB()
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		if d.opened != 2 {
			t.Fatal("contaminated physical connection reused")
		}
	}
}

func TestSingleDDLConnectionSamePhysicalReadbackAnd7200(t *testing.T) {
	d := &singleDDLDriver{}
	s, r := newSingleDDLSession(t, d)
	start := time.Now()
	s.executeCommit(context.Background())
	if d.ddls != 1 || d.opened != 1 || r.Stage != StageExec || r.StageStatus != StatusExecOK || r.ThreadId != 1 || !r.ExecComplete {
		t.Fatalf("execution failed: %+v %s", r, r.Buf.String())
	}
	var use, timeout, readback bool
	for _, call := range d.calls {
		if call.connection != 1 {
			t.Fatal("connection drift")
		}
		use = use || call.sql == "USE `test`;"
		timeout = timeout || strings.Contains(call.sql, "ob_query_timeout=7200000000, ob_trx_timeout=7200000000")
		readback = readback || strings.Contains(call.sql, "@@session.wait_timeout")
	}
	if !use || !timeout || !readback {
		t.Fatalf("setup/readback missing: %+v", d.calls)
	}
	for _, deadline := range d.deadlines {
		if deadline.Before(start.Add(7199*time.Second)) || deadline.After(start.Add(7201*time.Second)) || !deadline.Equal(d.deadlines[0]) {
			t.Fatalf("budget drift: %v", d.deadlines)
		}
	}
	s.recordSets.Rows()
	rows := s.recordSets.rc.data
	for i, row := range rows {
		if row[1].GetString() != "EXECUTED" || row[3].GetString() != "Execute Successfully" || !row[8].IsNull() || !row[10].IsNull() || row[12].GetInt64() != 0 {
			t.Fatalf("row %d: %v", i, row)
		}
	}
	assertDDLDiscarded(t, s, d)
}

func TestSingleDDLSetupFailuresZeroDispatch(t *testing.T) {
	for _, failure := range []string{"set", "readback", "autocommit", "wait", "query", "trx", "id", "cancel_setup", "cancel_readback"} {
		t.Run(failure, func(t *testing.T) {
			d := &singleDDLDriver{}
			s, r := newSingleDDLSession(t, d)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d.cancel = cancel
			switch failure {
			case "set":
				d.setupError = errors.New("SET rejected")
			case "readback":
				d.queryError = errors.New("readback rejected")
			case "cancel_setup":
				d.cancelAt = "setup"
			case "cancel_readback":
				d.cancelAt = "readback"
			default:
				d.mismatch = failure
			}
			s.executeCommit(ctx)
			if d.ddls != 0 || r.Stage != StageCheck || !s.hasErrorBefore() {
				t.Fatalf("not fail closed: ddls=%d stage=%d", d.ddls, r.Stage)
			}
			assertDDLDiscarded(t, s, d)
		})
	}
}

func TestSingleDDLSubmittedFailuresNeverRetry(t *testing.T) {
	for name, err := range map[string]error{"disconnect": driver.ErrBadConn, "invalid": mysqlDriver.ErrInvalidConn, "1046": &mysqlDriver.MySQLError{Number: 1046, Message: "No database selected"}, "cancel": context.Canceled, "deadline": context.DeadlineExceeded} {
		t.Run(name, func(t *testing.T) {
			d := &singleDDLDriver{ddlError: err}
			s, r := newSingleDDLSession(t, d)
			s.executeCommit(context.Background())
			if d.ddls != 1 || d.opened != 1 || r.Stage != StageExec || r.StageStatus != StatusExecFail || r.ErrLevel != 2 || r.ThreadId != 1 {
				t.Fatalf("unexpected failure/replay: ddls=%d opened=%d stage=%d status=%d", d.ddls, d.opened, r.Stage, r.StageStatus)
			}
			assertDDLDiscarded(t, s, d)
		})
	}
}

func TestSingleDDLContextCoversAcquisitionSetupAndDDL(t *testing.T) {
	for _, at := range []string{"acquire", "setup", "ddl"} {
		t.Run(at, func(t *testing.T) {
			d := &singleDDLDriver{blockAt: at}
			s, r := newSingleDDLSession(t, d)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			start := time.Now()
			s.executeCommit(ctx)
			if time.Since(start) > time.Second {
				t.Fatal("context not bounded")
			}
			want := 0
			if at == "ddl" {
				want = 1
			}
			if d.ddls != want || !s.hasErrorBefore() {
				t.Fatal("invalid timeout result")
			}
			if at == "ddl" && (r.Stage != StageExec || r.StageStatus != StatusExecFail) {
				t.Fatal("submitted timeout reported unsent")
			}
			for _, deadline := range d.deadlines {
				if !deadline.Equal(deadlineOf(ctx)) {
					t.Fatal("parent budget lost")
				}
			}
			assertDDLDiscarded(t, s, d)
		})
	}
}

func TestSingleDDLGuardCheckOfflineLargeAndAuditErrors(t *testing.T) {
	for _, name := range []string{"check", "offline_large_check", "offline_large_execute", "audit_error", "two_ddl", "no_use_context"} {
		t.Run(name, func(t *testing.T) {
			d := &singleDDLDriver{}
			s, r := newSingleDDLSession(t, d)
			switch name {
			case "check":
				s.opt.Check, s.opt.Execute = true, false
			case "offline_large_check":
				s.opt.Check, s.opt.Execute = true, false
				r.obOfflineLargeAlter = true
			case "offline_large_execute":
				r.obOfflineLargeAlter = true
			case "audit_error":
				r.ErrLevel = 2
				s.recordSets.MaxLevel = 2
			case "two_ddl":
				s.recordSets.Append(ddlRecords(t, "ALTER TABLE t ADD v2 int")[0])
			case "no_use_context":
				s.recordSets = NewRecordSets()
				s.recordSets.Append(r)
				s.dbName = ""
			}
			s.executeCommit(context.Background())
			if d.ddls != 0 || d.opened != 0 || r.Stage != StageCheck {
				t.Fatal("guard acquired/dispatched")
			}
			if strings.HasPrefix(name, "offline_large_") && !strings.Contains(s.recordSets.All()[0].Buf.String(), "single_ddl_offline_ddl_not_supported") {
				t.Fatal("large-table offline DDL not rejected")
			}
		})
	}
}

func TestSingleDDLCapabilityExactProtocolAndFailClosed(t *testing.T) {
	for _, body := range []string{"USE `test`", "", "USE sys", "USE mysql", "USE oceanbase", "USE information_schema", "USE performance_schema", "USE test;USE test", "USE test;ALTER TABLE t ADD v int", "ALTER TABLE t ADD v int"} {
		t.Run(fmt.Sprintf("body_%s", body), func(t *testing.T) {
			d := &singleDDLDriver{}
			s, _ := newSingleDDLSession(t, d)
			s.opt.Check, s.opt.Execute, s.opt.SingleDDLCapability = true, false, true
			s.recordSets = NewRecordSets()
			s.myRecord = nil
			for _, r := range ddlRecords(t, body) {
				s.recordSets.Append(r)
				s.myRecord = r
			}
			s.executeCommit(context.Background())
			s.recordSets.Rows()
			if d.opened != 0 || d.ddls != 0 {
				t.Fatal("capability dispatched")
			}
			if body != "USE `test`" {
				for _, r := range s.recordSets.All() {
					if r.ErrorMessage == singleDDLCapabilityMarker {
						t.Fatal("invalid proof")
					}
				}
				return
			}
			fields := s.recordSets.rc.Fields()
			names := []string{"order_id", "stage", "error_level", "stage_status", "error_message", "sql", "affected_rows", "sequence", "backup_dbname", "execute_time", "sqlsha1", "backup_time", "needMerge"}
			if len(fields) != 13 || len(s.recordSets.rc.data) != 1 {
				t.Fatal("protocol shape")
			}
			for i, name := range names {
				if fields[i].Column.Name.O != name {
					t.Fatal("wrong columns")
				}
			}
			row := s.recordSets.rc.data[0]
			if row[0].GetInt64() != 1 || row[1].GetString() != "CHECKED" || row[2].GetInt64() != 2 || row[3].GetString() != "Audit Completed" || row[4].GetString() != singleDDLCapabilityMarker || row[5].GetString() != body || row[6].GetInt64() != 0 || row[7].GetString() != "0_0_00000000" || !row[8].IsNull() || row[9].GetString() != "0" || !row[10].IsNull() || row[11].GetString() != "0" || row[12].GetInt64() != 0 {
				t.Fatalf("wrong marker row: %v", row)
			}
		})
	}
	for _, name := range []string{"audit_error", "execute", "missing_mode"} {
		t.Run(name, func(t *testing.T) {
			d := &singleDDLDriver{}
			s, _ := newSingleDDLSession(t, d)
			s.opt.Check, s.opt.Execute, s.opt.SingleDDLCapability = true, false, true
			s.recordSets = NewRecordSets()
			r := ddlRecords(t, "USE test")[0]
			s.recordSets.Append(r)
			s.myRecord = r
			if name == "audit_error" {
				r.ErrLevel = 2
				s.recordSets.MaxLevel = 2
			}
			if name == "execute" {
				s.opt.Check, s.opt.Execute = false, true
			}
			if name == "missing_mode" {
				s.opt.SingleDDL = false
			}
			s.executeSingleDDL(context.Background())
			if strings.Contains(r.ErrorMessage, singleDDLCapabilityMarker) || d.ddls != 0 || d.opened != 0 {
				t.Fatal("unsafe proof")
			}
		})
	}
}

func TestSingleDDLClearDiscardsPhysicalConnection(t *testing.T) {
	d := &singleDDLDriver{}
	s, _ := newSingleDDLSession(t, d)
	if err := s.prepareSingleDDLConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.clear()
	if d.closed != 1 || s.singleDDLConn != nil {
		t.Fatal("clear retained contaminated connection")
	}
	s.clear()
	if d.closed != 1 {
		t.Fatal("clear double closed")
	}
}

func TestSingleDDLProcessCommandRejectsBeforeMetadata(t *testing.T) {
	for _, text := range []string{"SET autocommit=1", "USE sys", "ALTER TABLE other.t ADD v int", "CREATE TABLE t SELECT 1 AS id"} {
		t.Run(text, func(t *testing.T) {
			s := NewInception()
			s.init()
			s.dbName = "test"
			s.opt = &SourceOptions{SingleDDL: true, Execute: true, WaitTimeout: 1}
			s.recordSets = NewRecordSets()
			r := ddlRecords(t, text)[0]
			s.myRecord = r
			_, err := s.processCommand(context.Background(), r.Type, r.Sql)
			if err != nil || r.ErrLevel != 2 || !strings.Contains(r.Buf.String(), "single_ddl_") {
				t.Fatal("statement reached metadata instead of guard")
			}
		})
	}
}

func TestSingleDDLParserWarningsRejectBeforeMetadata(t *testing.T) {
	s := NewInception()
	s.init()
	s.dbName = "test"
	s.opt = &SourceOptions{SingleDDL: true, Execute: true, WaitTimeout: 1}
	s.recordSets = NewRecordSets()
	text := "CREATE TABLE t (id int, FULLTEXT KEY ix(id) WITH PARSER ngram)"
	nodes, err := s.ParseSQL(context.Background(), text, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !s.queryPrintSummaryParseWarnings {
		t.Fatal("fixture needs parser warning")
	}
	s.myRecord = &Record{Sql: text, Type: nodes[0], Stage: StageCheck, Buf: new(bytes.Buffer)}
	if _, err := s.processCommand(context.Background(), nodes[0], text); err != nil {
		t.Fatal(err)
	}
	if s.myRecord.ErrLevel != 2 || !strings.Contains(s.myRecord.Buf.String(), "single_ddl_parser_warning") {
		t.Fatal("omitted syntax reached CHECK")
	}
}

func TestSingleDDLBatchEndRejectsTrailingBeforeDispatch(t *testing.T) {
	for _, text := range []string{
		"inception_magic_commit; ALTER TABLE t ADD v2 int;",
		"inception_magic_commit;\nALTER TABLE t ADD v2 int;",
	} {
		t.Run(text, func(t *testing.T) {
			d := &singleDDLDriver{}
			s, r := newSingleDDLSession(t, d)
			s.haveBegin = true
			s.txn.txnFuture = &txnFuture{}
			s.sessionVars.CommonGlobalLoaded = true
			_, err := s.executeInc(context.Background(), text)
			if err != nil {
				t.Fatal(err)
			}
			if d.ddls != 0 || d.opened != 0 || r.Stage != StageCheck || !s.hasErrorBefore() {
				t.Fatal("executed DDL before rejecting trailing statement")
			}
		})
	}
}

var _ ast.Visitor = (*singleDDLScopeVisitor)(nil)
