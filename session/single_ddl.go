package session

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/hanchuanchuan/goInception/ast"
)

const singleDDLMaxTimeout = 7200
const singleDDLCapabilityMarker = "ob_single_ddl_native_v1"

// YAML must not turn 1.0/1e0 into the integer 1 for this strict opt-in.
func parseSingleDDLInteger(value interface{}) (int, error) {
	switch value.(type) {
	case float32, float64, bool, nil:
		return 0, fmt.Errorf("single_ddl_invalid_integer_option")
	}
	return strconv.Atoi(strings.TrimSpace(fmt.Sprint(value)))
}

func validateSingleDDLOptions(opt *SourceOptions, dbType int) error {
	if opt == nil {
		return fmt.Errorf("single_ddl_missing_options")
	}
	if !opt.SingleDDL {
		if opt.SingleDDLCapability {
			return fmt.Errorf("single_ddl_capability_requires_single_ddl")
		}
		return nil
	}
	if dbType != DBTypeOceanBase {
		return fmt.Errorf("single_ddl_requires_oceanbase")
	}
	// Reject explicit system-tenant identities before opening the target.
	// Direct usernames without tenant syntax retain ordinary source validation.
	user := strings.ToLower(strings.TrimSpace(opt.User))
	if at := strings.LastIndex(user, "@"); at >= 0 {
		tenant := strings.SplitN(user[at+1:], "#", 2)[0]
		if tenant == "sys" {
			return fmt.Errorf("single_ddl_system_tenant_not_supported")
		}
	} else if parts := strings.Split(user, ":"); len(parts) == 3 && parts[1] == "sys" {
		return fmt.Errorf("single_ddl_system_tenant_not_supported")
	}
	if opt.db != "" {
		if err := validateSingleDDLDatabase(opt.db); err != nil {
			return err
		}
	}
	if opt.SingleDML || opt.OBCountProtection || opt.OBCountTimeout != 0 || opt.obCountTimeoutSet || opt.OBCountCapability {
		return fmt.Errorf("single_ddl_conflicting_mode")
	}
	if opt.Backup || opt.tranBatch != 0 {
		return fmt.Errorf("single_ddl_requires_no_backup_and_trans_zero")
	}
	if opt.Check == opt.Execute || opt.Print || opt.QueryPrintSummary || opt.Masking || opt.split || opt.middlewareExtend != "" {
		return fmt.Errorf("single_ddl_requires_check_or_execute")
	}
	if opt.WaitTimeout < 1 || opt.WaitTimeout > singleDDLMaxTimeout {
		return fmt.Errorf("single_ddl_timeout_must_be_between_1_and_%d", singleDDLMaxTimeout)
	}
	if opt.SingleDDLCapability && (!opt.Check || opt.Execute) {
		return fmt.Errorf("single_ddl_capability_requires_check_only")
	}
	return nil
}

func validateSingleDDLDatabase(database string) error {
	switch strings.ToLower(database) {
	case "", "sys", "oceanbase", "mysql", "information_schema", "performance_schema":
		return fmt.Errorf("single_ddl_requires_non_system_database")
	}
	return nil
}

// Scope comes solely from the AST, including references not traversed by the
// parser's Accept methods. This is independent of PRINT summary v1's narrower
// manual-DDL grammar and does not override the ordinary CHECK rules.
type singleDDLScopeVisitor struct {
	database string
	err      error
}

func (v *singleDDLScopeVisitor) table(table *ast.TableName) {
	if table == nil || table.Name.O == "" {
		v.err = fmt.Errorf("single_ddl_invalid_table")
		return
	}
	// Without lower_case_table_names evidence, case folding can cross databases.
	if table.Schema.O != "" && table.Schema.O != v.database {
		v.err = fmt.Errorf("single_ddl_cross_database_not_supported")
	}
}

func (v *singleDDLScopeVisitor) options(options []*ast.TableOption) {
	for _, option := range options {
		if option == nil {
			v.err = fmt.Errorf("single_ddl_invalid_table_option")
			continue
		}
		for _, table := range option.TableNames {
			v.table(table)
		}
	}
}

func (v *singleDDLScopeVisitor) Enter(node ast.Node) (ast.Node, bool) {
	switch n := node.(type) {
	case *ast.TableName:
		v.table(n)
	case *ast.CreateTableStmt:
		v.options(n.Options)
	case *ast.AlterTableSpec:
		v.options(n.Options)
	case *ast.ColumnOption:
		if n.Refer != nil {
			v.table(n.Refer.Table)
		}
	case *ast.ColumnName:
		if n.Schema.O != "" && n.Schema.O != v.database {
			v.err = fmt.Errorf("single_ddl_cross_database_not_supported")
		}
	case *ast.SubqueryExpr, *ast.SelectStmt, *ast.SetOprStmt:
		v.err = fmt.Errorf("single_ddl_select_not_supported")
	}
	return node, v.err != nil
}
func (v *singleDDLScopeVisitor) Leave(node ast.Node) (ast.Node, bool) { return node, v.err == nil }

