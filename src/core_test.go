// core_test.go — 离线逻辑与端到端（本地 TLS 模拟接口）回归测试。
//
// 全部使用标准库与合成数据：不读取真实凭据、不访问真实服务端、不消耗补登卡。
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func captureOutput(t *testing.T, fn func() int) (int, map[string]any) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var code int
	func() {
		defer func() { os.Stdout = old }()
		code = fn()
	}()
	_ = w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	line := strings.TrimSpace(string(data))
	var out map[string]any
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("输出不是合法 JSON: %q (%v)", line, err)
	}
	return code, out
}

func writeSession(t *testing.T, endpoint string, extra string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "workbuddy-desktop.info")
	content := fmt.Sprintf(`{"auth":{"accessToken":"test-token-abc","endpoint":%q%s},"account":{"uid":"user-123"}}`,
		endpoint, extra)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func mockGrowthResponse(path string) any {
	switch path {
	case "/v2/activity/growth/buddy/travel/status":
		return map[string]any{
			"state": "traveling", "location": map[string]any{"name": "海边"},
			"arrive_at": 3600, "server_now": 0,
		}
	case "/v2/activity/growth/tasks":
		return map[string]any{"tasks": []any{}}
	case "/v2/activity/growth/streak":
		return map[string]any{
			"code": 0, "launch_date": "2026-06-17",
			"streak":       map[string]any{"days": 7, "makeup_dates": []any{}},
			"makeup_cards": map[string]any{"balance": 0},
		}
	case "/v2/activity/growth/redeem/summary":
		return map[string]any{"starter_status": "claimed", "advanced_status": "claimed", "legendary_status": "claimed"}
	case "/v2/activity/growth/lottery/chances":
		return map[string]any{"balance": 0}
	case "/v2/activity/growth/buddy/quota":
		return map[string]any{"affordable": 0}
	case "/v2/activity/growth/energy":
		return map[string]any{"balance": 5}
	}
	return map[string]any{"code": 0}
}

