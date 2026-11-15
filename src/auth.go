// auth.go — 凭据发现、会话校验、加密信封识别、请求头构造与客户端运行时探测。
//
// 与 Python 版一致：只读本机会话；明文直接可用，sym-v1 字段信封交给客户端运行时
// 在子进程里解密，token 只留在内存中，且输出前统一脱敏。
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultEndpoint     = "https://copilot.tencent.com"
	tokenLimit          = 32768
	authInputLimit      = 65536
	authOutputLimit     = 65536
	authHelperTimeout   = 10.0
	credentialReadLimit = 4 * 1024 * 1024
	deniedMax           = 4
)

var readRetryDelays = []float64{0, 1, 2, 3, 5, 8, 13}

var authReasons = map[string]string{
	"INVALID_FORMAT":       "登录凭据格式无效，请检查凭据来源和客户端版本",
	"UNSUPPORTED_ENVELOPE": "尚不支持此加密凭据格式，请更新脚本；重新登录不会改变加密格式",
	"RUNTIME_NOT_FOUND":    "未找到 WorkBuddy 客户端，请用 WORKBUDDY_EXE 指定其可执行文件",
	"INVALID_RUNTIME_PATH": "WORKBUDDY_EXE 不是可用的可执行文件，请核对路径",
	"RUNTIME_UNAVAILABLE":  "客户端运行时不支持所需的原生存储接口，请检查客户端和脚本版本",
	"KEY_MISMATCH":         "凭据与所选客户端的密钥不匹配，请用 WORKBUDDY_EXE 指定对应客户端",
	"DECRYPT_FAILED":       "加密凭据认证失败，请检查客户端版本及凭据是否完整",
	"HELPER_TIMEOUT":       "凭据处理超时，已停止子进程，请稍后重试",
	"HELPER_PROTOCOL":      "客户端凭据助手返回无效结果，请检查客户端和脚本版本",
	"CREDENTIAL_BUSY":      "凭据文件被 WorkBuddy 占用，重试多次仍读不到；这不是登录失效，无需重新登录，稍后会自动再试",
	"CREDENTIAL_DENIED":    "读取凭据被系统拒绝（访问被拒），重试无效；常见于在受限环境/自动化沙箱里运行，或安全软件拦截。建议改用系统计划任务运行",
}

