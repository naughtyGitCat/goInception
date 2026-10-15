package session

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/hanchuanchuan/goInception/parser"
	"github.com/percona/go-mysql/query"
	"io"
	"strings"
	"testing"
	"time"
)

type obCountTestRows struct {
	rows       [][]driver.Value
	finalError error
	index      int
	extraSet   bool
	extraRows  [][]driver.Value
}

func (r *obCountTestRows) HasNextResultSet() bool { return r.extraSet }
func (r *obCountTestRows) NextResultSet() error {
	if !r.extraSet {
		return io.EOF
	}
	r.extraSet = false
	r.rows = r.extraRows
	r.index = 0
	return nil
}

func (r *obCountTestRows) Columns() []string { return []string{"count"} }
func (r *obCountTestRows) Close() error      { return nil }
func (r *obCountTestRows) Next(values []driver.Value) error {
	if r.index == len(r.rows) {
		if r.finalError != nil {
			return r.finalError
		}
		return io.EOF
	}
	copy(values, r.rows[r.index])
	r.index++
	return nil
}

func TestOBCountOptionsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		opt   SourceOptions
		db    int
		valid bool
	}{
		{"legacy", SourceOptions{}, DBTypeMysql, true},
		{"mysql", SourceOptions{OBCountProtection: true}, DBTypeMysql, false},
		{"timeout_without_guard", SourceOptions{OBCountTimeout: 30}, DBTypeOceanBase, false},
		{"capability_without_guard", SourceOptions{OBCountCapability: true}, DBTypeOceanBase, false},
		{"missing_real_count", SourceOptions{SingleDML: true, Check: true, WaitTimeout: 300, OBCountProtection: true}, DBTypeOceanBase, false},
		{"guard", SourceOptions{SingleDML: true, Check: true, WaitTimeout: 300, RealRowCount: true, OBCountProtection: true}, DBTypeOceanBase, true},
		{"capability_execute", SourceOptions{SingleDML: true, Execute: true, WaitTimeout: 300, RealRowCount: true, OBCountProtection: true, OBCountCapability: true}, DBTypeOceanBase, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSingleDMLOptions(&tc.opt, tc.db)
			if (err == nil) != tc.valid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestOBCountConnectionAndMutationIsolation(t *testing.T) {
	state := &singleDMLTestDriver{countRows: [][]driver.Value{{int64(50000)}}}
	s, r := newSingleDMLTestSession(t, state)
	s.opt.RealRowCount = true
	s.opt.OBCountProtection = true
	s.obCountContext = context.Background()
	s.explainOrAnalyzeSql(r.Sql)
	if r.ErrLevel != 0 || r.AffectedRows != 50000 {
		t.Fatalf("rows=%d err=%s", r.AffectedRows, r.Buf.String())
	}
	s.executeSingleDML(context.Background())
	if state.mutations != 1 || state.connections != 2 {
		t.Fatalf("mutations=%d connections=%d", state.mutations, state.connections)
	}
	var countConnection int64
	for _, call := range state.calls {
		if strings.Contains(strings.ToLower(call.sql), "count(") {
			countConnection = call.connection
		}
	}
	var weak, route, timeout, strong bool
	for _, call := range state.calls {
		if call.connection == countConnection {
			weak = weak || strings.Contains(call.sql, "'WEAK'")
			route = route || call.sql == "set @proxy_route_policy='follower_first';"
			if strings.HasPrefix(call.sql, "set session ob_query_timeout=") {
				var micros int64
				_, _ = fmt.Sscanf(call.sql, "set session ob_query_timeout=%d", &micros)
				timeout = timeout || (micros > 29000000 && micros <= 30000000)
			}
		}
		if call.connection == int64(r.ThreadId) {
			strong = strong || call.sql == "set session ob_read_consistency='STRONG';"
			if strings.Contains(call.sql, "'WEAK'") || strings.Contains(call.sql, "@proxy_route_policy") {
				t.Fatal("read route leaked to mutation")
			}
		}
	}
	if !weak || !route || !timeout || !strong {
		t.Fatalf("missing setup: %+v", state.calls)
	}
}

func TestOBCountFailuresNeverMutateOrRetry(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		rows                 [][]driver.Value
		queryErr, errorAfter error
	}{
		{name: "null", rows: [][]driver.Value{{nil}}}, {name: "negative", rows: [][]driver.Value{{int64(-1)}}},
		{name: "scan", rows: [][]driver.Value{{"invalid"}}}, {name: "empty"},
		{name: "multiple", rows: [][]driver.Value{{int64(1)}, {int64(2)}}},
		{name: "query", queryErr: driver.ErrBadConn}, {name: "rows_error", rows: [][]driver.Value{{int64(1)}}, errorAfter: errors.New("rows failed")},
		{name: "too_many", rows: [][]driver.Value{{int64(80001)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &singleDMLTestDriver{countRows: tc.rows, countError: tc.queryErr, countRowsError: tc.errorAfter}
			s, r := newSingleDMLTestSession(t, state)
			s.opt.RealRowCount = true
			s.opt.OBCountProtection = true
			s.obCountContext = context.Background()
			s.explainOrAnalyzeSql(r.Sql)
			s.executeSingleDML(context.Background())
			if r.ErrLevel != 2 || state.mutations != 0 || state.connections != 1 {
				t.Fatalf("level=%d mutations=%d connections=%d err=%s", r.ErrLevel, state.mutations, state.connections, r.Buf.String())
			}
		})
	}
}

func TestOBCountContextDeadline(t *testing.T) {
	state := &singleDMLTestDriver{countRows: [][]driver.Value{{int64(1)}}}
	s, r := newSingleDMLTestSession(t, state)
	s.opt.RealRowCount = true
	s.opt.OBCountProtection = true
	s.opt.OBCountTimeout = 1
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second/2))
	defer cancel()
	s.obCountContext = ctx
	s.explainOrAnalyzeSql(r.Sql)
	if state.countDeadline.IsZero() || state.countDeadline.After(time.Now().Add(time.Second/2)) {
		t.Fatalf("deadline=%v", state.countDeadline)
	}
}

