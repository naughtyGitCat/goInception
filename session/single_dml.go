package session

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/hanchuanchuan/goInception/ast"
)

const (
	singleDMLMaxTimeout = 3600
	singleDMLMaxRows    = 80000
)

func validateSingleDMLOptions(opt *SourceOptions, dbType int) error {
	if opt == nil {
		return fmt.Errorf("single_dml_missing_options")
	}
	if err := validateOBCountOptions(opt, dbType); err != nil {
		return err
	}
	if !opt.SingleDML {
		return nil
	}
	if dbType != DBTypeOceanBase {
		return fmt.Errorf("single_dml_requires_oceanbase")
	}
	if opt.Backup || opt.tranBatch != 0 {
		return fmt.Errorf("single_dml_requires_no_backup_and_trans_zero")
	}
	if opt.Check == opt.Execute || opt.Print || opt.Masking || opt.split || opt.middlewareExtend != "" {
		return fmt.Errorf("single_dml_requires_check_or_execute")
	}
	if opt.WaitTimeout < 1 || opt.WaitTimeout > singleDMLMaxTimeout {
		return fmt.Errorf("single_dml_timeout_must_be_between_1_and_%d", singleDMLMaxTimeout)
	}
	return nil
}

func singleDMLTable(refs *ast.TableRefsClause) bool {
	if refs == nil || refs.TableRefs == nil || refs.TableRefs.Right != nil {
		return false
	}
	source, ok := refs.TableRefs.Left.(*ast.TableSource)
	if !ok || source == nil {
		return false
	}
	table, ok := source.Source.(*ast.TableName)
	return ok && table != nil
}

func validateSingleDMLStatement(node ast.StmtNode) error {
	switch n := node.(type) {
	case *ast.UseStmt:
		if n != nil && n.DBName != "" {
			return nil
		}
	case *ast.UpdateStmt:
		if n != nil && n.With == nil && !n.MultipleTable && singleDMLTable(n.TableRefs) &&
			n.Where != nil && n.Limit == nil && n.Order == nil && len(n.List) > 0 {
			return nil
		}
	case *ast.DeleteStmt:
		if n != nil && n.With == nil && !n.IsMultiTable && n.Tables == nil && singleDMLTable(n.TableRefs) &&
			n.Where != nil && n.Limit == nil && n.Order == nil {
			return nil
		}
	case *ast.InsertStmt:
		if n != nil && !n.IsReplace && singleDMLTable(n.Table) && n.Select == nil &&
			len(n.Setlist) == 0 && len(n.Lists) > 0 {
			return nil
		}
	}
	return fmt.Errorf("single_dml_unsupported_statement")
}

func validateSingleDMLRecords(records []*Record) (*Record, error) {
	var mutation *Record
	count := 0
	for _, record := range records {
		if record == nil || record.Type == nil {
			return nil, fmt.Errorf("single_dml_invalid_record")
		}
		switch record.Type.(type) {
		case *ast.InsertStmt, *ast.UpdateStmt, *ast.DeleteStmt:
			mutation = record
			count++
		}
	}
	// CHECK-only capability probes must get this marker even if table lookup failed.
	if count != 1 {
		return nil, fmt.Errorf("single_dml_requires_one_mutation")
	}
	uses := 0
	seenMutation := false
	for _, record := range records {
		if err := validateSingleDMLStatement(record.Type); err != nil {
			return nil, err
		}
		if _, ok := record.Type.(*ast.UseStmt); ok {
			uses++
			if uses > 1 || seenMutation {
				return nil, fmt.Errorf("single_dml_requires_at_most_one_leading_use")
			}
		} else {
			seenMutation = true
		}
	}
	if update, ok := mutation.Type.(*ast.UpdateStmt); ok {
		if mutation.TableInfo == nil || len(mutation.TableInfo.Fields) == 0 {
			return nil, fmt.Errorf("single_dml_update_metadata_required")
		}
		for _, assignment := range update.List {
			if assignment == nil || assignment.Column == nil {
				return nil, fmt.Errorf("single_dml_invalid_assignment")
			}
			for _, field := range mutation.TableInfo.Fields {
				if !field.IsDeleted && strings.EqualFold(field.Key, "PRI") && strings.EqualFold(field.Field, assignment.Column.Name.O) {
					return nil, fmt.Errorf("single_dml_primary_key_assignment_not_supported")
				}
			}
		}
	}
	if mutation.AffectedRows < 0 {
		return nil, fmt.Errorf("single_dml_negative_row_estimate")
	}
	switch mutation.Type.(type) {
	case *ast.UpdateStmt, *ast.DeleteStmt:
		if mutation.AffectedRows > singleDMLMaxRows {
			return nil, fmt.Errorf("single_dml_estimated_rows_exceed_%d", singleDMLMaxRows)
		}
	}
	return mutation, nil
}