// runWithMock 启动本地 TLS 模拟接口，让 Go 版走完整真实 HTTP 栈。
func runWithMock(t *testing.T, action string, statusResponses []any, claimBody any) (int, map[string]any) {
	t.Helper()
	statusCalls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch {
		case r.URL.Path == "/v2/billing/meter/checkin-activity-status":
			if statusCalls < len(statusResponses) {
				body = statusResponses[statusCalls]
			} else {
				body = statusResponses[len(statusResponses)-1]
			}
			statusCalls++
		case r.URL.Path == "/v2/billing/meter/daily-checkin":
			body = claimBody
		default:
			body = mockGrowthResponse(r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)

	oldTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	setEnv(t, "WORKBUDDY_AUTH_FILE", writeSession(t, srv.URL, ""))
	setEnv(t, "WORKBUDDY_BUDGET_SECONDS", "")
	sensitiveValues = nil
	return captureOutput(t, func() int { return runAction(action) })
}

func withMockServer(t *testing.T, statusResponses []any, claimBody any) (int, map[string]any) {
	t.Helper()
	return runWithMock(t, "auto", statusResponses, claimBody)
}

func TestAutoAlreadyCheckedIn(t *testing.T) {
	code, out := withMockServer(t, []any{
		map[string]any{"today_checked_in": true, "today_credit": 100, "streak_days": 7, "total_credits": 700},
	}, nil)
	if code != 0 {
		t.Fatalf("退出码应为 0，实际 %d：%v", code, out)
	}
	if out["result"] != "ALREADY" {
		t.Fatalf("result = %v", out["result"])
	}
	want := "今日已签过（今日 +100，连续 7 天，累计 700 积分）"
	if out["report"] != want {
		t.Fatalf("report = %v，期望 %q", out["report"], want)
	}
	// 以下三项与 Python 原版在完全相同合成响应下的输出逐字对齐。
	wantGrowth := "Buddy 旅行中（海边，约 1.0 小时后回）（能量 5，连签 7 天）"
	if out["growth"] != wantGrowth {
		t.Fatalf("growth = %v，期望 %q", out["growth"], wantGrowth)
	}
	if out["needs_attention"] != false {
		t.Fatalf("needs_attention 应为 false: %v", out)
	}
	if out["growth_result"] != "GROWTH" {
		t.Fatalf("growth_result = %v", out["growth_result"])
	}
}

func TestAutoClaimed(t *testing.T) {
	code, out := withMockServer(t, []any{
		map[string]any{"today_checked_in": false},
		map[string]any{"today_checked_in": true, "streak_days": 8, "total_credits": 800, "is_streak_day": true},
	}, map[string]any{"credit": 100})
	if code != 0 || out["result"] != "CLAIMED" {
		t.Fatalf("code=%d out=%v", code, out)
	}
	want := "成功领取 100 积分，且为连签奖励日（连续 8 天，累计 800 积分）"
	if out["report"] != want {
		t.Fatalf("report = %v，期望 %q", out["report"], want)
	}
}

func TestAutoAlreadyViaClaim(t *testing.T) {
	code, out := withMockServer(t, []any{
		map[string]any{"today_checked_in": false},
		map[string]any{"today_checked_in": true, "today_credit": 100, "streak_days": 3, "total_credits": 300},
	}, map[string]any{"code": 10001, "msg": "今天已签到，请明天再来"})
	if code != 0 || out["result"] != "ALREADY" {
		t.Fatalf("code=%d out=%v", code, out)
	}
	wantReport := "今日已签过（服务端判定已领取）（今日 +100，连续 3 天，累计 300 积分）"
	if out["report"] != wantReport {
		t.Fatalf("report = %v，期望 %q", out["report"], wantReport)
	}
}

func TestAutoInactive(t *testing.T) {
	code, out := withMockServer(t, []any{
		map[string]any{"active": false, "activity_name": "夏日签到"},
	}, nil)
	if code != 0 || out["result"] != "INACTIVE" {
		t.Fatalf("code=%d out=%v", code, out)
	}
	if out["report"] != "签到活动未开启（夏日签到）" {
		t.Fatalf("report = %v", out["report"])
	}
}

func TestStatusAndClaimCommands(t *testing.T) {
	statusBody := map[string]any{"today_checked_in": true, "streak_days": 9}
	code, out := runWithMock(t, "status", []any{statusBody}, nil)
	if code != 0 || out["step"] != "status" || out["needs_attention"] != false {
		t.Fatalf("status: code=%d out=%v", code, out)
	}
	body, _ := out["body"].(map[string]any)
	if fmtAny(body["streak_days"]) != "9" {
		t.Fatalf("status body = %v", out["body"])
	}

	code, out = runWithMock(t, "claim", []any{statusBody}, map[string]any{"credit": 100})
	if code != 0 || out["step"] != "claim" || out["needs_attention"] != false {
		t.Fatalf("claim: code=%d out=%v", code, out)
	}
	body, _ = out["body"].(map[string]any)
	if fmtAny(body["credit"]) != "100" {
		t.Fatalf("claim body = %v", out["body"])
	}
}

func TestDoctorPlaintextAndNoAuth(t *testing.T) {
	path := writeSession(t, defaultEndpoint, "")
	setEnv(t, "WORKBUDDY_AUTH_FILE", path)
	sensitiveValues = nil
	code, out := captureOutput(t, func() int { return runAction("doctor") })
	if code != 0 || out["result"] != "AUTH_READY" || out["credential_format"] != "plaintext" {
		t.Fatalf("doctor: code=%d out=%v", code, out)
	}

	setEnv(t, "WORKBUDDY_AUTH_FILE", filepath.Join(t.TempDir(), "missing.info"))
	code, out = captureOutput(t, func() int { return runAction("auto") })
	if code != 2 || out["result"] != "NO_AUTH" {
		t.Fatalf("NO_AUTH: code=%d out=%v", code, out)
	}
}

func TestUnknownCommandAndBudgetWarning(t *testing.T) {
	path := writeSession(t, defaultEndpoint, "")
	setEnv(t, "WORKBUDDY_AUTH_FILE", path)
	setEnv(t, "WORKBUDDY_BUDGET_SECONDS", "abc")
	code, out := captureOutput(t, func() int { return runAction("nope") })
	if code != 2 || out["result"] != "ERROR" {
		t.Fatalf("code=%d out=%v", code, out)
	}
	if !strings.Contains(fmtAny(out["config_warning"]), "不是数字") {
		t.Fatalf("config_warning = %v", out["config_warning"])
	}
}

func TestEmitRedactsToken(t *testing.T) {
	sensitiveValues = []string{"secret-token"}
	t.Cleanup(func() { sensitiveValues = nil })
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	emit(map[string]any{"result": "OK", "note": "secret-token"}, "auto")
	_ = w.Close()
	os.Stdout = old
	data, _ := io.ReadAll(r)
	_ = r.Close()
	text := string(data)
	if !strings.Contains(text, "[REDACTED]") || strings.Contains(text, "secret-token") {
		t.Fatalf("脱敏失败: %q", text)
	}
}

func TestDigAndNumbers(t *testing.T) {
	obj := map[string]any{"data": map[string]any{"credit": json.Number("100")}}
	if fmtAny(dig(obj, "credit")) != "100" {
		t.Fatal("dig 未穿透 data 信封")
	}
	if asInt(json.Number("1e3"), 0) != 1000 || asInt("1.5", 0) != 1 || asInt(nil, 7) != 7 {
		t.Fatal("asInt 行为异常")
	}
	if fmtCreditStr(json.Number("100.0")) != "100" || fmtCreditStr("abc") != "abc" {
		t.Fatal("fmtCreditStr 行为异常")
	}
	if !truthy(json.Number("1")) || truthy(json.Number("0")) || truthy("") {
		t.Fatal("truthy 行为异常")
	}
	if !isIntNumber(json.Number("10001"), 10001) || isIntNumber("10001", 10001) {
		t.Fatal("isIntNumber 不应把字符串当数字")
	}
	if !apiSuccess(200, map[string]any{"code": json.Number("0")}) ||
		apiSuccess(200, map[string]any{"code": json.Number("1")}) {
		t.Fatal("apiSuccess 业务码判定异常")
	}
}

func TestSessionValidationAndHeaders(t *testing.T) {
	good := map[string]any{
		"auth":    map[string]any{"accessToken": "abc.def", "endpoint": defaultEndpoint, "domain": "workbuddy.cn"},
		"account": map[string]any{"uid": "u1", "enterpriseId": "e1"},
	}
	if err := validateSession(good); err != nil {
		t.Fatalf("合法会话被拒绝: %v", err)
	}
	resolved, err := resolveSession(good)
	if err != nil {
		t.Fatalf("resolveSession: %v", err)
	}
	headers, endpoint, err := buildHeaders(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if headers["Authorization"] != "Bearer abc.def" || headers["X-User-Id"] != "u1" ||
		headers["X-Domain"] != "workbuddy.cn" || headers["X-Enterprise-Id"] != "e1" ||
		headers["X-Tenant-Id"] != "e1" || endpoint != defaultEndpoint {
		t.Fatalf("请求头异常: %v %q", headers, endpoint)
	}
	bad := map[string]any{
		"auth":    map[string]any{"accessToken": "abc", "endpoint": "http://example.com"},
		"account": map[string]any{"uid": "u1"},
	}
	if err := validateSession(bad); err == nil {
		t.Fatal("非 https endpoint 应被拒绝")
	}
	missing := map[string]any{"auth": map[string]any{"accessToken": "abc"}, "account": map[string]any{}}
	err = validateSession(missing)
	if ae, ok := err.(*authError); !ok || ae.result != "NO_SESSION" {
		t.Fatalf("缺 uid 应返回 NO_SESSION: %v", err)
	}
}

func TestTokenFormatEnvelope(t *testing.T) {
	envelope := map[string]any{
		"suite":      1,
		"keyId":      "0123456789abcdef",
		"nonce":      base64.StdEncoding.EncodeToString(make([]byte, 12)),
		"authTag":    base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"ciphertext": base64.StdEncoding.EncodeToString([]byte("cipher")),
	}
	raw, _ := json.Marshal(envelope)
	value := map[string]any{
		"$wbEncrypted": 1,
		"envelope":     base64.StdEncoding.EncodeToString(raw),
	}
	kind, err := tokenFormat(value)
	if err != nil || kind != "sym-v1" {
		t.Fatalf("sym-v1 识别失败: %v %v", kind, err)
	}
	value["$wbEncrypted"] = 2
	if _, err := tokenFormat(value); err == nil {
		t.Fatal("非法标记应被拒绝")
	}
	delete(value, "$wbEncrypted")
	if _, err := tokenFormat(value); err == nil {
		t.Fatal("缺少标记应被拒绝")
	}
}

func TestMakeupCandidates(t *testing.T) {
	streak := map[string]any{
		"launch_date": "2026-06-17",
		"streak":      map[string]any{"makeup_dates": []any{"2026-11-10"}},
	}
	heatmap := map[string]any{
		"today": map[string]any{"date": "2026-11-15"},
		"cells": []any{
			map[string]any{"date": "2026-11-14", "score": 0},
			map[string]any{"date": "2026-11-13", "score": 0},
			map[string]any{"date": "2026-11-12", "score": 3},
			map[string]any{"date": "2026-11-10", "score": 0}, // 已补登
			map[string]any{"date": "2026-10-31", "score": 0}, // 非当月
			map[string]any{"date": "2026-11-16", "score": 0}, // 未来
		},
	}
	dates, err := makeupCandidates(streak, heatmap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(dates, ",") != "2026-11-14,2026-11-13" {
		t.Fatalf("候选日期异常: %v", dates)
	}
	broken := map[string]any{
		"today": map[string]any{"date": "2026-11-15"},
		"cells": []any{map[string]any{"date": "2026-11-14", "score": "0"}},
	}
	if _, err := makeupCandidates(streak, broken); err == nil {
		t.Fatal("字符串分数应被拒绝")
	}
	if _, err := makeupCandidates(streak, map[string]any{"today": map[string]any{"date": "2026-11-15"}}); err == nil {
		t.Fatal("缺少 cells 应报错")
	}
}

// 普通 Node 没有 Electron 的原生 binding，助手应回 RUNTIME_UNAVAILABLE。
// 这条用例验证内嵌 JS 语法、stdin/stdout 管道、超时与回复校验整条链路。
func TestHelperProtocolWithPlainNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("本机没有 node，跳过凭据助手协议测试")
	}
	startBudget("auto")
	_, err = runAuthHelper(node, map[string]any{"operation": "probe"})
	ae, ok := err.(*authError)
	if !ok || ae.reason != "RUNTIME_UNAVAILABLE" {
		t.Fatalf("助手协议结果异常: %v", err)
	}
}

func TestMiscHelpers(t *testing.T) {
	if !isTierLocked(403, map[string]any{"msg": "连续登录天数不足"}) {
		t.Fatal("未解锁档位判定失败")
	}
	if isTierLocked(400, map[string]any{"msg": "连续登录天数不足"}) {
		t.Fatal("非 403 不应判定为未解锁")
	}
	if !isUnknownTier(400, map[string]any{"msg": "unknown tier"}) {
		t.Fatal("unknown tier 判定失败")
	}
	if !isNoChance("insufficient lottery chance balance") {
		t.Fatal("无抽奖机会判定失败")
	}
	if got := clientToken("u"); !strings.HasPrefix(got, "u-") || len(got) != 38 {
		t.Fatalf("clientToken 格式异常: %q", got)
	}
	if fmtETA(1800, 0) != "，约 30 分钟后回" {
		t.Fatalf("fmtETA = %q", fmtETA(1800, 0))
	}
	if !isAlreadyCheckedIn(nil) || !isAlreadyCheckedIn(map[string]any{"code": json.Number("10001")}) {
		t.Fatal("已签判定失败")
	}
	if isHardFailure(400) || !isHardFailure(500) || !isHardFailure(codeNoNetwork) {
		t.Fatal("硬失败判定异常")
	}
}
