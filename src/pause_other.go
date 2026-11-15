//go:build !windows

// pause_other.go — 非 Windows 平台的按键等待兜底。
//
// 标准库没有跨平台的控制台 raw mode；读一行即可（按回车退出）。
// stdin 已关闭或是管道时 ReadString 立即返回 EOF，不会挂住自动化任务。
package main

import (
	"bufio"
	"os"
)

func waitAnyKey() {
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
