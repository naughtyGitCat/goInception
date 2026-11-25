package session

import (
	"errors"
	"strings"

	"github.com/hanchuanchuan/goInception/ast"
	"github.com/hanchuanchuan/goInception/format"
	"github.com/hanchuanchuan/goInception/model"
	"github.com/hanchuanchuan/goInception/mysql"
	"github.com/hanchuanchuan/goInception/parser"
	"github.com/hanchuanchuan/goInception/types"
	"github.com/hanchuanchuan/goInception/util/sqlexec"
)

// mergeAlterTable builds one ordered action list without contacting a target or auditing SQL.
func (s *session) mergeAlterTable(sql string) ([]sqlexec.RecordSet, error) {
	if strings.ContainsRune(sql, 0) {
		return nil, errors.New("MERGE_ALTER_TABLE: invalid SQL syntax")
	}
	charset, collation := s.sessionVars.GetCharsetInfo()
	executableComment, alterIgnoreTable, ambiguousQuoting, unsupportedIndexSyntax := parser.MergeAlterTablePayloadIssues(sql)
	if executableComment {
		return nil, errors.New("MERGE_ALTER_TABLE: executable comments are not supported")
	}
	if alterIgnoreTable {
		return nil, errors.New("MERGE_ALTER_TABLE: ALTER IGNORE TABLE is not supported")
	}
	if ambiguousQuoting {
		return nil, errors.New("MERGE_ALTER_TABLE: ambiguous SQL quoting is not supported")
	}
	if unsupportedIndexSyntax {
		return nil, errors.New("MERGE_ALTER_TABLE: unsupported index syntax")
	}
	nodes, warnings, err := parser.New().Parse(sql, charset, collation)
	if err != nil {
		return nil, errors.New("MERGE_ALTER_TABLE: invalid SQL syntax")
	}
	if len(warnings) != 0 {
		return nil, errors.New("MERGE_ALTER_TABLE: input produced a parser warning")
	}
	if len(nodes) < 2 {
		return nil, errors.New("MERGE_ALTER_TABLE: expected at least two ALTER TABLE statements")
	}

	// First pass: every root must target the same explicitly qualified table.
	// The session schema is deliberately ignored, so mixed qualification is
	// rejected instead of being completed from CurrentDB.
	alters := make([]*ast.AlterTableStmt, len(nodes))
	var schema, table string
	var explicit bool
	for i, node := range nodes {
		alter, ok := node.(*ast.AlterTableStmt)
		if !ok || alter.Table == nil {
			return nil, errors.New("MERGE_ALTER_TABLE: expected only ALTER TABLE statements")
		}
		target := alter.Table
		if target.Name.O == "" || target.Schema.O == "*" || len(target.IndexHints) != 0 ||
			len(target.PartitionNames) != 0 || target.AsOf != nil || target.TableSample != nil {
			return nil, errors.New("MERGE_ALTER_TABLE: expected a single named table")
		}
		qualified := target.Schema.O != ""
		if i == 0 {
			schema, table, explicit = target.Schema.O, target.Name.O, qualified
		} else {
			if qualified != explicit {
				return nil, errors.New("MERGE_ALTER_TABLE: mixed schema qualification is not supported")
			}
			if target.Schema.O != schema || target.Name.O != table {
				return nil, errors.New("MERGE_ALTER_TABLE: expected the same schema and table")
			}
		}
		alters[i] = alter
	}

	var builder strings.Builder
	ctx := format.NewRestoreCtx(format.DefaultRestoreFlags, &builder)
	columns := make(map[string]struct{})
	indexes := make(map[string]struct{})
	indexedColumns := make(map[string]struct{})
	afterReferences := make(map[string]struct{})
	for _, alter := range alters {
		if len(alter.Specs) == 0 {
			return nil, errors.New("MERGE_ALTER_TABLE: expected ALTER actions")
		}
		for _, spec := range alter.Specs {
			switch spec.Tp {
			case ast.AlterTableAddColumns, ast.AlterTableModifyColumn:
				column, err := mergeAlterColumn(spec)
				if err != nil {
					return nil, err
				}
				if _, exists := columns[column]; exists {
					return nil, errors.New("MERGE_ALTER_TABLE: a column may only be changed once")
				}
				if _, indexed := indexedColumns[column]; indexed {
					return nil, errors.New("MERGE_ALTER_TABLE: column changes must precede indexes on that column")
				}
				if _, referenced := afterReferences[column]; referenced {
					return nil, errors.New("MERGE_ALTER_TABLE: column changes must precede AFTER references to that column")
				}
				if spec.Position.Tp == ast.ColumnPositionAfter {
					after := spec.Position.RelativeColumn.Name.L
					if after == column {
						return nil, errors.New("MERGE_ALTER_TABLE: a column cannot refer to itself in AFTER")
					}
					afterReferences[after] = struct{}{}
				}
				columns[column] = struct{}{}
			case ast.AlterTableAddConstraint:
				if !plainMergeIndex(spec.Constraint) {
					return nil, errors.New("MERGE_ALTER_TABLE: expected a named nonunique column index")
				}
				name := strings.ToLower(spec.Constraint.Name)
				if _, exists := indexes[name]; exists {
					return nil, errors.New("MERGE_ALTER_TABLE: an index name may only be used once")
				}
				indexes[name] = struct{}{}
				for _, key := range spec.Constraint.Keys {
					indexedColumns[key.Column.Name.L] = struct{}{}
				}
			default:
				return nil, errors.New("MERGE_ALTER_TABLE: only ADD COLUMN, ADD INDEX or MODIFY COLUMN actions are supported")
			}
			if builder.Len() > 0 {
				builder.WriteString(", ")
			}
			if err := spec.Restore(ctx); err != nil {
				return nil, errors.New("MERGE_ALTER_TABLE: cannot render ALTER actions")
			}
		}
	}

	result := &recordSet{count: 1, fields: make([]*ast.ResultField, 3)}
	result.CreateFiled("schema", mysql.TypeVarString)
	result.CreateFiled("table", mysql.TypeVarString)
	result.CreateFiled("alterActions", mysql.TypeVarString)
	result.data = [][]types.Datum{{
		types.NewStringDatum(schema),
		types.NewStringDatum(table),
		types.NewStringDatum(builder.String()),
	}}
	return []sqlexec.RecordSet{result}, nil
}

