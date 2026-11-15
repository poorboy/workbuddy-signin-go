// emit.go — 唯一的结果输出通道：stdout 一行 JSON，silent 类命令额外落盘日志。
//
// 与 Python 版 emit 的行为一致：
//   - silent 前缀的命令不打印 stdout（无窗口运行时 stdout 可能是黑洞），只写日志；
//   - 其它命令打印 stdout；结果为 ERROR 时两条通道都走，避免失败记录整条丢失；
//   - 自定义日志路径不可写时回退到可执行文件同目录，绝不让输出函数抛异常。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// emit 保证不抛异常：序列化失败会退化成可打印文本，stdout 不可用也会退到日志。
func emit(out map[string]any, action string) {
	if out == nil {
		out = map[string]any{}
	}
	if _, ok := out["needs_attention"]; !ok {
		res, _ := out["result"].(string)
		out["needs_attention"] = attentionResults[res]
	}
	if configWarning != "" {
		out["config_warning"] = configWarning
	}
	payload := redactSensitive(dumpJSON(out))
	result, _ := out["result"].(string)
	if !strings.HasPrefix(action, "silent") {
		text := payload
		if wantPretty(action) {
			text = redactSensitive(renderPretty(out))
		}
		if _, err := fmt.Fprintln(os.Stdout, text); err == nil && result != "ERROR" {
			return
		}
	}
	writeSigninLog(payload)
}

// redactSensitive 把凭据、token 等敏感值从任何输出通道里抹掉。
func redactSensitive(s string) string {
	for _, secret := range sensitiveValues {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
	}
	return s
}

func dumpJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func defaultSigninLog() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "signin.log")
	}
	return "signin.log"
}

func writeSigninLog(payload string) {
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), payload)
	fallback := defaultSigninLog()
	paths := []string{}
	if custom := os.Getenv("WORKBUDDY_SIGNIN_LOG"); custom != "" {
		paths = append(paths, custom)
	}
	if len(paths) == 0 || paths[0] != fallback {
		paths = append(paths, fallback)
	}
	for _, path := range paths {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			continue
		}
		_, err = f.WriteString(line)
		_ = f.Close()
		if err == nil {
			return
		}
	}
}
