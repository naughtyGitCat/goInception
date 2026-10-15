package session

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/hanchuanchuan/goInception/parser"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type singleDMLTestCall struct {
	connection int64
	sql        string
}

type singleDMLTestDriver struct {
	mu             sync.Mutex
	connections    int64
	calls          []singleDMLTestCall
	mutations      int
	mutationError  error
	setupError     error
	routeError     error
	readOnly       bool
	countRows      [][]driver.Value
	countError     error
	countRowsError error
	countDeadline  time.Time
	countExtraSet  bool
	countExtraRows [][]driver.Value
}

func (d *singleDMLTestDriver) Open(string) (driver.Conn, error) {
	return d.Connect(context.Background())
}

func (d *singleDMLTestDriver) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.connections++
	return &singleDMLTestConn{state: d, id: d.connections}, nil
}

func (d *singleDMLTestDriver) Driver() driver.Driver { return d }

type singleDMLTestConn struct {
	state *singleDMLTestDriver
	id    int64
}

func (c *singleDMLTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c *singleDMLTestConn) Close() error { return nil }
func (c *singleDMLTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("single DML must not open a transaction")
}

func (c *singleDMLTestConn) ExecContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.calls = append(c.state.calls, singleDMLTestCall{c.id, query})
	lower := strings.ToLower(strings.TrimSpace(query))
	if strings.HasPrefix(lower, "update ") || strings.HasPrefix(lower, "insert ") || strings.HasPrefix(lower, "delete ") {
		c.state.mutations++
		if c.state.mutationError != nil {
			return nil, c.state.mutationError
		}
		return driver.RowsAffected(1), nil
	}
	if strings.Contains(lower, "@proxy_route_policy") && c.state.routeError != nil {
		return nil, c.state.routeError
	}
	if strings.Contains(lower, "ob_query_timeout") && c.state.setupError != nil {
		return nil, c.state.setupError
	}
	return driver.RowsAffected(0), nil
}

func (c *singleDMLTestConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.calls = append(c.state.calls, singleDMLTestCall{c.id, query})
	lower := strings.ToLower(query)
	if strings.Contains(lower, "count(") {
		c.state.countDeadline, _ = ctx.Deadline()
		if c.state.countError != nil {
			return nil, c.state.countError
		}
		return &obCountTestRows{rows: c.state.countRows, finalError: c.state.countRowsError, extraSet: c.state.countExtraSet, extraRows: c.state.countExtraRows}, nil
	}
	if strings.Contains(lower, "read_only") {
		value := "OFF"
		if c.state.readOnly {
			value = "ON"
		}
		return &singleDMLTestRows{columns: []string{"Variable_name", "Value"}, values: []driver.Value{"read_only", value}}, nil
	}
	if strings.Contains(lower, "autocommit") && strings.Contains(lower, "connection_id") {
		return &singleDMLTestRows{columns: []string{"autocommit", "connection_id"}, values: []driver.Value{int64(1), c.id}}, nil
	}
	if strings.Contains(lower, "connection_id") {
		return &singleDMLTestRows{columns: []string{"connection_id"}, values: []driver.Value{c.id}}, nil
	}
	return nil, errors.New("unexpected query: " + query)
}

type singleDMLTestRows struct {
	columns []string
	values  []driver.Value
	done    bool
}

func (r *singleDMLTestRows) Columns() []string { return r.columns }
func (r *singleDMLTestRows) Close() error      { return nil }
func (r *singleDMLTestRows) Next(values []driver.Value) error {
	if r.done {
		return io.EOF
	}
	copy(values, r.values)
	r.done = true
	return nil
}

func newSingleDMLTestSession(t *testing.T, state *singleDMLTestDriver) (*session, *Record) {
	t.Helper()
	db := sql.OpenDB(state)
	t.Cleanup(func() { _ = db.Close() })
	gormDB, err := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	nodes, _, err := parser.New().Parse("UPDATE t SET v = v + 1 WHERE id = 1", "utf8mb4", "utf8mb4_bin")
	if err != nil {
		t.Fatal(err)
	}
	mutation := &Record{
		Sql:   "UPDATE t SET v = v + 1 WHERE id = 1",
		Type:  nodes[0],
		Buf:   new(bytes.Buffer),
		Stage: StageCheck,
		TableInfo: &TableInfo{
			Name:   "t",
			Schema: "test",
			Fields: []FieldInfo{{Field: "id", Key: "PRI"}, {Field: "v"}},
		},
	}
	s := NewInception()
	s.db = gormDB
	s.dbType = DBTypeOceanBase
	s.dbName = "test"
	s.stage = StageCheck
	s.opt = &SourceOptions{SingleDML: true, Execute: true, IgnoreWarnings: true, WaitTimeout: 300}
	s.recordSets = NewRecordSets()
	s.recordSets.Append(mutation)
	s.myRecord = mutation
	return s, mutation
}

