package session

import (
	"context"
	"strings"
	"testing"
)

func TestOceanBaseEnumSetLiteralClassification(t *testing.T) {
	cases := []struct {
		name, old, next string
		online          bool
	}{
		{"unchanged uppercase", "('A','B')", "('A','B')", true},
		{"unchanged lowercase", "('a','b')", "('a','b')", true},
		{"case only", "('A','B')", "('a','b')", false},
		{"one case changed", "('A','B')", "('A','b')", false},
		{"order changed", "('A','B')", "('B','A')", false},
		{"member appended", "('A','B')", "('A','B','C')", false},
		{"member removed", "('A','B')", "('A')", false},
		{"member replaced", "('A','B')", "('A','C')", false},
		{"punctuation unchanged", "('A,B','C(D)')", "('A,B','C(D)')", true},
		{"punctuation case changed", "('A,B','C(D)')", "('a,b','c(d)')", false},
		{"escaped quote unchanged", `('A''B','C')`, `('A''B','C')`, true},
		{"escaped quote case changed", `('A''B','C')`, `('a''b','C')`, false},
		{"backslash unchanged", `('A\\B','C')`, `('A\\B','C')`, true},
		{"backslash case changed", `('A\\B','C')`, `('a\\b','C')`, false},
		{"backslash count changed", `('A\\B','C')`, `('A\\\\B','C')`, false},
		{"unicode case changed", "('Ä','B')", "('ä','B')", false},
		{"empty member unchanged", "('','A')", "('','A')", true},
	}
	for _, kind := range []string{"enum", "set"} {
		for _, collation := range []string{"utf8mb4_general_ci", "utf8mb4_bin", "utf8mb4_0900_as_cs"} {
			for _, verb := range []string{"MODIFY COLUMN e", "CHANGE COLUMN e renamed"} {
				for _, tc := range cases {
					t.Run(kind+"/"+collation+"/"+verb+"/"+tc.name, func(t *testing.T) {
						s, table := oceanbaseAddSession()
						table.Fields = append(table.Fields, FieldInfo{Field: "e", Type: strings.ToUpper(kind) + tc.old, Collation: collation})
						node := oceanbaseAddStatement(t, "ALTER TABLE t "+verb+" "+kind+tc.next)
						if got := s.isOBAlterOnline(node, table); got != tc.online {
							t.Fatalf("%s%s -> %s%s online=%v want=%v", kind, tc.old, kind, tc.next, got, tc.online)
						}
					})
				}
			}
		}
	}
	for _, types := range [][2]string{{"enum", "set"}, {"set", "enum"}} {
		s, table := oceanbaseAddSession()
		table.Fields = append(table.Fields, FieldInfo{Field: "e", Type: types[0] + "('A','B')"})
		if s.isOBAlterOnline(oceanbaseAddStatement(t, "ALTER TABLE t MODIFY COLUMN e "+types[1]+"('A','B')"), table) {
			t.Fatalf("%s -> %s must be offline", types[0], types[1])
		}
	}
}

func TestOceanBaseEnumSetOfflineNativeGuard(t *testing.T) {
	for _, kind := range []string{"enum", "set"} {
		for _, small := range []bool{false, true} {
			t.Run(kind+"/small="+map[bool]string{true: "true", false: "false"}[small], func(t *testing.T) {
				s, r := newSingleDDLSession(t, &singleDDLDriver{})
				_, table := oceanbaseAddSession()
				table.Fields = append(table.Fields, FieldInfo{Field: "e", Type: kind + "('A','B')", Collation: "utf8mb4_bin"})
				r.Sql = "ALTER TABLE t MODIFY COLUMN e " + kind + "('a','b')"
				node := oceanbaseAddStatement(t, r.Sql)
				r.Type = node
				s.inc.CheckOfflineDDL = false
				s.inc.CheckOfflineDDLLargeTableRows = 10
				count := 10
				if small {
					count = 1
				}
				d := &offlineTableTestDriver{probeRows: probeTestRows(count)}
				offlineTableDB(t, s, d)
				s.checkDDLOffline(node, table)
				if !r.obOfflineAlter || r.obOfflineSmallAlter != small || r.obOfflineLargeAlter == small || len(d.calls) != 1 {
					t.Fatalf("offline=%v small=%v large=%v probes=%v", r.obOfflineAlter, r.obOfflineSmallAlter, r.obOfflineLargeAlter, d.calls)
				}
				if small {
					if s.hasErrorBefore() {
						t.Fatalf("proven-small classification unexpectedly blocked: %s", r.Buf)
					}
					return
				}
				s.executeSingleDDL(context.Background())
				if !s.hasErrorBefore() || r.ExecComplete || len(d.singleDMLTestDriver.calls) != 0 {
					t.Fatalf("large ENUM/SET dispatched: completed=%v calls=%v", r.ExecComplete, d.singleDMLTestDriver.calls)
				}
			})
		}
	}
}
