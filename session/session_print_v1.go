package session

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/hanchuanchuan/goInception/ast"
	"github.com/hanchuanchuan/goInception/model"
	"github.com/hanchuanchuan/goInception/parser/opcode"
)

// Wire DTOs only: no parser enums, inferred/default schemas, or persistence fields.
type queryPrintV1Table struct {
	Role           string `json:"role"`
	Schema         string `json:"schema"`
	Name           string `json:"name"`
	SchemaExplicit bool   `json:"schemaExplicit"`
}

type queryPrintV1Summary struct {
	ContractVersion int                 `json:"contractVersion"`
	StatementType   string              `json:"statementType"`
	SqlSha256       string              `json:"sqlSha256"`
	Complete        bool                `json:"complete"`
	Database        string              `json:"database"`
	Tables          []queryPrintV1Table `json:"tables"`
	Actions         []string            `json:"actions"`
}

func summarizeQueryPrintV1(node ast.StmtNode, sql string) queryPrintV1Summary {
	summary := queryPrintV1Summary{ContractVersion: 1, StatementType: "unknown", SqlSha256: fmt.Sprintf("%x", sha256.Sum256([]byte(sql))), Tables: []queryPrintV1Table{}, Actions: []string{}}
	v := queryPrintV1Visitor{summary: &summary, roles: make(map[*ast.TableName]string), seen: make(map[*ast.TableName]bool), alterDefaultOptions: make(map[*ast.ColumnOption]bool), alterDefaultColumns: make(map[*ast.ColumnDef]bool)}
	switch n := node.(type) {
	case *ast.CreateTableStmt:
		summary.StatementType = "createTable"
		summary.Actions = []string{"createDefinition"}
		summary.Complete = n.Table != nil && len(n.Cols) > 0 && n.TemporaryKeyword == ast.TemporaryNone && !n.OnCommitDelete && n.ReferTable == nil && n.Select == nil && n.Partition == nil && len(n.ColGroupOption) == 0 && n.OnDuplicate == ast.OnDuplicateKeyHandlingError
		v.roles[n.Table] = "target"
	case *ast.AlterTableStmt:
		summary.StatementType = "alterTable"
		summary.Complete = n.Table != nil && len(n.Specs) > 0
		v.roles[n.Table] = "target"
		for _, spec := range n.Specs {
			action, ok := queryPrintV1AlterAction(spec.Tp)
			summary.Actions = append(summary.Actions, action)
			if !ok {
				summary.Complete = false
			}
			if spec.Tp == ast.AlterTableRenameTable {
				v.roles[spec.NewTable] = "renameTarget"
			}
		}
	case *ast.DropTableStmt:
		if !n.IsView && !n.IsMvView {
			summary.StatementType = "dropTable"
			summary.Actions = []string{"dropTable"}
			summary.Complete = len(n.Tables) > 0 && n.TemporaryKeyword == ast.TemporaryNone
			for _, table := range n.Tables {
				v.roles[table] = "target"
			}
		}
	case *ast.TruncateTableStmt:
		summary.StatementType = "truncateTable"
		summary.Actions = []string{"truncateTable"}
		summary.Complete = n.Table != nil
		v.roles[n.Table] = "target"
	case *ast.DropStatsStmt:
		summary.StatementType = "dropStats"
	case *ast.UseStmt:
		summary.StatementType = "use"
		summary.Database = n.DBName
		summary.Complete = true
	case *ast.SetStmt:
		summary.StatementType = "set"
	case *ast.InceptionCommitStmt:
		summary.StatementType = "batchEnd"
		summary.Complete = true
	}
	node.Accept(&v)
	return summary
}

func queryPrintV1AlterAction(tp ast.AlterTableType) (string, bool) {
	switch tp {
	case ast.AlterTableAddColumns:
		return "addColumn", true
	case ast.AlterTableAddConstraint:
		return "addConstraint", true
	case ast.AlterTableDropColumn:
		return "dropColumn", true
	case ast.AlterTableDropIndex:
		return "dropIndex", true
	case ast.AlterTableDropPrimaryKey:
		return "dropPrimaryKey", true
	case ast.AlterTableModifyColumn:
		return "modifyColumn", true
	case ast.AlterTableChangeColumn:
		return "changeColumn", true
	case ast.AlterTableAlterColumn:
		return "alterColumn", true
	case ast.AlterTableAlgorithm:
		return "algorithm", true
	case ast.AlterTableLock:
		return "lock", true
	case ast.AlterTableRenameTable:
		return "renameTable", false
	default:
		return "unknown", false
	}
}

