// main.go — 无 UI 的 Go 版入口：命令分发、时间预算、结果输出。
//
// 命令集与 Python 版一致（auto / silent / growth / silent-poll / silent-growth /
// status / claim / all / doctor）。silent 类命令把结果写进日志而不是 stdout，
// 定时任务直接重定向 stdout 或让 silent 自行落盘都可以。
package main

import (
	"fmt"
	"math"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 伪 HTTP 码：区分「没拿到响应」的两种原因。
const (
	codeNoNetwork = -1
	codeBudgetOut = -2

	defaultBudgetSeconds = 420.0
	maxBudgetSeconds     = 540.0
	pollBudgetSeconds    = 180.0
	pollMaxBudgetSeconds = 240.0
	requestTimeout       = 30.0
)

var (
	startedAt       time.Time
	budgetSeconds   = defaultBudgetSeconds
	configWarning   string
	sensitiveValues []string
	noPauseFlag     bool
	prettyFlag      bool
	jsonFlag        bool
)

var attentionResults = map[string]bool{
	"ERROR": true, "UNKNOWN": true, "NETWORK": true, "TIMEOUT": true,
	"NO_AUTH": true, "NO_SESSION": true, "AUTH_ERROR": true,
	"AUTH_REJECTED": true, "AUTH_BUSY": true, "FORBIDDEN": true,
}

// knownActions / pollActions 同时驱动命令白名单、报错提示与预算分档，避免三处漂移。
var (
	knownActions = []string{
		"auto", "silent", "growth", "silent-poll", "silent-growth",
		"status", "claim", "all", "doctor",
	}
	pollActions = map[string]bool{"silent-poll": true, "silent-growth": true}
)

func main() {
	action := parseArgs(os.Args[1:])
	code := func() (code int) {
		defer func() {
			if r := recover(); r != nil {
				emit(map[string]any{
					"result":          "ERROR",
					"report":          fmt.Sprintf("脚本运行异常（%s）", panicTypeName(r)),
					"needs_attention": true,
				}, action)
				code = 2
			}
		}()
		return runAction(action)
	}()
	// 双击运行时暂停一下，让人看得见那一行 JSON；
	// silent / 重定向 / 非交互环境由 pauseMode 直接跳过。
	pauseBeforeExit(action)
	os.Exit(code)
}

func panicTypeName(r any) string {
	if t := reflect.TypeOf(r); t != nil {
		if name := t.Name(); name != "" {
			return name
		}
		return t.String()
	}
	return "unknown"
}

// noPauseArgs 是命令行里的「执行完立即退出」开关。可以写在命令前后：
//
//	workbuddy-signin.exe auto --no-pause
//	workbuddy-signin.exe nopause
//
// 解析时去掉 - / _ 并忽略大小写；常见误拼（nppause、npause、nopase 等）
// 也按 no-pause 处理，避免计划任务因为拼错一个字母而卡在按键提示上。
var noPauseRe = regexp.MustCompile(`^n*o*p+a*u*s*e?$`)

func isNoPauseArg(arg string) bool {
	s := strings.ToLower(strings.TrimSpace(arg))
	s = strings.NewReplacer("-", "", "_", "").Replace(s)
	// 长度下限挡掉 n / no / nop 这类过短输入，避免误判。
	if len(s) < 5 || len(s) > 16 {
		return false
	}
	return noPauseRe.MatchString(s)
}

// isOutputStyleArg 解析输出格式开关，返回是否已消费该参数。
//
//	--pretty / pretty / text   强制美化（给人看）
//	--json / json / compact    强制单行 JSON（给脚本看）
//	--no-pretty                同 --json
func isOutputStyleArg(arg string) bool {
	s := strings.ToLower(strings.TrimSpace(arg))
	s = strings.TrimLeft(s, "-")
	s = strings.ReplaceAll(s, "_", "-")
	switch s {
	case "pretty", "text", "human":
		prettyFlag = true
		return true
	case "json", "compact", "raw", "no-pretty":
		jsonFlag = true
		return true
	}
	return false
}

// parseArgs 解析「命令 + 可选开关」。第一个非开关参数作为命令，
// 未给命令时回落 auto；多余的位置参数保持旧行为，忽略。
func parseArgs(args []string) string {
	action := ""
	prettyFlag, jsonFlag = false, false
	for _, arg := range args {
		if isOutputStyleArg(arg) {
			continue
		}
		if isNoPauseArg(arg) {
			noPauseFlag = true
			continue
		}
		if action == "" {
			action = arg
		}
	}
	if action == "" {
		return "auto"
	}
	return action
}

// envFlag 对应 Python _env_flag：空串与 0/false/no/off 一律视为关。
func envFlag(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// startBudget 按命令选默认预算与上限；轮询任务的计划任务时限更短，必须单独夹上限。
func startBudget(action string) {
	defaultSec, maxSec := defaultBudgetSeconds, maxBudgetSeconds
	if pollActions[action] {
		defaultSec, maxSec = pollBudgetSeconds, pollMaxBudgetSeconds
	}
	budgetSeconds = defaultSec
	configWarning = ""
	if raw := os.Getenv("WORKBUDDY_BUDGET_SECONDS"); raw != "" {
		val, err := strconv.ParseFloat(raw, 64)
		switch {
		case err != nil:
			configWarning = fmt.Sprintf("WORKBUDDY_BUDGET_SECONDS='%s' 不是数字，已回落 %d 秒", raw, int(defaultSec))
		case val <= 0 || math.IsNaN(val) || math.IsInf(val, -1):
			configWarning = fmt.Sprintf("WORKBUDDY_BUDGET_SECONDS=%s 必须为正数，已回落 %d 秒", raw, int(defaultSec))
		case val > maxSec || math.IsInf(val, 1):
			budgetSeconds = maxSec
			configWarning = fmt.Sprintf("WORKBUDDY_BUDGET_SECONDS=%s 超过本命令上限，已夹到 %d 秒（须小于该定时任务的 ExecutionTimeLimit）", raw, int(maxSec))
		default:
			budgetSeconds = val
		}
	}
	startedAt = time.Now()
}

func budgetLeft() float64 {
	if startedAt.IsZero() {
		return budgetSeconds
	}
	return budgetSeconds - time.Since(startedAt).Seconds()
}

func boundedTimeout() float64 {
	return math.Max(1, math.Min(requestTimeout, budgetLeft()))
}

func authFailureOutput(err error) map[string]any {
	if ae, ok := err.(*authError); ok {
		return ae.output()
	}
	return map[string]any{
		"result": "ERROR", "report": fmt.Sprintf("本地凭据处理失败（%v）", err),
		"needs_attention": true,
	}
}

func runAction(action string) int {
	startBudget(action)
	if !contains(knownActions, action) {
		emit(map[string]any{
			"result": "ERROR",
			"report": "未知命令：" + action + "（可用：" + strings.Join(knownActions, " / ") +
				"）；如需执行完立即退出，请加 --no-pause",
		}, action)
		return 2
	}

	authFile, lookedIn := findAuthFile()
	if authFile == "" || !fileExists(authFile) {
		report := "未找到 WorkBuddy 登录凭据。请先在本机登录 WorkBuddy 桌面端；" +
			"或设置环境变量 WORKBUDDY_AUTH_FILE 指向 workbuddy-desktop.info。"
		if override := os.Getenv("WORKBUDDY_AUTH_FILE"); override != "" {
			report = fmt.Sprintf("WORKBUDDY_AUTH_FILE 指向的文件不存在：%s", override)
		}
		emit(map[string]any{
			"result": "NO_AUTH", "report": report, "looked_in": lookedIn,
			"needs_attention": true,
		}, action)
		return 2
	}

	session, err := loadSessionRetry(authFile)
	if err != nil {
		if ae, ok := err.(*authError); ok {
			emit(ae.output(), action)
		} else {
			emit(map[string]any{"result": "ERROR", "report": err.Error()}, action)
		}
		return 2
	}

	if action == "doctor" {
		out, err := runDoctor(session)
		if err != nil {
			emit(authFailureOutput(err), action)
			return 1
		}
		emit(out, action)
		return 0
	}

	resolved, err := resolveSession(session)
	if err != nil {
		emit(authFailureOutput(err), action)
		return 1
	}
	headers, endpoint, err := buildHeaders(resolved)
	if err != nil {
		emit(authFailureOutput(err), action)
		return 1
	}

	switch action {
	case "auto", "silent":
		code, out, _ := runDaily(headers, endpoint)
		emit(out, action)
		return code
	case "silent-poll", "silent-growth":
		// 空跑默认不落盘；要看完整过程就设 WORKBUDDY_GROWTH_LOG_EMPTY=1。
		code, out, quiet := runDaily(headers, endpoint)
		out["trigger"] = "poll"
		if !quiet || envFlag("WORKBUDDY_GROWTH_LOG_EMPTY") {
			emit(out, action)
		}
		return code
	case "growth":
		code, out := runGrowth(headers, endpoint)
		out["needs_attention"] = code != 0
		emit(out, action)
		return code
	case "status", "all":
		if code := runStatusCommand(headers, endpoint, action); code != 0 || action != "all" {
			return code
		}
		return runClaimCommand(headers, endpoint, action)
	case "claim":
		return runClaimCommand(headers, endpoint, action)
	}
	return 0
}

func runStatusCommand(headers map[string]string, endpoint, action string) int {
	code, body := post(endpoint+"/v2/billing/meter/checkin-activity-status", headers, nil, true)
	if code == 401 || code == 403 {
		out := authFailure(code)
		out["step"] = "status"
		emit(out, action)
		return 1
	}
	ok := code >= 200 && code < 300
	emit(map[string]any{"step": "status", "http": code, "body": body, "needs_attention": !ok}, action)
	if !ok {
		return 1
	}
	return 0
}

func runClaimCommand(headers map[string]string, endpoint, action string) int {
	code, body := post(endpoint+"/v2/billing/meter/daily-checkin", headers, nil, true)
	if code == 401 || code == 403 {
		out := authFailure(code)
		out["step"] = "claim"
		emit(out, action)
		return 1
	}
	ok := (code >= 200 && code < 300) || (code == 400 && isAlreadyCheckedIn(body))
	emit(map[string]any{"step": "claim", "http": code, "body": body, "needs_attention": !ok}, action)
	if !ok {
		return 1
	}
	return 0
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
