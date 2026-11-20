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

func (s *session) parseAlterTarget(sql string) ([]sqlexec.RecordSet, error) {
	if strings.ContainsRune(sql, 0) {
		return nil, errors.New("PARSE_ALTER_TARGET: invalid SQL syntax")
	}
	charset, collation := s.sessionVars.GetCharsetInfo()
	// The parser must not inherit a mode learned from a previous target connection.
	nodes, warnings, err := parser.New().Parse(sql, charset, collation)
	if err != nil {
		return nil, errors.New("PARSE_ALTER_TARGET: invalid SQL syntax")
	}
	if len(warnings) != 0 {
		return nil, errors.New("PARSE_ALTER_TARGET: input produced a parser warning")
	}
	if len(nodes) != 1 {
		return nil, errors.New("PARSE_ALTER_TARGET: expected exactly one ALTER TABLE")
	}
	alter, ok := nodes[0].(*ast.AlterTableStmt)
	if !ok || alter.Table == nil || alter.Table.Name.O == "" {
		return nil, errors.New("PARSE_ALTER_TARGET: expected exactly one ALTER TABLE")
	}
	schema, table := alter.Table.Schema.O, alter.Table.Name.O
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
