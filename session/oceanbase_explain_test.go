package session

import (
	"strings"
	"testing"
)

// OceanBase 的 EXPLAIN FORMAT=JSON 按物理行拆成多行结果集返回（实测 4.5.0.0 / 5.0.1.0
// 行为一致），历史实现扫进单个 struct 只拿到第一行 "{"，导致受影响行数恒为 0。
// 这些用例锁住拼行行为，并覆盖「OceanBase 将来改成单行返回」的向前兼容。

func rowsOf(lines ...string) []OceanBaseQueryPlan {
	plans := make([]OceanBaseQueryPlan, 0, len(lines))
	for _, l := range lines {
		plans = append(plans, OceanBaseQueryPlan{QueryPlan: l})
	}
	return plans
}

// 真实形态：OceanBase 4.5 / 5.0 逐行返回
const obMultiRowPlan = `{
  "ID":0,
  "OPERATOR":"DELETE",
  "NAME":"",
  "EST.ROWS":90001,
  "EST.TIME(us)":504069,
  "CHILD_1":{
    "ID":1,
    "OPERATOR":"TABLE FULL SCAN",
    "NAME":"t_large",
    "EST.ROWS":90001,
    "EST.TIME(us)":51031
  }
}`

func TestJoinOceanBaseQueryPlan_MultiRow(t *testing.T) {
	plans := rowsOf(strings.Split(obMultiRowPlan, "\n")...)
	if len(plans) < 10 {
		t.Fatalf("测试数据行数不足: %d", len(plans))
	}
	got := joinOceanBaseQueryPlan(plans)
	if got != obMultiRowPlan {
		t.Fatalf("拼行结果与原文不一致:\n%s", got)
	}
}

// 向前兼容：OceanBase 若改成 MySQL 那样一行装完整 JSON，拼行必须等价于原样返回
func TestJoinOceanBaseQueryPlan_SingleRow_ForwardCompatible(t *testing.T) {
	got := joinOceanBaseQueryPlan(rowsOf(obMultiRowPlan))
	if got != obMultiRowPlan {
		t.Fatalf("单行返回时不应改动内容, got:\n%s", got)
	}

	rows, err := parseOceanBaseExplainRows(got)
	if err != nil {
		t.Fatalf("单行 JSON 应能解析: %v", err)
	}
	if len(rows) == 0 || rows[0].Rows != 90001 {
		t.Fatalf("单行 JSON 的根算子 EST.ROWS 应为 90001, got %+v", rows)
	}
}

func TestJoinOceanBaseQueryPlan_Empty(t *testing.T) {
	if got := joinOceanBaseQueryPlan(nil); got != "" {
		t.Fatalf("空结果应返回空串, got %q", got)
	}
	if got := joinOceanBaseQueryPlan([]OceanBaseQueryPlan{}); got != "" {
		t.Fatalf("空 slice 应返回空串, got %q", got)
	}
}

func TestParseOceanBaseExplainRows_MultiRowJoined(t *testing.T) {
	joined := joinOceanBaseQueryPlan(rowsOf(strings.Split(obMultiRowPlan, "\n")...))
	rows, err := parseOceanBaseExplainRows(joined)
	if err != nil {
		t.Fatalf("拼行后应能解析: %v", err)
	}
	// 根算子 DELETE + 直接子算子 TABLE FULL SCAN
	if len(rows) != 2 {
		t.Fatalf("应取到 2 个算子, got %d: %+v", len(rows), rows)
	}
	// explain_rule=first 取 rows[0]，必须是根算子
	if rows[0].Rows != 90001 {
		t.Fatalf("根算子 EST.ROWS 应为 90001, got %d", rows[0].Rows)
	}
}

// 回归守卫：只拿到第一行 "{" 时必须**报错**，而不是静默返回空导致行数恒为 0
func TestParseOceanBaseExplainRows_OnlyFirstLineIsError(t *testing.T) {
	if _, err := parseOceanBaseExplainRows("{"); err == nil {
		t.Fatal("只有 '{' 时必须返回错误，否则受影响行数会静默恒为 0")
	}
}

func TestParseOceanBaseExplainRows_EmptyAndBlank(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t "} {
		if _, err := parseOceanBaseExplainRows(in); err == nil {
			t.Fatalf("空输入 %q 应返回错误", in)
		}
	}
}

func TestParseOceanBaseExplainRows_NoOperator(t *testing.T) {
	// 合法 JSON 但没有 OPERATOR 字段
	if _, err := parseOceanBaseExplainRows(`{"foo":1,"bar":"x"}`); err == nil {
		t.Fatal("没有 OPERATOR 时应返回错误")
	}
}

func TestPlanSnippet(t *testing.T) {
	if got := planSnippet("short", 100); got != "short" {
		t.Fatalf("未超长不应改动, got %q", got)
	}
	long := strings.Repeat("x", 600)
	got := planSnippet(long, 500)
	if !strings.HasPrefix(got, strings.Repeat("x", 500)) {
		t.Fatal("超长时应保留前缀（便于排查），不能像 truncateString 那样转 md5")
	}
	if !strings.Contains(got, "600") {
		t.Fatalf("应带上原始长度, got tail %q", got[len(got)-30:])
	}
}
