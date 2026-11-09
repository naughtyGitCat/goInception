package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// In-memory SQL driver only: these tests cannot contact a frontend or database.
type testConnector struct {
	query func(context.Context, string) (driver.Rows, error)
}

func (c *testConnector) Connect(context.Context) (driver.Conn, error) { return &testConn{c}, nil }
func (c *testConnector) Driver() driver.Driver                        { return c }
func (c *testConnector) Open(string) (driver.Conn, error)             { return nil, errors.New("not supported") }

type testConn struct{ *testConnector }

func (*testConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (*testConn) Close() error                        { return nil }
func (*testConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (c *testConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	return c.query(ctx, query)
}

type testRows struct {
	columns []string
	values  [][]driver.Value
	endErr  error
	extra   bool
}

func (r *testRows) Columns() []string { return r.columns }
func (*testRows) Close() error        { return nil }
func (r *testRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		if r.endErr != nil {
			return r.endErr
		}
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}
func (r *testRows) HasNextResultSet() bool { return r.extra }
func (r *testRows) NextResultSet() error {
	if !r.extra {
		return io.EOF
	}
	r.extra = false
	return nil
}

func testDB(t *testing.T, query func(context.Context, string) (driver.Rows, error)) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&testConnector{query: query})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestFrontendPolicy(t *testing.T) {
	for _, asError := range []bool{false, true} {
		values := map[string]string{
			"check_offline_ddl": "true", "check_offline_ddl_large_table_size": "1",
			"check_offline_ddl_large_table_rows": "1000", "check_offline_ddl_large_table_as_error": "false",
		}
		if asError {
			values["check_offline_ddl_large_table_as_error"] = "true"
		}
		if err := validatePolicy(values, asError); err != nil {
			t.Fatal(err)
		}
		for key, want := range values {
			values[key] = "wrong"
			if validatePolicy(values, asError) == nil {
				t.Fatalf("accepted invalid %s", key)
			}
			delete(values, key)
			if validatePolicy(values, asError) == nil {
				t.Fatalf("accepted missing %s", key)
			}
			values[key] = want
		}
		values["check_offline_ddl_large_table_rows"] = "0"
		if validatePolicy(values, asError) == nil {
			t.Fatal("default 100000 fallback must not be mistaken for this 1000-row test policy")
		}
	}
}

func TestRowProbeBoundariesAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		count     int
		value     int64
		queryErr  error
		streamErr error
		extra     bool
		wantErr   bool
	}{
		{name: "empty"},
		{name: "999 small", count: 999, value: 1},
		{name: "1000 large", count: 1000, value: 1},
		{name: "1001 large", count: 1001, value: 1},
		{name: "over limit", count: 1002, value: 1, wantErr: true},
		{name: "invalid value", count: 1, value: 0, wantErr: true},
		{name: "timeout", queryErr: context.DeadlineExceeded, wantErr: true},
		{name: "partial read failure", count: 999, value: 1, streamErr: errors.New("read failed"), wantErr: true},
		{name: "extra result", extra: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t, func(ctx context.Context, query string) (driver.Rows, error) {
				if !strings.Contains(query, "READ_CONSISTENCY(STRONG)") || !strings.Contains(query, "QUERY_TIMEOUT(3000000)") || !strings.HasSuffix(query, "LIMIT 1001") || strings.Contains(query, "COUNT(") {
					t.Fatalf("unbounded or weak query: %s", query)
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
					t.Fatal("missing client deadline")
				}
				r := &testRows{columns: []string{"1"}, endErr: tc.streamErr, extra: tc.extra}
				for i := 0; i < tc.count; i++ {
					r.values = append(r.values, []driver.Value{tc.value})
				}
				return r, tc.queryErr
			})
			n, err := probeRows(db, "owned_fixture")
			if (err != nil) != tc.wantErr || (!tc.wantErr && n != tc.count) {
				t.Fatalf("got rows=%d err=%v", n, err)
			}
		})
	}
}