func mergeAlterColumn(spec *ast.AlterTableSpec) (string, error) {
	if len(spec.NewConstraints) != 0 {
		return "", errors.New("MERGE_ALTER_TABLE: table-level constraints are not supported")
	}
	if len(spec.NewColumns) != 1 {
		return "", errors.New("MERGE_ALTER_TABLE: expected exactly one column per column action")
	}
	column := spec.NewColumns[0]
	if column == nil || !plainMergeColumnName(column.Name) || column.Tp == nil || spec.Position == nil ||
		spec.IfExists || spec.IfNotExists || spec.Position.Tp == ast.ColumnPositionBefore ||
		(spec.Position.Tp == ast.ColumnPositionAfter && !plainMergeColumnName(spec.Position.RelativeColumn)) {
		return "", errors.New("MERGE_ALTER_TABLE: unsupported column definition")
	}
	// FieldType.Restore does not preserve explicit character sets on ENUM/SET.
	if (column.Tp.Tp == mysql.TypeEnum || column.Tp.Tp == mysql.TypeSet) &&
		(column.Tp.Charset != "" || column.Tp.Collate != "") {
		return "", errors.New("MERGE_ALTER_TABLE: unsupported column definition")
	}
	for _, option := range column.Options {
		switch option.Tp {
		case ast.ColumnOptionNotNull, ast.ColumnOptionNull, ast.ColumnOptionComment, ast.ColumnOptionCollate:
		case ast.ColumnOptionDefaultValue, ast.ColumnOptionOnUpdate:
			switch expr := option.Expr.(type) {
			case *ast.ValueExpr:
			case *ast.UnaryOperationExpr:
				if _, ok := expr.V.(*ast.ValueExpr); !ok {
					return "", errors.New("MERGE_ALTER_TABLE: unsupported column definition")
				}
			case *ast.FuncCallExpr:
				if expr.FnName.L != ast.CurrentTimestamp {
					return "", errors.New("MERGE_ALTER_TABLE: unsupported column definition")
				}
			default:
				return "", errors.New("MERGE_ALTER_TABLE: unsupported column definition")
			}
		default:
			return "", errors.New("MERGE_ALTER_TABLE: unsupported column definition")
		}
	}
	return column.Name.Name.L, nil
}

func plainMergeColumnName(name *ast.ColumnName) bool {
	return name != nil && name.Name.O != "" && name.Table.O == "" && name.Schema.O == ""
}

func plainMergeIndex(index *ast.Constraint) bool {
	if index == nil || (index.Tp != ast.ConstraintKey && index.Tp != ast.ConstraintIndex) ||
		index.Name == "" || index.IfNotExists || len(index.Keys) == 0 || len(index.ColGroupOption) != 0 {
		return false
	}
	if o := index.Option; o != nil && (o.PartitionIndexType != model.PartitionIndexTypeInvalid ||
		o.KeyBlockSize != 0 || (o.Tp != model.IndexTypeInvalid && o.Tp != model.IndexTypeBtree) ||
		o.ParserName.O != "" || o.Visibility != ast.VisibilityDefault ||
		o.PrimaryKeyTp != model.PrimaryKeyTypeDefault || len(o.Columns) != 0) {
		return false
	}
	for _, key := range index.Keys {
		if key == nil || key.Expr != nil || !plainMergeColumnName(key.Column) {
			return false
		}
	}
	return true
}