type queryPrintV1Visitor struct {
	summary             *queryPrintV1Summary
	roles               map[*ast.TableName]string
	seen                map[*ast.TableName]bool
	alterDefaultOptions map[*ast.ColumnOption]bool
	alterDefaultColumns map[*ast.ColumnDef]bool
}

func (v *queryPrintV1Visitor) table(n *ast.TableName) {
	if n == nil || v.seen[n] {
		return
	}
	v.seen[n] = true
	role := v.roles[n]
	if role == "" {
		role = "reference"
		v.summary.Complete = false
	}
	v.summary.Tables = append(v.summary.Tables, queryPrintV1Table{Role: role, Schema: n.Schema.O, Name: n.Name.O, SchemaExplicit: n.Schema.O != ""})
	if n.Name.O == "" || len(n.IndexHints) > 0 || len(n.PartitionNames) > 0 || n.TableSample != nil || n.AsOf != nil {
		v.summary.Complete = false
	}
}

// TableOption and ColumnOption.Refer are omitted by this parser's Accept methods.
// Traverse them explicitly rather than interpreting serialized trees or SQL text.
func (v *queryPrintV1Visitor) tableOptions(options []*ast.TableOption) {
	for _, option := range options {
		if option == nil {
			v.summary.Complete = false
			continue
		}
		for _, table := range option.TableNames {
			v.table(table)
		}
		if len(option.TableNames) > 0 {
			v.summary.Complete = false
		}
		switch option.Tp {
		case ast.TableOptionEngine:
			if !strings.EqualFold(option.StrValue, "InnoDB") {
				v.summary.Complete = false
			}
		case ast.TableOptionCharset, ast.TableOptionCollate, ast.TableOptionComment, ast.TableOptionAutoIncrement:
		default:
			v.summary.Complete = false
		}
	}
}

