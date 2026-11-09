// Copyright 2013 The ql Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSES/QL-LICENSE file.

package session

import (
	"fmt"
	"strings"

	"github.com/hanchuanchuan/goInception/ast"
	"github.com/hanchuanchuan/goInception/model"
	"github.com/hanchuanchuan/goInception/mysql"
	"github.com/hanchuanchuan/goInception/parser"
	"github.com/hanchuanchuan/goInception/types"
)

// OceanBase 的 Offline DDL 会创建影子表并在整个数据重组期间持有 EXCLUSIVE
// 表锁, DML 全程阻塞。这里只对能够确认的 Online DDL 返回 true;
// 任何未知子句、未知选项或无法稳定判定的类型变更都按 Offline 处理(fail-closed)。
func (s *session) isOBAlterOnline(node *ast.AlterTableStmt, t *TableInfo) bool {
	if node == nil || len(node.Specs) == 0 {
		return false
	}
	for _, spec := range node.Specs {
		if !s.isOBAlterSpecOnline(spec, t) {
			return false
		}
	}
	return true
}

func (s *session) isOBAlterSpecOnline(spec *ast.AlterTableSpec, t *TableInfo) bool {
	if spec == nil {
		return false
	}

	switch spec.Tp {
	case ast.AlterTableAddConstraint:
		return isOBOnlineIndexConstraint(spec.Constraint)
	case ast.AlterTableDropIndex, ast.AlterTableRenameIndex:
		return true
	case ast.AlterTableAddColumns:
		return s.isOBAddColumnsOnline(spec)
	case ast.AlterTableModifyColumn:
		return s.isOBModifyColumnOnline(spec, t, false)
	case ast.AlterTableChangeColumn:
		return s.isOBModifyColumnOnline(spec, t, true)
	case ast.AlterTableRenameColumn, ast.AlterTableAlterColumn, ast.AlterTableRenameTable:
		return true
	case ast.AlterTableAlgorithm, ast.AlterTableLock:
		// ALGORITHM/LOCK 只是执行提示, 在线/离线由 OceanBase 自行决定
		return true
	case ast.AlterTableOption:
		return isOBAlterTableOptionsOnline(spec.Options)
	case ast.AlterTableAddPartitions, ast.AlterTableDropPartition, ast.AlterTableTruncatePartition:
		return true
	case ast.AlterTablePartition:
		return isOBAlterAutoPartitionAttrsOnly(spec.Partition)
	default:
		// DROP COLUMN 需要物理删除数据, ALTER/EXCHANGE/REORGANIZE PARTITION
		// 等需要数据重分布, 均为 Offline DDL。
		return false
	}
}

// ADD INDEX/UNIQUE/KEY 在 OceanBase 上后台构建索引, 不阻塞 DML。
// PRIMARY KEY/FOREIGN KEY 需要重建表, 不放行。
func isOBOnlineIndexConstraint(c *ast.Constraint) bool {
	if c == nil {
		return false
	}
	switch c.Tp {
	case ast.ConstraintKey, ast.ConstraintIndex,
		ast.ConstraintUniq, ast.ConstraintUniqKey, ast.ConstraintUniqIndex:
		return true
	default:
		return false
	}
}

func (s *session) isOBAddColumnsOnline(spec *ast.AlterTableSpec) bool {
	if spec == nil || len(spec.NewColumns) == 0 {
		return false
	}
	for _, col := range spec.NewColumns {
		if col == nil {
			return false
		}
		for _, opt := range col.Options {
			switch opt.Tp {
			case ast.ColumnOptionNoOption,
				ast.ColumnOptionNotNull,
				ast.ColumnOptionNull,
				ast.ColumnOptionDefaultValue,
				ast.ColumnOptionOnUpdate,
				ast.ColumnOptionComment,
				ast.ColumnOptionCollate,
				ast.ColumnOptionGenerated,
				// AUTO_INCREMENT 是否在线由 classifyOceanBaseAddColumn 按版本判定
				ast.ColumnOptionAutoIncrement:
			default:
				return false
			}
		}
		if classifyOceanBaseAddColumn(s.obVersion, col, spec.Position) != oceanbaseAddOnline {
			return false
		}
	}
	return true
}

