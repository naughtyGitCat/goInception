package session

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	"github.com/hanchuanchuan/goInception/ast"
)

const obCountCapabilityMarker = "ob_single_dml_count_protection_v1"
const obCountDefaultTimeout = 30

func validateOBCountOptions(opt *SourceOptions, dbType int) error {
	requested := opt.OBCountProtection || opt.OBCountTimeout != 0 || opt.obCountTimeoutSet || opt.OBCountCapability
	if !requested {
		return nil
	}
	if dbType != DBTypeOceanBase {
		return fmt.Errorf("ob_count_requires_oceanbase")
	}
	if !opt.OBCountProtection || !opt.SingleDML || !opt.RealRowCount {
		return fmt.Errorf("ob_count_requires_single_dml_real_row_count_protection")
	}
	if (opt.OBCountTimeout != 0 || opt.obCountTimeoutSet) && (opt.OBCountTimeout < 1 || opt.OBCountTimeout > singleDMLMaxTimeout) {
		return fmt.Errorf("ob_count_timeout_must_be_between_1_and_%d", singleDMLMaxTimeout)
	}
	if opt.OBCountCapability && (!opt.Check || opt.Execute) {
		return fmt.Errorf("ob_count_capability_requires_check_only")
	}
	return nil
}

func (s *session) protectedOBCount() bool {
	return s.opt != nil && s.dbType == DBTypeOceanBase && s.opt.SingleDML && s.opt.RealRowCount && s.opt.OBCountProtection
}

// The read session must never return to the mutation/legacy pool with WEAK state.
// Raw's ErrBadConn discards the physical connection; it does not issue/retry SQL.
func discardOBCountConnection(conn *sql.Conn) {
	_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
	_ = conn.Close()
}

func (s *session) readProtectedOBCount(query string) (int64, error) {
	if !s.protectedOBCount() {
		return 0, fmt.Errorf("ob_count_protection_required")
	}
	if err := validateSingleDMLOptions(s.opt, s.dbType); err != nil {
		return 0, err
	}
	if s.obCountContext == nil {
		return 0, fmt.Errorf("ob_count_request_context_required")
	}
	if s.dbName == "" {
		return 0, fmt.Errorf("ob_count_database_required")
	}
	seconds := s.opt.OBCountTimeout
	if seconds == 0 {
		seconds = obCountDefaultTimeout
	}
	ctx, cancel := context.WithTimeout(s.obCountContext, time.Duration(seconds)*time.Second)
	defer cancel()
	db, err := s.db.DB()
	if err != nil {
		return 0, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer discardOBCountConnection(conn)
	// Budget includes connection acquisition, USE, routing, timeout setup, and Scan.
	remaining := time.Until(deadlineOf(ctx)).Microseconds()
	if remaining <= 0 {
		return 0, context.DeadlineExceeded
	}
	setup := []string{
		fmt.Sprintf("USE `%s`;", strings.ReplaceAll(s.dbName, "`", "``")),
		"set session autocommit=1;",
		fmt.Sprintf("set session ob_query_timeout=%d, ob_trx_timeout=%d;", remaining, remaining),
		"set session ob_read_consistency='WEAK';",
		"set @proxy_route_policy='follower_first';",
	}
	for _, statement := range setup {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return 0, err
		}
	}
	// Refresh the server budget after setup; context alone cannot prove server stop.
	remaining = time.Until(deadlineOf(ctx)).Microseconds()
	if remaining <= 0 {
		return 0, context.DeadlineExceeded
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("set session ob_query_timeout=%d, ob_trx_timeout=%d;", remaining, remaining)); err != nil {
		return 0, err
	}
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	if len(columns) != 1 {
		return 0, fmt.Errorf("ob_count_requires_one_column")
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("ob_count_requires_one_row")
	}
	var count sql.NullInt64
	if err := rows.Scan(&count); err != nil {
		return 0, err
	}
	if !count.Valid || count.Int64 < 0 {
		return 0, fmt.Errorf("ob_count_invalid_value")
	}
	if rows.Next() {
		return 0, fmt.Errorf("ob_count_requires_one_row")
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if rows.NextResultSet() {
		return 0, fmt.Errorf("ob_count_requires_one_result_set")
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return count.Int64, nil
}

func deadlineOf(ctx context.Context) time.Time { deadline, _ := ctx.Deadline(); return deadline }

func (s *session) getProtectedOBRowCount(query string) {
	if s.hasError() {
		return
	}
	count, err := s.readProtectedOBCount(query)
	if err != nil {
		s.appendErrorMsg("ob_count_failed: " + err.Error())
		return
	}
	s.myRecord.AffectedRows = count
	if s.inc.MaxUpdateRows > 0 && count > int64(s.inc.MaxUpdateRows) {
		switch s.myRecord.Type.(type) {
		case *ast.UpdateStmt, *ast.DeleteStmt:
			s.appendErrorNo(ER_UDPATE_TOO_MUCH_ROWS, count, s.inc.MaxUpdateRows)
		}
	}
	if count > singleDMLMaxRows {
		s.appendErrorMsg(fmt.Sprintf("single_dml_estimated_rows_exceed_%d", singleDMLMaxRows))
	}
}

func (s *session) proveOBCountCapability() {
	records := s.recordSets.All()
	if len(records) != 1 {
		s.rejectSingleDML(fmt.Errorf("ob_count_capability_requires_one_use"))
		return
	}
	use, ok := records[0].Type.(*ast.UseStmt)
	if !ok || use == nil || use.DBName == "" {
		s.rejectSingleDML(fmt.Errorf("ob_count_capability_requires_one_use"))
		return
	}
	if s.hasErrorBefore() {
		return
	}
	// Deliberate level-2 CHECK rejection, not a successful mutation or execution.
	// Identity is the sole caller-supplied USE statement; the 13-column shape is unchanged.
	s.rejectSingleDML(fmt.Errorf(obCountCapabilityMarker))
	s.myRecord.ErrorMessage = obCountCapabilityMarker
	s.myRecord.Stage = StageCheck
	s.myRecord.StageStatus = StatusAuditOk
}
