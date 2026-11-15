// pause_test.go — 「按任意键退出」的开关与无人值守安全回归测试。
package main

import (
	"io"
	"os"
	"testing"
	"time"
)

func TestPauseMode(t *testing.T) {
	cases := []struct {
		env    string
		action string
		want   bool
	}{
		// silent 是硬保护：即便显式强制也不暂停，否则计划任务会挂死。
		{"1", "silent", false},
		{"1", "silent-poll", false},
		{"1", "silent-growth", false},
		{"true", "silent-growth", false},
		{"0", "auto", false},
		{"off", "doctor", false},
		{"FALSE", "growth", false},
		{"1", "auto", true},
		{"yes", "growth", true},
		{"ON", "doctor", true},
	}
	for _, c := range cases {
		setEnv(t, "WORKBUDDY_PAUSE", c.env)
		if got := pauseMode(c.action); got != c.want {
			t.Errorf("WORKBUDDY_PAUSE=%q action=%q: pauseMode=%v，期望 %v",
				c.env, c.action, got, c.want)
		}
	}
}

func TestPauseModeUnsetFollowsConsole(t *testing.T) {
	for _, value := range []string{"", "  ", "maybe"} {
		setEnv(t, "WORKBUDDY_PAUSE", value)
		if got := pauseMode("auto"); got != interactiveConsole() {
			t.Errorf("WORKBUDDY_PAUSE=%q 应回落到交互检测，实际 %v", value, got)
		}
	}
}

// stdout 重定向到文件时绝不能等待输入。
func TestInteractiveConsoleFalseWhenRedirected(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "redirect-*.log")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = f
	t.Cleanup(func() {
		os.Stdout = old
		_ = f.Close()
	})
	if interactiveConsole() {
		t.Fatal("stdout 重定向到文件时不该被当成交互控制台")
	}
	setEnv(t, "WORKBUDDY_PAUSE", "")
	if pauseMode("auto") {
		t.Fatal("stdout 重定向时不应暂停")
	}
}

// 暂停提示必须走 stderr，stdout 保持纯一行 JSON；非交互时不产生任何输出。
func TestPauseBeforeExitSilentWritesNothing(t *testing.T) {
	setEnv(t, "WORKBUDDY_PAUSE", "")
	oldErr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = oldErr })

	pauseBeforeExit("silent")
	_ = w.Close()
	data, _ := io.ReadAll(r)
	_ = r.Close()
	if len(data) != 0 {
		t.Fatalf("silent 命令不应输出暂停提示，实际 %q", data)
	}
}

// 兜底：非交互环境下 pauseBeforeExit 必须立刻返回，不能阻塞自动化任务。
func TestPauseBeforeExitNeverBlocksWhenRedirected(t *testing.T) {
	setEnv(t, "WORKBUDDY_PAUSE", "0")
	done := make(chan struct{})
	go func() {
		pauseBeforeExit("auto")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("非交互运行被暂停逻辑阻塞")
	}
}

// 命令行 --no-pause 的解析：可写在命令前后，也接受 nopause / nopase 及常见误拼。
func TestParseArgsNoPause(t *testing.T) {
	cases := []struct {
		args    []string
		action  string
		noPause bool
	}{
		{nil, "auto", false},
		{[]string{"silent"}, "silent", false},
		{[]string{"nopause"}, "auto", true},
		{[]string{"nopase"}, "auto", true},
		{[]string{"nppause"}, "auto", true},
		{[]string{"npause"}, "auto", true},
		{[]string{"NO-PAUSE"}, "auto", true},
		{[]string{"no_pause"}, "auto", true},
		{[]string{"--no-pause"}, "auto", true},
		{[]string{"auto", "--nopause"}, "auto", true},
		{[]string{"--no-pause", "growth"}, "growth", true},
		{[]string{"growth", "-no-pause"}, "growth", true},
		{[]string{"status", "all"}, "status", false},
		{[]string{"nope"}, "nope", false},
	}
	for _, c := range cases {
		noPauseFlag = false
		got := parseArgs(c.args)
		if got != c.action || noPauseFlag != c.noPause {
			t.Errorf("parseArgs(%q) = (%q, %v)，期望 (%q, %v)",
				c.args, got, noPauseFlag, c.action, c.noPause)
		}
	}
	noPauseFlag = false
}

// --no-pause 必须压过 WORKBUDDY_PAUSE=1，否则计划任务仍会等待按键。
func TestNoPauseOverridesEnv(t *testing.T) {
	setEnv(t, "WORKBUDDY_PAUSE", "1")
	noPauseFlag = true
	t.Cleanup(func() { noPauseFlag = false })
	if pauseMode("auto") {
		t.Fatal("--no-pause 应覆盖 WORKBUDDY_PAUSE=1")
	}
}
