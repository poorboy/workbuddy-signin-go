//go:build windows

// auth_windows.go — Windows 专属：共享读凭据、错误码分类、注册表发现客户端、隐藏子进程窗口。
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var (
	registryControlRe = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	registryIconRe    = regexp.MustCompile(`,\s*-?\d+$`)
	registryNameRe    = regexp.MustCompile(`(?i)^WorkBuddy(?:\s+\d+(?:\.\d+)*(?:[-+][0-9A-Za-z.-]+)?)?$`)
	registryEnvRe     = regexp.MustCompile(`%([^%]+)%`)
)

// readFileShared 用 CreateFileW 打开共享模式全开的句柄，容忍客户端正在写凭据。
func readFileShared(path string) ([]byte, error) {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := syscall.CreateFile(ptr, syscall.GENERIC_READ,
		0x00000001|0x00000002|0x00000004, nil, syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer syscall.CloseHandle(handle)

	var out []byte
	buf := make([]byte, 65536)
	for len(out) < credentialReadLimit {
		var got uint32
		if err := syscall.ReadFile(handle, buf, &got, nil); err != nil {
			return nil, &os.PathError{Op: "read", Path: path, Err: err}
		}
		if got == 0 {
			break
		}
		out = append(out, buf[:got]...)
	}
	return out, nil
}

// classifyReadErr 返回 (win32 码, 是否计入「访问被拒」计数)。
// -1 表示文件/路径不存在，0 表示不值得重试的其它错误。
func classifyReadErr(err error) (int, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return 0, false
	}
	switch uintptr(errno) {
	case 2, 3:
		return -1, false
	case 5:
		return 5, true
	case 32, 33:
		return int(errno), false
	}
	return 0, false
}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// expandEnvPercent 只替换确实存在的 %VAR%，未定义的变量保持原样（与 Win32 API 一致）。
func expandEnvPercent(value string) string {
	return registryEnvRe.ReplaceAllStringFunc(value, func(match string) string {
		name := match[1 : len(match)-1]
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return match
	})
}

func regText(key syscall.Handle, field string) string {
	name, err := syscall.UTF16PtrFromString(field)
	if err != nil {
		return ""
	}
	var kind, size uint32
	if err := syscall.RegQueryValueEx(key, name, nil, &kind, nil, &size); err != nil || size == 0 {
		return ""
	}
	if kind != 1 && kind != 2 { // REG_SZ / REG_EXPAND_SZ
		return ""
	}
	buf := make([]byte, size)
	if err := syscall.RegQueryValueEx(key, name, nil, &kind, &buf[0], &size); err != nil {
		return ""
	}
	// 某些条目不带结尾 NUL，按实际长度逐字读取，避免越界。
	u16 := make([]uint16, 0, size/2)
	for i := 0; i+1 < int(size); i += 2 {
		u16 = append(u16, uint16(buf[i])|uint16(buf[i+1])<<8)
	}
	value := syscall.UTF16ToString(u16)
	if kind == 2 {
		value = expandEnvPercent(value)
	}
	return value
}

// regRuntimePath 只解析安装目录或 DisplayIcon，绝不解释卸载命令。
func regRuntimePath(value string, icon bool) string {
	if value == "" || registryControlRe.MatchString(value) {
		return ""
	}
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, `"`) {
		suffix := ""
		if icon {
			suffix = `(?:\s*,\s*-?\d+)?`
		}
		match := regexp.MustCompile(`^"([^"]+)"` + suffix + `$`).FindStringSubmatch(value)
		if match == nil {
			return ""
		}
		value = match[1]
	} else if strings.Contains(value, `"`) {
		return ""
	} else if icon {
		value = strings.TrimSpace(registryIconRe.ReplaceAllString(value, ""))
	}
	if filepath.VolumeName(value) == "" || !filepath.IsAbs(value) {
		return ""
	}
	if !icon {
		value = filepath.Join(value, "WorkBuddy.exe")
	}
	value = filepath.Clean(value)
	if !strings.EqualFold(filepath.Base(value), "workbuddy.exe") {
		return ""
	}
	return value
}

// registryRuntime 按 HKCU/HKLM × 64/32 视图的顺序查找已登记的客户端。
func registryRuntime() string {
	subkey, err := syscall.UTF16PtrFromString(`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`)
	if err != nil {
		return ""
	}
	seen := map[string]bool{}
	for _, hive := range []syscall.Handle{syscall.HKEY_CURRENT_USER, syscall.HKEY_LOCAL_MACHINE} {
		for _, view := range []uint32{0x0100, 0x0200} {
			var root syscall.Handle
			if err := syscall.RegOpenKeyEx(hive, subkey, 0, 0x20019|view, &root); err != nil {
				continue
			}
			found := ""
			for index := uint32(0); ; index++ {
				var nameBuf [256]uint16
				nameLen := uint32(len(nameBuf))
				if err := syscall.RegEnumKeyEx(root, index, &nameBuf[0], &nameLen,
					nil, nil, nil, nil); err != nil {
					break
				}
				name := syscall.UTF16ToString(nameBuf[:nameLen])
				namePtr, err := syscall.UTF16PtrFromString(name)
				if err != nil {
					continue
				}
				var entry syscall.Handle
				if err := syscall.RegOpenKeyEx(root, namePtr, 0, 0x20019|view, &entry); err != nil {
					continue
				}
				display := regText(entry, "DisplayName")
				if display != "" && registryNameRe.MatchString(strings.TrimSpace(display)) {
					for _, field := range []string{"InstallLocation", "DisplayIcon"} {
						path := regRuntimePath(regText(entry, field), field == "DisplayIcon")
						if path == "" {
							continue
						}
						norm := strings.ToLower(path)
						if seen[norm] {
							continue
						}
						seen[norm] = true
						if fileExists(path) {
							found = path
							break
						}
					}
				}
				_ = syscall.RegCloseKey(entry)
				if found != "" {
					break
				}
			}
			_ = syscall.RegCloseKey(root)
			if found != "" {
				return found
			}
		}
	}
	return ""
}
