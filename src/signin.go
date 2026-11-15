// signin.go — 签到主流程：查状态 → 未签才领 → 一行汇报；以及完整日常流程的组装。
package main

import (
	"fmt"
	"strings"
)

func authFailure(code int) map[string]any {
	if code == 401 {
		return map[string]any{
			"result": "AUTH_REJECTED", "http": code, "needs_attention": true,
			"report": "服务端拒绝认证（HTTP 401），请检查客户端登录状态、凭据对应的服务地址及脚本版本",
		}
	}
	return map[string]any{
		"result": "FORBIDDEN", "http": code, "needs_attention": true,
		"report": "服务端拒绝此操作（HTTP 403），请检查账号权限或活动条件；不能据此判断登录过期",
	}
}

func isAlreadyCheckedIn(body any) bool {
	if body == nil {
		return true
	}
	m, ok := body.(map[string]any)
	if !ok {
		return false
	}
	msg := strOr(m["msg"], "")
	return isIntNumber(m["code"], 10001) || strings.Contains(msg, "已签")
}

// refreshStatus 领奖后再查一次状态，拿服务端算好的连签/累计值；查不到就回落到旧状态。
func refreshStatus(headers map[string]string, endpoint string, fallback any) any {
	code, body := post(endpoint+"/v2/billing/meter/checkin-activity-status", headers, nil, true)
	if code >= 200 && code < 300 {
		if _, ok := body.(map[string]any); ok {
			return body
		}
	}
	return fallback
}

func alreadyReport(status any, via string) map[string]any {
	todayCredit := orFirst(dig(status, "today_credit"), dig(status, "daily_credit"))
	streakDays := dig(status, "streak_days")
	totalCredits := dig(status, "total_credits")
	isStreakDay := dig(status, "is_streak_day")
	nextStreakDay := dig(status, "next_streak_day")

	inner := []string{}
	if todayCredit != nil {
		inner = append(inner, "今日 +"+fmtCreditStr(todayCredit))
	}
	if streakDays != nil {
		inner = append(inner, "连续 "+fmtAny(streakDays)+" 天")
	}
	if totalCredits != nil {
		inner = append(inner, "累计 "+fmtCreditStr(totalCredits)+" 积分")
	}
	prefix := via
	if prefix == "" {
		prefix = "今日已签过"
	}
	report := prefix
	if len(inner) > 0 {
		report = fmt.Sprintf("%s（%s）", prefix, strings.Join(inner, "，"))
	}
	return map[string]any{
		"result": "ALREADY", "report": report,
		"today_credit": todayCredit, "streak_days": streakDays,
		"total_credits": totalCredits, "is_streak_day": isStreakDay,
		"next_streak_day": nextStreakDay,
	}
}