func TestTableSizePreservesValidZero(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   driver.Value
		want    float64
		wantErr bool
	}{
		{"valid zero", float64(0), 0, false},
		{"fraction is not zero", float64(0.25), 0.25, false},
		{"null is unknown", nil, 0, true},
		{"invalid", "bad", 0, true},
		{"negative", float64(-1), 0, true},
		{"nan", math.NaN(), 0, true},
		{"infinite", math.Inf(1), 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t, func(context.Context, string) (driver.Rows, error) {
				return &testRows{columns: []string{"size"}, values: [][]driver.Value{{tc.value}}}, nil
			})
			size, err := tableSizeMB(db, "owned_fixture")
			if (err != nil) != tc.wantErr || (!tc.wantErr && size != tc.want) {
				t.Fatalf("got size=%v err=%v", size, err)
			}
		})
	}
}

func TestFixtureInsertAndExecutionEvidence(t *testing.T) {
	for _, count := range []int{999, 1000, 1001} {
		query := fixtureInsert("owned_fixture", count)
		if strings.Count(query, "'small',1") != count || !strings.HasPrefix(query, "INSERT INTO `owned_fixture` (id,v,dropme) VALUES (1,'small',1)") {
			t.Fatalf("incorrect fixture with %d rows", count)
		}
	}
	ddl := wireRow{SQL: "ALTER TABLE t ADD PRIMARY KEY(id)", Stage: "EXECUTED", Status: "Execute Successfully"}
	if executedOK(nil) || executedOK([]wireRow{{SQL: "USE test", Stage: "EXECUTED", Status: "Execute Successfully"}}) || !executedOK([]wireRow{ddl}) {
		t.Fatal("execution evidence must include an executed DDL")
	}
	ddl.Level = 2
	if executedOK([]wireRow{ddl}) || !dispatched([]wireRow{ddl}) {
		t.Fatal("an executed failure is not success or a pre-dispatch rejection")
	}
}

func TestSnapshotFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		count  int
		endErr error
		extra  bool
	}{
		{name: "over bound", count: 1002},
		{name: "partial read", count: 1, endErr: errors.New("read failed")},
		{name: "extra result", extra: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t, func(_ context.Context, query string) (driver.Rows, error) {
				if strings.HasPrefix(query, "SHOW CREATE TABLE") {
					return &testRows{columns: []string{"Table", "Create Table"}, values: [][]driver.Value{{"owned_fixture", "schema"}}}, nil
				}
				r := &testRows{columns: []string{"id"}, endErr: tc.endErr, extra: tc.extra}
				for i := 0; i < tc.count; i++ {
					r.values = append(r.values, []driver.Value{int64(i)})
				}
				return r, nil
			})
			if _, err := snapshot(db, "owned_fixture"); err == nil {
				t.Fatal("incomplete snapshot must not prove unchanged data")
			}
		})
	}
}

func TestSnapshotIncludesSchemaAndAllValues(t *testing.T) {
	db := testDB(t, func(_ context.Context, query string) (driver.Rows, error) {
		if strings.HasPrefix(query, "SHOW CREATE TABLE") {
			return &testRows{columns: []string{"Table", "Create Table"}, values: [][]driver.Value{{"owned_fixture", "schema before"}}}, nil
		}
		if !strings.Contains(query, "READ_CONSISTENCY(STRONG)") || !strings.HasSuffix(query, "ORDER BY id LIMIT 1002") {
			t.Fatalf("unbounded or weak snapshot: %s", query)
		}
		return &testRows{columns: []string{"id", "txt", "bin", "num"}, values: [][]driver.Value{{int64(1), "hello", []byte("world"), int64(123)}}}, nil
	})
	before, err := snapshot(db, "owned_fixture")
	if err != nil || before.Schema != "schema before" || len(before.Values) != 1 || len(before.Values[0]) != 4 {
		t.Fatalf("snapshot=%v err=%v", before, err)
	}
	for i, want := range []string{"1", "hello", "world", "123"} {
		if before.Values[0][i] != (sql.NullString{String: want, Valid: true}) {
			t.Fatalf("value %d not preserved: %v", i, before.Values)
		}
	}
	after := before
	after.Schema = "schema after"
	if reflect.DeepEqual(before, after) {
		t.Fatal("schema mutation must be observable")
	}
}
