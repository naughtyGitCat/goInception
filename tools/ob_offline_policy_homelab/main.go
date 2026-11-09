// Package main exercises the OceanBase online/offline ALTER policy on an
// isolated homelab frontend. It only touches owned, random-named tables in the
// dba/test business tenant. It never connects to sys, changes grants, or drops
// anything it did not create.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

const (
	obHost        = "10.136.38.71"
	obPort        = 2883
	frontendHost  = "10.136.38.51"
	thresholdMB   = 1
	thresholdRows = 1000
)

type wireRow struct {
	ID       int     `json:"orderId"`
	Stage    string  `json:"stage"`
	Level    int     `json:"errorLevel"`
	Status   string  `json:"stageStatus"`
	Error    *string `json:"errorMessage"`
	SQL      string  `json:"sql"`
	Affected int64   `json:"affectedRows"`
	Sequence string  `json:"sequence"`
}

type evidence struct {
	Name    string    `json:"name"`
	Passed  bool      `json:"passed"`
	Blocked bool      `json:"environmentBlocked,omitempty"`
	Elapsed int64     `json:"elapsedMs"`
	Rows    []wireRow `json:"rows,omitempty"`
	Detail  any       `json:"detail,omitempty"`
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func frontendDB(port int) (*sql.DB, error) {
	c := mysql.NewConfig()
	c.Net = "tcp"
	c.Addr = fmt.Sprintf("%s:%d", frontendHost, port)
	c.Timeout = 5 * time.Second
	c.ReadTimeout = 150 * time.Second
	c.WriteTimeout = 5 * time.Second
	return sql.Open("mysql", c.FormatDSN())
}

// Check only: never reconfigure a frontend or perform tenant maintenance.
func frontendPolicy(port int, asError bool) error {
	db, err := frontendDB(port)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "inception show variables like 'check_offline_ddl%';")
	if err != nil {
		return err
	}
	defer rows.Close()
	values := make(map[string]string)
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return err
		}
		values[name] = value
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return validatePolicy(values, asError)
}

func validatePolicy(values map[string]string, asError bool) error {
	for name, want := range map[string]string{
		"check_offline_ddl":                      "true",
		"check_offline_ddl_large_table_size":     fmt.Sprint(thresholdMB),
		"check_offline_ddl_large_table_rows":     fmt.Sprint(thresholdRows),
		"check_offline_ddl_large_table_as_error": fmt.Sprint(asError),
	} {
		if values[name] != want {
			return fmt.Errorf("isolated frontend requires %s=%s, got %q", name, want, values[name])
		}
	}
	return nil
}

// frame sends one goInception protocol request. singleDDL selects the native
// execution contract; otherwise the ordinary audit/CHECK path is used.
func frame(port int, user, password string, singleDDL bool, wait int, body, flags string) ([]wireRow, int64, error) {
	db, err := frontendDB(port)
	if err != nil {
		return nil, 0, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	opts := fmt.Sprintf("--user=%s;--password=%s;--host=%s;--port=%d;", quote(user), quote(password), quote(obHost), obPort)
	if singleDDL {
		opts += fmt.Sprintf("--single-ddl=1;--disable-remote-backup;--trans=0;--wait-timeout=%d;--enable-ignore-warnings;", wait)
	}
	opts += flags
	q := "/*" + opts + "*/\ninception_magic_start;\nUSE `test`;\n" + body + "\ninception_magic_commit;"
	started := time.Now()
	r, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, time.Since(started).Milliseconds(), err
	}
	defer r.Close()
	var result []wireRow
	for {
		cols, err := r.Columns()
		if err != nil {
			return nil, 0, err
		}
		if len(cols) != 13 {
			return nil, 0, fmt.Errorf("unexpected column count %d", len(cols))
		}
		for r.Next() {
			var row wireRow
			var backup, sha *string
			var execTime, backupTime string
			var merge int
			if err := r.Scan(&row.ID, &row.Stage, &row.Level, &row.Status, &row.Error, &row.SQL, &row.Affected, &row.Sequence, &backup, &execTime, &sha, &backupTime, &merge); err != nil {
				return nil, 0, err
			}
			result = append(result, row)
		}
		if err := r.Err(); err != nil {
			return nil, 0, err
		}
		if !r.NextResultSet() {
			break
		}
	}
	return result, time.Since(started).Milliseconds(), r.Err()
}

