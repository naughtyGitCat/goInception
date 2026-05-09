// Copyright 2016 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package parser

import (
	"github.com/hanchuanchuan/goInception/ast"
	. "github.com/pingcap/check"
)

var _ = Suite(&testPartitionSuite{})

type testPartitionSuite struct {
}

// TestPartitionSyntax 测试分区语法解析
func (s *testPartitionSuite) TestPartitionSyntax(c *C) {
	tests := []struct {
		sql         string
		expectError bool
		description string
	}{
		{
			sql:         "select * from t;",
			expectError: false,
			description: "简单SELECT语句",
		},
		{
			sql:         "select * from t partition(p0);",
			expectError: false,
			description: "分区语法",
		},
		{
			sql:         "select * from t partition(p0,p1,p2);",
			expectError: false,
			description: "多分区语法",
		},
		{
			// MySQL 标准语法: partition 在 alias 之前
			// （production 的 parser.y 与 MySQL 一致；OB Integration 的 commit
			// 把分区放在 alias 之后，是非标准写法，故这里调整为标准写法）
			sql:         "select * from t partition(p0) as alias;",
			expectError: false,
			description: "分区+别名语法",
		},
	}

	for _, tt := range tests {
		c.Logf("\n=== 测试: %s ===", tt.description)
		c.Logf("SQL: %s", tt.sql)

		stmt, err := New().ParseOneStmt(tt.sql, "", "")
		if tt.expectError {
			c.Check(err, NotNil, Commentf("期望解析错误但解析成功: %s", tt.sql))
			continue
		}

		if err != nil {
			c.Errorf("解析错误: %v", err)
			continue
		}

		c.Logf("解析成功!")

		// 如果是SELECT语句，检查分区结构
		if selectStmt, ok := stmt.(*ast.SelectStmt); ok {
			s.checkSelectPartitionStructure(c, selectStmt)
		}
	}
}

// checkSelectPartitionStructure 检查SELECT语句的分区结构
func (s *testPartitionSuite) checkSelectPartitionStructure(c *C, selectStmt *ast.SelectStmt) {
	if selectStmt.From == nil || selectStmt.From.TableRefs == nil {
		c.Log("SELECT语句没有FROM子句")
		return
	}

	left := selectStmt.From.TableRefs.Left
	if left == nil {
		c.Log("SELECT语句FROM子句为空")
		return
	}

	tableSource, ok := left.(*ast.TableSource)
	if !ok {
		c.Log("FROM子句不是TableSource类型")
		return
	}

	tableName, ok := tableSource.Source.(*ast.TableName)
	if !ok {
		c.Log("TableSource的Source不是TableName类型")
		return
	}

	c.Logf("表名: %s", tableName.Name.String())
	c.Logf("分区数量: %d", len(tableName.PartitionNames))

	if len(tableName.PartitionNames) > 0 {
		for i, partitionName := range tableName.PartitionNames {
			c.Logf("分区名[%d]: %s", i, partitionName.String())
		}
	}
}
