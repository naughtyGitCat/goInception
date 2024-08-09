// Copyright 2023 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package ast_test

import (
	"fmt"
	"testing"

	"github.com/hanchuanchuan/goInception/ast"
	"github.com/hanchuanchuan/goInception/parser"
	"github.com/stretchr/testify/require"
)

func TestFunctionVisitorCover(t *testing.T) {
	stmts := []ast.StmtNode{
		&ast.FunctionInfo{FunctionBody: &ast.ProcedureBlock{}},
		&ast.DropFunctionStmt{},
	}
	for _, v := range stmts {
		v.Accept(visitor{})
		v.Accept(visitor1{})
	}
}

func TestFunction(t *testing.T) {
	p := parser.New()
	testcases := []string{"create function proc_2(id bigint) RETURNS int(10) begin select s;SELECT * FROM `t1`;SELECT * FROM `t2`;INSERT INTO `t1` VALUES (111);RETURN currval(seq_name);END;",
		"create function sp_t1() returns int RETURN 1;"}
	for _, testcase := range testcases {
		stmt, _, err := p.Parse(testcase, "", "")
		if err != nil {
			fmt.Println(testcase)
		}
		require.NoError(t, err)
		_, ok := stmt[0].(*ast.FunctionInfo)
		require.True(t, ok, testcase)
	}
}

func TestShowCreateFunction(t *testing.T) {
	p := parser.New()
	stmt, _, err := p.Parse("show create function proc_2", "", "")
	require.NoError(t, err)
	_, ok := stmt[0].(*ast.ShowStmt)
	require.True(t, ok)
	stmt, _, err = p.Parse("drop function proc_2", "", "")
	require.NoError(t, err)
	_, ok = stmt[0].(*ast.DropFunctionStmt)
	require.True(t, ok)
}

func TestFunctionVisitor(t *testing.T) {
	sqls := []string{
		"create function proc_2(id bigint) RETURNS int(10) begin select s;SELECT * FROM `t1`;SELECT * FROM `t2`;INSERT INTO `t1` VALUES (111);RETURN currval(seq_name);END;",
		"create function proc_2(id bigint) RETURNS int(10) RETURN currval(seq_name);",
		"create function proc_2(id bigint) RETURNS int(10) DETERMINISTIC RETURN currval(seq_name);",
		"create definer = 'root' function proc_2(id bigint) RETURNS int(10) NOT DETERMINISTIC RETURN currval(seq_name);",
		"show create function proc_2;",
		"drop function proc_2;",
	}
	parse := parser.New()
	for _, sql := range sqls {
		stmts, _, err := parse.Parse(sql, "", "")
		require.NoError(t, err)
		for _, stmt := range stmts {
			stmt.Accept(visitor{})
			stmt.Accept(visitor1{})
		}
	}
}

func TestFunctionRestore(t *testing.T) {
	testCases := []NodeRestoreTestCase{
		{"CREATE DEFINER = CURRENT_USER FUNCTION `func_2`() RETURNS INT(10) BEGIN SELECT 1; END",
			"CREATE DEFINER = CURRENT_USER FUNCTION `func_2`() RETURNS INT(10) BEGIN SELECT 1; END",
		},
	}
	extractNodeFunc := func(node ast.Node) ast.Node {
		return node.(*ast.FunctionInfo)
	}
	runNodeRestoreTest(t, testCases, "%s", extractNodeFunc)
}