var (
	validTokenRe = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)
	printableRe  = regexp.MustCompile(`^[\x21-\x7e]+$`) // 长度上限在 validateSession 里单独检查
	controlRe    = regexp.MustCompile(`[\s\x00-\x1f\x7f]`)
	keyIDRe      = regexp.MustCompile(`^[0-9a-f]{16}$`)
	electronRe   = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,64}$`)
	macPlistRe   = regexp.MustCompile(`(?s)<key>\s*CFBundleExecutable\s*</key>\s*<string>\s*([^<]*?)\s*</string>`)
)

type authError struct {
	reason string
	result string
	msg    string
}

func (e *authError) Error() string { return e.msg }

func newAuthError(reason, result string) *authError {
	if result == "" {
		result = "AUTH_ERROR"
	}
	msg, ok := authReasons[reason]
	if !ok {
		msg = "本地未找到有效登录会话，请先登录客户端"
	}
	return &authError{reason: reason, result: result, msg: msg}
}

func (e *authError) output() map[string]any {
	return map[string]any{
		"result": e.result, "reason": e.reason, "report": e.msg, "needs_attention": true,
	}
}

type badJSONError struct{ msg string }

func (e *badJSONError) Error() string { return e.msg }

type readFailedError struct{ msg string }

func (e *readFailedError) Error() string { return e.msg }

func validToken(token any) bool {
	s, ok := token.(string)
	return ok && len(s) > 0 && len(s) <= tokenLimit && validTokenRe.MatchString(s)
}

// missing 对应 Python 的 `value in (None, "")`，特意不用 truthy：数字 0 不该被当成缺失。
func missing(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok && s == "" {
		return true
	}
	return false
}

func validateSession(session any) error {
	m, ok := session.(map[string]any)
	if !ok {
		return newAuthError("INVALID_FORMAT", "")
	}
	for _, field := range []string{"auth", "account"} {
		v, exists := m[field]
		if !exists || v == nil {
			return newAuthError("MISSING_SESSION", "NO_SESSION")
		}
		if _, ok := v.(map[string]any); !ok {
			return newAuthError("INVALID_FORMAT", "")
		}
	}
	auth := m["auth"].(map[string]any)
	account := m["account"].(map[string]any)
	if missing(auth["accessToken"]) || missing(account["uid"]) {
		return newAuthError("MISSING_SESSION", "NO_SESSION")
	}
	for _, source := range []struct {
		values map[string]any
		fields []string
	}{
		{account, []string{"uid", "enterpriseId"}},
		{auth, []string{"domain"}},
	} {
		for _, field := range source.fields {
			value := source.values[field]
			if missing(value) {
				continue
			}
			s, ok := value.(string)
			if !ok || len(s) > 2048 || !printableRe.MatchString(s) {
				return newAuthError("INVALID_FORMAT", "")
			}
		}
	}
	endpoint := defaultEndpoint
	if v := auth["endpoint"]; !missing(v) {
		s, ok := v.(string)
		if !ok {
			return newAuthError("INVALID_FORMAT", "")
		}
		endpoint = s
	}
	if controlRe.MatchString(endpoint) {
		return newAuthError("INVALID_FORMAT", "")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return newAuthError("INVALID_FORMAT", "")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 0 || n > 65535 {
			return newAuthError("INVALID_FORMAT", "")
		}
	}
	return nil
}

func canonicalB64(s string) ([]byte, bool) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != s {
		return nil, false
	}
	return decoded, true
}

func jsonInteger(v any) (int64, bool) {
	var f float64
	switch x := v.(type) {
	case json.Number:
		// Python 里 `type(x) is int`：1.0 / 1e0 解析成 float，必须拒绝。
		if strings.ContainsAny(x.String(), ".eE") {
			return 0, false
		}
		parsed, err := x.Float64()
		if err != nil {
			return 0, false
		}
		f = parsed
	case int:
		f = float64(x)
	case float64:
		f = x
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f {
		return 0, false
	}
	return int64(f), true
}

// tokenFormat 只接受明文的 token 或已识别的 sym-v1 / suite 1 字段信封。
func tokenFormat(value any) (string, error) {
	if s, ok := value.(string); ok {
		if !validToken(s) {
			return "", newAuthError("INVALID_FORMAT", "")
		}
		return "plaintext", nil
	}
	m, ok := value.(map[string]any)
	if !ok {
		return "", newAuthError("INVALID_FORMAT", "")
	}
	if len(m) != 2 {
		return "", newAuthError("UNSUPPORTED_ENVELOPE", "")
	}
	flag, ok := m["$wbEncrypted"]
	if !ok {
		return "", newAuthError("UNSUPPORTED_ENVELOPE", "")
	}
	if _, exists := m["envelope"]; !exists {
		return "", newAuthError("UNSUPPORTED_ENVELOPE", "")
	}
	if n, ok := jsonInteger(flag); !ok || n != 1 {
		return "", newAuthError("UNSUPPORTED_ENVELOPE", "")
	}
	encoded, ok := m["envelope"].(string)
	if !ok || len(encoded) == 0 || len(encoded) > authInputLimit-1024 {
		return "", newAuthError("INVALID_FORMAT", "")
	}
	raw, ok := canonicalB64(encoded)
	if !ok || !utf8.Valid(raw) {
		return "", newAuthError("INVALID_FORMAT", "")
	}
	parsed, err := decodeJSON(raw)
	if err != nil {
		return "", newAuthError("INVALID_FORMAT", "")
	}
	env, ok := parsed.(map[string]any)
	if !ok {
		return "", newAuthError("INVALID_FORMAT", "")
	}
	suite, ok := jsonInteger(env["suite"])
	if !ok {
		return "", newAuthError("INVALID_FORMAT", "")
	}
	if suite != 1 {
		return "", newAuthError("UNSUPPORTED_ENVELOPE", "")
	}
	if len(env) != 5 {
		return "", newAuthError("INVALID_FORMAT", "")
	}
	keyID, ok := env["keyId"].(string)
	if !ok || !keyIDRe.MatchString(keyID) {
		return "", newAuthError("INVALID_FORMAT", "")
	}
	// 用切片而非 map 遍历：字段校验顺序确定，出错时定位更稳定。
	for _, field := range []struct {
		name   string
		length int
	}{{"nonce", 12}, {"authTag", 16}, {"ciphertext", -1}} {
		item, exists := env[field.name]
		s, ok := item.(string)
		if !exists || !ok {
			return "", newAuthError("INVALID_FORMAT", "")
		}
		decoded, ok := canonicalB64(s)
		if !ok || (field.length >= 0 && len(decoded) != field.length) {
			return "", newAuthError("INVALID_FORMAT", "")
		}
	}
	return "sym-v1", nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isExecutable(path string) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().Perm()&0111 != 0
}

func userHome() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "."
}

func expandHome(path string) string {
	if path == "~" {
		return userHome()
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(userHome(), path[2:])
	}
	return path
}

func absolute(path string) string {
	abs, err := filepath.Abs(expandHome(path))
	if err != nil {
		return filepath.Clean(expandHome(path))
	}
	return abs
}

func findAuthFile() (string, []string) {
	if override := os.Getenv("WORKBUDDY_AUTH_FILE"); override != "" {
		if fileExists(override) {
			return override, []string{override}
		}
		return "", []string{override}
	}
	home := userHome()
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		local = filepath.Join(home, "AppData", "Local")
	}
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".local", "share")
	}
	authParts := []string{"CodeBuddyExtension", "Data", "Public", "auth"}
	join := func(base string, name string) string {
		return filepath.Join(append([]string{base}, append(authParts, name)...)...)
	}
	candidates := []string{
		join(local, "workbuddy-desktop.info"),
		join(filepath.Join(home, "Library", "Application Support"), "workbuddy-desktop.info"),
		join(xdg, "Tencent-Cloud.coding-copilot.info"),
		join(filepath.Join(home, ".config"), "workbuddy-desktop.info"),
		filepath.Join(home, ".workbuddy", "auth", "workbuddy-desktop.info"),
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate, candidates
		}
	}
	return "", candidates
}

// loadSessionRetry 容忍客户端刷新 token 时对凭据文件的短暂独占。
func loadSessionRetry(path string) (any, error) {
	denied := 0
	for _, wait := range readRetryDelays {
		if wait > 0 {
			if budgetLeft() < wait+1 {
				break
			}
			time.Sleep(time.Duration(wait * float64(time.Second)))
		}
		data, err := readFileShared(path)
		if err == nil {
			if !utf8.Valid(data) {
				return nil, &badJSONError{msg: "登录凭据文件内容损坏（UnicodeDecodeError: 非法 UTF-8），请重新登录 WorkBuddy 桌面端"}
			}
			value, err := decodeJSON(data)
			if err != nil {
				return nil, &badJSONError{msg: fmt.Sprintf("登录凭据文件不是合法 JSON（%v），请重新登录 WorkBuddy 桌面端", err)}
			}
			return value, nil
		}
		code, isDenied := classifyReadErr(err)
		if code == 0 || code == -1 {
			return nil, &readFailedError{msg: fmt.Sprintf(
				"读取登录凭据失败（%s: %v）；若文件被占用或权限受限，稍后会自动重试，也可运行 doctor 排查",
				panicTypeName(err), err)}
		}
		if isDenied {
			denied++
			if denied >= deniedMax {
				return nil, newAuthError("CREDENTIAL_DENIED", "AUTH_BUSY")
			}
		}
	}
	return nil, newAuthError("CREDENTIAL_BUSY", "AUTH_BUSY")
}

func resolveSession(session any) (map[string]any, error) {
	if err := validateSession(session); err != nil {
		return nil, err
	}
	m := session.(map[string]any)
	auth := m["auth"].(map[string]any)
	token := auth["accessToken"]
	kind, err := tokenFormat(token)
	if err != nil {
		return nil, err
	}
	if kind != "plaintext" {
		exe, err := findWorkbuddyRuntime()
		if err != nil {
			return nil, err
		}
		reply, err := runAuthHelper(exe, map[string]any{"operation": "decrypt", "value": token})
		if err != nil {
			return nil, err
		}
		token = reply["accessToken"]
	}
	if s, ok := token.(string); ok {
		sensitiveValues = append(sensitiveValues, s)
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	patched := make(map[string]any, len(auth))
	for k, v := range auth {
		patched[k] = v
	}
	patched["accessToken"] = token
	result["auth"] = patched
	return result, nil
}

func runDoctor(session any) (map[string]any, error) {
	if err := validateSession(session); err != nil {
		return nil, err
	}
	auth := session.(map[string]any)["auth"].(map[string]any)
	kind, err := tokenFormat(auth["accessToken"])
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"result": "AUTH_READY", "credential_format": kind, "needs_attention": false,
		"report": "本地凭据格式有效；未验证服务端登录状态", "online_checked": false,
	}
	if kind == "sym-v1" {
		exe, err := findWorkbuddyRuntime()
		if err != nil {
			return nil, err
		}
		probe, err := runAuthHelper(exe, map[string]any{"operation": "probe"})
		if err != nil {
			return nil, err
		}
		out["runtime"] = exe
		out["electron_version"] = probe["electron"]
		out["report"] = "已识别加密凭据，运行时具备解密能力；未提取密钥、未验证解密或服务端登录状态"
	}
	return out, nil
}

func buildHeaders(session map[string]any) (map[string]string, string, error) {
	if err := validateSession(session); err != nil {
		return nil, "", err
	}
	auth := session["auth"].(map[string]any)
	account := session["account"].(map[string]any)
	token := auth["accessToken"]
	if !validToken(token) {
		return nil, "", newAuthError("INVALID_FORMAT", "")
	}
	uid := fmtAny(account["uid"])
	headers := map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer " + token.(string),
		"Content-Type":  "application/json",
		"X-User-Id":     uid,
		"User-Agent":    "WorkBuddy",
	}
	if truthy(account["enterpriseId"]) {
		enterprise := fmtAny(account["enterpriseId"])
		headers["X-Enterprise-Id"] = enterprise
		headers["X-Tenant-Id"] = enterprise
	}
	if truthy(auth["domain"]) {
		headers["X-Domain"] = fmtAny(auth["domain"])
	}
	endpoint := defaultEndpoint
	if truthy(auth["endpoint"]) {
		endpoint = fmtAny(auth["endpoint"])
	}
	endpoint = strings.TrimRight(endpoint, "/")
	return headers, endpoint, nil
}

func macRuntime(bundle string) string {
	data, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	match := macPlistRe.FindSubmatch(data)
	if match == nil {
		return ""
	}
	name := strings.TrimSpace(string(match[1]))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return ""
	}
	return filepath.Join(bundle, "Contents", "MacOS", name)
}

func findWorkbuddyRuntime() (string, error) {
	if override := os.Getenv("WORKBUDDY_EXE"); override != "" {
		path := absolute(override)
		if !fileExists(path) || !isExecutable(path) {
			return "", newAuthError("INVALID_RUNTIME_PATH", "")
		}
		return path, nil
	}
	home := userHome()
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		candidates = append(candidates, filepath.Join(local, "Programs", "WorkBuddy", "WorkBuddy.exe"))
		for _, key := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			if value := os.Getenv(key); value != "" {
				candidates = append(candidates, filepath.Join(value, "WorkBuddy", "WorkBuddy.exe"))
			}
		}
	case "darwin":
		for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
			if path := macRuntime(filepath.Join(root, "WorkBuddy.app")); path != "" {
				candidates = append(candidates, path)
			}
		}
	}
	for _, path := range candidates {
		if path != "" && fileExists(path) && isExecutable(path) {
			return absolute(path), nil
		}
	}
	if path := registryRuntime(); path != "" {
		return path, nil
	}
	return "", newAuthError("RUNTIME_NOT_FOUND", "")
}
