// align_test.go — 与 Python 原版的功能/文案对齐回归测试。
//
// TestReportParityVectors 里的期望字符串由 signin.py 用同一批合成响应生成
// （见交付说明里的对照脚本），改动文案前请重新跑一遍原版核对。
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func captureStdoutText(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := func() int {
		defer func() { os.Stdout = old }()
		return fn()
	}()
	_ = w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return code, string(data)
}

// runVector 用真实 HTTP 栈跑一个命令，返回退出码与 stdout 文本。
func runVector(t *testing.T, action string, statuses []any, codes []int, claim any) (int, string) {
	t.Helper()
	statusCalls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		code := 200
		idx := func(n, total int) int {
			if n < total {
				return n
			}
			return total - 1
		}
		switch {
		case r.URL.Path == "/v2/billing/meter/checkin-activity-status":
			body = statuses[idx(statusCalls, len(statuses))]
			if len(codes) > 0 {
				code = codes[idx(statusCalls, len(codes))]
			}
			statusCalls++
		case r.URL.Path == "/v2/billing/meter/daily-checkin":
			body = claim
		default:
			body = mockGrowthResponse(r.URL.Path)
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)

	oldTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	setEnv(t, "WORKBUDDY_AUTH_FILE", writeSession(t, srv.URL, ""))
	setEnv(t, "WORKBUDDY_BUDGET_SECONDS", "")
	sensitiveValues = nil
	return captureStdoutText(t, func() int { return runAction(action) })
}

func parseOneLine(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &out); err != nil {
		t.Fatalf("输出不是单行合法 JSON: %q (%v)", text, err)
	}
	return out
}

func noRetries(t *testing.T) {
	t.Helper()
	oldNet, oldSrv := networkRetryDelays, serverRetryDelays
	networkRetryDelays, serverRetryDelays = nil, nil
	t.Cleanup(func() { networkRetryDelays, serverRetryDelays = oldNet, oldSrv })
}

const growthIdleReport = "Buddy 旅行中（海边，约 1.0 小时后回）（能量 5，连签 7 天）"

type parityVector struct {
	name         string
	statuses     []any
	codes        []int
	claim        any
	code         int
	result       string
	report       string
	growth       string
	growthResult string
}

// 这些期望值来自 signin.py 的 run_daily（同一批合成响应、同一套 growth 假响应）。
func parityVectors() []parityVector {
	return []parityVector{
		{"ALREADY", []any{map[string]any{"today_checked_in": true, "today_credit": 100, "streak_days": 7, "total_credits": 700}}, nil, nil,
			0, "ALREADY", "今日已签过（今日 +100，连续 7 天，累计 700 积分）", growthIdleReport, "GROWTH"},
		{"CLAIMED", []any{map[string]any{"today_checked_in": false},
			map[string]any{"today_checked_in": true, "streak_days": 8, "total_credits": 800, "is_streak_day": true}}, nil,
			map[string]any{"credit": 100},
			0, "CLAIMED", "成功领取 100 积分，且为连签奖励日（连续 8 天，累计 800 积分）", growthIdleReport, "GROWTH"},
		{"ALREADY_VIA_CLAIM", []any{map[string]any{"today_checked_in": false},
			map[string]any{"today_checked_in": true, "today_credit": 100, "streak_days": 3, "total_credits": 300}}, nil,
			map[string]any{"code": 10001, "msg": "今天已签到，请明天再来"},
			0, "ALREADY", "今日已签过（服务端判定已领取）（今日 +100，连续 3 天，累计 300 积分）", growthIdleReport, "GROWTH"},
		{"INACTIVE", []any{map[string]any{"active": false, "activity_name": "夏日签到"}}, nil, nil,
			0, "INACTIVE", "签到活动未开启（夏日签到）", growthIdleReport, "GROWTH"},
		{"INACTIVE_EMPTY_NAME", []any{map[string]any{"active": false, "activity_name": ""}}, nil, nil,
			0, "INACTIVE", "签到活动未开启", growthIdleReport, "GROWTH"},
		{"CLAIMED_NO_STREAK", []any{map[string]any{"today_checked_in": false},
			map[string]any{"today_checked_in": true}}, nil, map[string]any{"credit": 50},
			0, "CLAIMED", "成功领取 50 积分", growthIdleReport, "GROWTH"},
		{"CLAIMED_STREAK_NO_TOTAL", []any{map[string]any{"today_checked_in": false},
			map[string]any{"today_checked_in": true, "streak_days": 4}}, nil, map[string]any{"credit": 60},
			0, "CLAIMED", "成功领取 60 积分（连续 4 天）", growthIdleReport, "GROWTH"},
		{"ALREADY_BARE", []any{map[string]any{"today_checked_in": true}}, nil, nil,
			0, "ALREADY", "今日已签过", growthIdleReport, "GROWTH"},
		{"CLAIM_ERROR", []any{map[string]any{"today_checked_in": false}}, nil,
			map[string]any{"code": 500, "msg": "活动已结束"},
			1, "ERROR", "领取失败：活动已结束（HTTP 200）", growthIdleReport, "GROWTH"},
		{"CLAIM_UNKNOWN", []any{map[string]any{"today_checked_in": false}}, nil,
			map[string]any{"unexpected": "shape"},
			1, "UNKNOWN", `未识别的领取返回，请检查接口：{"unexpected": "shape"}`, growthIdleReport, "GROWTH"},
		{"AUTH_401", []any{map[string]any{}}, []int{401}, nil,
			1, "AUTH_REJECTED", "服务端拒绝认证（HTTP 401），请检查客户端登录状态、凭据对应的服务地址及脚本版本",
			"认证或权限检查未通过，成长中心跳过", "AUTH_REJECTED"},
		{"HTTP_500", []any{map[string]any{}}, []int{500}, nil,
			1, "ERROR", "签到接口返回异常（HTTP 500），请重新登录客户端或稍后重试", growthIdleReport, "GROWTH"},
		{"CREDIT_STRING", []any{map[string]any{"today_checked_in": false},
			map[string]any{"today_checked_in": true, "streak_days": 2, "total_credits": "1.5"}}, nil,
			map[string]any{"credit": "1e3"},
			0, "CLAIMED", "成功领取 1e3 积分（连续 2 天，累计 1.5 积分）", growthIdleReport, "GROWTH"},
	}
}