func hasMessage(rows []wireRow, needle string) bool {
	for _, r := range rows {
		if r.Error != nil && strings.Contains(*r.Error, needle) {
			return true
		}
	}
	return false
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func maxLevel(rows []wireRow) int {
	level := 0
	for _, r := range rows {
		if r.Level > level {
			level = r.Level
		}
	}
	return level
}

func executedOK(rows []wireRow) bool {
	ok := ddlRow(rows) != nil
	for _, r := range rows {
		ok = ok && r.Stage == "EXECUTED" && r.Status == "Execute Successfully" && r.Level != 2
	}
	return ok
}

func ddlRow(rows []wireRow) *wireRow {
	for i := range rows {
		if strings.Contains(strings.ToUpper(rows[i].SQL), "ALTER TABLE") {
			return &rows[i]
		}
	}
	return nil
}

func columnExists(db *sql.DB, table, column string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='test' AND TABLE_NAME=? AND COLUMN_NAME=?`, table, column).Scan(&n)
	return n == 1, err
}

func hasPrimaryKey(db *sql.DB, table string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='test' AND TABLE_NAME=? AND INDEX_NAME='PRIMARY'`, table).Scan(&n)
	return n > 0, err
}

// Keep the raw size: NULL/errors and fractional MB must not masquerade as 0.
func tableSizeMB(db *sql.DB, table string) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var v float64
	err := db.QueryRowContext(ctx, `SELECT /*+ READ_CONSISTENCY(WEAK) QUERY_TIMEOUT(3000000) */
		round(sum(t2.data_size/1024/1024), 2)
		FROM oceanbase.DBA_OB_TABLE_LOCATIONS t1, oceanbase.DBA_OB_TABLET_REPLICAS t2
		WHERE t1.svr_ip = t2.svr_ip AND t1.ls_id = t2.ls_id AND t1.tablet_id = t2.tablet_id
		AND t1.database_name = 'test' AND t1.table_name = ?`, table).Scan(&v)
	if err == nil && (v < 0 || math.IsNaN(v) || math.IsInf(v, 0)) {
		err = fmt.Errorf("invalid table size %v", v)
	}
	return v, err
}

func fixtureInsert(table string, count int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "INSERT INTO `%s` (id,v,dropme) VALUES ", table)
	for i := 1; i <= count; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "(%d,'small',1)", i)
	}
	return b.String()
}

func rowProbeSQL(table string) string {
	return fmt.Sprintf("SELECT /*+ READ_CONSISTENCY(STRONG) QUERY_TIMEOUT(3000000) */ 1 FROM `test`.`%s` LIMIT %d", table, thresholdRows+1)
}

// A low TABLE_ROWS estimate never proves small; read at most threshold+1 rows.
func probeRows(db *sql.DB, table string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, rowProbeSQL(table))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var one int
		if err := rows.Scan(&one); err != nil {
			return n, err
		}
		n++
		if one != 1 || n > thresholdRows+1 {
			return n, fmt.Errorf("invalid row probe result")
		}
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	if rows.NextResultSet() {
		return n, fmt.Errorf("unexpected row probe result set")
	}
	return n, rows.Err()
}

type tableSnapshot struct {
	Schema string
	Values [][]sql.NullString
}

