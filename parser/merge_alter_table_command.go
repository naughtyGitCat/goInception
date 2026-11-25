package parser

import (
	"errors"
	"strings"
)

// ReadMergeAlterTableCommand accepts the local command only as a standalone
// request. The subcommand is not a keyword in the SQL grammar.
func ReadMergeAlterTableCommand(sql string) (payload string, matched bool, err error) {
	s := NewScanner(sql)
	first, atStatementStart := true, true
	for {
		previous := s.r.pos().Offset
		tok, _, word := s.scan()
		if atStatementStart && tok == identifier && strings.EqualFold(word, "INCEPTION") {
			tok, _, word = s.scan()
			if tok == identifier && strings.EqualFold(word, "MERGE_ALTER_TABLE") {
				if !first || s.inBangComment {
					return "", true, errors.New("MERGE_ALTER_TABLE: must be a standalone command")
				}
				return s.readMergeAlterTableArgument()
			}
		}
		if tok == 0 || s.r.pos().Offset <= previous {
			return "", false, nil
		}
		first, atStatementStart = false, tok == ';'
	}
}

func (s *Scanner) readMergeAlterTableArgument() (string, bool, error) {
	tok, pos, payload := s.scan()
	if tok != stringLit || s.r.s[pos.Offset] != '\'' || s.inBangComment {
		return "", true, errors.New("MERGE_ALTER_TABLE: expected one quoted SQL argument")
	}
	tok, _, _ = s.scan()
	if tok == ';' {
		tok, _, _ = s.scan()
	}
	if tok != 0 || len(s.errs) != 0 || s.inBangComment || !s.r.eof() {
		return "", true, errors.New("MERGE_ALTER_TABLE: must be a standalone command")
	}
	return payload, true, nil
}

// MergeAlterTablePayloadIssues detects syntax that the default parser and AST printer cannot preserve.
func MergeAlterTablePayloadIssues(sql string) (executableComment, alterIgnoreTable, ambiguousQuoting, unsupportedIndexSyntax bool) {
	s := NewScanner(sql)
	atStatementStart := true
	const (
		expectNone = iota
		expectIgnore
		expectTable
	)
	expect := expectNone
	for {
		previous := s.r.pos().Offset
		tok, pos, word := s.scan()
		if s.inBangComment {
			executableComment = true
		}
		if tok == stringLit && (sql[pos.Offset] == '"' ||
			strings.ContainsRune(sql[pos.Offset:s.r.pos().Offset], '\\')) {
			ambiguousQuoting = true
		}
		if tok == 0 || len(s.errs) != 0 || s.r.pos().Offset <= previous {
			return
		}
		if tok == identifier && sql[pos.Offset] != '`' {
			switch strings.ToUpper(word) {
			case "STORING", "LOCAL", "GLOBAL":
				unsupportedIndexSyntax = true
			}
		}
		switch expect {
		case expectIgnore:
			expect = expectNone
			if tok == identifier && strings.EqualFold(word, "IGNORE") {
				expect = expectTable
			}
		case expectTable:
			expect = expectNone
			if tok == identifier && strings.EqualFold(word, "TABLE") {
				alterIgnoreTable = true
			}
		}
		if atStatementStart && tok == identifier && strings.EqualFold(word, "ALTER") {
			expect = expectIgnore
		}
		atStatementStart = tok == ';'
	}
}