func TestReportParityVectors(t *testing.T) {
	noRetries(t)
	for _, v := range parityVectors() {
		t.Run(v.name, func(t *testing.T) {
			code, text := runVector(t, "auto", v.statuses, v.codes, v.claim)
			out := parseOneLine(t, text)
			if code != v.code || out["result"] != v.result {
				t.Fatalf("code=%d result=%v，期望 %d/%s；完整输出 %v", code, out["result"], v.code, v.result, out)
			}
			if out["report"] != v.report {
				t.Fatalf("report = %v\n期望 = %q", out["report"], v.report)
			}
			if out["growth"] != v.growth {
				t.Fatalf("growth = %v\n期望 = %q", out["growth"], v.growth)
			}
			if out["growth_result"] != v.growthResult {
				t.Fatalf("growth_result = %v，期望 %q", out["growth_result"], v.growthResult)
			}
		})
	}
}

func TestNetworkAndTimeoutVectors(t *testing.T) {
	noRetries(t)
	// 真实连接失败：Python 假响应里 error 为空，Go 会带上系统错误文本，
	// 因此这里只比结果与短路行为，括号内的错误详情不逐字比对。
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	startBudget("auto")
	code, out := runAuto(map[string]string{}, url)
	if code != 1 || out["result"] != "NETWORK" || !strings.HasPrefix(fmtAny(out["report"]), "网络不可达，签到跳过，下次自动重试（") {
		t.Fatalf("NETWORK: code=%d out=%v", code, out)
	}
	cd, d, quiet := runDaily(map[string]string{}, url)
	if cd != 1 || d["growth_result"] != "NETWORK" || quiet {
		t.Fatalf("NETWORK 短路异常: %d %v", cd, d)
	}

	startedAt = time.Now().Add(-time.Hour)
	t.Cleanup(func() { startedAt = time.Time{} })
	code, out = runAuto(map[string]string{}, "https://mock")
	if code != 1 || out["result"] != "TIMEOUT" ||
		out["report"] != "已达本次运行时间预算，签到跳过，下次自动重试" {
		t.Fatalf("TIMEOUT: code=%d out=%v", code, out)
	}
	cd, d, quiet = runDaily(map[string]string{}, "https://mock")
	if cd != 1 || d["growth_result"] != "TIMEOUT" || quiet {
		t.Fatalf("TIMEOUT 短路异常: %d %v", cd, d)
	}
}

