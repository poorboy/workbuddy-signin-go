// forbidden_test.go — 成长中心「业务 403」回归测试。
//
// 真实场景：POST /buddy/travel/depart 返回 403 {"msg":"no active buddy"}。
// 这是「账号还没有激活的 Buddy」这类常态条件，不是登录失效或权限不足；
// 旧逻辑把它当成鉴权失败，中止整轮成长中心并把已成功的签到标成需要关注。
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type mockReply struct {
	code int
	body any
}

func runWithReplies(t *testing.T, replies map[string]mockReply) (int, map[string]any, []string) {
	t.Helper()
	var mu sync.Mutex
	seen := []string{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		reply, ok := replies[r.URL.Path]
		if !ok {
			reply = mockReply{200, map[string]any{"code": 0}}
		}
		w.WriteHeader(reply.code)
		_ = json.NewEncoder(w).Encode(reply.body)
	}))
	t.Cleanup(srv.Close)
	oldTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	setEnv(t, "WORKBUDDY_AUTH_FILE", writeSession(t, srv.URL, ""))
	setEnv(t, "WORKBUDDY_BUDGET_SECONDS", "")
	sensitiveValues = nil
	code, out := captureOutput(t, func() int { return runAction("auto") })
	mu.Lock()
	defer mu.Unlock()
	return code, out, append([]string(nil), seen...)
}

func noActiveBuddyReplies() map[string]mockReply {
	return map[string]mockReply{
		"/v2/billing/meter/checkin-activity-status": {200, map[string]any{
			"today_checked_in": true, "today_credit": 100,
			"streak_days": 16, "total_credits": 1600,
		}},
		"/v2/activity/growth/buddy/travel/status": {200, map[string]any{
			"state": "idle", "daily_limit_reached": false,
		}},
		"/v2/activity/growth/buddy/travel/config": {200, map[string]any{
			"locations": []any{map[string]any{"id": 1, "name": "咖啡馆"}},
		}},
		"/v2/activity/growth/buddy/travel/depart": {403, map[string]any{
			"code": 403, "msg": "no active buddy",
		}},
		"/v2/activity/growth/tasks": {200, map[string]any{"tasks": []any{}}},
		"/v2/activity/growth/streak": {200, map[string]any{
			"streak":       map[string]any{"days": 2, "makeup_dates": []any{}},
			"makeup_cards": map[string]any{"balance": 0},
		}},
		"/v2/activity/growth/redeem/summary": {200, map[string]any{
			"starter_status": "locked", "advanced_status": "locked", "legendary_status": "locked",
		}},
		"/v2/activity/growth/lottery/chances": {200, map[string]any{"balance": 0}},
		"/v2/activity/growth/buddy/quota":     {200, map[string]any{"affordable": 0, "balance": 0}},
		"/v2/activity/growth/energy":          {200, map[string]any{"balance": 0}},
	}
}

func seenPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func TestBusinessForbiddenHelpers(t *testing.T) {
	cases := []struct {
		code int
		body any
		want bool
	}{
		{403, map[string]any{"msg": "no active buddy"}, true},
		{403, map[string]any{"msg": "No Active Buddy"}, true},
		{403, map[string]any{"data": map[string]any{"msg": "no active buddy"}}, true},
		{403, map[string]any{"msg": "连续登录天数不足"}, true},
		{403, map[string]any{"msg": "permission denied"}, false},
		{403, map[string]any{"msg": ""}, false},
		{401, map[string]any{"msg": "no active buddy"}, false},
		{400, map[string]any{"msg": "no active buddy"}, false},
	}
	for _, c := range cases {
		if got := isBusinessForbidden(c.code, c.body); got != c.want {
			t.Errorf("isBusinessForbidden(%d, %v) = %v，期望 %v", c.code, c.body, got, c.want)
		}
	}
	if forbiddenMsg(map[string]any{"msg": "no active buddy"}) != "no active buddy" {
		t.Fatal("应原样带出服务端 msg")
	}
	if forbiddenMsg(map[string]any{}) != "活动条件未满足" {
		t.Fatal("缺失 msg 时应有兜底文案")
	}
}

