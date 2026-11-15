//go:build windows

// pause_windows.go — 等待一次真实按键（不需要按回车）。
package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode    = kernel32.NewProc("GetConsoleMode")
	procReadConsoleInputW = kernel32.NewProc("ReadConsoleInputW")
)

const keyEventType = 0x0001

// 与 Windows KEY_EVENT_RECORD / INPUT_RECORD 的内存布局保持一致。
type keyEventRecord struct {
	KeyDown         int32
	RepeatCount     uint16
	VirtualKeyCode  uint16
	VirtualScanCode uint16
	UnicodeChar     uint16
	ControlKeyState uint32
}

type inputRecord struct {
	EventType uint16
	_         uint16
	Key       keyEventRecord
}

// waitAnyKey 直接读控制台输入队列，因此回车、空格、方向键、功能键都算「任意键」，
// 也不会把按键回显到窗口里。stdin 不是控制台（重定向、管道、NUL）时立即返回，
// 绝不阻塞无人值守的定时任务。
func waitAnyKey() {
	h := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return
	}
	for {
		var rec inputRecord
		var read uint32
		r, _, _ := procReadConsoleInputW.Call(
			uintptr(h), uintptr(unsafe.Pointer(&rec)), 1,
			uintptr(unsafe.Pointer(&read)), 0)
		if r == 0 || read == 0 {
			return
		}
		if rec.EventType == keyEventType && rec.Key.KeyDown != 0 {
			return
		}
	}
}