func singleDMLTimeoutSQL(seconds int) (string, error) {
	if seconds < 1 || seconds > singleDMLMaxTimeout {
		return "", fmt.Errorf("single_dml_timeout_must_be_between_1_and_%d", singleDMLMaxTimeout)
	}
	microseconds := int64(seconds) * 1000000
	return fmt.Sprintf("set session wait_timeout=%d, ob_query_timeout=%d, ob_trx_timeout=%d;", seconds, microseconds, microseconds), nil
}

func (s *session) rejectSingleDML(err error) {
	if s.recordSets == nil {
		s.recordSets = NewRecordSets()
	}
	if records := s.recordSets.All(); len(records) > 0 {
		s.myRecord = records[0]
	}
	if s.myRecord == nil {
		s.myRecord = &Record{Stage: StageCheck, Buf: new(bytes.Buffer)}
	}
	if s.myRecord.Buf == nil {
		s.myRecord.Buf = new(bytes.Buffer)
	}
	s.appendErrorMsg(err.Error())
	if len(s.recordSets.All()) == 0 {
		s.recordSets.Append(s.myRecord)
	}
}

func (s *session) prepareSingleDMLConnection(ctx context.Context) error {
	if s.dbName == "" {
		return fmt.Errorf("single_dml_database_required")
	}
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	s.singleDMLConn = conn
	s.singleDMLContext = ctx
	if s.inc.CheckReadOnly {
		var name, value string
		if err := conn.QueryRowContext(ctx, "show variables like 'read_only';").Scan(&name, &value); err != nil {
			return err
		}
		switch strings.ToUpper(strings.TrimSpace(value)) {
		case "OFF", "0":
		case "ON", "1":
			return fmt.Errorf("single_dml_target_is_read_only")
		default:
			return fmt.Errorf("single_dml_read_only_state_unknown")
		}
	}
	timeoutSQL, err := singleDMLTimeoutSQL(s.opt.WaitTimeout)
	if err != nil {
		return err
	}
	setup := []string{
		fmt.Sprintf("USE `%s`;", strings.ReplaceAll(s.dbName, "`", "``")),
		"set session autocommit=1;",
		timeoutSQL,
	}
	if s.protectedOBCount() {
		setup = append(setup, "set session ob_read_consistency='STRONG';")
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
	var autocommit int
	var connectionID uint64
	if err := conn.QueryRowContext(ctx, "select @@session.autocommit, connection_id();").Scan(&autocommit, &connectionID); err != nil {
		return err
	}
	if autocommit != 1 || connectionID == 0 || uint32(connectionID%uint64(math.MaxUint32+1)) == 0 {
		return fmt.Errorf("single_dml_connection_state_invalid")
	}
	s.threadID = uint32(connectionID % uint64(math.MaxUint32+1))
	return nil
}

func (s *session) closeSingleDMLConnection() {
	if s.singleDMLConn != nil {
		_ = s.singleDMLConn.Close()
		s.singleDMLConn = nil
	}
	s.singleDMLContext = nil
}

func (s *session) executeSingleDML(ctx context.Context) {
	if s.opt == nil || !s.opt.SingleDML {
		s.rejectSingleDML(fmt.Errorf("single_dml_mode_required"))
		return
	}
	if err := validateSingleDMLOptions(s.opt, s.dbType); err != nil {
		s.rejectSingleDML(err)
		return
	}
	if s.opt.OBCountCapability {
		s.proveOBCountCapability()
		return
	}
	mutation, err := validateSingleDMLRecords(s.recordSets.All())
	if err != nil {
		s.rejectSingleDML(err)
		return
	}
	if s.opt.Check || !s.opt.Execute || s.hasErrorBefore() {
		return
	}
	defer s.closeSingleDMLConnection()
	if err := s.prepareSingleDMLConnection(ctx); err != nil {
		s.rejectSingleDML(err)
		return
	}
	if err := ctx.Err(); err != nil {
		s.rejectSingleDML(err)
		return
	}
	for _, record := range s.recordSets.All() {
		if _, ok := record.Type.(*ast.UseStmt); ok {
			record.Stage = StageExec
			record.StageStatus = StatusExecOK
			record.ExecComplete = true
			record.AffectedRows = 0
			record.ThreadId = s.threadID
			record.ExecTimestamp = time.Now().Unix()
			record.ExecTime = "0.000"
		}
	}
	s.stage = StageExec
	s.myRecord = mutation
	mutation.Stage = StageExec
	// s.exec routes this mode only to the pinned Conn; it never reconnects or replays.
	s.executeRemoteStatement(mutation, false)
}