func (s *session) isOBModifyColumnOnline(spec *ast.AlterTableSpec, t *TableInfo, changeColumn bool) bool {
	if spec == nil || len(spec.NewColumns) != 1 {
		return false
	}
	if spec.Position != nil && spec.Position.Tp != ast.ColumnPositionNone {
		return false
	}
	newColumn := spec.NewColumns[0]
	if newColumn == nil {
		return false
	}

	oldName := newColumn.Name.Name.O
	if changeColumn {
		if spec.OldColumnName == nil {
			return false
		}
		oldName = spec.OldColumnName.Name.O
	}
	oldField := findOBField(t, oldName)
	if oldField == nil {
		return false
	}

	for _, opt := range newColumn.Options {
		switch opt.Tp {
		case ast.ColumnOptionNoOption,
			ast.ColumnOptionNotNull,
			ast.ColumnOptionNull,
			ast.ColumnOptionDefaultValue,
			ast.ColumnOptionOnUpdate,
			ast.ColumnOptionComment:
		case ast.ColumnOptionCollate:
			// 排序规则变化需要重写数据。
			if oldField.Collation != "" && !strings.EqualFold(opt.StrValue, oldField.Collation) {
				return false
			}
		default:
			return false
		}
	}
	return isOBColumnTypeOnlineChange(oldField, newColumn.Tp)
}

