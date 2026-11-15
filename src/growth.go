// growth.go — 成长中心：旅行礼物、任务、补登、连登兑换、盲盒、Buddy 盲盒、能量与连签展示。
//
// 每一段单独守护，任何一段异常都不会吞掉已领到的奖励；认证/权限拒绝则结束本轮。
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

const makeupMaxPerRun = 1

var redeemRewards = map[string]string{
	"7d":  "+2 能量 +1 补登卡 +1 次抽奖",
	"14d": "+50 积分 +3 能量 +1 补登卡 +1 次抽奖",
	"28d": "+150 积分 +5 能量 +1 补登卡 +1 次抽奖",
}

type redeemTier struct {
	tier      string
	statusKey string
	label     string
	days      int
}

var redeemTiers = []redeemTier{
	{"7d", "starter", "入门", 7},
	{"14d", "advanced", "进阶", 14},
	{"28d", "legendary", "巅峰", 28},
}

var isoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

type growthState struct {
	headers     map[string]string
	base        string
	parts       []string
	credits     int
	failures    int
	hard        int
	successes   int
	streakBody  any
	streakStale bool
	abortOut    map[string]any
}

func (g *growthState) add(format string, args ...any) {
	g.parts = append(g.parts, fmt.Sprintf(format, args...))
}

func (g *growthState) checkAuth(code int) bool {
	if code == 401 || code == 403 {
		g.abortOut = authFailure(code)
		return true
	}
	return false
}

func (g *growthState) noteHTTP(code int, body any, label string, required bool) bool {
	if code >= 200 && code < 300 && (!required || apiSuccess(code, body)) {
		return false
	}
	reason := httpLabel(code)
	detail := ""
	if m, ok := body.(map[string]any); ok {
		detail = strOr(orFirst(m["error"], m["msg"]), "")
	}
	suffix := reason
	if detail != "" {
		if detail == reason {
			suffix = detail
		} else {
			suffix = fmt.Sprintf("%s（%s）", reason, detail)
		}
	}
	g.add("%s失败：%s", label, suffix)
	if required || isHardFailure(code) {
		g.failures++
		g.hard++
	}
	return true
}

func (g *growthState) guard(prefix string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			g.add("%s（%s: %v）", prefix, panicTypeName(r), r)
			g.failures++
			g.hard++
		}
	}()
	fn()
}

func (g *growthState) section(name string, fn func()) {
	g.guard(name+"模块异常", fn)
}

func nestedString(obj any, key string, def string) string {
	m, ok := obj.(map[string]any)
	if !ok {
		return def
	}
	if v, exists := m[key]; exists {
		return fmtAny(v)
	}
	return def
}

