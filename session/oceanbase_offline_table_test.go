package session

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type offlineTableTestDriver struct {
	*singleDMLTestDriver
	sizeRows    [][]driver.Value
	probeRows   [][]driver.Value
	queryError  error
	streamError error
	extra       bool
	calls       []string
	deadline    time.Time
}

func (d *offlineTableTestDriver) Connect(ctx context.Context) (driver.Conn, error) {
	c, e := d.singleDMLTestDriver.Connect(ctx)
	if e != nil {
		return nil, e
	}
	return &offlineTableTestConn{singleDMLTestConn: c.(*singleDMLTestConn), state: d}, nil
}

type offlineTableTestConn struct {
	*singleDMLTestConn
	state *offlineTableTestDriver
}

func (c *offlineTableTestConn) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d := c.state
	d.calls = append(d.calls, q)
	d.deadline, _ = ctx.Deadline()
	if d.queryError != nil {
		return nil, d.queryError
	}
	if strings.Contains(q, "TABLE_ROWS") {
		return &singleDMLTestRows{columns: []string{"TABLE_ROWS", "TABLE_COLLATION"}, values: []driver.Value{int64(0), "utf8mb4_general_ci"}}, nil
	}
	values := d.probeRows
	if strings.Contains(q, "data_size") {
		values = d.sizeRows
	}
	return &obCountTestRows{rows: values, finalError: d.streamError, extraSet: d.extra, extraRows: [][]driver.Value{{int64(1)}}}, nil
}
func offlineTableDB(t *testing.T, s *session, d *offlineTableTestDriver) {
	t.Helper()
	d.singleDMLTestDriver = &singleDMLTestDriver{}
	db := sql.OpenDB(d)
	t.Cleanup(func() { _ = db.Close() })
	g, e := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	s.db = g
}
func probeTestRows(n int) [][]driver.Value {
	r := make([][]driver.Value, n)
	for i := range r {
		r[i] = []driver.Value{int64(1)}
	}
	return r
}

func TestOceanBaseOfflineTableRowsPolicy(t *testing.T) {
	for _, tc := range []struct {
		name         string
		size         uint
		known        bool
		estimate     int64
		count        int
		queryError   error
		streamError  error
		extra        bool
		small, large bool
		level        uint8
		queries      int
	}{
		{name: "size alone large", size: 100, known: true, large: true, level: 1},
		{name: "estimate alone large", known: true, estimate: 10, large: true, level: 1},
		{name: "large estimate despite unknown size", estimate: 10, large: true, level: 1},
		{name: "empty table", known: true, small: true, queries: 1},
		{name: "one row", known: true, count: 1, small: true, queries: 1},
		{name: "below threshold", known: true, count: 9, small: true, queries: 1},
		{name: "threshold equality", known: true, count: 10, large: true, level: 1, queries: 1},
		{name: "estimate zero but probe large", known: true, count: 11, large: true, level: 1, queries: 1},
		{name: "unknown size is not small", level: 2},
		{name: "probe timeout", known: true, queryError: context.DeadlineExceeded, level: 2, queries: 1},
		{name: "probe read error", known: true, count: 1, streamError: errors.New("read failed"), level: 2, queries: 1},
		{name: "extra result set", known: true, extra: true, level: 2, queries: 1},
		{name: "probe exceeded SQL limit", known: true, count: 12, level: 2, queries: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, table := oceanbaseAddSession()
			s.opt.IgnoreWarnings = true
			s.inc.CheckOfflineDDLLargeTableSize = 100
			s.inc.CheckOfflineDDLLargeTableRows = 10
			table.TableSize, table.TableSizeKnown = tc.size, tc.known
			s.myRecord.AffectedRows = tc.estimate
			d := &offlineTableTestDriver{probeRows: probeTestRows(tc.count), queryError: tc.queryError, streamError: tc.streamError, extra: tc.extra}
			offlineTableDB(t, s, d)
			s.checkDDLOffline(oceanbaseAddStatement(t, "ALTER TABLE t DROP COLUMN tail"), table)
			if s.myRecord.obOfflineSmallAlter != tc.small || s.myRecord.obOfflineLargeAlter != tc.large || s.myRecord.ErrLevel != tc.level {
				t.Fatalf("small=%v large=%v level=%d want=%v/%v/%d message=%s", s.myRecord.obOfflineSmallAlter, s.myRecord.obOfflineLargeAlter, s.myRecord.ErrLevel, tc.small, tc.large, tc.level, s.myRecord.Buf)
			}
			if len(d.calls) != tc.queries {
				t.Fatalf("queries=%v want=%d", d.calls, tc.queries)
			}
			if tc.queries > 0 {
				q := d.calls[0]
				if !strings.Contains(q, "LIMIT 11") || !strings.Contains(q, "QUERY_TIMEOUT(3000000)") || !strings.Contains(q, "READ_CONSISTENCY(STRONG)") || strings.Contains(strings.ToUpper(q), "COUNT(") {
					t.Fatalf("unbounded/weak probe: %s", q)
				}
				if d.deadline.IsZero() || time.Until(d.deadline) > 3*time.Second {
					t.Fatalf("missing probe deadline: %v", d.deadline)
				}
			}
		})
	}
}