// 403 no active buddy 只跳过「派 Buddy」，后续各段照常执行，签到结果不被污染。
func TestBusiness403DoesNotAbortGrowth(t *testing.T) {
	code, out, seen := runWithReplies(t, noActiveBuddyReplies())
	if code != 0 {
		t.Fatalf("退出码应为 0，实际 %d：%v", code, out)
	}
	if out["result"] != "ALREADY" {
		t.Fatalf("result 应保留签到结果，实际 %v", out["result"])
	}
	if out["growth_result"] != "GROWTH" {
		t.Fatalf("growth_result = %v，不应再是 FORBIDDEN", out["growth_result"])
	}
	if out["needs_attention"] != false {
		t.Fatalf("needs_attention 应为 false：%v", out)
	}
	growth := fmtAny(out["growth"])
	// 与 Python 原版在同一组合成响应下的输出逐字一致（见 parity_403.py）。
	if growth != "派 Buddy 跳过：no active buddy（能量 0，连签 2 天）" {
		t.Fatalf("growth = %q，与 Python 原版不一致", growth)
	}
	if !strings.Contains(growth, "派 Buddy 跳过：no active buddy") {
		t.Fatalf("growth 应说明跳过原因：%q", growth)
	}
	wantReport := "今日已签过（今日 +100，连续 16 天，累计 1600 积分）"
	if out["report"] != wantReport {
		t.Fatalf("report = %v，期望 %q", out["report"], wantReport)
	}
	if strings.Contains(fmtAny(out["report"]), "服务端拒绝此操作") {
		t.Fatalf("report 不应再拼上权限失败文案：%v", out["report"])
	}
	// 关键：证明整轮没有被中止，任务/兑换/盲盒/Buddy 盲盒都跑到了。
	for _, path := range []string{
		"/v2/activity/growth/tasks",
		"/v2/activity/growth/streak",
		"/v2/activity/growth/redeem/summary",
		"/v2/activity/growth/lottery/chances",
		"/v2/activity/growth/buddy/quota",
	} {
		if !seenPath(seen, path) {
			t.Errorf("后续段 %s 未执行，说明整轮仍被 403 中止；实际请求：%v", path, seen)
		}
	}
}

// 未知语义的 403 仍按权限拒绝整轮中止，避免拿失效凭据继续打接口。
func TestUnknown403StillAbortsGrowth(t *testing.T) {
	replies := noActiveBuddyReplies()
	replies["/v2/activity/growth/buddy/travel/depart"] = mockReply{403,
		map[string]any{"code": 403, "msg": "permission denied"}}
	code, out, seen := runWithReplies(t, replies)
	if code == 0 {
		t.Fatalf("未知 403 应返回非零退出码：%v", out)
	}
	if out["growth_result"] != "FORBIDDEN" {
		t.Fatalf("growth_result = %v，期望 FORBIDDEN", out["growth_result"])
	}
	if out["needs_attention"] != true {
		t.Fatalf("needs_attention 应为 true：%v", out)
	}
	if seenPath(seen, "/v2/activity/growth/tasks") {
		t.Fatalf("未知 403 后不应继续访问成长中心：%v", seen)
	}
}

// 已有「兑换档位未解锁」的 403 语义也复用同一判定，文案一致。
func TestTierLocked403SkipsDepart(t *testing.T) {
	replies := noActiveBuddyReplies()
	replies["/v2/activity/growth/buddy/travel/depart"] = mockReply{403,
		map[string]any{"code": 403, "msg": "连续登录天数不足"}}
	_, out, _ := runWithReplies(t, replies)
	if !strings.Contains(fmtAny(out["growth"]), "派 Buddy 跳过：连续登录天数不足") {
		t.Fatalf("growth = %v", out["growth"])
	}
}