func TestCommandSetMatchesPython(t *testing.T) {
	want := []string{"auto", "silent", "growth", "silent-poll", "silent-growth", "status", "claim", "all", "doctor"}
	if !reflect.DeepEqual(knownActions, want) {
		t.Fatalf("命令集 = %v，期望 %v", knownActions, want)
	}
	code, text := captureStdoutText(t, func() int { return runAction("nope") })
	out := parseOneLine(t, text)
	if code != 2 || out["result"] != "ERROR" || !strings.Contains(fmtAny(out["report"]), strings.Join(want, " / ")) {
		t.Fatalf("未知命令提示异常: %d %v", code, out)
	}
	if out["needs_attention"] != true {
		t.Fatalf("未知命令应标记 needs_attention: %v", out)
	}
}

func TestBudgetTiersAndWarnings(t *testing.T) {
	setEnv(t, "WORKBUDDY_BUDGET_SECONDS", "")
	startBudget("auto")
	if budgetSeconds != defaultBudgetSeconds {
		t.Fatalf("auto 默认预算 = %v", budgetSeconds)
	}
	startBudget("silent-poll")
	if budgetSeconds != pollBudgetSeconds {
		t.Fatalf("轮询默认预算 = %v，期望 180", budgetSeconds)
	}
	startBudget("silent-growth")
	if budgetSeconds != pollBudgetSeconds {
		t.Fatalf("silent-growth 默认预算 = %v，期望 180", budgetSeconds)
	}

	setEnv(t, "WORKBUDDY_BUDGET_SECONDS", "999")
	startBudget("auto")
	if budgetSeconds != maxBudgetSeconds || !strings.Contains(configWarning, "夹到 540 秒") {
		t.Fatalf("auto 夹上限异常: %v / %q", budgetSeconds, configWarning)
	}
	startBudget("silent-poll")
	if budgetSeconds != pollMaxBudgetSeconds || !strings.Contains(configWarning, "夹到 240 秒") {
		t.Fatalf("轮询夹上限异常: %v / %q", budgetSeconds, configWarning)
	}
	setEnv(t, "WORKBUDDY_BUDGET_SECONDS", "abc")
	startBudget("silent-poll")
	if budgetSeconds != pollBudgetSeconds || !strings.Contains(configWarning, "回落 180 秒") {
		t.Fatalf("轮询非法配置回落异常: %v / %q", budgetSeconds, configWarning)
	}
	if !envFlag("WORKBUDDY_GROWTH_LOG_EMPTY_TEST") {
		setEnv(t, "WORKBUDDY_GROWTH_LOG_EMPTY_TEST", "yes")
		if !envFlag("WORKBUDDY_GROWTH_LOG_EMPTY_TEST") {
			t.Fatal("envFlag 未识别 yes")
		}
	}
}

func TestEmitLogChannels(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "signin.log")
	setEnv(t, "WORKBUDDY_SIGNIN_LOG", logPath)
	readLog := func() string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return ""
		}
		return string(data)
	}

	// silent 前缀：不写 stdout，只落盘。
	_, text := captureStdoutText(t, func() int {
		emit(map[string]any{"result": "OK", "report": "轮询记录"}, "silent-poll")
		return 0
	})
	if text != "" || !strings.Contains(readLog(), "[") || !strings.Contains(readLog(), "轮询记录") {
		t.Fatalf("silent 落盘异常: stdout=%q log=%q", text, readLog())
	}
	before := len(readLog())

	// 普通命令：只写 stdout，日志不增长。
	_, text = captureStdoutText(t, func() int {
		emit(map[string]any{"result": "OK", "report": "普通输出"}, "auto")
		return 0
	})
	if !strings.Contains(text, "普通输出") || len(readLog()) != before {
		t.Fatalf("普通命令通道异常: stdout=%q log=%q", text, readLog())
	}

	// ERROR 两条通道都走，避免无窗口运行时失败记录整条丢失。
	_, text = captureStdoutText(t, func() int {
		emit(map[string]any{"result": "ERROR", "report": "失败记录"}, "auto")
		return 0
	})
	if !strings.Contains(text, "失败记录") || !strings.Contains(readLog()[before:], "失败记录") {
		t.Fatalf("ERROR 双通道异常: stdout=%q log=%q", text, readLog())
	}
}