func TestOceanBaseTableSizeCollectionIsExplicit(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		rows                    [][]driver.Value
		queryError, streamError error
		extra                   bool
		known                   bool
		size                    uint
	}{
		{name: "valid zero", rows: [][]driver.Value{{float64(0)}}, known: true},
		{name: "valid fractional MB", rows: [][]driver.Value{{float64(0.25)}}, known: true},
		{name: "valid size", rows: [][]driver.Value{{float64(120)}}, known: true, size: 120},
		{name: "null", rows: [][]driver.Value{{nil}}},
		{name: "no rows"},
		{name: "bad value", rows: [][]driver.Value{{"bad"}}},
		{name: "negative", rows: [][]driver.Value{{float64(-1)}}},
		{name: "nan", rows: [][]driver.Value{{math.NaN()}}},
		{name: "infinity", rows: [][]driver.Value{{math.Inf(1)}}},
		{name: "query failure", queryError: errors.New("denied")},
		{name: "stream failure", rows: [][]driver.Value{{float64(0)}}, streamError: errors.New("read failed")},
		{name: "multiple rows", rows: [][]driver.Value{{float64(0)}, {float64(0)}}},
		{name: "extra result set", rows: [][]driver.Value{{float64(0)}}, extra: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, table := oceanbaseAddSession()
			table.TableSize = 42
			table.TableSizeKnown = true
			d := &offlineTableTestDriver{sizeRows: tc.rows, queryError: tc.queryError, streamError: tc.streamError, extra: tc.extra}
			offlineTableDB(t, s, d)
			err := s.collectOBTableSize(table)
			if table.TableSizeKnown != tc.known || table.TableSize != tc.size {
				t.Fatalf("known=%v size=%d want=%v/%d", table.TableSizeKnown, table.TableSize, tc.known, tc.size)
			}
			if (err == nil) != tc.known {
				t.Fatalf("size collection error=%v known=%v", err, tc.known)
			}
		})
	}
}

func TestOceanBaseOfflineRowsOnlyAndOnlineSkip(t *testing.T) {
	for _, online := range []bool{false, true} {
		s, table := oceanbaseAddSession()
		s.inc.CheckOfflineDDLLargeTableRows = 10
		d := &offlineTableTestDriver{probeRows: probeTestRows(1)}
		offlineTableDB(t, s, d)
		statement := "ALTER TABLE t DROP COLUMN tail"
		if online {
			statement = "ALTER TABLE t ADD COLUMN added INT"
		}
		s.checkDDLOffline(oceanbaseAddStatement(t, statement), table)
		if s.myRecord.ErrLevel != 0 || s.myRecord.obOfflineSmallAlter == online {
			t.Fatalf("online=%v message=%s", online, s.myRecord.Buf)
		}
		want := 1
		if online {
			want = 0
		}
		if len(d.calls) != want {
			t.Fatalf("online=%v calls=%v", online, d.calls)
		}
	}
	s, table := oceanbaseAddSession()
	s.inc.CheckOfflineDDLLargeTableSize = 100
	if s.offlineDDLLargeTableRows() != 100000 {
		t.Fatal("size-only policy needs bounded proof default")
	}
	s.checkDDLOffline(oceanbaseAddStatement(t, "ALTER TABLE t ADD COLUMN added INT"), table)
	if s.myRecord.ErrLevel != 0 {
		t.Fatalf("online DDL must not need a row probe: %s", s.myRecord.Buf)
	}
}

