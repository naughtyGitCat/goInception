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
	"github.com/hanchuanchuan/goInception/util/auth"
	"github.com/pingcap/errors"
)

var (
	_ StmtNode = &TriggerInfo{}
	_ StmtNode = &DropTriggerStmt{}
)

type TriggerTimeKwd int

const (
	TriggerTimeKwdBefore TriggerTimeKwd = iota
	TriggerTimeKwdAfter
)

type TriggerEventKwd int

const (
	TriggerEventKwdInsert TriggerEventKwd = iota
	TriggerEventKwdUpdate
	TriggerEventKwdDelete
)

type TriggerPlaceKwd int

const (
	TriggerPlaceKwdNone TriggerPlaceKwd = iota
	TriggerPlaceKwdFollows
	TriggerPlaceKwdPrecedes
)

// TriggerInfo stores all trigger information.
type TriggerInfo struct {
	stmtNode
	Definer      *auth.UserIdentity
	TriggerName  *TableName
	TriggerTime  TriggerTimeKwd
	TriggerEvent TriggerEventKwd
	Table        *TableName
	TriggerPlace TriggerPlaceKwd
	TriggerBody  StmtNode //trigger body statement
}

// Restore implements Node interface.
func (n *TriggerInfo) Restore(ctx *format.RestoreCtx) error {
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
	ctx.WriteKeyWord(" TRIGGER ")
	err := n.TriggerName.Restore(ctx)
	if err != nil {
		return err
	}
	switch n.TriggerTime {
	case TriggerTimeKwdBefore:
		ctx.WriteKeyWord(" BEFORE ")
	case TriggerTimeKwdAfter:
		ctx.WriteKeyWord(" AFTER ")
	default:
		return errors.Errorf("invalid TriggerTimeKwd: %d", n.TriggerTime)
	}
	switch n.TriggerEvent {
	case TriggerEventKwdInsert:
		ctx.WriteKeyWord("INSERT")
	case TriggerEventKwdUpdate:
		ctx.WriteKeyWord("UPDATE")
	case TriggerEventKwdDelete:
		ctx.WriteKeyWord("DELETE")
	default:
		return errors.Errorf("invalid TriggerEventKwd: %d", n.TriggerEvent)
	}
	ctx.WriteKeyWord(" ON ")
	if err := n.Table.Restore(ctx); err != nil {
		return errors.Annotate(err, "An error occurred while splicing CreateTriggerStmt Table")
	}
	ctx.WriteKeyWord(" FOR EACH ROW ")
	switch n.TriggerPlace {
	case TriggerPlaceKwdNone:
	case TriggerPlaceKwdFollows:
		ctx.WriteKeyWord(" FOLLOWS ")
	case TriggerPlaceKwdPrecedes:
		ctx.WriteKeyWord(" PRECEDES ")
	default:
		return errors.Errorf("invalid TriggerPlaceKwd: %d", n.TriggerPlace)
	}
	err = (n.TriggerBody).Restore(ctx)
	if err != nil {
		return err
	}
	return nil
}

// Accept implements Node Accept interface.
func (n *TriggerInfo) Accept(v Visitor) (Node, bool) {
	newNode, skipChildren := v.Enter(n)
	if skipChildren {
		return v.Leave(newNode)
	}
	n = newNode.(*TriggerInfo)
	node, ok := n.TriggerBody.Accept(v)
	if !ok {
		return n, false
	}
	n.TriggerBody = node.(StmtNode)
	return v.Leave(n)
}

// DropTriggerStmt represents the ast of `drop trigger`
type DropTriggerStmt struct {
	stmtNode

	IfExists    bool
	TriggerName *TableName
}

// Restore implements DropProcedureStmt interface.
func (n *DropTriggerStmt) Restore(ctx *format.RestoreCtx) error {
	ctx.WriteKeyWord("DROP TRIGGER ")
	if n.IfExists {
		ctx.WriteKeyWord("IF EXISTS ")
	}
	err := n.TriggerName.Restore(ctx)
	if err != nil {
		return err
	}
	return nil
}

// Accept implements Node interface.
func (n *DropTriggerStmt) Accept(v Visitor) (Node, bool) {
	newNode, skipChildren := v.Enter(n)
	if skipChildren {
		return v.Leave(newNode)
	}
	n = newNode.(*DropTriggerStmt)
	return v.Leave(n)
}
