// pretty.go — 面向人的终端输出：把一行 JSON 结果渲染成中文分块文本。
//
// 是否美化只看输出目标：stdout 连着控制台时默认美化，管道 / 重定向时仍然
// 输出单行 JSON；也可以用 --pretty / --json（或 WORKBUDDY_OUTPUT）强制指定。
// silent 类命令不写 stdout，日志始终是单行 JSON，因此自动化解析不受影响。
package main

import (
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

type prettyField struct {
	key   string
	label string
}

// prettyFields 决定美化输出的字段顺序与中文标签；result / report 单独处理。
var prettyFields = []prettyField{
	{"step", "步骤"},
	{"http", "HTTP 状态"},
	{"credit", "本次积分"},
	{"today_credit", "今日积分"},
	{"streak_days", "连续签到"},
	{"total_credits", "累计积分"},
	{"is_streak_day", "连签日"},
	{"next_streak_day", "下次连签"},
	{"active", "活动开启"},
	{"growth", "成长中心"},
	{"growth_result", "成长结果"},
	{"trigger", "触发方式"},
	{"config_warning", "配置警告"},
	{"looked_in", "查找路径"},
	{"status_body", "响应内容"},
	{"body", "响应内容"},
	{"error", "错误详情"},
}

// prettyTitles 把结果码翻译成一句话，缺少映射时回落到原始结果码。
var prettyTitles = map[string]string{
	"OK":            "执行完成",
	"CLAIMED":       "签到成功",
	"ALREADY":       "今日已签到",
	"INACTIVE":      "签到活动未开启",
	"GROWTH":        "成长中心",
	"ERROR":         "执行失败",
	"NETWORK":       "网络不可达",
	"TIMEOUT":       "超出时间预算",
	"NO_AUTH":       "未找到登录凭据",
	"NO_SESSION":    "登录状态无效",
	"AUTH_ERROR":    "凭据解析失败",
	"AUTH_REJECTED": "认证被拒绝",
	"AUTH_BUSY":     "凭据被占用",
	"FORBIDDEN":     "操作被拒绝",
	"UNKNOWN":       "未识别结果",
}

// wantPretty 判断本次 stdout 是否走美化格式。
//
// 优先级：--json > --pretty > WORKBUDDY_OUTPUT > 控制台自动检测；
// silent 类命令永不写 stdout，加什么开关都一样。
func wantPretty(action string) bool {
	if strings.HasPrefix(action, "silent") {
		return false
	}
	if jsonFlag {
		return false
	}
	if prettyFlag {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WORKBUDDY_OUTPUT"))) {
	case "pretty", "text", "human":
		return true
	case "json", "compact", "raw", "machine":
		return false
	}
	return stdoutIsTerminal()
}

// stdoutIsTerminal 只关心 stdout：stdin 来自管道不影响“给人看”的输出。
func stdoutIsTerminal() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// renderPretty 生成一个结果块；末尾自带换行，连续两次 emit（如 all 命令）
// 之间会自然空一行。
func renderPretty(out map[string]any) string {
	result := strings.TrimSpace(fmtAny(out["result"]))
	report := strings.TrimSpace(fmtAny(out["report"]))
	if report == "None" {
		report = ""
	}

	mark := "[OK]"
	if attentionResults[result] {
		mark = "[!!]"
	}

	head := report
	if head == "" {
		if title, ok := prettyTitles[result]; ok {
			head = title
		} else if result != "" {
			head = result
		} else {
			head = "执行完成"
		}
	}
	line := mark + " " + head
	if result != "" && result != "OK" {
		line += "  ·  " + result
	}

	rows := prettyRows(out)
	var b strings.Builder
	b.WriteString(line)
	b.WriteString("\n")
	if len(rows) > 0 {
		b.WriteString("\n")
		width := 0
		for _, row := range rows {
			if n := utf8.RuneCountInString(row[0]); n > width {
				width = n
			}
		}
		for _, row := range rows {
			writePrettyRow(&b, row[0], row[1], width)
		}
	}
	return b.String()
}

// prettyRows 收集要展示的键值对：已知字段按固定顺序，未知字段按字典序追加。
func prettyRows(out map[string]any) [][2]string {
	seen := map[string]bool{
		"result": true, "report": true,
	}
	rows := make([][2]string, 0, len(out))

	for _, field := range prettyFields {
		seen[field.key] = true
		if field.key == "growth_result" && strings.TrimSpace(fmtAny(out["growth"])) != "" {
			// growth 文本已经说明结果时，不再重复英文结果码。
			continue
		}
		value, ok := prettyFieldValue(field.key, out[field.key])
		if ok {
			rows = append(rows, [2]string{field.label, value})
		}
	}

	// needs_attention 不单独占一行，只在需要处理时提示。
	if v, ok := out["needs_attention"].(bool); ok && v {
		rows = append(rows, [2]string{"需要处理", "是"})
	}

	extra := make([]string, 0, len(out))
	for key := range out {
		if seen[key] || key == "needs_attention" {
			continue
		}
		extra = append(extra, key)
	}
	sort.Strings(extra)
	for _, key := range extra {
		value, ok := prettyFieldValue(key, out[key])
		if ok {
			rows = append(rows, [2]string{key, value})
		}
	}
	return rows
}

// prettyFieldValue 把单个字段转成展示文本；返回 false 表示该字段没有信息量。
func prettyFieldValue(key string, v any) (string, bool) {
	if v == nil {
		return "", false
	}
	if !truthy(v) {
		// 数字 0 只在连签相关字段里保留，其余视为无信息。
		switch key {
		case "next_streak_day":
			return "", false
		case "is_streak_day", "active":
			return "否", true
		}
		if s, ok := v.(string); !ok || s == "" {
			return "", false
		}
	}

	switch key {
	case "streak_days":
		return fmtAny(v) + " 天", true
	case "is_streak_day", "active":
		if b, ok := v.(bool); ok {
			if b {
				return "是", true
			}
			return "否", true
		}
		return fmtAny(v), true
	case "trigger":
		if s, ok := v.(string); ok && s == "poll" {
			return "轮询", true
		}
		return prettyScalar(v), true
	case "step":
		switch s, _ := v.(string); s {
		case "status":
			return "状态查询", true
		case "claim":
			return "领取", true
		}
		return prettyScalar(v), true
	case "looked_in":
		return prettyList(v), true
	}
	return prettyScalar(v), true
}

func prettyScalar(v any) string {
	switch v.(type) {
	case map[string]any, []any, []string:
		return dumpJSON(v)
	}
	return fmtAny(v)
}

func prettyList(v any) string {
	switch x := v.(type) {
	case []string:
		if len(x) == 0 {
			return ""
		}
		return strings.Join(x, "\n")
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			parts = append(parts, prettyScalar(item))
		}
		return strings.Join(parts, "\n")
	}
	return prettyScalar(v)
}

// writePrettyRow 对齐标签列；值里带换行时（如多个凭据路径）保持缩进。
func writePrettyRow(b *strings.Builder, label, value string, width int) {
	pad := width - utf8.RuneCountInString(label)
	if pad < 0 {
		pad = 0
	}
	lines := strings.Split(value, "\n")
	b.WriteString("  ")
	b.WriteString(label)
	b.WriteString(strings.Repeat(" ", pad))
	b.WriteString("  ")
	b.WriteString(lines[0])
	b.WriteString("\n")
	for _, line := range lines[1:] {
		b.WriteString("  ")
		b.WriteString(strings.Repeat(" ", width))
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
}