func TestOceanBaseOfflineProbeFailuresPreventNativeDDL(t *testing.T) {
	for _, tc := range []struct {
		name       string
		known      bool
		estimate   int64
		probeError error
	}{
		{name: "unknown size"},
		{name: "timeout", known: true, probeError: context.DeadlineExceeded},
		{name: "query denied", known: true, probeError: errors.New("SELECT denied")},
		{name: "large with warnings disabled", known: true, estimate: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r := newSingleDDLSession(t, &singleDDLDriver{})
			_, table := oceanbaseAddSession()
			r.Sql = "ALTER TABLE t DROP COLUMN tail"
			node := oceanbaseAddStatement(t, r.Sql)
			r.Type, r.AffectedRows = node, tc.estimate
			s.inc.CheckOfflineDDL = false
			s.inc.CheckOfflineDDLLargeTableSize, s.inc.CheckOfflineDDLLargeTableRows = 100, 10
			table.TableSizeKnown = tc.known
			d := &offlineTableTestDriver{queryError: tc.probeError}
			offlineTableDB(t, s, d)
			s.checkDDLOffline(node, table)
			s.executeSingleDDL(context.Background())
			if !s.hasErrorBefore() || r.ExecComplete || r.Stage == StageExec || len(d.singleDMLTestDriver.calls) != 0 {
				t.Fatalf("unsafe dispatch: stage=%v completed=%v calls=%v error=%s", r.Stage, r.ExecComplete, d.singleDMLTestDriver.calls, r.Buf)
			}
		})
	}
}

func TestOceanBaseOfflineProofIsNotCached(t *testing.T) {
	s, table := oceanbaseAddSession()
	s.inc.CheckOfflineDDLLargeTableRows = 10
	d := &offlineTableTestDriver{probeRows: probeTestRows(1)}
	offlineTableDB(t, s, d)
	node := oceanbaseAddStatement(t, "ALTER TABLE t DROP COLUMN tail")
	s.checkDDLOffline(node, table)
	if !s.myRecord.obOfflineSmallAlter {
		t.Fatal("missing first small-table proof")
	}
	d.queryError = errors.New("second read failed")
	s.checkDDLOffline(node, table)
	if s.myRecord.obOfflineSmallAlter || s.myRecord.obOfflineLargeAlter || s.myRecord.ErrLevel != 2 || len(d.calls) != 2 {
		t.Fatalf("stale proof reused: small=%v calls=%v message=%s", s.myRecord.obOfflineSmallAlter, d.calls, s.myRecord.Buf)
	}
}

func TestOceanBaseOfflineRowsInvalidResults(t *testing.T) {
	for _, value := range []driver.Value{nil, "invalid", int64(0), int64(2)} {
		s, table := oceanbaseAddSession()
		s.inc.CheckOfflineDDLLargeTableRows = 10
		d := &offlineTableTestDriver{probeRows: [][]driver.Value{{value}}}
		offlineTableDB(t, s, d)
		s.checkDDLOffline(oceanbaseAddStatement(t, "ALTER TABLE t DROP COLUMN tail"), table)
		if s.myRecord.obOfflineSmallAlter || s.myRecord.ErrLevel != 2 {
			t.Fatalf("invalid result accepted: %v", value)
		}
	}
	for _, threshold := range []uint{0, ^uint(0)} {
		s, table := oceanbaseAddSession()
		d := &offlineTableTestDriver{}
		offlineTableDB(t, s, d)
		if _, err := s.probeOBOfflineTableRows(table, threshold); err == nil || len(d.calls) != 0 {
			t.Fatal("invalid limit was sent")
		}
	}
}

