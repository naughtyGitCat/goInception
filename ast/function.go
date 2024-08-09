// Copyright 2023 PingCAP, Inc.
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

package ast

import (
	"github.com/hanchuanchuan/goInception/format"
	"github.com/hanchuanchuan/goInception/types"
	"github.com/hanchuanchuan/goInception/util/auth"
	"github.com/pingcap/errors"
)

var (
	_ StmtNode = &FunctionInfo{}
	_ StmtNode = &DropFunctionStmt{}
)

// FunctionInfo stores all function information.
type FunctionInfo struct {
	stmtNode
	Definer           *auth.UserIdentity
	IfNotExists       bool
	FunctionName      *TableName
	FunctionParam     []*StoreParameter //function param
	FunctionParamType *types.FieldType
	FunctionBody      StmtNode //function body statement
	FunctionParamStr  string   //function parameter string
	FunctionOptions   []*RoutineOption
}

// Restore implements Node interface.
func (n *FunctionInfo) Restore(ctx *format.RestoreCtx) error {
	ctx.WriteKeyWord("CREATE")
	ctx.WriteKeyWord(" DEFINER")
	ctx.WritePlain(" = ")

	// todo Use n.Definer.Restore(ctx) to replace this part
	if n.Definer.CurrentUser {
		ctx.WriteKeyWord("current_user")
	} else {
		ctx.WriteName(n.Definer.Username)
		if n.Definer.Hostname != "" {
			ctx.WritePlain("@")
			ctx.WriteName(n.Definer.Hostname)
		}
	}
	ctx.WriteKeyWord(" FUNCTION ")
	if n.IfNotExists {
		ctx.WriteKeyWord("IF NOT EXISTS ")
	}
	err := n.FunctionName.Restore(ctx)
	if err != nil {
		return err
	}
	ctx.WritePlain("(")
	for i, FunctionParam := range n.FunctionParam {
		if i > 0 {
			ctx.WritePlain(",")
		}
		err := FunctionParam.Restore(ctx)
		if err != nil {
			return err
		}
	}
	ctx.WritePlain(") ")
	ctx.WriteKeyWord("RETURNS")
	if n.FunctionParamType != nil {
		ctx.WritePlain(" ")
		if err := n.FunctionParamType.Restore(ctx); err != nil {
			return errors.Annotate(err, "An error occurred while splicing FunctionParamType Type")
		}
		ctx.WritePlain(" ")
	}
	err = (n.FunctionBody).Restore(ctx)
	if err != nil {
		return err
	}
	return nil
}

// Accept implements Node Accept interface.
func (n *FunctionInfo) Accept(v Visitor) (Node, bool) {
	newNode, skipChildren := v.Enter(n)
	if skipChildren {
		return v.Leave(newNode)
	}
	n = newNode.(*FunctionInfo)
	for i, FunctionParam := range n.FunctionParam {
		node, ok := FunctionParam.Accept(v)
		if !ok {
			return n, false
		}
		n.FunctionParam[i] = node.(*StoreParameter)
	}
	node, ok := n.FunctionBody.Accept(v)
	if !ok {
		return n, false
	}
	n.FunctionBody = node.(StmtNode)
	return v.Leave(n)
}

// RoutineOptionType is the type for RoutineOption
type RoutineOptionType int

// RoutineOption types.
const (
	RoutineOptionNone RoutineOptionType = iota
	RoutineOptionComment
	RoutineOptionLanguageSql
	RoutineOptionDeterministic
	RoutineOptionNotDeterministic
	RoutineOptionSqlSecurity
)

// RoutineOption is used for parsing procedure and function option from SQL.
type RoutineOption struct {
	Tp       RoutineOptionType
	StrValue string
}

func (n *RoutineOption) Restore(ctx *format.RestoreCtx) error {
	switch n.Tp {
	case RoutineOptionComment:
		ctx.WriteKeyWord("COMMENT ")
		if n.StrValue != "" {
			ctx.WritePlain(n.StrValue)
		}
	case RoutineOptionLanguageSql:
		ctx.WriteKeyWord("LANGUAGE SQL")
	case RoutineOptionDeterministic:
		ctx.WriteKeyWord("DETERMINISTIC")
	case RoutineOptionNotDeterministic:
		ctx.WriteKeyWord("NOT DETERMINISTIC")
	default:
		return errors.Errorf("invalid RoutineOption: %d", n.Tp)
	}
	return nil
}