func (g *growthState) travel() {
	scode, sbody := get(g.base+"/buddy/travel/status", g.headers)
	if scode == codeBudgetOut {
		g.abortOut = map[string]any{"result": "TIMEOUT", "report": "时间预算耗尽，成长中心跳过，下次自动重试"}
		return
	}
	if scode == codeNoNetwork {
		errText := ""
		if m, ok := sbody.(map[string]any); ok {
			errText = strOr(m["error"], "")
		}
		g.abortOut = map[string]any{
			"result": "NETWORK",
			"report": fmt.Sprintf("网络不可达，成长中心跳过（%s）", errText),
		}
		return
	}
	if g.checkAuth(scode) {
		return
	}
	travel := ""
	if scode >= 200 && scode < 300 {
		travel = strOr(dig(sbody, "state"), "")
	}
	dailyLimit := false
	if scode >= 200 && scode < 300 {
		dailyLimit = truthy(dig(sbody, "daily_limit_reached"))
	}
	g.noteHTTP(scode, sbody, "查旅行状态", false)

	claimed := false
	if travel == "arrived" {
		ccode, cbody := post(g.base+"/buddy/travel/claim", g.headers,
			map[string]any{"record_id": dig(sbody, "record_id")}, false)
		if g.checkAuth(ccode) {
			return
		}
		if ccode >= 200 && ccode < 300 && dig(cbody, "reward_credit") != nil {
			got := asInt(dig(cbody, "reward_credit"), 0)
			g.credits += got
			g.add("领旅行礼物 +%s 积分", fmtCreditStr(got))
			g.successes++
			claimed = true
		} else {
			msg := strOr(dig(cbody, "msg"), "")
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d", ccode)
			}
			g.add("领旅行礼物失败：%s", msg)
			g.failures++
			if isHardFailure(ccode) {
				g.hard++
			}
		}
		if claimed {
			travel = "idle"
		}
	}

	switch {
	case travel == "idle" && dailyLimit:
		g.add("今日旅行名额已用完")
	case travel == "idle":
		ccode, cbody := get(g.base+"/buddy/travel/config", g.headers)
		if g.checkAuth(ccode) {
			return
		}
		var locs []any
		if ccode >= 200 && ccode < 300 {
			locs, _ = dig(cbody, "locations").([]any)
		}
		if len(locs) > 0 {
			if loc, ok := locs[0].(map[string]any); ok {
				dcode, dbody := post(g.base+"/buddy/travel/depart", g.headers,
					map[string]any{"location_id": loc["id"]}, false)
				if isBusinessForbidden(dcode, dbody) {
					// 常态业务条件（如还没有激活的 Buddy）：只跳过派出发，
					// 后续任务、补登、兑换、盲盒照常执行。
					g.add("派 Buddy 跳过：%s", forbiddenMsg(dbody))
				} else if g.checkAuth(dcode) {
					return
				} else if dcode >= 200 && dcode < 300 {
					location := dig(dbody, "location")
					locName := nestedString(location, "name", "?")
					dur := dig(dbody, "duration_hours")
					if !truthy(dur) {
						dur = nil
						if m, ok := location.(map[string]any); ok {
							if v, exists := m["duration_hours"]; exists {
								dur = v
							}
						}
					}
					if !truthy(dur) {
						dur = "?"
					}
					g.add("派 Buddy 去%s（%s 小时后回）", locName, fmtAny(dur))
					g.successes++
				} else {
					msg := strOr(dig(dbody, "msg"), "")
					if msg == "" {
						msg = fmt.Sprintf("HTTP %d", dcode)
					}
					g.add("派 Buddy 失败：%s", msg)
					g.failures++
					if isHardFailure(dcode) {
						g.hard++
					}
				}
			}
		}
	case travel == "traveling":
		locName := nestedString(dig(sbody, "location"), "name", "?")
		eta := fmtETA(dig(sbody, "arrive_at"), dig(sbody, "server_now"))
		g.add("Buddy 旅行中（%s%s）", locName, eta)
	}
}

