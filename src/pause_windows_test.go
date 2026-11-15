//go:build windows

// pause_windows_test.go — Windows 控制台按键等待的安全性。
package main

import (
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// stdin 不是控制台时 waitAnyKey 必须立即返回：定时任务、CI、重定向都靠这条兜底。
func TestWaitAnyKeyReturnsImmediatelyForNonConsole(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = old
		_ = f.Close()
	})
	done := make(chan struct{})
	go func() {
		waitAnyKey()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("非控制台 stdin 不该阻塞等待按键")
	}
}

// 往真实控制台输入队列注入一个 KEY_EVENT，验证 waitAnyKey 能读到它并立即返回。
// 这条覆盖「双击运行后按任意键退出」的主路径：INPUT_RECORD 布局或 API 用错时，
// 程序会一直卡在等待按键上。没有可用控制台的环境（CI）直接跳过。
func TestWaitAnyKeyConsumesInjectedKey(t *testing.T) {
	name, err := syscall.UTF16PtrFromString("CONIN$")
	if err != nil {
		t.Fatal(err)
	}
	h, err := syscall.CreateFile(name,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Skipf("当前进程没有可用的控制台输入：%v", err)
	}

	rec := inputRecord{EventType: keyEventType}
	rec.Key.KeyDown = 1
	rec.Key.VirtualKeyCode = 0x0D // VK_RETURN
	rec.Key.UnicodeChar = 0x0D
	var written uint32
	writeInput := kernel32.NewProc("WriteConsoleInputW")
	if r, _, _ := writeInput.Call(uintptr(h), uintptr(unsafe.Pointer(&rec)), 1,
		uintptr(unsafe.Pointer(&written))); r == 0 {
		t.Skip("无法向控制台注入输入事件")
	}

	old := os.Stdin
	os.Stdin = os.NewFile(uintptr(h), "CONIN$")
	t.Cleanup(func() { os.Stdin = old })

	done := make(chan struct{})
	go func() {
		waitAnyKey()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("注入的按键未被消费；双击运行时会卡在等待按键上")
	}
}
