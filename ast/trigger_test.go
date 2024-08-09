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

func TestTriggerVisitorCover(t *testing.T) {
	stmts := []ast.StmtNode{
		&ast.TriggerInfo{TriggerBody: &ast.ProcedureBlock{}},
		&ast.DropTriggerStmt{},
	}
	for _, v := range stmts {
		v.Accept(visitor{})
		v.Accept(visitor1{})
	}
}

func TestTrigger(t *testing.T) {
	p := parser.New()
	testcases := []string{"create trigger proc_2 before delete on t1 for each row precedes begin INSERT INTO test2 SET a2 = NEW.a1; end;"}
	for _, testcase := range testcases {
		stmt, _, err := p.Parse(testcase, "", "")
		if err != nil {
			fmt.Println(testcase)
		}
		require.NoError(t, err)
		_, ok := stmt[0].(*ast.TriggerInfo)
		require.True(t, ok, testcase)
	}
}

func TestShowCreateTrigger(t *testing.T) {
	p := parser.New()
	stmt, _, err := p.Parse("show create trigger proc_2", "", "")
	require.NoError(t, err)
	_, ok := stmt[0].(*ast.ShowStmt)
	require.True(t, ok)
	stmt, _, err = p.Parse("drop trigger proc_2", "", "")
	require.NoError(t, err)
	_, ok = stmt[0].(*ast.DropTriggerStmt)
	require.True(t, ok)
}

func TestTriggerVisitor(t *testing.T) {
	sqls := []string{
		"create trigger proc_2 before delete on t1 for each row precedes begin INSERT INTO test2 SET a2 = NEW.a1; end;",
		"CREATE TRIGGER ins_sum BEFORE INSERT ON account FOR EACH ROW begin IF NEW.amount < 0 THEN SET NEW.amount = 0; END IF; END;",
		"CREATE TRIGGER ins_sum BEFORE INSERT ON account FOR EACH ROW SET @sum = @sum + NEW.amount;",
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

func TestTriggerRestore(t *testing.T) {
	testCases := []NodeRestoreTestCase{
		{"CREATE DEFINER = CURRENT_USER TRIGGER `trigg_2` BEFORE INSERT ON `t1` FOR EACH ROW BEGIN SELECT 1; END",
			"CREATE DEFINER = CURRENT_USER TRIGGER `trigg_2` BEFORE INSERT ON `t1` FOR EACH ROW BEGIN SELECT 1; END",
		},
	}
	extractNodeFunc := func(node ast.Node) ast.Node {
		return node.(*ast.TriggerInfo)
	}
	runNodeRestoreTest(t, testCases, "%s", extractNodeFunc)
}