func (g *growthState) tasks() {
	tcode, tbody := get(g.base+"/tasks", g.headers)
	if g.checkAuth(tcode) {
		return
	}
	if g.noteHTTP(tcode, tbody, "查任务列表", false) {
		return
	}
	tasks, _ := dig(tbody, "tasks").([]any)
	titles := map[string]string{}
	pending := []any{}
	for _, item := range tasks {
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		code := t["task_code"]
		title := t["title"]
		if !truthy(title) {
			title = code
		}
		titles[fmtAny(code)] = fmtAny(title)
		if truthy(code) && !truthy(t["locked"]) && strOr(t["accept_status"], "") == "not_accepted" {
			pending = append(pending, code)
		}
	}

	for i := 0; i < len(pending); i += 20 {
		if budgetLeft() <= 0 {
			g.add("时间预算耗尽，剩余任务下次再接单")
			break
		}
		end := i + 20
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[i:end]
		acode, abody := post(g.base+"/tasks/accept", g.headers,
			map[string]any{"task_codes": batch}, false)
		if g.checkAuth(acode) {
			return
		}
		results, ok := dig(abody, "results").([]any)
		if !ok {
			status := "error"
			if acode >= 200 && acode < 300 {
				status = "ok"
			}
			results = []any{}
			for _, code := range batch {
				results = append(results, map[string]any{
					"task_code": code, "status": status, "message": dig(abody, "msg"),
				})
			}
		}
		for _, item := range results {
			r, ok := item.(map[string]any)
			if !ok {
				continue
			}
			code := r["task_code"]
			title, exists := titles[fmtAny(code)]
			if !exists {
				title = fmtAny(code)
			}
			if strOr(r["status"], "") == "error" {
				msg := strOr(r["message"], "")
				if msg == "" {
					msg = fmt.Sprintf("HTTP %d", acode)
				}
				g.add("领取任务「%s」失败：%s", title, msg)
				g.failures++
				if isHardFailure(acode) {
					g.hard++
				}
			} else {
				g.add("领取任务「%s」（进度开始计）", title)
				g.successes++
			}
		}
	}

	for _, item := range tasks {
		if budgetLeft() <= 0 {
			g.add("时间预算耗尽，剩余任务奖下次再领")
			break
		}
		t, ok := item.(map[string]any)
		if !ok || truthy(t["locked"]) || strOr(t["accept_status"], "") != "completed" {
			continue
		}
		code := t["task_code"]
		title, exists := titles[fmtAny(code)]
		if !exists {
			title = fmtAny(code)
		}
		claimTitle := title
		claimCode := code
		g.guard(fmt.Sprintf("领任务奖「%s」异常", fmtAny(code)), func() {
			ccode, cbody := post(fmt.Sprintf("%s/tasks/%s/claim", g.base, fmtAny(claimCode)),
				g.headers, map[string]any{}, false)
			if g.checkAuth(ccode) {
				return
			}
			if ccode >= 200 && ccode < 300 && !truthy(dig(cbody, "already_claimed")) {
				rc := firstInt(cbody, "credit", t["reward_credit"])
				re := firstInt(cbody, "energy", t["reward_energy"])
				g.credits += rc
				g.add("领任务奖「%s」+credit%s+energy%s", claimTitle, fmtCreditStr(rc), fmtCreditStr(re))
				g.successes++
			} else if ccode >= 200 && ccode < 300 {
				g.add("任务奖「%s」已领过", claimTitle)
			} else {
				msg := strOr(dig(cbody, "msg"), "")
				if msg == "" {
					msg = fmt.Sprintf("HTTP %d", ccode)
				}
				g.add("领任务奖「%s」失败：%s", claimTitle, msg)
				g.failures++
				if isHardFailure(ccode) {
					g.hard++
				}
			}
		})
		if g.abortOut != nil {
			return
		}
	}
}

// numericOnly 只接受真正的 JSON 数字，字符串分数按格式错误处理。
func numericOnly(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// makeupCandidates 只从服务端活跃日历里挑当月、今天之前、活动上线之后且明确为零分的日期。
// makeup_dates 是「已补登」记录，必须排除；日历缺项不推测。
func makeupCandidates(streakBody, heatmapBody any) ([]string, error) {
	parseDate := func(value any) (time.Time, error) {
		s, ok := value.(string)
		if !ok || !isoDateRe.MatchString(s) {
			return time.Time{}, fmt.Errorf("补登日期格式无效")
		}
		day, err := time.Parse("2006-01-02", s)
		if err != nil {
			return time.Time{}, fmt.Errorf("补登日期格式无效")
		}
		return day, nil
	}
	todayObj, ok := dig(heatmapBody, "today").(map[string]any)
	if !ok {
		return nil, fmt.Errorf("活跃日历缺少服务端日期")
	}
	today, err := parseDate(todayObj["date"])
	if err != nil {
		return nil, err
	}
	launch, err := parseDate(dig(streakBody, "launch_date"))
	if err != nil {
		return nil, err
	}
	start := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	if launch.After(start) {
		start = launch
	}
	floor := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
	if floor.After(start) {
		start = floor
	}
	streak, ok := dig(streakBody, "streak").(map[string]any)
	if !ok {
		return nil, fmt.Errorf("连登状态格式无效")
	}
	rawMadeUp := streak["makeup_dates"]
	if rawMadeUp == nil {
		rawMadeUp = []any{}
	}
	madeUpList, ok := rawMadeUp.([]any)
	if !ok {
		return nil, fmt.Errorf("已补登记录格式无效")
	}
	madeUp := map[string]bool{}
	for _, value := range madeUpList {
		day, err := parseDate(value)
		if err != nil {
			return nil, err
		}
		madeUp[day.Format("2006-01-02")] = true
	}
	cells, ok := dig(heatmapBody, "cells").([]any)
	if !ok {
		return nil, fmt.Errorf("活跃日历缺少日期列表")
	}
	scores := map[string]float64{}
	for _, cell := range cells {
		item, ok := cell.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("活跃日历记录格式无效")
		}
		day, err := parseDate(item["date"])
		if err != nil {
			return nil, err
		}
		score, ok := numericOnly(item["score"])
		if !ok || math.IsNaN(score) || math.IsInf(score, 0) || score < 0 {
			return nil, fmt.Errorf("活跃日历分数无效")
		}
		key := day.Format("2006-01-02")
		if previous, exists := scores[key]; exists && previous != score {
			return nil, fmt.Errorf("活跃日历同一日期的记录冲突")
		}
		scores[key] = score
	}
	startKey := start.Format("2006-01-02")
	todayKey := today.Format("2006-01-02")
	result := []string{}
	for key, score := range scores {
		if key >= startKey && key < todayKey && score == 0 && !madeUp[key] {
			result = append(result, key)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(result)))
	return result, nil
}