func (v *queryPrintV1Visitor) Enter(node ast.Node) (ast.Node, bool) {
	switch n := node.(type) {
	case *ast.CreateTableStmt:
		v.table(n.Table)
		v.tableOptions(n.Options)
	case *ast.AlterTableStmt:
	case *ast.DropTableStmt, *ast.TruncateTableStmt, *ast.UseStmt, *ast.InceptionCommitStmt:
	case *ast.TableName:
		v.table(n)
	case *ast.AlterTableSpec:
		// Reject every payload outside the narrow supported action's own shape.
		if len(n.Options) > 0 {
			v.tableOptions(n.Options)
			v.summary.Complete = false
		}
		if n.NoWriteToBinlog || n.OnAllPartitions || len(n.OrderByList) > 0 || n.NewTable != nil || len(n.ColGroupName) > 0 || n.NewColumnName != nil || n.Comment != "" || n.FromKey.O != "" || n.ToKey.O != "" || n.Partition != nil || len(n.PartitionNames) > 0 || len(n.PartDefinitions) > 0 || n.WithValidation || n.Num != 0 || n.Visibility != ast.VisibilityDefault || n.TiFlashReplica != nil || len(n.PlacementSpecs) > 0 || n.Writeable || n.Statistics != nil || n.AttributesSpec != nil {
			v.summary.Complete = false
		}
		if n.NewTable != nil {
			v.table(n.NewTable)
		}
		_, supported := queryPrintV1AlterAction(n.Tp)
		if !supported {
			v.summary.Complete = false
		}
		switch n.Tp {
		case ast.AlterTableAddColumns, ast.AlterTableModifyColumn, ast.AlterTableChangeColumn:
			if len(n.NewColumns) == 0 || n.Constraint != nil || (n.Tp != ast.AlterTableAddColumns && len(n.NewColumns) != 1) || (n.Tp == ast.AlterTableChangeColumn && n.OldColumnName == nil) {
				v.summary.Complete = false
			}
		case ast.AlterTableAddConstraint:
			if n.Constraint == nil || len(n.NewColumns) > 0 || len(n.NewConstraints) > 0 || n.OldColumnName != nil || n.Position != nil {
				v.summary.Complete = false
			}
		case ast.AlterTableAlterColumn:
			if len(n.NewColumns) != 1 || n.Constraint != nil || len(n.NewConstraints) > 0 || n.OldColumnName != nil || n.Position != nil {
				v.summary.Complete = false
			} else {
				col := n.NewColumns[0]
				v.alterDefaultColumns[col] = true
				if col.Tp != nil || len(col.Options) > 1 {
					v.summary.Complete = false
				}
				for _, option := range col.Options {
					// SET DEFAULT is parsed as NoOption, unlike CREATE column defaults.
					v.alterDefaultOptions[option] = true
				}
			}
		default:
			if len(n.NewColumns) > 0 || len(n.NewConstraints) > 0 || n.Constraint != nil || n.Position != nil {
				v.summary.Complete = false
			}
		}
		if n.Tp == ast.AlterTableAlgorithm && n.Algorithm > ast.AlgorithmTypeInstant {
			v.summary.Complete = false
		}
		if n.Tp == ast.AlterTableLock && (n.LockType < ast.LockTypeNone || n.LockType > ast.LockTypeExclusive) {
			v.summary.Complete = false
		}
	case *ast.ColumnDef:
		// ALTER COLUMN SET/DROP DEFAULT has no FieldType; the enclosing action validates its shape.
		if n.Name == nil || (n.Tp == nil && !v.alterDefaultColumns[n]) {
			v.summary.Complete = false
		}
	case *ast.ColumnName:
		if n.Schema.O != "" || n.Table.O != "" {
			v.summary.Complete = false
		}
	case *ast.ColumnPosition:
		if n.Tp != ast.ColumnPositionNone && n.Tp != ast.ColumnPositionFirst && n.Tp != ast.ColumnPositionAfter {
			v.summary.Complete = false
		}
	case *ast.ColumnOption:
		if n.Refer != nil {
			v.summary.Complete = false
			if n.Refer.Table != nil {
				v.table(n.Refer.Table)
			}
		}
		if n.Stored || n.UintValue != 0 || n.AutoRandomBitLength != 0 || n.PrimaryKeyTp != model.PrimaryKeyTypeDefault || n.Visibility != ast.VisibilityDefault {
			v.summary.Complete = false
		}
		switch n.Tp {
		case ast.ColumnOptionNoOption:
			if !v.alterDefaultOptions[n] || !queryPrintV1Literal(n.Expr) {
				v.summary.Complete = false
			}
		case ast.ColumnOptionPrimaryKey, ast.ColumnOptionNotNull, ast.ColumnOptionAutoIncrement, ast.ColumnOptionUniqKey, ast.ColumnOptionNull, ast.ColumnOptionCollate:
			if n.Expr != nil {
				v.summary.Complete = false
			}
		case ast.ColumnOptionDefaultValue, ast.ColumnOptionComment:
			if !queryPrintV1Literal(n.Expr) {
				v.summary.Complete = false
			}
		default:
			v.summary.Complete = false
		}
	case *ast.Constraint:
		switch n.Tp {
		case ast.ConstraintPrimaryKey, ast.ConstraintKey, ast.ConstraintIndex, ast.ConstraintUniq, ast.ConstraintUniqKey, ast.ConstraintUniqIndex:
		default:
			v.summary.Complete = false
		}
		if n.Refer != nil || n.Expr != nil || len(n.ColGroupOption) > 0 || len(n.ColumnNames) > 0 || len(n.Keys) == 0 {
			v.summary.Complete = false
		}
	case *ast.IndexPartSpecification:
		if n.Column == nil || n.Expr != nil {
			v.summary.Complete = false
		}
	case *ast.IndexOption:
		if n.PartitionIndexType != model.PartitionIndexTypeInvalid || n.KeyBlockSize != 0 || (n.Tp != model.IndexTypeInvalid && n.Tp != model.IndexTypeBtree && n.Tp != model.IndexTypeHash) || n.ParserName.O != "" || n.Visibility != ast.VisibilityDefault || n.PrimaryKeyTp != model.PrimaryKeyTypeDefault || len(n.Columns) > 0 {
			v.summary.Complete = false
		}
	case *ast.ValueExpr:
	case *ast.UnaryOperationExpr:
		if !queryPrintV1Literal(n) {
			v.summary.Complete = false
		}
	default:
		// Includes subqueries, generated/check/function expressions, views, and future AST nodes.
		v.summary.Complete = false
	}
	return node, false
}
func (*queryPrintV1Visitor) Leave(node ast.Node) (ast.Node, bool) { return node, true }

func queryPrintV1Literal(expr ast.ExprNode) bool {
	switch n := expr.(type) {
	case *ast.ValueExpr:
		return true
	case *ast.UnaryOperationExpr:
		return (n.Op == opcode.Plus || n.Op == opcode.Minus) && queryPrintV1Literal(n.V)
	default:
		return false
	}
}