func TestOBCountCapabilityOnlyUse(t *testing.T) {
	state := &singleDMLTestDriver{}
	s, r := newSingleDMLTestSession(t, state)
	s.opt.Check = true
	s.opt.Execute = false
	s.opt.RealRowCount = true
	s.opt.OBCountProtection = true
	s.opt.OBCountCapability = true
	s.recordSets = NewRecordSets()
	s.myRecord = nil
	s.executeSingleDML(context.Background())
	if state.mutations != 0 || state.connections != 0 {
		t.Fatal("capability dispatched SQL")
	}
	// Empty or mutation bodies must not receive the proof marker.
	if s.myRecord != nil && strings.Contains(s.myRecord.Buf.String(), obCountCapabilityMarker) {
		t.Fatal("empty body proved capability")
	}
	s.recordSets = NewRecordSets()
	s.recordSets.Append(r)
	s.myRecord = r
	s.executeSingleDML(context.Background())
	if strings.Contains(r.Buf.String(), obCountCapabilityMarker) {
		t.Fatal("mutation body proved capability")
	}
}

func TestOBCountCapabilityProtocol(t *testing.T) {
	state := &singleDMLTestDriver{}
	s, _ := newSingleDMLTestSession(t, state)
	s.opt.Check = true
	s.opt.Execute = false
	s.opt.RealRowCount = true
	s.opt.OBCountProtection = true
	s.opt.OBCountCapability = true
	nodes, _, err := parser.New().Parse("USE `test`", "utf8mb4", "utf8mb4_bin")
	if err != nil {
		t.Fatal(err)
	}
	use := &Record{Sql: "USE `test`", Type: nodes[0], Stage: StageCheck, Buf: new(bytes.Buffer)}
	s.recordSets = NewRecordSets()
	s.recordSets.Append(use)
	s.myRecord = use
	s.executeSingleDML(context.Background())
	if state.connections != 0 || state.mutations != 0 {
		t.Fatal("probe executed target SQL")
	}
	s.recordSets.Rows()
	fields := s.recordSets.rc.Fields()
	names := []string{"order_id", "stage", "error_level", "stage_status", "error_message", "sql", "affected_rows", "sequence", "backup_dbname", "execute_time", "sqlsha1", "backup_time", "needMerge"}
	if len(fields) != 13 || len(s.recordSets.rc.data) != 1 {
		t.Fatal("protocol shape")
	}
	for i, n := range names {
		if fields[i].Column.Name.O != n {
			t.Fatalf("column %d=%s", i, fields[i].Column.Name.O)
		}
	}
	row := s.recordSets.rc.data[0]
	if row[0].GetInt64() != 1 || row[1].GetString() != "CHECKED" || row[2].GetInt64() != 2 || row[3].GetString() != "Audit Completed" || row[4].GetString() != obCountCapabilityMarker || row[5].GetString() != "USE `test`" || row[6].GetInt64() != 0 || row[7].GetString() != "0_0_00000000" || !row[8].IsNull() || row[9].GetString() != "0" || !row[10].IsNull() || row[11].GetString() != "0" || row[12].GetInt64() != 0 {
		t.Fatalf("unexpected row: %v", row)
	}
}

