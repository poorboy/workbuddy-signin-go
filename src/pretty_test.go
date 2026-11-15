// pretty_test.go — 终端美化输出的渲染与开关优先级回归测试。
package main

import (
	"strings"
	"testing"
)

func TestPrettyRenderDailyResult(t *testing.T) {
	out := map[string]any{
		"result":          "ALREADY",
		"report":          "今日已签过（今日 +100，连续 44 天，累计 4400 积分）",
		"today_credit":    100,
		"streak_days":     44,
		"total_credits":   4400,
		"is_streak_day":   false,
		"next_streak_day": 0,
		"growth":          "Buddy 旅行中（咖啡馆，约 35 分钟后回）（能量 3，连签 2 天）",
		"growth_result":   "GROWTH",
		"needs_attention": false,
	}

	text := renderPretty(out)
	for _, want := range []string{
		"[OK] 今日已签过",
		"ALREADY",
		"今日积分", "100",
		"连续签到", "44 天",
		"累计积分", "4400",
		"成长中心", "Buddy 旅行中",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("美化输出缺少 %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"growth_result", "GROWTH", `"result"`, "needs_attention"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("美化输出不应包含 %q:\n%s", unwanted, text)
		}
	}
	if strings.Contains(text, "今日已签过（今日 +100，连续 44 天，累计 4400 积分）\n今日积分") {
		t.Errorf("标题与字段之间缺少空行:\n%s", text)
	}
}

func TestPrettyRenderError(t *testing.T) {
	out := map[string]any{
		"result":          "ERROR",
		"report":          "未知命令：nope",
		"needs_attention": true,
	}
	text := renderPretty(out)
	if !strings.HasPrefix(text, "[!!] 未知命令：nope") {
		t.Errorf("错误标题异常:\n%s", text)
	}
	if !strings.Contains(text, "需要处理") || !strings.Contains(text, "是") {
		t.Errorf("缺少需要处理提示:\n%s", text)
	}
}

func TestPrettyRenderUnknownAndNestedFields(t *testing.T) {
	out := map[string]any{
		"result": "OK",
		"alpha":  1,
		"zeta":   "末位",
		"body":   map[string]any{"ok": true},
	}
	text := renderPretty(out)
	// 未知字段按字典序排在已知字段之后。
	if strings.Index(text, "alpha") > strings.Index(text, "zeta") {
		t.Errorf("未知字段未按字典序排列:\n%s", text)
	}
	if !strings.Contains(text, `{"ok":true}`) || !strings.Contains(text, "响应内容") {
		t.Errorf("嵌套字段未输出为 JSON:\n%s", text)
	}
}

func TestOutputStyleArgs(t *testing.T) {
	noPauseFlag = false
	defer func() { prettyFlag, jsonFlag, noPauseFlag = false, false, false }()

	cases := []struct {
		args   []string
		action string
		pretty bool
		json   bool
	}{
		{nil, "auto", false, false},
		{[]string{"--pretty"}, "auto", true, false},
		{[]string{"pretty", "auto"}, "auto", true, false},
		{[]string{"growth", "--text"}, "growth", true, false},
		{[]string{"--json", "auto"}, "auto", false, true},
		{[]string{"auto", "--compact"}, "auto", false, true},
		{[]string{"--no-pretty"}, "auto", false, true},
		{[]string{"--pretty", "--json"}, "auto", true, true},
		{[]string{"--nopause", "--pretty"}, "auto", true, false},
	}
	for _, c := range cases {
		got := parseArgs(c.args)
		if got != c.action || prettyFlag != c.pretty || jsonFlag != c.json {
			t.Errorf("parseArgs(%q) = (%q, pretty=%v, json=%v)，期望 (%q, %v, %v)",
				c.args, got, prettyFlag, jsonFlag, c.action, c.pretty, c.json)
		}
	}
}

func TestWantPrettyPriority(t *testing.T) {
	defer func() { prettyFlag, jsonFlag = false, false }()
	setEnv(t, "WORKBUDDY_OUTPUT", "")
	prettyFlag, jsonFlag = false, false
	// 测试里 stdout 是管道，默认应是单行 JSON。
	if wantPretty("auto") {
		t.Fatal("管道输出不应自动美化")
	}
	if wantPretty("silent-growth") {
		t.Fatal("silent 命令不应美化")
	}

	prettyFlag = true
	if !wantPretty("auto") {
		t.Fatal("--pretty 未生效")
	}
	if wantPretty("silent") {
		t.Fatal("--pretty 不应让 silent 命令写 stdout")
	}
	jsonFlag = true
	if wantPretty("auto") {
		t.Fatal("--json 应覆盖 --pretty")
	}

	prettyFlag, jsonFlag = false, false
	setEnv(t, "WORKBUDDY_OUTPUT", "pretty")
	if !wantPretty("auto") {
		t.Fatal("WORKBUDDY_OUTPUT=pretty 未生效")
	}
	setEnv(t, "WORKBUDDY_OUTPUT", "json")
	if wantPretty("auto") {
		t.Fatal("WORKBUDDY_OUTPUT=json 未生效")
	}
}

func TestEmitPrettyWhenForced(t *testing.T) {
	setEnv(t, "WORKBUDDY_SIGNIN_LOG", t.TempDir()+"/signin.log")
	prettyFlag, jsonFlag = true, false
	defer func() { prettyFlag, jsonFlag = false, false }()

	_, text := captureStdoutText(t, func() int {
		emit(map[string]any{
			"result": "CLAIMED", "report": "成功领取 100 积分",
			"credit": 100, "streak_days": 45,
		}, "auto")
		return 0
	})
	if !strings.Contains(text, "[OK] 成功领取 100 积分") ||
		!strings.Contains(text, "连续签到") ||
		strings.HasPrefix(strings.TrimSpace(text), "{") {
		t.Fatalf("强制美化输出异常: %q", text)
	}
}

func TestEmitJSONWhenForced(t *testing.T) {
	setEnv(t, "WORKBUDDY_SIGNIN_LOG", t.TempDir()+"/signin.log")
	jsonFlag = true
	defer func() { jsonFlag = false }()

	_, text := captureStdoutText(t, func() int {
		emit(map[string]any{"result": "OK", "report": "脚本模式"}, "auto")
		return 0
	})
	out := parseOneLine(t, text)
	if out["report"] != "脚本模式" {
		t.Fatalf("--json 输出异常: %v", out)
	}
}