func validateSingleDDLStatement(node ast.StmtNode, database string) error {
	if use, ok := node.(*ast.UseStmt); ok {
		if use == nil {
			return fmt.Errorf("single_ddl_invalid_use")
		}
		return validateSingleDDLDatabase(use.DBName)
	}
	if err := validateSingleDDLDatabase(database); err != nil {
		return err
	}
	valid := false
	switch n := node.(type) {
	case *ast.CreateTableStmt:
		valid = n != nil && n.Table != nil && n.Select == nil && n.TemporaryKeyword == ast.TemporaryNone
	case *ast.AlterTableStmt:
		valid = n != nil && n.Table != nil && len(n.Specs) > 0
		if valid {
			for _, spec := range n.Specs {
				// EXCHANGE changes both the partitioned table and staging table.
				// NewTable is otherwise allowed only for a single-table rename.
				if spec == nil || spec.Tp == ast.AlterTableExchangePartition || (spec.NewTable != nil && spec.Tp != ast.AlterTableRenameTable) {
					return fmt.Errorf("single_ddl_multiple_mutation_targets_not_supported")
				}
			}
		}
	case *ast.DropTableStmt:
		valid = n != nil && len(n.Tables) == 1 && n.Tables[0] != nil && !n.IsView && !n.IsMvView && n.TemporaryKeyword == ast.TemporaryNone
	case *ast.CreateIndexStmt:
		valid = n != nil && n.Table != nil
	case *ast.DropIndexStmt:
		valid = n != nil && n.Table != nil
	case *ast.TruncateTableStmt:
		valid = n != nil && n.Table != nil
	case *ast.RenameTableStmt:
		valid = n != nil && n.OldTable != nil && n.NewTable != nil && len(n.TableToTables) == 1
	}
	if !valid {
		return fmt.Errorf("single_ddl_unsupported_statement")
	}
	visitor := &singleDDLScopeVisitor{database: database}
	node.Accept(visitor)
	return visitor.err
}

func validateSingleDDLRecords(records []*Record, database string) (*Record, error) {
	var ddl *Record
	uses := 0
	for _, record := range records {
		if record == nil || record.Type == nil {
			return nil, fmt.Errorf("single_ddl_invalid_record")
		}
		if use, ok := record.Type.(*ast.UseStmt); ok {
			uses++
			if uses > 1 || ddl != nil {
				return nil, fmt.Errorf("single_ddl_requires_at_most_one_leading_use")
			}
			if use == nil {
				return nil, fmt.Errorf("single_ddl_invalid_use")
			}
			database = use.DBName
		} else if ddl != nil {
			return nil, fmt.Errorf("single_ddl_requires_one_ddl")
		} else {
			ddl = record
		}
		if err := validateSingleDDLStatement(record.Type, database); err != nil {
			return nil, err
		}
		// Native execution has no pt-osc/gh-ost fallback: an offline DDL on a
		// large table would hold an EXCLUSIVE lock and block DML for the whole
		// rebuild. Small-table offline DDL is left to the ordinary timeout.
		if record.obOfflineLargeAlter {
			return nil, fmt.Errorf("single_ddl_offline_ddl_not_supported")
		}
	}
	if ddl == nil {
		return nil, fmt.Errorf("single_ddl_requires_one_ddl")
	}
	return ddl, nil
}

func (s *session) rejectSingleDDL(err error) {
	// Identical standard 13-column rejection bookkeeping, not DML execution.
	s.rejectSingleDML(err)
}

func (s *session) proveSingleDDLCapability() {
	records := s.recordSets.All()
	if len(records) != 1 {
		s.rejectSingleDDL(fmt.Errorf("single_ddl_capability_requires_one_use"))
		return
	}
	use, ok := records[0].Type.(*ast.UseStmt)
	if !ok || use == nil {
		s.rejectSingleDDL(fmt.Errorf("single_ddl_capability_requires_one_use"))
		return
	}
	if err := validateSingleDDLDatabase(use.DBName); err != nil {
		s.rejectSingleDDL(err)
		return
	}
	if s.hasErrorBefore() || records[0].ErrLevel != 0 {
		return
	}
	s.rejectSingleDDL(fmt.Errorf(singleDDLCapabilityMarker))
	s.myRecord.ErrorMessage = singleDDLCapabilityMarker
	s.myRecord.Stage = StageCheck
	s.myRecord.StageStatus = StatusAuditOk
}

func (s *session) closeSingleDDLConnection() {
	if s.singleDDLConn != nil {
		// sql.Conn.Close alone returns SET state to the pool. ErrBadConn through
		// Raw discards the physical connection without issuing/retrying SQL.
		discardOBCountConnection(s.singleDDLConn)
		s.singleDDLConn = nil
	}
}