func redeemRewardDesc(body any, tier string) string {
	bits := []string{}
	if credit := asInt(dig(body, "credit_granted"), 0); credit != 0 {
		bits = append(bits, "+"+fmtCreditStr(credit)+" 积分")
	}
	if energy := asInt(dig(body, "energy_granted"), 0); energy != 0 {
		bits = append(bits, "+"+fmtCreditStr(energy)+" 能量")
	}
	if cards := asInt(dig(body, "cards_granted"), 0); cards != 0 {
		bits = append(bits, "+"+fmtCreditStr(cards)+" 补登卡")
	}
	if chances := asInt(dig(body, "chances_granted"), 0); chances != 0 {
		bits = append(bits, "+"+fmtCreditStr(chances)+" 次抽奖")
	}
	if len(bits) > 0 {
		return "（" + strings.Join(bits, " ") + "）"
	}
	desc, ok := redeemRewards[tier]
	if !ok {
		desc = "奖励已到账"
	}
	return "（" + desc + "）"
}

func (g *growthState) makeup() {
	mcode, mbody := get(g.base+"/streak", g.headers)
	if g.checkAuth(mcode) {
		return
	}
	if g.noteHTTP(mcode, mbody, "查连登状态", true) {
		return
	}
	g.streakBody = mbody
	cardsObj := dig(mbody, "makeup_cards")
	cards := 0
	if m, ok := cardsObj.(map[string]any); ok {
		cards = asInt(m["balance"], 0)
	} else {
		cards = asInt(cardsObj, 0)
	}
	dates := []string{}
	if cards > 0 {
		hcode, hbody := get(g.base+"/heatmap", g.headers)
		if g.checkAuth(hcode) {
			return
		}
		if !g.noteHTTP(hcode, hbody, "查补登日历", true) {
			var err error
			dates, err = makeupCandidates(mbody, hbody)
			if err != nil {
				g.add("补登模块异常（ValueError: %s）", err)
				g.failures++
				g.hard++
				return
			}
		}
	}
	if len(dates) == 0 {
		return
	}
	madeUp := 0
	limit := cards
	if limit > makeupMaxPerRun {
		limit = makeupMaxPerRun
	}
	for i, date := range dates {
		if i >= limit {
			break
		}
		if budgetLeft() <= 0 {
			g.add("时间预算耗尽，剩余补登下次再做")
			break
		}
		ucode, ubody := post(g.base+"/makeup-cards/use", g.headers,
			map[string]any{"target_date": date}, false)
		if g.checkAuth(ucode) {
			return
		}
		if apiSuccess(ucode, ubody) {
			cards--
			madeUp++
			g.streakStale = true
			leftObj := dig(ubody, "makeup_cards")
			leftCards := cards
			if m, ok := leftObj.(map[string]any); ok {
				leftCards = asInt(m["balance"], cards)
			} else {
				leftCards = asInt(leftObj, cards)
			}
			g.add("补登 %s（剩 %s 张卡）", date, fmtCreditStr(leftCards))
			if leftCards < 0 {
				leftCards = 0
			}
			cards = leftCards
			g.successes++
		} else {
			msg := strOr(dig(ubody, "msg"), "")
			if ucode == 400 && strings.EqualFold(strings.TrimSpace(msg), "date is not broken, no makeup needed") {
				g.add("%s 已活跃或已补登，无需再次补登", date)
				break
			}
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d", ucode)
			}
			g.add("补登 %s 失败：%s", date, msg)
			g.failures++
			g.hard++
			break
		}
	}
	if madeUp > 0 && len(dates) > madeUp && cards > 0 {
		g.add("另有 %s 天可补、剩 %s 张卡，下轮继续",
			fmtCreditStr(len(dates)-madeUp), fmtCreditStr(cards))
	}
}