// ALTER TABLE 的元数据选项。AUTO_INCREMENT 值、行格式、压缩/块大小及注释
// 只改元数据; 字符集只在未指定数据转换(CONVERT TO)时是元数据变更。
func isOBAlterTableOptionsOnline(options []*ast.TableOption) bool {
	if len(options) == 0 {
		return false
	}
	for _, opt := range options {
		if opt == nil {
			return false
		}
		switch opt.Tp {
		case ast.TableOptionAutoIncrement,
			ast.TableOptionRowFormat,
			ast.TableOptionCompression,
			ast.TableOptionBlockSize,
			ast.TableOptionComment,
			ast.TableOptionCollate:
		case ast.TableOptionCharset:
			if opt.UintValue != ast.TableOptionCharsetWithoutConvertTo {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// 自动分区属性调整只放行“未携带显式分区定义”的场景。
func isOBAlterAutoPartitionAttrsOnly(partition *ast.PartitionOptions) bool {
	if partition == nil {
		return false
	}
	return partition.Tp == model.PartitionTypeRange &&
		partition.Expr == nil &&
		len(partition.ColumnNames) == 0 &&
		len(partition.Definitions) == 0 &&
		partition.Sub == nil
}

func findOBField(t *TableInfo, name string) *FieldInfo {
	if t == nil || name == "" {
		return nil
	}
	for i := range t.Fields {
		if !t.Fields[i].IsDeleted && strings.EqualFold(t.Fields[i].Field, name) {
			return &t.Fields[i]
		}
	}
	return nil
}

// isOBColumnTypeOnlineChange 判断 MODIFY/CHANGE 的类型变更是否为 Online DDL。
// 只接受同符号整型扩宽、字符串/大对象同家族扩容、VARCHAR(x<=255)<->TINYTEXT
// 以及同类型参数不缩小; 其余一律按 Offline 处理。
func isOBColumnTypeOnlineChange(oldField *FieldInfo, newType *types.FieldType) bool {
	if oldField == nil || newType == nil {
		return false
	}
	oldType := strings.ToLower(strings.TrimSpace(oldField.Type))
	newTypeStr := strings.ToLower(strings.TrimSpace(newType.CompactStr()))
	if oldType == "" || newTypeStr == "" {
		return false
	}

	// 字符集变化需要重写数据。
	if newType.Charset != "" && oldField.Collation != "" &&
		!strings.EqualFold(newType.Charset, strings.SplitN(oldField.Collation, "_", 2)[0]) {
		return false
	}

	oldBase := GetDataTypeBase(oldType)
	newBase := GetDataTypeBase(newTypeStr)
	oldUnsigned := strings.Contains(oldType, "unsigned")
	newUnsigned := mysql.HasUnsignedFlag(newType.Flag)

	if oldBase == newBase {
		if _, ok := obIntegerRank(oldBase); ok {
			// 整型 display width 只是元数据, 同符号即可。
			return oldUnsigned == newUnsigned
		}
		if oldBase == "enum" || oldBase == "set" {
			return obEnumSetMembersUnchanged(oldField.Type, newType)
		}
		return obTypeLengthGrowOnly(oldType, newTypeStr)
	}

	if oldRank, ok := obIntegerRank(oldBase); ok {
		newRank, ok := obIntegerRank(newBase)
		// 整型跨类型扩宽(如 int -> bigint)为 Online DDL, 收窄需重写数据。
		return ok && oldUnsigned == newUnsigned && newRank >= oldRank
	}
	if lobTypeChangeAllowed(oldType, newTypeStr) {
		return true
	}
	return obVarcharTinyTextOnline(oldType, newTypeStr)
}

func obEnumSetMembersUnchanged(oldType string, newType *types.FieldType) bool {
	// 元数据类型包含 SQL 字面值；不能对整串折叠大小写或按逗号拆分。
	stmt, err := parser.New().ParseOneStmt("CREATE TABLE t (v "+oldType+")", "", "")
	if err != nil {
		return false
	}
	create, ok := stmt.(*ast.CreateTableStmt)
	if !ok || len(create.Cols) != 1 || len(create.Cols[0].Options) != 0 || len(create.Constraints) != 0 {
		return false
	}
	old := create.Cols[0].Tp
	if old.Tp != newType.Tp || len(old.Elems) != len(newType.Elems) {
		return false
	}
	for i, member := range old.Elems {
		// CI 也保守处理：不在审核端模拟排序规则的字面值等价关系。
		if member != newType.Elems[i] {
			return false
		}
	}
	return true
}

func obIntegerRank(base string) (int, bool) {
	switch base {
	case "tinyint":
		return 2, true
	case "smallint":
		return 3, true
	case "mediumint":
		return 4, true
	case "int", "integer":
		return 5, true
	case "bigint":
		return 6, true
	}
	return 0, false
}

// obTypeLengthGrowOnly 要求类型容量不缩小。
// DECIMAL(p,s) 分别比较整数位 p-s 与小数位 s, 不能仅比较总精度 p。
func obTypeLengthGrowOnly(oldType, newType string) bool {
	oldLen := GetDataTypeLength(oldType)
	newLen := GetDataTypeLength(newType)
	if GetDataTypeBase(oldType) == "decimal" && GetDataTypeBase(newType) == "decimal" {
		// DECIMAL / DECIMAL(p) 的默认值分别为 (10,0) / (p,0)。
		for _, dims := range []*[]int{&oldLen, &newLen} {
			if len(*dims) == 1 {
				if (*dims)[0] == -1 {
					(*dims)[0] = 10
				}
				*dims = append(*dims, 0)
			}
		}
		return len(oldLen) == 2 && len(newLen) == 2 &&
			oldLen[1] >= 0 && oldLen[0] >= oldLen[1] &&
			newLen[1] >= oldLen[1] && newLen[0]-newLen[1] >= oldLen[0]-oldLen[1]
	}
	if len(oldLen) != len(newLen) || len(oldLen) == 0 {
		return false
	}
	if oldLen[0] < 0 || newLen[0] < 0 {
		return oldLen[0] < 0 && newLen[0] < 0
	}
	for i := range oldLen {
		if newLen[i] < oldLen[i] {
			return false
		}
	}
	return true
}

// VARCHAR(x) 与 TINYTEXT 在 x <= 255 时为 Online DDL。
func obVarcharTinyTextOnline(oldType, newType string) bool {
	switch {
	case GetDataTypeBase(oldType) == "varchar" && GetDataTypeBase(newType) == "tinytext":
		return GetDataTypeLength(oldType)[0] <= 255
	case GetDataTypeBase(oldType) == "tinytext" && GetDataTypeBase(newType) == "varchar":
		return GetDataTypeLength(newType)[0] >= 255
	}
	return false
}

// offlineDDLLargeTableSize 返回判定“大表”的 MB 门槛。
// 专用配置优先; 未配置时沿用 OSC 的最小表大小, 与现有工具分流保持一致;
// 两者都为 0 时关闭大表检查。
func (s *session) offlineDDLLargeTableSize() uint {
	if s.inc.CheckOfflineDDLLargeTableSize > 0 {
		return s.inc.CheckOfflineDDLLargeTableSize
	}
	if (s.osc.OscOn || s.ghost.GhostOn) && s.osc.OscMinTableSize > 0 {
		return s.osc.OscMinTableSize
	}
	return 0
}

// 显式行数优先; 已启用大小门槛时默认最多探测10万行以确认小表。
func (s *session) offlineDDLLargeTableRows() uint {
	if s.inc.CheckOfflineDDLLargeTableRows > 0 {
		return s.inc.CheckOfflineDDLLargeTableRows
	}
	if s.offlineDDLLargeTableSize() > 0 {
		return 100000
	}
	return 0
}

func (s *session) isOfflineDDLLargeTable(t *TableInfo) bool {
	threshold := s.offlineDDLLargeTableSize()
	return threshold > 0 && t != nil && t.TableSizeKnown && t.TableSize >= threshold
}

// 小表豁免仅来自本条ALTER的强一致限量探测, 不能由默认0或缓存推断。
func (s *session) offlineDDLIsSmallTable(t *TableInfo) bool {
	return t != nil && s.myRecord.obOfflineSmallAlter
}

// appendColumnTypeChange 报告列类型变更。在 OceanBase 上收窄类型需要重建表,
// 小表降级为告警, 大表(或无法判定大小时)按统一口径报错。
func (s *session) appendColumnTypeChange(t *TableInfo, field, oldType, newType string) {
	if s.dbType == DBTypeOceanBase && s.inc.CheckOfflineDDL {
		if s.offlineDDLIsSmallTable(t) {
			s.appendWarning(ER_CHANGE_COLUMN_TYPE, field, oldType, newType)
		} else {
			// 该错误码默认只是告警级别, 大表需要显式升级为报错
			s.appendErrorWithLevel(ER_CHANGE_COLUMN_TYPE, 2, field, oldType, newType)
		}
		return
	}
	s.appendErrorNo(ER_CHANGE_COLUMN_TYPE, field, oldType, newType)
}

// checkOfflineDDLLargeTable 对大表上的 Offline DDL 告警或报错。
// OceanBase 会在整个数据重组期间持有 EXCLUSIVE 表锁并阻塞 DML,
// 大表重建可能持续数分钟以上, 需要显式让调用方知情。
func (s *session) checkOfflineDDLLargeTable(t *TableInfo) {
	if !s.myRecord.obOfflineLargeAlter {
		return
	}
	reason := fmt.Sprintf("rows >= %d (estimated or bounded strong-read result)", s.offlineDDLLargeTableRows())
	if s.isOfflineDDLLargeTable(t) {
		reason = fmt.Sprintf("%d MB >= %d MB", t.TableSize, s.offlineDDLLargeTableSize())
	}
	msg := fmt.Sprintf("ALTER TABLE `%s`.`%s` is an offline DDL on a large table (%s): "+
		"OceanBase rebuilds the table under an EXCLUSIVE lock and blocks DML until it finishes",
		t.Schema, t.Name, reason)
	if s.inc.CheckOfflineDDLLargeTableAsError {
		s.appendErrorMsg(msg)
	} else {
		s.appendWarningMessage(msg)
	}
}
