package session

import (
	"errors"
	"strings"

	"github.com/hanchuanchuan/goInception/ast"
	"github.com/hanchuanchuan/goInception/mysql"
	"github.com/hanchuanchuan/goInception/parser"
	"github.com/hanchuanchuan/goInception/types"
	"github.com/hanchuanchuan/goInception/util/sqlexec"
)

func (s *session) parseDMLTarget(sql string) ([]sqlexec.RecordSet, error) {
	if strings.ContainsRune(sql, 0) {
		return nil, errors.New("PARSE_DML_TARGET: invalid SQL syntax")
	}
	charset, collation := s.sessionVars.GetCharsetInfo()
	nodes, warnings, err := parser.New().Parse(sql, charset, collation)
	if err != nil {
		return nil, errors.New("PARSE_DML_TARGET: invalid SQL syntax")
	}
	if len(warnings) != 0 {
		return nil, errors.New("PARSE_DML_TARGET: input produced a parser warning")
	}
	if len(nodes) != 1 {
		return nil, errors.New("PARSE_DML_TARGET: expected exactly one UPDATE or DELETE")
	}
	var target *ast.TableName
	switch n := nodes[0].(type) {
	case *ast.UpdateStmt:
		if !n.MultipleTable && n.With == nil && singleDMLTable(n.TableRefs) {
			source := n.TableRefs.TableRefs.Left.(*ast.TableSource)
			table := source.Source.(*ast.TableName)
			if assignmentsMatchTarget(n.List, table, source.AsName.O) {
				target = table
			}
		}
	case *ast.DeleteStmt:
		if !n.IsMultiTable && n.Tables == nil && n.With == nil && singleDMLTable(n.TableRefs) {
			target = n.TableRefs.TableRefs.Left.(*ast.TableSource).Source.(*ast.TableName)
		}
	default:
		return nil, errors.New("PARSE_DML_TARGET: expected exactly one UPDATE or DELETE")
	}
	if target == nil || target.Name.O == "" || target.Schema.O == "*" || target.AsOf != nil || target.TableSample != nil {
		return nil, errors.New("PARSE_DML_TARGET: expected a single named table")
	}
	schema, table := target.Schema.O, target.Name.O
	var explicit int64
	if schema != "" {
		explicit = 1
	}
	result := &recordSet{count: 1, fields: make([]*ast.ResultField, 3)}
	result.CreateFiled("schema", mysql.TypeVarString)
	result.CreateFiled("table", mysql.TypeVarString)
	result.CreateFiled("schemaExplicit", mysql.TypeLonglong)
	result.data = [][]types.Datum{{types.NewStringDatum(schema), types.NewStringDatum(table), types.NewIntDatum(explicit)}}
	return []sqlexec.RecordSet{result}, nil
}

func assignmentsMatchTarget(list []*ast.Assignment, table *ast.TableName, alias string) bool {
	qualifier := table.Name.O
	if alias != "" {
		qualifier = alias
	}
	for _, assignment := range list {
		column := assignment.Column
		if column.Table.O != "" && column.Table.O != qualifier {
			return false
		}
		if column.Schema.O != "" && (alias != "" || column.Schema.O != table.Schema.O) {
			return false
		}
	}
	return true
}
