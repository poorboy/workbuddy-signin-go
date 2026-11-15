//go:build !windows

// auth_other.go — 非 Windows 平台：普通文件读取 + 对应的错误分类；注册表探测为空实现。
package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"syscall"
)

func readFileShared(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// classifyReadErr 返回 (重试码, 是否计入「访问被拒」计数)。
// -1 表示文件不存在，0 表示不值得重试；非 Windows 上权限错误只重试、不触发拒绝上限。
func classifyReadErr(err error) (int, bool) {
	if errors.Is(err, fs.ErrNotExist) {
		return -1, false
	}
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EAGAIN) {
		return 5, false
	}
	return 0, false
}

func registryRuntime() string { return "" }

func hideWindow(cmd *exec.Cmd) {}