func (g *growthState) redeem() {
	rcode, rbody := get(g.base+"/redeem/summary", g.headers)
	if g.checkAuth(rcode) {
		return
	}
	if g.noteHTTP(rcode, rbody, "查连登兑换", false) {
		return
	}
	for _, tier := range redeemTiers {
		if budgetLeft() <= 0 {
			g.add("时间预算耗尽，剩余连登兑换下次再领")
			break
		}
		status := dig(rbody, tier.statusKey+"_status")
		if !truthy(status) || strOr(status, "") == "claimed" || strOr(status, "") == "locked" {
			continue
		}
		c2code, c2body := post(g.base+"/redeem", g.headers,
			map[string]any{"tier": tier.tier, "client_token": clientToken("u")}, false)
		if isUnknownTier(c2code, c2body) {
			c2code, c2body = post(g.base+"/redeem", g.headers,
				map[string]any{"tier": tier.days, "client_token": clientToken("u")}, false)
		}
		// 403「连登天数不足」是业务常态，必须先于认证判断，否则会误中止整个成长中心。
		if isTierLocked(c2code, c2body) {
			g.add("连登兑换「%s」未解锁（连登天数不足）", tier.label)
			continue
		}
		if g.checkAuth(c2code) {
			return
		}
		if c2code >= 200 && c2code < 300 {
			g.credits += asInt(dig(c2body, "credit_granted"), 0)
			g.add("连登兑换「%s」%s", tier.label, redeemRewardDesc(c2body, tier.tier))
			g.successes++
		} else {
			msg := strOr(dig(c2body, "msg"), "")
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d", c2code)
			}
			g.add("连登兑换「%s」失败：%s", tier.label, msg)
			g.failures++
			if isHardFailure(c2code) {
				g.hard++
			}
		}
	}
}

func (g *growthState) lottery() {
	lcode, lbody := get(g.base+"/lottery/chances", g.headers)
	if g.checkAuth(lcode) {
		return
	}
	chances := 0
	if !g.noteHTTP(lcode, lbody, "查抽奖机会", false) {
		chances = asInt(dig(lbody, "balance"), 0)
	}
	if chances <= 0 {
		return
	}
	dcode, dbody := post(g.base+"/lottery/draw", g.headers,
		map[string]any{"client_token": clientToken("u")}, false)
	if g.checkAuth(dcode) {
		return
	}
	if dcode >= 200 && dcode < 300 {
		prize := fmtAny(orFirst(dig(dbody, "prize_name"), dig(dbody, "prize"), "未知"))
		if truthy(dig(dbody, "need_address")) || truthy(dig(dbody, "require_address")) {
			prize += "（实物奖，需到成长中心填写收件信息）"
		}
		g.add("开盲盒获得：%s", prize)
		g.successes++
		if chances > 1 {
			g.add("还剩 %s 次抽奖机会，下轮继续", fmtCreditStr(chances-1))
		}
	} else {
		msg := strOr(dig(dbody, "msg"), "")
		if isNoChance(msg) {
			g.add("开盲盒：%s", strOr(msg, "无抽奖机会"))
		} else {
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d", dcode)
			}
			g.add("开盲盒失败：%s", msg)
			g.failures++
			if isHardFailure(dcode) {
				g.hard++
			}
		}
	}
}

