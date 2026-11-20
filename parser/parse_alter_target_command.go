package parser

import (
	"errors"
	"strings"
)

// ReadParseAlterTargetCommand accepts the local command only as a standalone
// request. The subcommand is not a keyword in the SQL grammar.
func ReadParseAlterTargetCommand(sql string) (payload string, matched bool, err error) {
	s := NewScanner(sql)
	first, atStatementStart := true, true
	for {
		previous := s.r.pos().Offset
		tok, _, word := s.scan()
		if atStatementStart && tok == identifier && strings.EqualFold(word, "INCEPTION") {
			tok, _, word = s.scan()
			if tok == identifier && strings.EqualFold(word, "PARSE_ALTER_TARGET") {
				if !first || s.inBangComment {
					return "", true, errors.New("PARSE_ALTER_TARGET: must be a standalone command")
				}
				return s.readParseAlterTargetArgument()
			}
		}
		if tok == 0 || s.r.pos().Offset <= previous {
			return "", false, nil
		}
		first, atStatementStart = false, tok == ';'
	}
}

func (s *Scanner) readParseAlterTargetArgument() (string, bool, error) {
	tok, pos, payload := s.scan()
	if tok != stringLit || s.r.s[pos.Offset] != '\'' || s.inBangComment {
		return "", true, errors.New("PARSE_ALTER_TARGET: expected one quoted SQL argument")
	}
	tok, _, _ = s.scan()
	if tok == ';' {
		tok, _, _ = s.scan()
	}
	if tok != 0 || len(s.errs) != 0 || s.inBangComment || !s.r.eof() {
		return "", true, errors.New("PARSE_ALTER_TARGET: must be a standalone command")
	}
	return payload, true, nil
}
