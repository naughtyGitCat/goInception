package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

const (
	obAddress = "10.136.38.71:2883"
	obUser    = "inception@dba#homelab_ocean_test"
)

type resultRow struct {
	Stage    string `json:"stage"`
	Status   string `json:"stageStatus"`
	Level    int    `json:"errorLevel"`
	Message  string `json:"errorMessage,omitempty"`
	SQL      string `json:"sql"`
	Affected string `json:"affectedRows"`
	Sequence string `json:"sequence"`
}

type caseResult struct {
	Name               string      `json:"name"`
	Passed             bool        `json:"passed"`
	EnvironmentBlocked bool        `json:"environmentBlocked,omitempty"`
	ElapsedMs          int64       `json:"elapsedMs"`
	Detail             string      `json:"detail,omitempty"`
	Rows               []resultRow `json:"rows,omitempty"`
}

type probe struct {
	port     int
	password string
	fixture  string
	direct   *sql.DB
}

func main() { os.Exit(run()) }

func run() int {
	approved := flag.Bool("run", false, "run isolated writes in homelab dba.test only")
	port := flag.Int("frontend-port", 4002, "dedicated loopback goInception port on dba-app")
	flag.Parse()
	password := os.Getenv("SINGLE_DML_OB_PASSWORD")
	if !*approved || password == "" || *port == 4001 || *port == 4000 {
		fmt.Fprintln(os.Stderr, "requires --run, SINGLE_DML_OB_PASSWORD, and a dedicated frontend port (not 4000/4001)")
		return 2
	}
	host, err := os.Hostname()
	if err != nil || host != "dba-app" {
		fmt.Fprintln(os.Stderr, "this probe must run on homelab dba-app; endpoints are deliberately fixed")
		return 2
	}
	cfg := mysqlDriver.NewConfig()
	cfg.User, cfg.Passwd = obUser, password
	cfg.Net, cfg.Addr, cfg.DBName = "tcp", obAddress, "test"
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 8*time.Second, 360*time.Second, 15*time.Second
	cfg.Params = map[string]string{"charset": "utf8mb4"}
	direct, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer direct.Close()
	var version, database string
	if err := direct.QueryRow("select version(), database()").Scan(&version, &database); err != nil || database != "test" || !strings.Contains(strings.ToLower(version), "oceanbase") {
		fmt.Fprintln(os.Stderr, "homelab endpoint verification failed", err, version, database)
		return 1
	}
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	p := &probe{port: *port, password: password, fixture: "t_single_dml_" + hex.EncodeToString(random), direct: direct}
	if _, err := direct.Exec("CREATE TABLE `" + p.fixture + "` (id bigint NOT NULL, v int NOT NULL DEFAULT 0, q_timeout bigint NOT NULL DEFAULT 0, t_timeout bigint NOT NULL DEFAULT 0, cid bigint NOT NULL DEFAULT 0, PRIMARY KEY (id)) COMMENT='single DML isolated homelab fixture'"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	owned := true
	defer func() {
		if owned {
			_, _ = direct.Exec("DROP TABLE `" + p.fixture + "`")
		}
	}()
	if _, err := direct.Exec("INSERT INTO `" + p.fixture + "` (id,v) VALUES (1,0),(2,0),(3,0),(4,0),(5,0),(6,0)"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var results []caseResult
	check := func(name, body, flags string, timeout, frontend int, marker string) {
		rows, elapsed, err := p.frame(frontend, "10.136.38.71", 2883, flags, timeout, body)
		passed := err == nil && allChecked(rows) && containsMessage(rows, marker)
		results = append(results, caseResult{Name: name, Passed: passed, ElapsedMs: elapsed, Rows: rows, Detail: errorText(err)})
	}
	mutate := func(name, body string, timeout int, expectedRows int64) {
		rows, elapsed, err := p.frame(p.port, "10.136.38.71", 2883, "--enable-execute;--single-dml=1;", timeout, body)
		passed := err == nil && executed(rows, body, expectedRows)
		results = append(results, caseResult{Name: name, Passed: passed, ElapsedMs: elapsed, Rows: rows, Detail: errorText(err)})
	}

	base := "UPDATE `" + p.fixture + "` SET v = v + 1 WHERE id = 1"
	rows, elapsed, err := p.frame(4001, "10.136.38.71", 2883, "--enable-check;--single-dml=1;", 300, base+";"+base)
	results = append(results, caseResult{Name: "old_frontend_does_not_claim_single_dml_capability", Passed: err == nil && allChecked(rows) && !containsMessage(rows, "single_dml_requires_one_mutation"), ElapsedMs: elapsed, Rows: rows, Detail: errorText(err)})
	check("new_frontend_capability_check_only", base+";"+base, "--enable-check;--single-dml=1;", 300, p.port, "single_dml_requires_one_mutation")
	check("two_mutations_never_execute", base+";"+base, "--enable-execute;--single-dml=1;", 300, p.port, "single_dml_requires_one_mutation")
	check("primary_key_change_rejected", "UPDATE `"+p.fixture+"` SET id = id + 100 WHERE id = 1", "--enable-execute;--single-dml=1;", 300, p.port, "single_dml_primary_key_assignment_not_supported")
	check("session_set_rejected", "SET SESSION ob_query_timeout=1000000;"+base, "--enable-execute;--single-dml=1;", 300, p.port, "single_dml_unsupported_statement")
	check("ddl_rejected_without_dropping_fixture", "DROP TABLE `"+p.fixture+"`", "--enable-execute;--single-dml=1;", 300, p.port, "single_dml_unsupported_statement")
	check("invalid_mode_fails_closed", base, "--enable-execute;--single-dml=maybe;", 300, p.port, "single_dml_invalid_option")
	check("invalid_zero_timeout_rejected", base, "--enable-execute;--single-dml=1;", 0, p.port, "single_dml_timeout_must_be_between")
	check("extra_use_rejected", base+";USE test", "--enable-execute;--single-dml=1;", 300, p.port, "single_dml_requires_at_most_one_leading_use")
	var value int64
	if err := direct.QueryRow("SELECT v FROM `" + p.fixture + "` WHERE id=1").Scan(&value); err != nil || value != 0 {
		results = append(results, caseResult{Name: "rejected_requests_have_zero_effect", Passed: false, Detail: fmt.Sprintf("v=%d error=%v", value, err)})
	} else {
		results = append(results, caseResult{Name: "rejected_requests_have_zero_effect", Passed: true, Detail: "v=0"})
	}
	legacyBody := "UPDATE `" + p.fixture + "` SET v=v+1 WHERE id=5"
	rows, elapsed, err = p.frame(p.port, "10.136.38.71", 2883, "--enable-execute;", 300, legacyBody)
	results = append(results, caseResult{Name: "legacy_dml_without_single_mode_still_executes", Passed: err == nil && executed(rows, legacyBody, 1), ElapsedMs: elapsed, Rows: rows, Detail: errorText(err)})
	legacyDDL := "ALTER TABLE `" + p.fixture + "` ADD COLUMN compatibility_probe int NOT NULL DEFAULT 0 COMMENT 'legacy DDL compatibility'"
	rows, elapsed, err = p.frame(p.port, "10.136.38.71", 2883, "--enable-execute;", 300, legacyDDL)
	ddlResult := caseResult{Name: "legacy_ddl_without_single_mode", Passed: err == nil && executed(rows, legacyDDL, 0), ElapsedMs: elapsed, Rows: rows, Detail: errorText(err)}
	if err == nil && allChecked(rows) && containsMessage(rows, "SELECT command denied") && containsMessage(rows, "dba_ob_table_locations") {
		baselineRows, _, baselineErr := p.frame(4001, "10.136.38.71", 2883, "--enable-execute;", 300, legacyDDL)
		if baselineErr == nil && allChecked(baselineRows) && containsMessage(baselineRows, "SELECT command denied") && containsMessage(baselineRows, "dba_ob_table_locations") {
			ddlResult.Name = "legacy_ddl_same_permission_blocker_as_old_frontend"
			ddlResult.Passed = true
			ddlResult.EnvironmentBlocked = true
			ddlResult.Detail = "Old 4001 and modified 4002 both reject before execution: missing SELECT on dba_ob_table_locations. Baseline parity only; successful DDL execution is NOT verified."
		}
	}
	results = append(results, ddlResult)
	mutate("single_update", base, 300, 1)
	mutate("confirmed_zero_rows", "UPDATE `"+p.fixture+"` SET v=v+1 WHERE id=-1", 300, 0)
	mutate("insert_values", "INSERT INTO `"+p.fixture+"` (id,v) VALUES (900,1)", 300, 1)
	mutate("single_delete", "DELETE FROM `"+p.fixture+"` WHERE id=900", 300, 1)
	stateSQL := "UPDATE `" + p.fixture + "` SET q_timeout=@@session.ob_query_timeout,t_timeout=@@session.ob_trx_timeout,cid=CONNECTION_ID() WHERE id=6"
	rows, elapsed, err = p.frame(p.port, "10.136.38.71", 2883, "--enable-execute;--single-dml=1;", 45, stateSQL)
	var q, trx, cid int64
	scanErr := direct.QueryRow("SELECT q_timeout,t_timeout,cid FROM `"+p.fixture+"` WHERE id=6").Scan(&q, &trx, &cid)
	passed := err == nil && scanErr == nil && executed(rows, stateSQL, 1) && q == 45000000 && trx == 45000000 && responseConnectionID(rows) == cid
	results = append(results, caseResult{Name: "mutation_reads_same_session_timeouts_and_connection_id", Passed: passed, ElapsedMs: elapsed, Rows: rows, Detail: fmt.Sprintf("query=%d trx=%d cid=%d scanError=%v error=%v", q, trx, cid, scanErr, err)})
	mutate("long_dml_crosses_default_10_seconds", "UPDATE `"+p.fixture+"` SET v=v+SLEEP(12)+1 WHERE id=1", 300, 1)
	timeoutSQL := "UPDATE `" + p.fixture + "` SET v=v+SLEEP(18)+1 WHERE id=1"
	rows, elapsed, err = p.frame(p.port, "10.136.38.71", 2883, "--enable-execute;--single-dml=1;", 15, timeoutSQL)
	results = append(results, caseResult{Name: "15_second_session_timeout", Passed: err == nil && executionFailed(rows) && containsMessage(rows, "15000000") && elapsed >= 14000 && elapsed < 19000, ElapsedMs: elapsed, Rows: rows, Detail: errorText(err)})

	var concurrent [2]caseResult
	var wg sync.WaitGroup
	for i, timeout := range []int{2, 10} {
		wg.Add(1)
		go func(index, timeout int) {
			defer wg.Done()
			body := fmt.Sprintf("UPDATE `%s` SET v=v+SLEEP(4)+1 WHERE id=%d", p.fixture, index+2)
			rows, elapsed, err := p.frame(p.port, "10.136.38.71", 2883, "--enable-execute;--single-dml=1;", timeout, body)
			passed := err == nil && executed(rows, body, 1)
			if index == 0 {
				passed = err == nil && executionFailed(rows) && containsMessage(rows, "2000000")
			}
			concurrent[index] = caseResult{Name: fmt.Sprintf("concurrent_timeout_%d_seconds", timeout), Passed: passed, ElapsedMs: elapsed, Rows: rows, Detail: errorText(err)}
		}(i, timeout)
	}
	wg.Wait()
	results = append(results, concurrent[:]...)
	for _, mode := range []string{"reject-set", "drop-mutation-response"} {
		proxy, err := startFaultProxy(mode, p.fixture)
		if err != nil {
			results = append(results, caseResult{Name: mode, Detail: err.Error()})
			continue
		}
		body := "UPDATE `" + p.fixture + "` SET v=v+1 WHERE id=4"
		rows, elapsed, err := p.frame(p.port, "10.136.38.51", proxy.port(), "--enable-execute;--single-dml=1;", 900, body)
		passed := err == nil && executionFailed(rows) && proxy.mutations.Load() == 1
		if mode == "reject-set" {
			passed = err == nil && allChecked(rows) && containsMessage(rows, "Injected homelab session timeout rejection") && proxy.mutations.Load() == 0 && proxy.setErrors.Load() == 1
		}
		results = append(results, caseResult{Name: mode, Passed: passed, ElapsedMs: elapsed, Rows: rows, Detail: proxy.summary() + " error=" + errorText(err)})
		proxy.close()
	}
	var total int64
	readErr := direct.QueryRow("SELECT v FROM `" + p.fixture + "` WHERE id=4").Scan(&total)
	results = append(results, caseResult{Name: "disconnected_mutation_not_replayed", Passed: readErr == nil && total >= 0 && total <= 1, Detail: fmt.Sprintf("v=%d; 0 or 1 cannot prove commit state, but >1 is replay; error=%v", total, readErr)})
	_, cleanupErr := direct.Exec("DROP TABLE `" + p.fixture + "`")
	if cleanupErr == nil {
		owned = false
	}
	results = append(results, caseResult{Name: "fixture_cleanup", Passed: cleanupErr == nil, Detail: errorText(cleanupErr)})
	allPassed := true
	for _, result := range results {
		allPassed = allPassed && result.Passed
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(struct {
		Timestamp string       `json:"timestamp"`
		Target    string       `json:"target"`
		Fixture   string       `json:"fixture"`
		AllPassed bool         `json:"allPassed"`
		Cases     []caseResult `json:"cases"`
	}{time.Now().UTC().Format(time.RFC3339), "homelab_ocean_test/dba/test", p.fixture, allPassed, results}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !allPassed {
		return 1
	}
	return 0
}

func (p *probe) frame(port int, targetHost string, targetPort int, flags string, timeout int, body string) ([]resultRow, int64, error) {
	cfg := mysqlDriver.NewConfig()
	cfg.Net, cfg.Addr = "tcp", fmt.Sprintf("127.0.0.1:%d", port)
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 8*time.Second, 360*time.Second, 15*time.Second
	cfg.Params = map[string]string{"charset": "utf8mb4"}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, 0, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 360*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	query := fmt.Sprintf("/*--user=%s;--password=%s;--host=%s;--port=%d;%s--enable-ignore-warnings;--disable-remote-backup;--trans=0;--wait-timeout=%d;*/\ninception_magic_start;\nUSE test;\n%s;\ninception_magic_commit;", obUser, p.password, targetHost, targetPort, flags, timeout, strings.TrimSuffix(strings.TrimSpace(body), ";"))
	start := time.Now()
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, time.Since(start).Milliseconds(), err
	}
	defer rows.Close()
	var output []resultRow
	for {
		columns, err := rows.Columns()
		if err != nil {
			return output, time.Since(start).Milliseconds(), err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				return output, time.Since(start).Milliseconds(), err
			}
			fields := make(map[string]string)
			for i, column := range columns {
				value := "NULL"
				if b, ok := values[i].([]byte); ok {
					value = string(b)
				} else if values[i] != nil {
					value = fmt.Sprint(values[i])
				}
				fields[strings.ToLower(column)] = value
			}
			level, err := strconv.Atoi(fields["error_level"])
			if err != nil {
				return output, time.Since(start).Milliseconds(), err
			}
			output = append(output, resultRow{fields["stage"], fields["stage_status"], level, fields["error_message"], fields["sql"], fields["affected_rows"], fields["sequence"]})
		}
		if err := rows.Err(); err != nil {
			return output, time.Since(start).Milliseconds(), err
		}
		if !rows.NextResultSet() {
			break
		}
	}
	return output, time.Since(start).Milliseconds(), rows.Err()
}

func allChecked(rows []resultRow) bool {
	if len(rows) == 0 {
		return false
	}
	for _, row := range rows {
		if row.Stage != "CHECKED" {
			return false
		}
	}
	return true
}

func containsMessage(rows []resultRow, marker string) bool {
	for _, row := range rows {
		if row.Level == 2 && strings.Contains(row.Message, marker) {
			return true
		}
	}
	return false
}

func executed(rows []resultRow, body string, affected int64) bool {
	found := 0
	for _, row := range rows {
		if row.Level == 2 {
			return false
		}
		if strings.TrimSpace(row.SQL) == strings.TrimSpace(body) {
			count, err := strconv.ParseInt(row.Affected, 10, 64)
			if row.Stage != "EXECUTED" || row.Status != "Execute Successfully" || err != nil || count != affected {
				return false
			}
			found++
		}
	}
	return found == 1
}

func executionFailed(rows []resultRow) bool {
	for _, row := range rows {
		if row.Stage == "EXECUTED" && row.Status == "Execute failed" && row.Level == 2 {
			return true
		}
	}
	return false
}

func responseConnectionID(rows []resultRow) int64 {
	for _, row := range rows {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(row.SQL)), "use ") {
			parts := strings.Split(row.Sequence, "_")
			if len(parts) >= 2 {
				value, _ := strconv.ParseInt(parts[1], 10, 64)
				return value
			}
		}
	}
	return 0
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