func (s *session) prepareSingleDDLConnection(ctx context.Context) error {
	if err := validateSingleDDLDatabase(s.dbName); err != nil {
		return err
	}
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	s.singleDDLConn = conn
	if s.inc.CheckReadOnly {
		var name, value string
		if err := conn.QueryRowContext(ctx, "show variables like 'read_only';").Scan(&name, &value); err != nil {
			return err
		}
		if value != "0" && !strings.EqualFold(value, "OFF") {
			return fmt.Errorf("single_ddl_target_is_read_only_or_unknown")
		}
	}
	seconds := int64(s.opt.WaitTimeout)
	microseconds := seconds * 1000000
	setup := []string{
		fmt.Sprintf("USE `%s`;", strings.ReplaceAll(s.dbName, "`", "``")),
		"set session autocommit=1;",
		fmt.Sprintf("set session wait_timeout=%d, ob_query_timeout=%d, ob_trx_timeout=%d;", seconds, microseconds, microseconds),
	}
	if s.inc.SqlSafeUpdates == 0 || s.inc.SqlSafeUpdates == 1 {
		setup = append(setup, fmt.Sprintf("set session sql_safe_updates=%d;", s.inc.SqlSafeUpdates))
	}
	if s.inc.LockWaitTimeout > 0 {
		setup = append(setup, fmt.Sprintf("set session lock_wait_timeout=%d;", s.inc.LockWaitTimeout))
	}
	for _, statement := range setup {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	var autocommit, waitTimeout, queryTimeout, trxTimeout int64
	var connectionID uint64
	if err := conn.QueryRowContext(ctx, "select @@session.autocommit, @@session.wait_timeout, @@session.ob_query_timeout, @@session.ob_trx_timeout, connection_id();").Scan(&autocommit, &waitTimeout, &queryTimeout, &trxTimeout, &connectionID); err != nil {
		return err
	}
	if autocommit != 1 || waitTimeout != seconds || queryTimeout != microseconds || trxTimeout != microseconds || connectionID == 0 || uint32(connectionID%uint64(math.MaxUint32+1)) == 0 {
		return fmt.Errorf("single_ddl_connection_state_invalid")
	}
	s.threadID = uint32(connectionID % uint64(math.MaxUint32+1))
	return ctx.Err()
}

func (s *session) executeSingleDDL(ctx context.Context) {
	defer s.closeSingleDDLConnection()
	if s.opt == nil || !s.opt.SingleDDL {
		s.rejectSingleDDL(fmt.Errorf("single_ddl_mode_required"))
		return
	}
	if err := validateSingleDDLOptions(s.opt, s.dbType); err != nil {
		s.rejectSingleDDL(err)
		return
	}
	if s.opt.SingleDDLCapability {
		s.proveSingleDDLCapability()
		return
	}
	ddl, err := validateSingleDDLRecords(s.recordSets.All(), s.dbName)
	if err != nil {
		s.rejectSingleDDL(err)
		return
	}
	if s.opt.Check || !s.opt.Execute || s.hasErrorBefore() {
		return
	}
	// One budget includes pool acquisition, USE/SET/readback, and the sole DDL
	// ExecContext. The caller may shorten it, never lengthen it.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.opt.WaitTimeout)*time.Second)
	defer cancel()
	if err := s.prepareSingleDDLConnection(ctx); err != nil {
		s.rejectSingleDDL(err)
		return
	}
	if err := ctx.Err(); err != nil {
		s.rejectSingleDDL(err)
		return
	}
	for _, record := range s.recordSets.All() {
		if _, ok := record.Type.(*ast.UseStmt); ok {
			record.Stage, record.StageStatus, record.ExecComplete = StageExec, StatusExecOK, true
			record.AffectedRows, record.ThreadId, record.ExecTimestamp, record.ExecTime = 0, s.threadID, time.Now().Unix(), "0.000"
		}
	}
	s.stage, s.myRecord = StageExec, ddl
	ddl.Stage, ddl.ThreadId = StageExec, s.threadID
	start := time.Now()
	// Deliberately do not call s.exec/initConnection/executeRemoteStatement:
	// none of their retry, backup, OSC, or generic replay branches are allowed.
	result, err := s.singleDDLConn.ExecContext(ctx, ddl.Sql)
	ddl.ExecTime, ddl.ExecTimestamp = fmt.Sprintf("%.3f", time.Since(start).Seconds()), time.Now().Unix()
	if err == nil {
		ddl.AffectedRows, err = result.RowsAffected()
	}
	if err != nil {
		if ddl.Buf == nil {
			ddl.Buf = new(bytes.Buffer)
		}
		if myErr, ok := err.(*mysqlDriver.MySQLError); ok {
			s.appendErrorMsg(myErr.Message)
		} else {
			s.appendErrorMsg(err.Error())
		}
		ddl.StageStatus = StatusExecFail
		return
	}
	ddl.StageStatus, ddl.ExecComplete = StatusExecOK, true
}