func TestOBCountFingerprintDoesNotBypassActualCount(t *testing.T) {
	state := &singleDMLTestDriver{countRows: [][]driver.Value{{int64(77)}}}
	s, r := newSingleDMLTestSession(t, state)
	s.opt.RealRowCount = true
	s.opt.OBCountProtection = true
	s.obCountContext = context.Background()
	s.inc.EnableFingerprint = true
	s.sqlFingerprint = map[string]*Record{query.Id(query.Fingerprint(r.Sql)): {AffectedRows: 1, Buf: new(bytes.Buffer)}}
	s.explainOrAnalyzeSql(r.Sql)
	if r.AffectedRows != 77 || state.connections != 1 {
		t.Fatalf("cached COUNT bypass: %d", r.AffectedRows)
	}
}

func TestOBCountSetupAndCancellationFailClosed(t *testing.T) {
	for _, name := range []string{"timeout_setup", "route_setup", "cancelled", "deadline"} {
		t.Run(name, func(t *testing.T) {
			state := &singleDMLTestDriver{countRows: [][]driver.Value{{int64(1)}}}
			s, r := newSingleDMLTestSession(t, state)
			s.opt.RealRowCount = true
			s.opt.OBCountProtection = true
			s.obCountContext = context.Background()
			switch name {
			case "timeout_setup":
				state.setupError = errors.New("timeout rejected")
			case "route_setup":
				state.routeError = errors.New("follower_first rejected")
			case "cancelled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				s.obCountContext = ctx
			case "deadline":
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				s.obCountContext = ctx
			}
			s.explainOrAnalyzeSql(r.Sql)
			s.executeSingleDML(context.Background())
			if r.ErrLevel != 2 || state.mutations != 0 {
				t.Fatalf("did not close: %s", r.Buf.String())
			}
			for _, call := range state.calls {
				if strings.Contains(call.sql, "count(") {
					t.Fatal("COUNT dispatched after setup/cancel failure")
				}
			}
		})
	}
}

func TestOBCountSourceOptionAliases(t *testing.T) {
	for _, tc := range []struct {
		options string
		token   string
	}{
		{"--single-dml=1;--real-row-count=1;--disable-remote-backup;--enable-check;--wait-timeout=300;--enable-ob-count-protection;--ob-count-timeout=7;--enable-ob-count-capability;", ""},
		{"--enable-ob-count-protection;--ob-count-timeout=0.5;", "ob_count_invalid_integer_option"},
		{"--ob-count-protection=maybe;", "ob_count_invalid_option"},
	} {
		s := NewInception()
		s.init()
		s.recordSets = NewRecordSets()
		s.myRecord = &Record{Stage: StageCheck, Buf: new(bytes.Buffer)}
		s.parseOptions("/*" + tc.options + "*/inception_magic_start")
		if tc.token != "" {
			if !strings.Contains(s.myRecord.Buf.String(), tc.token) {
				t.Fatal(s.myRecord.Buf.String())
			}
			continue
		}
		if !s.opt.SingleDML || !s.opt.RealRowCount || !s.opt.OBCountProtection || !s.opt.OBCountCapability || s.opt.OBCountTimeout != 7 || s.opt.Backup {
			t.Fatalf("aliases: %+v", s.opt)
		}
	}
}

func TestOBCountAdditionalResultSetsFailClosed(t *testing.T) {
	for _, extra := range [][][]driver.Value{nil, {{int64(2)}}} {
		state := &singleDMLTestDriver{countRows: [][]driver.Value{{int64(1)}}, countExtraSet: true, countExtraRows: extra}
		s, r := newSingleDMLTestSession(t, state)
		s.opt.RealRowCount = true
		s.opt.OBCountProtection = true
		s.obCountContext = context.Background()
		s.explainOrAnalyzeSql(r.Sql)
		s.executeSingleDML(context.Background())
		if r.ErrLevel != 2 || state.mutations != 0 || state.connections != 1 {
			t.Fatalf("extra result accepted: level=%d mutations=%d", r.ErrLevel, state.mutations)
		}
	}
}