func TestOceanBaseOfflineNoProbeForDisabledMySQLOrNewTable(t *testing.T) {
	for _, kind := range []string{"disabled", "mysql", "new"} {
		s, table := oceanbaseAddSession()
		if kind != "disabled" {
			s.inc.CheckOfflineDDLLargeTableRows = 10
		}
		if kind == "mysql" {
			s.dbType = DBTypeMysql
		}
		if kind == "new" {
			table.IsNew = true
		}
		d := &offlineTableTestDriver{}
		offlineTableDB(t, s, d)
		s.checkDDLOffline(oceanbaseAddStatement(t, "ALTER TABLE t DROP COLUMN tail"), table)
		want := uint8(0)
		if kind == "new" {
			want = 2
		}
		if len(d.calls) != 0 || s.myRecord.obOfflineSmallAlter || s.myRecord.ErrLevel != want {
			t.Fatalf("kind=%s calls=%v error=%s", kind, d.calls, s.myRecord.Buf)
		}
	}
}

// 覆盖真实ALTER调用链, 避免在分类前由大小采集错误误拒绝无关操作。
func TestOceanBaseOfflineWholeAlterPolicyScope(t *testing.T) {
	for _, tc := range []struct {
		name, statement string
		size, rows      uint
		level           uint8
		probes          int
	}{
		{"online-with-policy", "ALTER TABLE t AUTO_INCREMENT=100", 100, 10, 0, 0},
		{"both-disabled", "ALTER TABLE t DROP COLUMN tail", 0, 0, 0, 0},
		{"rows-only", "ALTER TABLE t DROP COLUMN tail", 0, 10, 0, 1},
		{"offline-needs-size", "ALTER TABLE t DROP COLUMN tail", 100, 10, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newSingleDDLSession(t, &singleDDLDriver{})
			base, table := oceanbaseAddSession()
			s.inc = base.inc
			s.inc.CheckOfflineDDL = false
			s.inc.CheckOfflineDDLLargeTableSize, s.inc.CheckOfflineDDLLargeTableRows = tc.size, tc.rows
			s.opt = &SourceOptions{Check: true, IgnoreWarnings: true}
			s.obVersion, s.dbVersion = base.obVersion, base.dbVersion
			table.Name = "t"
			s.tableCacheList = map[string]*TableInfo{"test.t": table}
			s.dbCacheList = map[string]*DBInfo{"test": {Name: "test"}}
			d := &offlineTableTestDriver{sizeRows: [][]driver.Value{{nil}}, probeRows: probeTestRows(1)}
			offlineTableDB(t, s, d)
			s.checkAlterTable(oceanbaseAddStatement(t, tc.statement), tc.statement, false)
			probes := 0
			for _, call := range d.calls {
				if strings.Contains(call, "READ_CONSISTENCY(STRONG)") {
					probes++
				}
			}
			if s.myRecord.ErrLevel != tc.level || probes != tc.probes {
				t.Fatalf("level=%d probes=%d error=%s", s.myRecord.ErrLevel, probes, s.myRecord.Buf)
			}
		})
	}
}

func TestOceanBaseOfflineProbeEscapesIdentifiers(t *testing.T) {
	s, table := oceanbaseAddSession()
	s.inc.CheckOfflineDDLLargeTableRows = 10
	table.Schema = "te`st"
	table.Name = "ta`ble"
	d := &offlineTableTestDriver{}
	offlineTableDB(t, s, d)
	s.checkDDLOffline(oceanbaseAddStatement(t, "ALTER TABLE t DROP COLUMN tail"), table)
	if len(d.calls) != 1 || !strings.Contains(d.calls[0], "`te``st`.`ta``ble`") {
		t.Fatalf("unescaped identifiers: %v", d.calls)
	}
}