func TestAllCommandEmitsBothSteps(t *testing.T) {
	noRetries(t)
	code, text := runVector(t, "all", []any{map[string]any{"today_checked_in": true}}, nil, nil)
	if code != 0 {
		t.Fatalf("all 退出码 = %d, stdout=%q", code, text)
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 2 {
		t.Fatalf("all 应输出两行，实际 %d 行: %q", len(lines), text)
	}
	first, second := parseOneLine(t, lines[0]), parseOneLine(t, lines[1])
	if first["step"] != "status" || second["step"] != "claim" {
		t.Fatalf("all 步骤顺序异常: %v / %v", first, second)
	}
}

func TestPollQuietAndTrigger(t *testing.T) {
	noRetries(t)
	logPath := filepath.Join(t.TempDir(), "poll.log")
	setEnv(t, "WORKBUDDY_SIGNIN_LOG", logPath)
	statuses := []any{map[string]any{"today_checked_in": true, "today_credit": 100}}

	// 空跑不落盘，也不打 stdout。
	_, text := runVector(t, "silent-poll", statuses, nil, nil)
	if text != "" {
		t.Fatalf("空跑不应有 stdout: %q", text)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("空跑不应写日志")
	}

	// 打开 WORKBUDDY_GROWTH_LOG_EMPTY 后写日志，并带上 trigger=poll。
	setEnv(t, "WORKBUDDY_GROWTH_LOG_EMPTY", "1")
	_, text = runVector(t, "silent-poll", statuses, nil, nil)
	if text != "" {
		t.Fatalf("silent-poll 不应有 stdout: %q", text)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(data))
	pos := strings.Index(line, "] ")
	if pos < 0 {
		t.Fatalf("日志格式异常: %q", line)
	}
	out := parseOneLine(t, line[pos+2:])
	if out["trigger"] != "poll" || out["result"] != "ALREADY" {
		t.Fatalf("轮询日志内容异常: %v", out)
	}
}

func TestPythonNumberSemantics(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"1e3", "1e3"}, {"1.5", "1.5"}, {" 100 ", "100"}, {"abc", "abc"},
		{json.Number("100.0"), "100"}, {json.Number("1.5"), "1"},
		{true, "1"}, {false, "0"}, {nil, "None"},
	}
	for _, c := range cases {
		if got := fmtCreditStr(c.in); got != c.want {
			t.Fatalf("fmtCreditStr(%v) = %q，期望 %q", c.in, got, c.want)
		}
	}
	if !businessCodeZero(json.Number("0")) || businessCodeZero(json.Number("0.0")) ||
		businessCodeZero(json.Number("1e0")) || businessCodeZero("0") || businessCodeZero(true) {
		t.Fatal("businessCodeZero 应严格等价于 Python type(code) is int and code == 0")
	}
	if apiSuccess(200, map[string]any{"code": json.Number("0.0")}) {
		t.Fatal("浮点业务码 0.0 不应判成功")
	}
	// Python round(2.5) == 2（五成双），Go 的 math.Round 会得 3。
	if got := fmtETA(150, 0); got != "，约 2 分钟后回" {
		t.Fatalf("fmtETA(150,0) = %q", got)
	}
	if got := pyDumps(map[string]any{"unexpected": "shape"}); got != `{"unexpected": "shape"}` {
		t.Fatalf("pyDumps = %q", got)
	}
	if got := truncateRunes(strings.Repeat("汉", 210), 200); len([]rune(got)) != 200 {
		t.Fatal("中文截断应按字符计数")
	}
}

func TestRunDailyKeepsSigninWhenGrowthPanics(t *testing.T) {
	noRetries(t)
	oldRunner := growthRunner
	growthRunner = func(map[string]string, string) (int, map[string]any) { panic("boom") }
	t.Cleanup(func() { growthRunner = oldRunner })

	code, text := runVector(t, "auto", []any{map[string]any{"today_checked_in": true, "today_credit": 100,
		"streak_days": 7, "total_credits": 700}}, nil, nil)
	out := parseOneLine(t, text)
	if code != 1 || out["result"] != "ALREADY" || out["growth_result"] != "ERROR" {
		t.Fatalf("成长中心异常不应吞掉签到结果: code=%d out=%v", code, out)
	}
	report := fmtAny(out["report"])
	if !strings.HasPrefix(report, "今日已签过（今日 +100") || !strings.Contains(report, "成长中心异常") {
		t.Fatalf("汇报拼接异常: %q", report)
	}
}
