// pause.go — 交互式运行时「按任意键退出」。
//
// CLI 程序双击运行时控制台会一闪而过，看不到那一行结果；因此在输出之后
// 等一次按键。但无人值守场景绝不能等输入：定时任务、日志重定向、silent
// 命令、以及带 --no-pause 的运行一律直接退出，否则计划任务会挂死到超时。
package main

import (
	"fmt"
	"os"
	"strings"
)

// pauseMode 判断本次运行结束时是否需要等待按键。
//
// 优先级：命令行 --no-pause（最高）> silent 命令（硬保护）> WORKBUDDY_PAUSE 显式开关 > 交互控制台检测。
func pauseMode(action string) bool {
	if noPauseFlag {
		return false
	}
	if strings.HasPrefix(action, "silent") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WORKBUDDY_PAUSE"))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return interactiveConsole()
}

// interactiveConsole 判断 stdin/stdout 是否都连着控制台。
// stdout 被重定向到日志、或 stdin 来自管道/NUL 时返回 false。
func interactiveConsole() bool {
	in, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	out, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return in.Mode()&os.ModeCharDevice != 0 && out.Mode()&os.ModeCharDevice != 0
}

// pauseBeforeExit 提示写 stderr，保持 stdout 仍是纯净的一行 JSON，
// 这样把 stdout 重定向到文件或交给调度脚本解析时不会被污染。
func pauseBeforeExit(action string) {
	if !pauseMode(action) {
		return
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stderr, "按任意键退出...")
	waitAnyKey()
	fmt.Fprintln(os.Stderr)
}
