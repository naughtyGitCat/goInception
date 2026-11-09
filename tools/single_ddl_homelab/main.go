// Package main exercises an isolated goInception socket against homelab's business tenant.
// It never connects to sys, changes grants, replaces shared services, or replays uncertain DDL.
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
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

const target = "10.136.38.71:2883"

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
type probe struct {
	port           int
	user, password string
}

func frame(port int, user, password, host string, targetPort, seconds int, body, flags string) ([]wireRow, int64, error) {
	c := mysql.NewConfig()
	c.Net = "tcp"
	c.Addr = fmt.Sprintf("10.136.38.51:%d", port)
	c.Timeout = 5 * time.Second
	c.ReadTimeout = 150 * time.Second
	c.WriteTimeout = 5 * time.Second
	db, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		return nil, 0, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	q := fmt.Sprintf("/*--user=%s;--password=%s;--host=%s;--port=%d;--single-ddl=1;--disable-remote-backup;--trans=0;--wait-timeout=%d;--enable-ignore-warnings;%s*/\ninception_magic_start;\nUSE `test`;\n%s\ninception_magic_commit;", quote(user), quote(password), quote(host), targetPort, seconds, flags, body)
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
func (p probe) request(body, flags string, t int) ([]wireRow, int64, error) {
	return frame(p.port, p.user, p.password, "10.136.38.71", 2883, t, body, flags)
}
func success(rows []wireRow) bool {
	if len(rows) != 2 {
		return false
	}
	for _, r := range rows {
		if r.Stage != "EXECUTED" || r.Status != "Execute Successfully" || r.Level == 2 {
			return false
		}
	}
	return true
}
func rejected(rows []wireRow, marker string) bool {
	found := false
	for _, r := range rows {
		if r.Stage != "CHECKED" {
			return false
		}
		found = found || (r.Level == 2 && r.Error != nil && strings.Contains(*r.Error, marker))
	}
	return found
}
func main() { os.Exit(run()) }
func run() int {
	enabled := flag.Bool("run", false, "authorize this tool's new homelab fixture objects only")
	port := flag.Int("frontend-port", 4005, "isolated frontend, never shared4000/4001")
	flag.Parse()
	hostname, _ := os.Hostname()
	audit := os.Getenv("SINGLE_DDL_OB_PASSWORD")
	admin := os.Getenv("SINGLE_DDL_OB_ADMIN_PASSWORD")
	if !*enabled || hostname != "dba-app" || audit == "" || admin == "" || *port < 4002 {
		fmt.Fprintln(os.Stderr, "requires dba-app, --run, dedicated port, both private credential environment variables")
		return 2
	}
	p := probe{*port, "inception@dba#homelab_ocean_test", audit}
	priv := probe{*port, "root@dba#homelab_ocean_test", admin}
	c := mysql.NewConfig()
	c.Net = "tcp"
	c.Addr = target
	c.User = priv.user
	c.Passwd = admin
	c.DBName = "test"
	c.Timeout = 5 * time.Second
	c.ReadTimeout = 150 * time.Second
	c.Params = map[string]string{"charset": "utf8mb4"}
	db, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open failed")
		return 1
	}
	defer db.Close()
	// Fixture preparation has its own explicit bounded BUSINESS session; no global setting changes.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("SET SESSION ob_query_timeout=120000000, ob_trx_timeout=120000000"); err != nil {
		fmt.Fprintln(os.Stderr, "fixture session budget unavailable")
		return 1
	}
	var version, database, user string
	var tenant int
	err = db.QueryRow("SELECT VERSION(),DATABASE(),CURRENT_USER(),(SELECT tenant_id FROM oceanbase.DBA_OB_TENANTS WHERE tenant_name='dba')").Scan(&version, &database, &user, &tenant)
	if err != nil || database != "test" || tenant != 1002 || !strings.Contains(version, "OceanBase") {
		fmt.Fprintln(os.Stderr, "wrong homelab business endpoint")
		return 1
	}
	random := make([]byte, 5)
	if _, err := rand.Read(random); err != nil {
		return 1
	}
	prefix := "ddl_native_1108_" + hex.EncodeToString(random)
	names := []string{prefix + "_audit", prefix + "_big", prefix + "_set", prefix + "_lost", prefix + "_short"}
	owned := map[string]bool{}
	defer func() {
		for name := range owned {
			_, _ = db.Exec("DROP TABLE IF EXISTS `" + name + "`")
		}
	}()
	var tests []evidence
	add := func(name string, ok bool, elapsed int64, rows []wireRow, detail any) {
		tests = append(tests, evidence{Name: name, Passed: ok, Elapsed: elapsed, Rows: rows, Detail: detail})
	}
	rows, ms, err := p.request("", "--enable-check;--enable-single-ddl-capability;", 7200)
	marker := len(rows) == 1 && rows[0].Error != nil && *rows[0].Error == "ob_single_ddl_native_v1" && rows[0].Stage == "CHECKED" && rows[0].Level == 2
	var errorDetail string
	if err != nil {
		errorDetail = err.Error()
	}
	add("new_capability_7200", err == nil && marker, ms, rows, errorDetail)
	if !marker {
		_ = json.NewEncoder(os.Stdout).Encode(tests)
		return 1
	}
	create := func(name string) string {
		return "CREATE TABLE `" + name + "` (id INT NOT NULL PRIMARY KEY) COMMENT='isolated native DDL probe';"
	}
	rows, ms, err = p.request(create(names[0]), "--enable-execute;", 7200)
	ok := err == nil && success(rows)
	if ok {
		owned[names[0]] = true
	}
	add("audit_identity_create_7200", ok, ms, rows, nil)
	if ok {
		rows, ms, err = p.request("DROP TABLE `"+names[0]+"`;", "--enable-execute;", 60)
		ok = err == nil && success(rows)
		if ok {
			delete(owned, names[0])
		}
		add("audit_identity_drop", ok, ms, rows, nil)
	}
	rows, ms, err = p.request(create(names[2])+create(names[3]), "--enable-execute;", 60)
	add("two_ddl_rejected", err == nil && rejected(rows, "single_ddl"), ms, rows, nil)
	// Fault proxy observes SET and DDL on the SAME frontend-to-OB connection and records timeout readback.
	for i, mode := range []string{"reject-set", "lose-ddl-response"} {
		name := names[i+2]
		proxy, err := startProxy(mode, name)
		if err != nil {
			add(mode, false, 0, nil, "proxy startup failed")
			continue
		}
		rows, ms, err = frame(*port, p.user, p.password, "10.136.38.51", proxy.port(), 7200, create(name), "--enable-execute;")
		proxy.close()
		detail := proxy.snapshot()
		n := detail.DDLCalls
		verified := n == 0 && err == nil && rejected(rows, "Injected")
		if mode == "lose-ddl-response" {
			verified = n == 1
			owned[name] = true
			executedFailed := false
			for _, r := range rows {
				executedFailed = executedFailed || (r.Stage == "EXECUTED" && r.Status == "Execute failed")
			}
			verified = verified && (err != nil || executedFailed)
		}
		add(mode, verified, ms, rows, detail)
	}
	// Build a bounded 250k-row fixture using existing privileged BUSINESS credentials only.
	big := names[1]
	_, err = db.Exec("CREATE TABLE `" + big + "` (id BIGINT NOT NULL PRIMARY KEY, v VARCHAR(180) NOT NULL) COMMENT='owned long native index fixture'")
	if err != nil {
		add("fixture_create", false, 0, nil, err.Error())
	} else {
		owned[big] = true
		for begin := 0; begin < 250000; begin += 1000 {
			var b strings.Builder
			b.WriteString("INSERT INTO `" + big + "` VALUES ")
			for j := 0; j < 1000; j++ {
				if j > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, "(%d,REPEAT('%08d',20))", begin+j, 250000-begin-j)
			}
			if _, err = db.Exec(b.String()); err != nil {
				break
			}
		}
		add("bounded_fixture_250k", err == nil, 0, nil, nil)
		// Bounded expansion to 4,000,000 rows (~640MiB logical value payload) on the owned table only.
		// The previous 250k real index completed in 1.5s and did NOT prove a >10s execution.
		if err == nil {
			for n := 250000; n < 4000000; n *= 2 {
				_, err = db.Exec(fmt.Sprintf("INSERT INTO `%s` SELECT id+%d,v FROM `%s`", big, n, big))
				if err != nil {
					break
				}
			}
			var count int64
			if err == nil {
				err = db.QueryRow("SELECT COUNT(*) FROM `" + big + "`").Scan(&count)
			}
			var fixtureError string
			if err != nil {
				fixtureError = err.Error()
			}
			add("bounded_fixture_4m", err == nil && count == 4000000, 0, nil, map[string]any{"rows": count, "maxRows": 4000000, "error": fixtureError})
		}
		if err == nil {
			// Inception audit permissions are NOT escalated. Existing ALTER dependencies may be blocked.
			sqlText := "ALTER TABLE `" + big + "` ADD COLUMN permission_probe INT NOT NULL DEFAULT 0 COMMENT 'audit identity check';"
			rows, ms, err = p.request(sqlText, "--enable-check;", 60)
			blocked := err == nil && rejected(rows, "command denied")
			tests = append(tests, evidence{Name: "audit_identity_existing_alter", Passed: err == nil && (blocked || !rejected(rows, "")), Blocked: blocked, Elapsed: ms, Rows: rows})
			// CREATE INDEX is a genuine target DDL, not SELECT SLEEP. Measure real server wait.
			proxy, e := startProxy("observe", big)
			if e != nil {
				add("long_index_proxy", false, 0, nil, nil)
			} else {
				rows, ms, err = frame(*port, priv.user, priv.password, "10.136.38.51", proxy.port(), 60, "CREATE INDEX idx_long ON `"+big+"` (v);", "--enable-execute;")
				proxy.close()
				var indexes int
				_ = db.QueryRow("SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='test' AND TABLE_NAME=? AND INDEX_NAME='idx_long'", big).Scan(&indexes)
				add("real_index_create", err == nil && success(rows) && indexes == 1, ms, rows, proxy.snapshot())
				add("real_ddl_exceeds_default_10s", err == nil && success(rows) && ms > 10000, ms, rows, map[string]any{"indexExists": indexes == 1, "note": "not a SLEEP-only test; false means long-duration acceptance remains unproven"})
			}
			rows, ms, err = priv.request("CREATE INDEX idx_short ON `"+big+"` (v);", "--enable-execute;", 1)
			failed := false
			for _, r := range rows {
				failed = failed || (r.Stage == "EXECUTED" && r.Status == "Execute failed")
			}
			add("short_budget_real_index", failed || err != nil, ms, rows, nil)
		}
	}
	for name := range owned {
		_, e := db.Exec("DROP TABLE IF EXISTS `" + name + "`")
		add("cleanup_"+name, e == nil, 0, nil, nil)
		if e == nil {
			delete(owned, name)
		}
	}
	all := true
	for _, t := range tests {
		all = all && t.Passed
	}
	out := struct {
		Timestamp string     `json:"timestamp"`
		Target    string     `json:"target"`
		Version   string     `json:"version"`
		User      string     `json:"businessAdminIdentity"`
		Tenant    int        `json:"tenantId"`
		Fixture   string     `json:"fixturePrefix"`
		AllPassed bool       `json:"allPassed"`
		Cases     []evidence `json:"cases"`
	}{time.Now().UTC().Format(time.RFC3339), target, version, user, tenant, prefix, all, tests}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	if !all {
		return 1
	}
	return 0
}
