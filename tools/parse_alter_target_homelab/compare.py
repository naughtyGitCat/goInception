import argparse
import json
import subprocess

import pymysql
from pymysql.constants import CLIENT


CASES = [
    ("simple", "ALTER TABLE Orders ADD COLUMN priority INT", ("", "Orders", 0)),
    ("qualified", "ALTER TABLE `ShadowTest`.`Orders` ADD INDEX i_user (user_id);", ("ShadowTest", "Orders", 1)),
    ("qualified_dot", "ALTER TABLE `Shadow.Test`.`Orders`.`bad` ADD COLUMN x INT", None),
    ("rename_original", "ALTER TABLE `OldDb`.`OldTable` RENAME TO `NewDb`.`NewTable`", ("OldDb", "OldTable", 1)),
    ("algorithm_and_lock", "ALTER TABLE `ShadowTest`.`Orders`\n  ADD INDEX i_user (user_id),\n  ALGORITHM=INPLACE, LOCK=NONE;", ("ShadowTest", "Orders", 1)),
    ("unicode_identifier", "ALTER TABLE `测试库`.`订单.明细` ADD COLUMN `备注` VARCHAR(40)", ("测试库", "订单.明细", 1)),
    ("nested_quotes_and_linebreaks", "ALTER TABLE `ShadowTest`.`Orders` ADD COLUMN note VARCHAR(80) COMMENT '中;\n文 '', \\\\ text'", ("ShadowTest", "Orders", 1)),
    ("nonreserved_name", "ALTER TABLE parse_alter_target ADD COLUMN parse_alter_target INT", ("", "parse_alter_target", 0)),
    ("old_word_remains_identifier", "ALTER TABLE parse ADD COLUMN parse INT", ("", "parse", 0)),
    ("non_alter", "DROP TABLE `ShadowTest`.`Orders`", None),
    ("multiple", "ALTER TABLE tab_a ADD COLUMN x INT;\nALTER TABLE tab_b ADD COLUMN y INT", None),
    ("incomplete", "ALTER TABLE `ShadowTest`.`Orders` ADD COLUMN", None),
    ("empty", "", None),
]


LEGACY_DIFFERENCES = {
    "qualified_dot": (2, ""),
    "algorithm_and_lock": (2, ""),
    "non_alter": (0, "Orders"),
    "multiple": (0, "tab_a"),
    "incomplete": (2, ""),
    "empty": (3, ""),
}


def compare_outcomes(name, expected, old, new):
    legacy_expected = LEGACY_DIFFERENCES.get(name)
    if legacy_expected is None:
        legacy_expected = (0, expected[1])
    new_matches = "errorCode" in new if expected is None else tuple(new.get("row", ())) == expected
    return (old["exitCode"], old["stdout"]) == legacy_expected and new_matches


def legacy_result(command, sql):
    proc = subprocess.run(command, input=sql.encode("utf-8"), capture_output=True, timeout=15)
    return {
        "exitCode": proc.returncode,
        "stdout": proc.stdout.decode("utf-8", "replace").strip(),
        "stderr": next(iter(proc.stderr.decode("utf-8", "replace").splitlines()), ""),
    }


def parse_target(cursor, sql):
    try:
        cursor.execute("INCEPTION PARSE_ALTER_TARGET %s", (sql,))
    except pymysql.MySQLError as exc:
        code, detail = exc.args
        assert code < 2000, exc
        return {"errorCode": code, "error": detail}
    columns = tuple(item[0] for item in cursor.description)
    assert columns == ("schema", "table", "schemaExplicit"), columns
    rows = cursor.fetchall()
    assert len(rows) == 1 and not cursor.nextset(), rows
    return {"row": rows[0]}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, required=True)
    ap.add_argument("--legacy", nargs=argparse.REMAINDER, required=True)
    args = ap.parse_args()
    if args.port in {4000, 4001} or not args.legacy:
        ap.error("use an isolated port and provide the legacy parser command last")
    conn = pymysql.connect(host="127.0.0.1", port=args.port, user="root", charset="utf8mb4", client_flag=CLIENT.MULTI_STATEMENTS, connect_timeout=5, read_timeout=5, write_timeout=5)
    mismatches = []
    try:
        with conn.cursor() as cursor:
            for name, sql, expected in CASES:
                old = legacy_result(args.legacy, sql)
                new = parse_target(cursor, sql)
                if not compare_outcomes(name, expected, old, new):
                    mismatches.append(name)
                same_name = old["stdout"] == new["row"][1] if old["exitCode"] == 0 and "row" in new else None
                print(json.dumps({"case": name, "legacy": old, "goInception": new, "sameTable": same_name}, ensure_ascii=False))
            for name, suffix in (("same_line", "; SELECT 1"), ("next_line", ";\nSELECT 1")):
                try:
                    cursor.execute("INCEPTION PARSE_ALTER_TARGET %s" + suffix, (CASES[0][1],))
                except pymysql.MySQLError as exc:
                    assert exc.args[0] < 2000, exc
                    print(json.dumps({"outerBatch": name, "errorCode": exc.args[0], "error": exc.args[1]}))
                else:
                    mismatches.append("outer_batch_" + name)
            cursor.execute("INCEPTION SHOW VARIABLES LIKE 'osc_on'")
            result = cursor.fetchall()
            assert len(result) == 1 and result[0][0] == "osc_on", result
            print(json.dumps({"existingCommand": "INCEPTION SHOW VARIABLES", "row": result[0]}))
    finally:
        conn.close()
    assert not mismatches, mismatches


if __name__ == "__main__":
    main()