func (g *growthState) buddyBox() {
	qcode, qbody := get(g.base+"/buddy/quota", g.headers)
	if g.checkAuth(qcode) {
		return
	}
	if g.noteHTTP(qcode, qbody, "查 Buddy 能量", false) {
		return
	}
	affordable := asInt(dig(qbody, "affordable"), 0)
	maxOpen := asInt(dig(qbody, "max_open_count"), 1)
	if maxOpen == 0 {
		maxOpen = 1
	}
	if affordable <= 0 {
		return
	}
	count := affordable
	if maxOpen < count {
		count = maxOpen
	}
	ocode, obody := post(g.base+"/buddy/open", g.headers,
		map[string]any{"count": count, "client_token": clientToken("u")}, false)
	if g.checkAuth(ocode) {
		return
	}
	if ocode >= 200 && ocode < 300 {
		name := orFirst(dig(obody, "buddy"), dig(obody, "name"), dig(obody, "buddies"))
		nameStr, ok := name.(string)
		if !ok {
			nameStr = "新 Buddy"
		}
		g.add("开 Buddy 盲盒 ×%s（%s）", fmtCreditStr(count), nameStr)
		g.successes++
	} else {
		msg := strOr(dig(obody, "msg"), "")
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", ocode)
		}
		g.add("开 Buddy 盲盒失败：%s", msg)
		g.failures++
		if isHardFailure(ocode) {
			g.hard++
		}
	}
}

// displayValues 只取纯展示值；对应 Python 的 try/except pass：
// 这里出任何意外都按「没取到」处理，不能让整轮签到的汇报失败。
func (g *growthState) displayValues() (energy, streakDays any) {
	defer func() {
		if r := recover(); r != nil {
			energy, streakDays = nil, nil
		}
	}()
	if budgetLeft() > 0 {
		ecode, ebody := get(g.base+"/energy", g.headers)
		if ecode != 401 && ecode != 403 && ecode >= 200 && ecode < 300 {
			energy = dig(ebody, "balance")
		}
	}
	if g.streakBody != nil && !g.streakStale {
		if streak, ok := dig(g.streakBody, "streak").(map[string]any); ok {
			streakDays = streak["days"]
		}
	} else if budgetLeft() > 0 {
		scode2, sbody2 := get(g.base+"/streak", g.headers)
		if scode2 != 401 && scode2 != 403 {
			if streak, ok := dig(sbody2, "streak").(map[string]any); ok {
				streakDays = streak["days"]
			}
		}
	}
	return energy, streakDays
}

func runGrowth(headers map[string]string, endpoint string) (int, map[string]any) {
	g := &growthState{headers: headers, base: endpoint + "/v2/activity/growth"}

	// 旅行段没有预算守卫（与 Python 一致）；其余各段统一按预算闸门执行，
	// 认证/权限拒绝一旦置位就整轮中止。
	g.section("旅行", g.travel)
	if g.abortOut != nil {
		return 1, g.abortOut
	}
	sections := []struct {
		name string
		skip string
		fn   func()
	}{
		{"任务", "时间预算耗尽，任务领奖跳过", g.tasks},
		{"补登", "时间预算耗尽，补登跳过", g.makeup},
		{"连登兑换", "时间预算耗尽，连登兑换跳过", g.redeem},
		{"盲盒", "时间预算耗尽，盲盒跳过", g.lottery},
		{"Buddy 盲盒", "时间预算耗尽，Buddy 盲盒跳过", g.buddyBox},
	}
	for _, s := range sections {
		if budgetLeft() <= 0 {
			g.add("%s", s.skip)
			continue
		}
		g.section(s.name, s.fn)
		if g.abortOut != nil {
			return 1, g.abortOut
		}
	}

	energy, streakDays := g.displayValues()
	tail := []string{}
	if energy != nil {
		tail = append(tail, "能量 "+fmtAny(energy))
	}
	if streakDays != nil {
		tail = append(tail, "连签 "+fmtAny(streakDays)+" 天")
	}
	if g.credits != 0 {
		tail = append(tail, "本次 +共 "+fmtCreditStr(g.credits)+" 积分")
	}
	report := ""
	switch {
	case len(g.parts) > 0:
		report = strings.Join(g.parts, "；")
	case g.failures > 0:
		report = "成长中心各步骤均失败"
	default:
		report = "成长中心无可领取项"
	}
	if len(tail) > 0 {
		report += "（" + strings.Join(tail, "，") + "）"
	}
	code := 0
	if g.hard > 0 {
		code = 1
	}
	out := map[string]any{
		"result": "GROWTH", "report": report, "credits_gained": g.credits,
		"energy": energy, "streak_days": streakDays,
		"idle": g.successes == 0 && g.failures == 0,
	}
	if g.failures > 0 {
		out["failures"] = g.failures
	}
	return code, out
}