func TestSingleDMLConnection_PrepareAndMutationSharePhysicalConnection(t *testing.T) {
	state := &singleDMLTestDriver{}
	s, mutation := newSingleDMLTestSession(t, state)
	s.executeSingleDML(context.Background())
	if mutation.Stage != StageExec || mutation.StageStatus != StatusExecOK || mutation.ErrLevel != 0 {
		t.Fatalf("expected executed success, got stage=%d status=%d level=%d error=%s", mutation.Stage, mutation.StageStatus, mutation.ErrLevel, mutation.Buf.String())
	}
	if mutation.AffectedRows != 1 || state.mutations != 1 {
		t.Fatalf("rows=%d mutation calls=%d", mutation.AffectedRows, state.mutations)
	}
	var sawUse, sawTimeout, sawAutocommit bool
	for _, call := range state.calls {
		if call.connection != int64(mutation.ThreadId) {
			t.Fatalf("connection drift: call=%+v thread=%d", call, mutation.ThreadId)
		}
		lower := strings.ToLower(call.sql)
		sawUse = sawUse || strings.HasPrefix(lower, "use ")
		sawTimeout = sawTimeout || strings.Contains(lower, "ob_query_timeout=300000000")
		sawAutocommit = sawAutocommit || strings.Contains(lower, "autocommit=1")
	}
	if !sawUse || !sawTimeout || !sawAutocommit {
		t.Fatalf("missing setup: use=%v timeout=%v autocommit=%v calls=%+v", sawUse, sawTimeout, sawAutocommit, state.calls)
	}
}

func TestSingleDMLConnection_SetupFailureNeverDispatchesMutation(t *testing.T) {
	state := &singleDMLTestDriver{setupError: errors.New("session timeout injection rejected")}
	s, mutation := newSingleDMLTestSession(t, state)
	s.executeSingleDML(context.Background())
	if state.mutations != 0 || mutation.Stage != StageCheck || mutation.ErrLevel != 2 {
		t.Fatalf("setup failure must remain checked and unsent: mutations=%d stage=%d level=%d", state.mutations, mutation.Stage, mutation.ErrLevel)
	}
}

func TestSingleDMLConnection_MutationErrorsNeverReconnectOrReplay(t *testing.T) {
	for name, mutationError := range map[string]error{
		"bad connection":         driver.ErrBadConn,
		"invalid connection":     mysqlDriver.ErrInvalidConn,
		"1046 with long timeout": &mysqlDriver.MySQLError{Number: 1046, Message: "No database selected"},
		"cancelled":              context.Canceled,
	} {
		t.Run(name, func(t *testing.T) {
			state := &singleDMLTestDriver{mutationError: mutationError}
			s, mutation := newSingleDMLTestSession(t, state)
			s.opt.WaitTimeout = 900
			s.executeSingleDML(context.Background())
			if state.mutations != 1 || state.connections != 1 {
				t.Fatalf("must not replay: mutation calls=%d connections=%d", state.mutations, state.connections)
			}
			if mutation.Stage != StageExec || mutation.StageStatus != StatusExecFail || mutation.ErrLevel != 2 {
				t.Fatalf("expected explicit exec failure: stage=%d status=%d level=%d", mutation.Stage, mutation.StageStatus, mutation.ErrLevel)
			}
		})
	}
}

func TestSingleDMLConnection_ReadOnlyTargetNeverDispatchesMutation(t *testing.T) {
	state := &singleDMLTestDriver{readOnly: true}
	s, mutation := newSingleDMLTestSession(t, state)
	s.inc.CheckReadOnly = true
	s.executeSingleDML(context.Background())
	if state.mutations != 0 || mutation.Stage != StageCheck || mutation.ErrLevel != 2 {
		t.Fatalf("read-only must reject before mutation: mutations=%d stage=%d level=%d", state.mutations, mutation.Stage, mutation.ErrLevel)
	}
}

func TestSingleDMLConnection_CheckOnlyCountProbeDoesNotOpenTargetConnection(t *testing.T) {
	state := &singleDMLTestDriver{}
	s, mutation := newSingleDMLTestSession(t, state)
	s.opt.Check, s.opt.Execute = true, false
	second := *mutation
	second.TableInfo = nil
	second.Buf = new(bytes.Buffer)
	s.recordSets.Append(&second)
	s.executeSingleDML(context.Background())
	if state.mutations != 0 || state.connections != 0 || !strings.Contains(mutation.Buf.String(), "single_dml_requires_one_mutation") {
		t.Fatalf("count probe must reject without execution: connections=%d mutations=%d error=%s", state.connections, state.mutations, mutation.Buf.String())
	}
}

func TestSingleDMLConnection_LegacyGlobalWaitTimeoutDoesNotOverrideOBQueryTimeout(t *testing.T) {
	state := &singleDMLTestDriver{}
	s, _ := newSingleDMLTestSession(t, state)
	s.opt.SingleDML = false
	s.opt.WaitTimeout = 0
	s.inc.WaitTimeout = 300
	s.modifyWaitTimeout()
	if len(state.calls) != 1 || strings.ToLower(state.calls[0].sql) != "set session wait_timeout=300;" {
		t.Fatalf("legacy global wait_timeout semantics changed: %+v", state.calls)
	}
}

func TestSingleDMLConnection_LegacyRequestWaitTimeoutStillOverridesGlobal(t *testing.T) {
	state := &singleDMLTestDriver{}
	s, _ := newSingleDMLTestSession(t, state)
	s.opt.SingleDML = false
	s.opt.WaitTimeout = 15
	s.inc.WaitTimeout = 300
	s.modifyWaitTimeout()
	if len(state.calls) != 1 || !strings.Contains(strings.ToLower(state.calls[0].sql), "ob_query_timeout=15000000") {
		t.Fatalf("request timeout lost precedence: %+v", state.calls)
	}
}

func TestSingleDMLConnection_CancelledBeforePrepareNeverDispatchesMutation(t *testing.T) {
	state := &singleDMLTestDriver{}
	s, mutation := newSingleDMLTestSession(t, state)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.executeSingleDML(ctx)
	if state.mutations != 0 || mutation.Stage != StageCheck || mutation.ErrLevel != 2 {
		t.Fatalf("pre-send cancel must stay checked: mutations=%d stage=%d level=%d", state.mutations, mutation.Stage, mutation.ErrLevel)
	}
}