// These owned fixtures have at most threshold+1 rows. Compare every column/value,
// not just the absence of an added PK, when proving a rejected native request.
func snapshot(db *sql.DB, table string) (tableSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var result tableSnapshot
	var name string
	if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE `test`.`"+table+"`").Scan(&name, &result.Schema); err != nil {
		return result, err
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT /*+ READ_CONSISTENCY(STRONG) QUERY_TIMEOUT(3000000) */ * FROM `test`.`%s` ORDER BY id LIMIT %d", table, thresholdRows+2))
	if err != nil {
		return result, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return result, err
	}
	for rows.Next() {
		values := make([]sql.NullString, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return result, err
		}
		result.Values = append(result.Values, values)
		if len(result.Values) > thresholdRows+1 {
			return result, fmt.Errorf("fixture exceeded snapshot bound")
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if rows.NextResultSet() {
		return result, fmt.Errorf("unexpected snapshot result set")
	}
	return result, rows.Err()
}

func dispatched(rows []wireRow) bool {
	for _, r := range rows {
		if r.Stage == "EXECUTED" {
			return true
		}
	}
	return false
}

func main() { os.Exit(run()) }

func run() int {
	enabled := flag.Bool("run", false, "authorize this tool's own homelab fixtures only")
	port := flag.Int("frontend-port", 4005, "isolated frontend, never shared 4000/4001")
	phase := flag.String("phase", "warn", "warn or error: expected level for large-table offline DDL")
	flag.Parse()
	hostname, _ := os.Hostname()
	audit := os.Getenv("SINGLE_DDL_OB_PASSWORD")
	admin := os.Getenv("SINGLE_DDL_OB_ADMIN_PASSWORD")
	if !*enabled || hostname != "dba-app" || audit == "" || admin == "" || *port < 4002 || *port > 65535 {
		fmt.Fprintln(os.Stderr, "requires dba-app, --run, dedicated port, both private credential environment variables")
		return 2
	}
	expectedLevel := 1
	if *phase == "error" {
		expectedLevel = 2
	} else if *phase != "warn" {
		fmt.Fprintln(os.Stderr, "phase must be warn or error")
		return 2
	}
	if err := frontendPolicy(*port, *phase == "error"); err != nil {
		fmt.Fprintln(os.Stderr, "frontend policy preflight failed:", err)
		return 1
	}
	user := "root@dba#homelab_ocean_test"

	c := mysql.NewConfig()
	c.Net, c.Addr = "tcp", fmt.Sprintf("%s:%d", obHost, obPort)
	c.User, c.Passwd, c.DBName = user, admin, "test"
	c.Timeout = 5 * time.Second
	c.ReadTimeout = 150 * time.Second
	c.Params = map[string]string{"charset": "utf8mb4"}
	db, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open failed")
		return 1
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec("SET SESSION ob_query_timeout=120000000, ob_trx_timeout=120000000"); err != nil {
		fmt.Fprintln(os.Stderr, "fixture session budget unavailable")
		return 1
	}
	var version, database, currentUser, serverHost string
	var tenant int
	err = db.QueryRow("SELECT VERSION(),DATABASE(),CURRENT_USER(),@@hostname,(SELECT tenant_id FROM oceanbase.DBA_OB_TENANTS WHERE tenant_name='dba')").Scan(&version, &database, &currentUser, &serverHost, &tenant)
	if err != nil || database != "test" || tenant != 1002 || !strings.Contains(version, "OceanBase") {
		fmt.Fprintln(os.Stderr, "wrong homelab business endpoint")
		return 1
	}

	random := make([]byte, 5)
	if _, err := rand.Read(random); err != nil {
		return 1
	}
	prefix := "ddl_policy_1109_" + hex.EncodeToString(random)
	big, small := prefix+"_big", prefix+"_small"
	empty, above := prefix+"_empty", prefix+"_above"
	fixtures := []struct {
		name string
		rows int
	}{{empty, 0}, {small, thresholdRows - 1}, {big, thresholdRows}, {above, thresholdRows + 1}}
	var owned []string
	defer func() {
		for _, name := range owned {
			_, _ = db.Exec("DROP TABLE IF EXISTS `" + name + "`")
		}
	}()

	var tests []evidence
	add := func(name string, ok bool, elapsed int64, rows []wireRow, detail any) {
		tests = append(tests, evidence{Name: name, Passed: ok, Elapsed: elapsed, Rows: rows, Detail: detail})
	}

	// Tiny owned fixtures deliberately keep catalog size at a valid zero. No
	// freezes, merges, statistics refreshes or tenant-wide maintenance are allowed.
	ready := true
	for _, f := range fixtures {
		if _, err := db.Exec("CREATE TABLE `" + f.name + "` (id BIGINT NOT NULL, v VARCHAR(180) NOT NULL, dropme INT NOT NULL DEFAULT 0) COMMENT='owned offline policy fixture'"); err != nil {
			fmt.Fprintln(os.Stderr, "fixture create failed")
			return 1
		}
		owned = append(owned, f.name) // Never drop a table whose CREATE failed.
		if f.rows > 0 {
			if _, err := db.Exec(fixtureInsert(f.name, f.rows)); err != nil {
				fmt.Fprintln(os.Stderr, "fixture fill failed")
				return 1
			}
		}
		size, sizeErr := tableSizeMB(db, f.name)
		n, probeErr := probeRows(db, f.name)
		var estimate *int64
		estimateErr := db.QueryRow("SELECT TABLE_ROWS FROM information_schema.TABLES WHERE TABLE_SCHEMA='test' AND TABLE_NAME=?", f.name).Scan(&estimate)
		ok := sizeErr == nil && size == 0 && probeErr == nil && n == f.rows && estimateErr == nil
		ready = ready && ok
		add(fmt.Sprintf("fixture_%d_rows_zero_MB", f.rows), ok, 0, nil, map[string]any{
			"tableMB": size, "thresholdMB": thresholdMB, "thresholdRows": thresholdRows,
			"rowsInserted": f.rows, "strongProbeRows": n, "estimatedRows": estimate,
			"sizeError": errString(sizeErr), "probeError": errString(probeErr), "estimateError": errString(estimateErr),
		})
	}
	if !ready {
		_ = json.NewEncoder(os.Stdout).Encode(tests)
		fmt.Fprintln(os.Stderr, "zero-size row fixtures not proven; refusing native requests")
		return 1
	}

	// OBProxy may return an empty @@hostname; capture actual observer addresses.
	var observerIPs string
	if err := db.QueryRow("SELECT GROUP_CONCAT(DISTINCT svr_ip ORDER BY svr_ip) FROM oceanbase.DBA_OB_TABLE_LOCATIONS WHERE database_name='test' AND table_name=?", empty).Scan(&observerIPs); err != nil || observerIPs == "" {
		fmt.Fprintln(os.Stderr, "observer address verification failed")
		return 1
	}

	// Classic CHECK proves all four boundaries before any native request.
	// TABLE_ROWS is deliberately not used as evidence of a small table.
	for _, f := range fixtures {
		rows, ms, err := frame(*port, user, admin, false, 0, "ALTER TABLE `"+f.name+"` ADD PRIMARY KEY(id);", "--enable-check;")
		wantLevel := 0
		if f.rows >= thresholdRows {
			wantLevel = expectedLevel
		}
		ok := err == nil && ddlRow(rows) != nil && maxLevel(rows) == wantLevel &&
			hasMessage(rows, "offline DDL on a large table") == (f.rows >= thresholdRows) && !dispatched(rows)
		ready = ready && ok
		add(fmt.Sprintf("classic_offline_%d_rows", f.rows), ok, ms, rows, map[string]any{"expectedLevel": wantLevel, "error": errString(err)})
	}
	if !ready {
		_ = json.NewEncoder(os.Stdout).Encode(tests)
		fmt.Fprintln(os.Stderr, "row policy boundary checks failed; refusing native requests")
		return 1
	}

	// Large online DDL remains CHECK-only: no large-fixture native mutation.
	rows, ms, err := frame(*port, user, admin, false, 0, "ALTER TABLE `"+big+"` ADD COLUMN added INT NOT NULL DEFAULT 0 COMMENT 'online probe';", "--enable-check;")
	add("classic_large_online", err == nil && ddlRow(rows) != nil && maxLevel(rows) == 0 && !hasMessage(rows, "offline DDL on a large table") && !dispatched(rows), ms, rows, map[string]any{"error": errString(err)})

	// Native offline requests: 1000/1001 must reject without changing schema or
	// any value; empty/999 may execute and must preserve every row/value.
	for _, f := range fixtures {
		before, beforeErr := snapshot(db, f.name)
		if beforeErr != nil {
			add(fmt.Sprintf("native_offline_%d_rows", f.rows), false, 0, nil, map[string]any{"beforeError": errString(beforeErr)})
			continue // No request without a verifiable baseline.
		}
		rows, ms, err := frame(*port, user, admin, true, 60, "ALTER TABLE `"+f.name+"` ADD PRIMARY KEY(id);", "--enable-execute;")
		after, afterErr := snapshot(db, f.name)
		pk, pkErr := hasPrimaryKey(db, f.name)
		valuesUnchanged := afterErr == nil && reflect.DeepEqual(before.Values, after.Values)
		schemaUnchanged := afterErr == nil && before.Schema == after.Schema
		ok := err == nil && afterErr == nil && pkErr == nil && valuesUnchanged
		if f.rows >= thresholdRows {
			ok = ok && ddlRow(rows) != nil && maxLevel(rows) == 2 && hasMessage(rows, "single_ddl_offline_ddl_not_supported") && !dispatched(rows) && !pk && schemaUnchanged
		} else {
			ok = ok && executedOK(rows) && pk && !schemaUnchanged
		}
		add(fmt.Sprintf("native_offline_%d_rows", f.rows), ok, ms, rows, map[string]any{
			"dispatched": dispatched(rows), "primaryKeyAdded": pk, "schemaUnchanged": schemaUnchanged, "valuesUnchanged": valuesUnchanged,
			"error": errString(err), "afterError": errString(afterErr), "schemaError": errString(pkErr),
		})
	}

	// 7. 既有规则按门槛分级: 小表告警, 大表报错。
	rows, ms, err = frame(*port, user, admin, false, 0, "ALTER TABLE `"+small+"` DROP COLUMN dropme;", "--enable-check;")
	add("legacy_small_drop_column_warns", err == nil && maxLevel(rows) == 1 && hasMessage(rows, "Can't drop column"), ms, rows, map[string]any{"expectedLevel": 1, "error": errString(err)})

	rows, ms, err = frame(*port, user, admin, false, 0, "ALTER TABLE `"+big+"` DROP COLUMN dropme;", "--enable-check;")
	largeDrop := hasMessage(rows, "Can't drop column") || hasMessage(rows, "offline DDL on a large table")
	add("legacy_large_drop_column_errors", err == nil && ddlRow(rows) != nil && maxLevel(rows) == 2 && largeDrop && !dispatched(rows), ms, rows, map[string]any{"expectedLevel": 2, "error": errString(err)})

	rows, ms, err = frame(*port, user, admin, false, 0, "ALTER TABLE `"+small+"` MODIFY COLUMN v VARCHAR(90);", "--enable-check;")
	add("legacy_small_narrowing_warns", err == nil && maxLevel(rows) == 1 && hasMessage(rows, "varchar(180) -> varchar(90)"), ms, rows, map[string]any{"expectedLevel": 1, "error": errString(err)})

	rows, ms, err = frame(*port, user, admin, false, 0, "ALTER TABLE `"+big+"` MODIFY COLUMN v VARCHAR(90);", "--enable-check;")
	// as_error=true 时大表策略错误会先短路, 类型变更文案可能不出现。
	largeNarrow := hasMessage(rows, "varchar(180) -> varchar(90)") || hasMessage(rows, "offline DDL on a large table")
	add("legacy_large_narrowing_errors", err == nil && maxLevel(rows) == 2 && largeNarrow, ms, rows, map[string]any{"expectedLevel": 2, "error": errString(err)})

	// The boundary fixtures have no TEXT/BLOB fields. Keep cross-family probes
	// on a separate owned small table, reusing its three independent columns.
	conversion := prefix + "_types"
	if _, err := db.Exec("CREATE TABLE `" + conversion + "` (id INT NOT NULL PRIMARY KEY, txt TEXT NOT NULL, bin BLOB NOT NULL, num INT NOT NULL) COMMENT='owned type conversion fixture'"); err != nil {
		fmt.Fprintln(os.Stderr, "type fixture create failed")
		return 1
	}
	owned = append(owned, conversion)
	if _, err := db.Exec("INSERT INTO `" + conversion + "` VALUES (1,'hello',X'776F726C64',123)"); err != nil {
		fmt.Fprintln(os.Stderr, "type fixture fill failed")
		return 1
	}
	typeSize, typeSizeErr := tableSizeMB(db, conversion)
	typeRows, typeProbeErr := probeRows(db, conversion)
	typeReady := typeSizeErr == nil && typeSize < thresholdMB && typeProbeErr == nil && typeRows == 1
	add("fixture_cross_family_small", typeReady, 0, nil, map[string]any{"tableMB": typeSize, "strongProbeRows": typeRows, "sizeError": errString(typeSizeErr), "probeError": errString(typeProbeErr)})
	if !typeReady {
		_ = json.NewEncoder(os.Stdout).Encode(tests)
		return 1
	}
	for _, change := range []struct {
		name, column, definition, wantType string
	}{
		{"text_to_blob", "txt", "BLOB NOT NULL", "blob"},
		{"blob_to_text", "bin", "TEXT NOT NULL", "text"},
		{"int_to_varchar", "num", "VARCHAR(20) NOT NULL", "varchar(20)"},
	} {
		body := "ALTER TABLE `" + conversion + "` MODIFY COLUMN `" + change.column + "` " + change.definition + ";"
		rows, ms, err := frame(*port, user, admin, false, 0, body, "--enable-check;")
		add("classic_small_"+change.name, err == nil && ddlRow(rows) != nil && maxLevel(rows) == 1 && hasMessage(rows, "Type conversion warning for column '"+conversion+"."+change.column+"'") && !hasMessage(rows, "offline DDL on a large table") && !dispatched(rows), ms, rows, map[string]any{"error": errString(err)})

		before, beforeErr := snapshot(db, conversion)
		if beforeErr != nil {
			add("native_small_"+change.name, false, 0, nil, map[string]any{"beforeError": errString(beforeErr)})
			continue
		}
		rows, ms, err = frame(*port, user, admin, true, 60, body, "--enable-execute;")
		after, afterErr := snapshot(db, conversion)
		var columnType string
		typeErr := db.QueryRow("SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='test' AND TABLE_NAME=? AND COLUMN_NAME=?", conversion, change.column).Scan(&columnType)
		valuesUnchanged := afterErr == nil && reflect.DeepEqual(before.Values, after.Values)
		add("native_small_"+change.name, err == nil && executedOK(rows) && afterErr == nil && typeErr == nil && columnType == change.wantType && before.Schema != after.Schema && valuesUnchanged, ms, rows, map[string]any{
			"columnType": columnType, "valuesUnchanged": valuesUnchanged, "error": errString(err), "afterError": errString(afterErr), "schemaError": errString(typeErr),
		})
	}

	// Small offline DROP COLUMN: verify removal and all surviving values.
	beforeDrop, beforeDropErr := snapshot(db, small)
	if beforeDropErr != nil {
		add("native_small_drop_column_executes", false, 0, nil, map[string]any{"beforeError": errString(beforeDropErr)})
	} else {
		rows, ms, err = frame(*port, user, admin, true, 60, "ALTER TABLE `"+small+"` DROP COLUMN dropme;", "--enable-execute;")
		dropped, schemaErr := columnExists(db, small, "dropme")
		afterDrop, afterErr := snapshot(db, small)
		for i, values := range beforeDrop.Values {
			beforeDrop.Values[i] = append(values[:2:2], values[3:]...)
		}
		valuesUnchanged := afterErr == nil && reflect.DeepEqual(beforeDrop.Values, afterDrop.Values)
		add("native_small_drop_column_executes", err == nil && executedOK(rows) && schemaErr == nil && !dropped && beforeDrop.Schema != afterDrop.Schema && valuesUnchanged, ms, rows, map[string]any{
			"dropmeStillExists": dropped, "valuesUnchanged": valuesUnchanged, "error": errString(err), "afterError": errString(afterErr), "schemaError": errString(schemaErr),
		})
	}

	tests = append(tests, enumSetChecks(db, *port, expectedLevel, user, admin, prefix, &owned)...)

	// Fixed audit identity may be blocked by authentication/catalog privileges.
	// Record the actual blocker; never change credentials or grants.
	rows, ms, err = frame(*port, "inception@dba#homelab_ocean_test", audit, false, 0, "ALTER TABLE `"+big+"` ADD COLUMN audit_probe INT NOT NULL DEFAULT 0;", "--enable-check;")
	blocked := hasMessage(rows, "command denied") || hasMessage(rows, "Access denied")
	tests = append(tests, evidence{Name: "audit_identity_existing_alter", Passed: err == nil && !blocked && maxLevel(rows) < 2, Blocked: blocked, Elapsed: ms, Rows: rows, Detail: map[string]any{"error": errString(err)}})

	var remaining []string
	for _, name := range owned {
		_, cleanupErr := db.Exec("DROP TABLE IF EXISTS `" + name + "`")
		add("cleanup_"+name, cleanupErr == nil, 0, nil, map[string]any{"error": errString(cleanupErr)})
		if cleanupErr != nil {
			remaining = append(remaining, name)
		}
	}
	owned = remaining // Deferred cleanup retries only our failed drops.

	all := true
	for _, t := range tests {
		if !t.Blocked {
			all = all && t.Passed
		}
	}
	out := struct {
		Timestamp   string     `json:"timestamp"`
		Target      string     `json:"target"`
		ServerHost  string     `json:"serverHost"`
		ObserverIPs string     `json:"observerIps"`
		Version     string     `json:"version"`
		User        string     `json:"businessAdminIdentity"`
		Tenant      int        `json:"tenantId"`
		Phase       string     `json:"phase"`
		Fixture     string     `json:"fixturePrefix"`
		AllPassed   bool       `json:"allPassed"`
		Cases       []evidence `json:"cases"`
	}{time.Now().UTC().Format(time.RFC3339), fmt.Sprintf("%s:%d", obHost, obPort), serverHost, observerIPs, version, currentUser, tenant, *phase, prefix, all, tests}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	if !all {
		return 1
	}
	return 0
}
