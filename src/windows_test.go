//go:build windows

// windows_test.go — Windows 专属逻辑：共享读凭据、注册表路径解析、环境变量展开。
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileShared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.info")
	want := []byte(`{"auth":{"accessToken":"x"}}`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readFileShared(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("共享读取失败: %q %v", got, err)
	}
	_, err = os.ReadFile(filepath.Join(t.TempDir(), "missing.info"))
	if code, _ := classifyReadErr(err); code != -1 {
		t.Fatalf("文件不存在应分类为 -1，实际 %d", code)
	}
}

func TestRegistryPathParsing(t *testing.T) {
	cases := []struct {
		value string
		icon  bool
		want  string
	}{
		{`C:\Program Files\WorkBuddy`, false, `C:\Program Files\WorkBuddy\WorkBuddy.exe`},
		{`"D:\Program Files\WorkBuddy"`, false, `D:\Program Files\WorkBuddy\WorkBuddy.exe`},
		{`C:\Program Files\WorkBuddy\WorkBuddy.exe,0`, true, `C:\Program Files\WorkBuddy\WorkBuddy.exe`},
		{`"C:\Program Files\WorkBuddy\WorkBuddy.exe",0`, true, `C:\Program Files\WorkBuddy\WorkBuddy.exe`},
		{`C:\Program Files\Other\Other.exe`, true, ""},
		{`relative\WorkBuddy`, false, ""},
		{"C:\\WorkBuddy\x01\\WorkBuddy.exe", true, ""},
		{`C:\Program Files\WorkBuddy\readme.txt`, true, ""},
	}
	for _, c := range cases {
		if got := regRuntimePath(c.value, c.icon); got != c.want {
			t.Errorf("regRuntimePath(%q, icon=%v) = %q，期望 %q", c.value, c.icon, got, c.want)
		}
	}
}

func TestExpandEnvPercent(t *testing.T) {
	t.Setenv("WB_TEST_ROOT", `D:\Apps`)
	if got := expandEnvPercent(`%WB_TEST_ROOT%\WorkBuddy\WorkBuddy.exe`); got != `D:\Apps\WorkBuddy\WorkBuddy.exe` {
		t.Fatalf("环境变量展开失败: %q", got)
	}
	if got := expandEnvPercent(`%WB_UNDEFINED_XX%\WorkBuddy.exe`); got != `%WB_UNDEFINED_XX%\WorkBuddy.exe` {
		t.Fatalf("未定义变量应保持原样: %q", got)
	}
}

func TestRegistryDisplayName(t *testing.T) {
	for _, name := range []string{"WorkBuddy", "WorkBuddy 5.8.3", "workbuddy 5.7.3-beta.1"} {
		if !registryNameRe.MatchString(name) {
			t.Errorf("应匹配 DisplayName: %q", name)
		}
	}
	for _, name := range []string{"WorkBuddy Helper", "CodeBuddy", "WorkBuddy.exe"} {
		if registryNameRe.MatchString(name) {
			t.Errorf("不应匹配 DisplayName: %q", name)
		}
	}
}
