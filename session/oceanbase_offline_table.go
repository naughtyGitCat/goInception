package session

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
)

const obOfflineTableProbeTimeout = 3 * time.Second

// 不缓存小表证明: 每条ALTER都用变更前的真实表重新确认。
func (s *session) checkOBOfflineTableSize(t *TableInfo) {
	rowThreshold := s.offlineDDLLargeTableRows()
	if rowThreshold == 0 {
		return
	}
	if s.isOfflineDDLLargeTable(t) {
		s.myRecord.obOfflineLargeAlter = true
		return
	}
	// TABLE_ROWS 只用于证明大表, 估计值偏低/0不能用来豁免。
	if s.myRecord.AffectedRows > 0 && uint64(s.myRecord.AffectedRows) >= uint64(rowThreshold) {
		s.myRecord.obOfflineRows = rowThreshold // 仅表示已达门槛, 不是精确计数
		s.myRecord.obOfflineLargeAlter = true
		return
	}
	if t == nil || t.IsNew || (s.offlineDDLLargeTableSize() > 0 && !t.TableSizeKnown) {
		s.appendErrorMsg("ob_offline_table_size_unknown: cannot establish small-table exemption from missing table size or an audit-only table")
		return
	}
	rows, err := s.probeOBOfflineTableRows(t, rowThreshold)
	if err != nil {
		s.appendErrorMsg(fmt.Sprintf("ob_offline_table_rows_unknown: cannot establish small-table exemption: %v", err))
		return
	}
	s.myRecord.obOfflineRows = rows
	s.myRecord.obOfflineLargeAlter = rows >= rowThreshold
	s.myRecord.obOfflineSmallAlter = rows < rowThreshold
}

// 读取常量而非COUNT(*), 最多返回门槛+1行; STRONG避免副本延迟误判小表。
// 客户端与服务端均限时, 不修改连接池会话的路由/一致性设置。
func (s *session) probeOBOfflineTableRows(t *TableInfo, threshold uint) (uint, error) {
	if threshold == 0 || uint64(threshold) >= uint64(math.MaxInt64) {
		return 0, fmt.Errorf("invalid offline DDL row threshold")
	}
	if s.db == nil {
		return 0, fmt.Errorf("missing database connection")
	}
	db, err := s.db.DB()
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), obOfflineTableProbeTimeout)
	defer cancel()
	quote := func(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
	limit := threshold + 1
	query := fmt.Sprintf("SELECT /*+ QUERY_TIMEOUT(3000000) READ_CONSISTENCY(STRONG) */ 1 FROM %s.%s LIMIT %d",
		quote(t.Schema), quote(t.Name), limit)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var count uint
	for rows.Next() {
		var value sql.NullInt64
		if err := rows.Scan(&value); err != nil {
			return 0, err
		}
		if !value.Valid || value.Int64 != 1 || count >= limit {
			return 0, fmt.Errorf("unexpected offline DDL row probe result")
		}
		count++
	}
	if err := finishOBOfflineTableRead(ctx, rows); err != nil {
		return 0, err
	}
	return count, nil
}

// 对OB大小结果明确校验NULL、结果基数、扫描与流错误; 合法0与未知分开。
func (s *session) collectOBTableSize(t *TableInfo) error {
	t.TableSize, t.TableSizeKnown = 0, false
	if t.IsNew {
		return nil
	}
	if s.db == nil {
		return fmt.Errorf("missing database connection")
	}
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), obOfflineTableProbeTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT /*+ QUERY_TIMEOUT(3000000) READ_CONSISTENCY(WEAK) */
		ROUND(SUM(t2.data_size/1024/1024), 2)
		FROM oceanbase.DBA_OB_TABLE_LOCATIONS t1, oceanbase.DBA_OB_TABLET_REPLICAS t2
		WHERE t1.svr_ip = t2.svr_ip AND t1.ls_id = t2.ls_id AND t1.tablet_id = t2.tablet_id
		AND t1.database_name = ? AND t1.table_name = ?`, t.Schema, t.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	var size sql.NullFloat64
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return fmt.Errorf("missing table size result")
	}
	if err := rows.Scan(&size); err != nil {
		return err
	}
	if !size.Valid || size.Float64 < 0 || math.IsNaN(size.Float64) || math.IsInf(size.Float64, 0) || size.Float64 >= float64(^uint(0)) {
		return fmt.Errorf("invalid or NULL table size")
	}
	if rows.Next() {
		return fmt.Errorf("multiple table size results")
	}
	if err := finishOBOfflineTableRead(ctx, rows); err != nil {
		return err
	}
	t.TableSize, t.TableSizeKnown = uint(size.Float64), true
	return nil
}

func finishOBOfflineTableRead(ctx context.Context, rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		return err
	}
	if rows.NextResultSet() {
		return fmt.Errorf("unexpected extra table metadata result set")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return ctx.Err()
}
