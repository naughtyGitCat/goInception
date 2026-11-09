package main

import (
	"database/sql"
	"fmt"
	"reflect"
)

func enumSetChecks(db *sql.DB, port, expectedLevel int, user, password, prefix string, owned *[]string) []evidence {
	var results []evidence
	for index, collation := range []string{"utf8mb4_general_ci", "utf8mb4_bin", "utf8mb4_0900_as_cs"} {
		for _, count := range []int{0, thresholdRows} {
			name := fmt.Sprintf("%s_enum_%d_%d", prefix, index, count)
			label := fmt.Sprintf("enum_set_%s_%d", collation, count)
			add := func(suffix string, ok bool, ms int64, rows []wireRow, detail any) {
				results = append(results, evidence{Name: label + "_" + suffix, Passed: ok, Elapsed: ms, Rows: rows, Detail: detail})
			}
			definition := "CREATE TABLE `" + name + "` (id BIGINT NOT NULL, v VARCHAR(180) NOT NULL, dropme INT NOT NULL DEFAULT 0, enum_col ENUM('A','B') NOT NULL DEFAULT 'A', set_col SET('A','B') NOT NULL DEFAULT 'A') DEFAULT CHARSET=utf8mb4 COLLATE=" + collation
			if _, err := db.Exec(definition); err != nil {
				add("fixture_create", false, 0, nil, errString(err))
				continue
			}
			*owned = append(*owned, name)
			if count > 0 {
				if _, err := db.Exec(fixtureInsert(name, count)); err != nil {
					add("fixture_fill", false, 0, nil, errString(err))
					continue
				}
			}
			n, countErr := probeRows(db, name)
			size, sizeErr := tableSizeMB(db, name)
			ready := countErr == nil && n == count && sizeErr == nil && size < thresholdMB
			add("fixture", ready, 0, nil, map[string]any{"rows": n, "sizeMB": size, "countError": errString(countErr), "sizeError": errString(sizeErr)})
			if !ready {
				continue
			}
			for _, kind := range []string{"ENUM", "SET"} {
				column := "enum_col"
				if kind == "SET" {
					column = "set_col"
				}
				body := "ALTER TABLE `" + name + "` MODIFY COLUMN `" + column + "` " + kind
				unchanged := body + "('A','B') NOT NULL DEFAULT 'A';"
				changed := body + "('a','b') NOT NULL DEFAULT 'a';"
				rows, ms, err := frame(port, user, password, false, 0, unchanged, "--enable-check;")
				identicalOK := err == nil && ddlRow(rows) != nil && maxLevel(rows) < 2 && !hasMessage(rows, "offline DDL on a large table") && !dispatched(rows)
				add(kind+"_identical_check", identicalOK, ms, rows, errString(err))
				rows, ms, err = frame(port, user, password, false, 0, changed, "--enable-check;")
				large := count >= thresholdRows
				checkOK := err == nil && ddlRow(rows) != nil && !dispatched(rows) && hasMessage(rows, "offline DDL on a large table") == large
				if large {
					checkOK = checkOK && maxLevel(rows) == expectedLevel
				} else {
					checkOK = checkOK && maxLevel(rows) < 2
				}
				add(kind+"_case_change_check", checkOK, ms, rows, errString(err))
				if !identicalOK || !checkOK {
					continue
				}
				before, beforeErr := snapshot(db, name)
				if beforeErr != nil {
					add(kind+"_native", false, 0, nil, errString(beforeErr))
					continue
				}
				rows, ms, err = frame(port, user, password, true, 60, changed, "--enable-execute;")
				after, afterErr := snapshot(db, name)
				valuesSame := afterErr == nil && reflect.DeepEqual(before.Values, after.Values)
				schemaSame := afterErr == nil && before.Schema == after.Schema
				ok := err == nil && afterErr == nil && valuesSame
				if large {
					ok = ok && schemaSame && !dispatched(rows) && maxLevel(rows) == 2 && hasMessage(rows, "single_ddl_offline_ddl_not_supported")
				} else {
					var columnType string
					typeErr := db.QueryRow("SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='test' AND TABLE_NAME=? AND COLUMN_NAME=?", name, column).Scan(&columnType)
					want := "enum('a','b')"
					if kind == "SET" {
						want = "set('a','b')"
					}
					ok = ok && executedOK(rows) && !schemaSame && typeErr == nil && columnType == want
				}
				add(kind+"_native", ok, ms, rows, map[string]any{"schemaUnchanged": schemaSame, "valuesUnchanged": valuesSame, "error": errString(err), "snapshotError": errString(afterErr)})
			}
		}
	}
	return results
}
