package session

import (
	"bytes"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestSingleDMLSourceOptionErrorsDoNotLogCredentials(t *testing.T) {
	var output bytes.Buffer
	previous := log.StandardLogger().Out
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	for _, options := range []string{
		"--password=SENSITIVE_LOG_TEST;--single-dml=1;--wait-timeout=300;--enable-execute;",
		"--password='SENSITIVE_LOG_TEST;--single-dml=1;--enable-execute;",
	} {
		s := NewInception()
		s.init()
		s.recordSets = NewRecordSets()
		s.myRecord = &Record{Stage: StageCheck, Buf: new(bytes.Buffer)}
		s.parseOptions("/*" + options + "*/inception_magic_start")
		if s.myRecord.ErrLevel != 2 {
			t.Fatal("invalid configuration must fail closed")
		}
	}
	if strings.Contains(output.String(), "SENSITIVE_LOG_TEST") || strings.Contains(output.String(), "password:") {
		t.Fatalf("source validation logged credentials: %s", output.String())
	}
}

func TestSingleDMLSourceOptionParsing(t *testing.T) {
	cases := []struct {
		name       string
		options    string
		mode       bool
		timeout    int
		errorToken string
	}{
		{"enabled_numeric", "--single-dml=1;--wait-timeout=900;--trans=0;--enable-execute;", true, 900, ""},
		{"enabled_boolean", "--enable-single-dml;--wait-timeout=15;--enable-check;", true, 15, ""},
		{"disabled_legacy", "--disable-single-dml;--wait-timeout=7200;--enable-execute;", false, 7200, ""},
		{"bad_mode", "--single-dml=maybe;--wait-timeout=300;--enable-execute;", false, 300, "single_dml_invalid_option"},
		{"fractional_timeout", "--single-dml=1;--wait-timeout=1.5;--enable-execute;", true, 0, "single_dml_invalid_integer_option"},
		{"invalid_timeout", "--single-dml=1;--wait-timeout=not-a-number;--enable-execute;", true, 0, "single_dml_invalid_integer_option"},
		{"fractional_trans", "--single-dml=1;--wait-timeout=300;--trans=0.5;--enable-execute;", true, 300, "single_dml_invalid_integer_option"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewInception()
			s.init()
			s.recordSets = NewRecordSets()
			s.myRecord = &Record{Stage: StageCheck, Buf: new(bytes.Buffer)}
			// No target is supplied, so successful decoding stops at source validation without dialing a DB.
			s.parseOptions("/*" + tc.options + "*/inception_magic_start")
			if s.opt == nil || s.opt.SingleDML != tc.mode {
				t.Fatalf("mode=%v, options=%+v", tc.mode, s.opt)
			}
			if tc.errorToken == "" {
				if s.opt.WaitTimeout != tc.timeout {
					t.Fatalf("timeout=%d, want %d", s.opt.WaitTimeout, tc.timeout)
				}
				if strings.Contains(s.myRecord.Buf.String(), "single_dml_invalid") {
					t.Fatalf("valid options rejected: %s", s.myRecord.Buf.String())
				}
			} else if s.myRecord.ErrLevel != 2 || !strings.Contains(s.myRecord.Buf.String(), tc.errorToken) {
				t.Fatalf("invalid option must fail closed, level=%d error=%s", s.myRecord.ErrLevel, s.myRecord.Buf.String())
			}
		})
	}
}