func runAuto(headers map[string]string, endpoint string) (int, map[string]any) {
	scode, sbody := post(endpoint+"/v2/billing/meter/checkin-activity-status", headers, nil, true)

	if scode == codeBudgetOut {
		return 1, map[string]any{"result": "TIMEOUT", "report": "已达本次运行时间预算，签到跳过，下次自动重试"}
	}
	if scode == codeNoNetwork {
		errText := ""
		if m, ok := sbody.(map[string]any); ok {
			errText = strOr(m["error"], "")
		}
		return 1, map[string]any{
			"result": "NETWORK",
			"report": fmt.Sprintf("网络不可达，签到跳过，下次自动重试（%s）", errText),
			"error":  errText,
		}
	}
	if scode == 401 || scode == 403 {
		return 1, authFailure(scode)
	}
	if scode < 200 || scode >= 300 {
		return 1, map[string]any{
			"result": "ERROR",
			"report": fmt.Sprintf("签到接口返回异常（HTTP %d），请重新登录客户端或稍后重试", scode),
			"http":   scode, "status_body": sbody,
		}
	}

	status := sbody
	if _, ok := sbody.(map[string]any); !ok {
		status = map[string]any{}
	}
	active := dig(status, "active")
	activityName := dig(status, "activity_name")
	if flag, ok := active.(bool); ok && !flag {
		report := "签到活动未开启"
		if truthy(activityName) {
			report += "（" + fmtAny(activityName) + "）"
		}
		return 0, map[string]any{"result": "INACTIVE", "report": report, "active": false}
	}

	checked := dig(status, "today_checked_in")
	checkedIn := false
	if flag, ok := checked.(bool); ok && flag {
		checkedIn = true
	}
	if isIntNumber(checked, 1) {
		checkedIn = true
	}
	if checkedIn {
		return 0, alreadyReport(status, "")
	}

	ccode, cbody := post(endpoint+"/v2/billing/meter/daily-checkin", headers, nil, true)
	if ccode == codeNoNetwork || ccode == codeBudgetOut {
		errText := ""
		if m, ok := cbody.(map[string]any); ok {
			errText = strOr(m["error"], "")
		}
		result := "TIMEOUT"
		if ccode == codeNoNetwork {
			result = "NETWORK"
		}
		return 1, map[string]any{
			"result": result,
			"report": fmt.Sprintf("领取请求未能送达，下次自动重试（%s）", errText),
		}
	}
	if ccode == 401 || ccode == 403 {
		return 1, authFailure(ccode)
	}

	if isAlreadyCheckedIn(cbody) {
		fresh := refreshStatus(headers, endpoint, status)
		return 0, alreadyReport(fresh, "今日已签过（服务端判定已领取）")
	}

	credit := dig(cbody, "credit")
	if credit != nil {
		fresh := refreshStatus(headers, endpoint, status)
		streakDays := orFirst(dig(fresh, "streak_days"), dig(status, "streak_days"))
		totalCredits := dig(fresh, "total_credits")
		isStreakDay := dig(fresh, "is_streak_day")
		nextStreakDay := dig(fresh, "next_streak_day")
		bonus := ""
		if truthy(isStreakDay) {
			bonus = "，且为连签奖励日"
		}
		cum := ""
		if totalCredits != nil {
			cum = "，累计 " + fmtCreditStr(totalCredits) + " 积分"
		}
		streak := ""
		if streakDays != nil {
			streak = fmt.Sprintf("（连续 %s 天%s）", fmtAny(streakDays), cum)
		} else if cum != "" {
			streak = "（" + strings.TrimPrefix(cum, "，") + "）"
		}
		report := fmt.Sprintf("成功领取 %s 积分%s%s", fmtCreditStr(credit), bonus, streak)
		return 0, map[string]any{
			"result": "CLAIMED", "report": report, "credit": credit,
			"streak_days": streakDays, "total_credits": totalCredits,
			"is_streak_day": isStreakDay, "next_streak_day": nextStreakDay,
		}
	}

	if m, ok := cbody.(map[string]any); ok {
		_, hasCode := m["code"]
		_, hasMsg := m["msg"]
		if hasCode || hasMsg {
			msg := strOr(m["msg"], "")
			if msg == "" {
				msg = "code " + fmtAny(m["code"])
			}
			return 1, map[string]any{
				"result": "ERROR", "report": fmt.Sprintf("领取失败：%s（HTTP %d）", msg, ccode),
				"http": ccode, "claim_body": cbody,
			}
		}
	}
	raw := truncateRunes(pyDumps(cbody), 200)
	return 1, map[string]any{
		"result": "UNKNOWN", "report": "未识别的领取返回，请检查接口：" + raw,
		"http": ccode, "claim_body": cbody,
	}
}

// growthRunner 是可替换的间接层，只用于让「成长中心整体异常」这条兜底路径可测。
var growthRunner = runGrowth

// safeRunGrowth 对应 run_daily 里包住成长中心的 try/except：
// 成长中心整体出意外也不能吞掉「签到已成功」这条事实，只把失败并进汇报。
func safeRunGrowth(headers map[string]string, endpoint string) (code int, out map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			code, out = 1, map[string]any{
				"result": "ERROR",
				"report": fmt.Sprintf("成长中心异常（%s: %v）", panicTypeName(r), r),
			}
		}
	}()
	return growthRunner(headers, endpoint)
}

func runDaily(headers map[string]string, endpoint string) (int, map[string]any, bool) {
	code, out := runAuto(headers, endpoint)
	result, _ := out["result"].(string)
	if result == "NETWORK" || result == "TIMEOUT" {
		out["growth"] = "网络不可达或时间预算耗尽，成长中心跳过"
		out["growth_result"] = result
		return code, out, false
	}
	if result == "NO_SESSION" || result == "AUTH_REJECTED" || result == "FORBIDDEN" {
		out["growth"] = "认证或权限检查未通过，成长中心跳过"
		out["growth_result"] = result
		return code, out, false
	}

	gcode, gout := safeRunGrowth(headers, endpoint)
	out["growth"] = gout["report"]
	out["growth_result"] = gout["result"]
	if truthy(gout["credits_gained"]) || gcode != 0 {
		out["report"] = fmtAny(out["report"]) + "；" + fmtAny(gout["report"])
	}
	if gcode != 0 && code == 0 {
		code = gcode
	}
	out["needs_attention"] = code != 0
	quiet := (result == "ALREADY" || result == "INACTIVE") && truthy(gout["idle"])
	return code, out, quiet
}
