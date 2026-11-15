// api.go — HTTP 请求、时间预算内的退避重试，以及 JSON 取值/数字/文案工具。
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	networkRetryDelays = []float64{5, 15, 30, 60, 90}
	serverRetryDelays  = []float64{3, 10}

	// 复用同一个 Client 才能复用 TLS 连接；Transport 留空，运行时取 http.DefaultTransport，
	// 单次超时交给 context，这样测试注入的模拟 Transport 依然生效。
	httpClient = &http.Client{}
)

// decodeJSON 与 Python json.loads 保持一致：数字保留原始字面量（UseNumber），
// 且拒绝尾随内容，避免服务端多写一段垃圾时仍被当成合法响应。
func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("响应含尾随内容")
	}
	return value, nil
}

func doRequest(url string, headers map[string]string, method string, payload any, timeout float64) (int, any) {
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return codeNoNetwork, map[string]any{"error": err.Error()}
		}
		reader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(timeout*float64(time.Second)))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return codeNoNetwork, map[string]any{"error": err.Error()}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return codeNoNetwork, map[string]any{"error": err.Error()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return codeNoNetwork, map[string]any{"error": err.Error()}
	}
	value, err := decodeJSON(data)
	if err != nil {
		return resp.StatusCode, map[string]any{"raw": truncateRunes(string(data), 500)}
	}
	return resp.StatusCode, value
}

// truncateRunes 对应 Python 的字符串切片：按字符而非字节截断，避免切断 UTF-8 汉字。
func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit])
}

// pyDumps 复刻 Python json.dumps(..., ensure_ascii=False, default=str) 的呈现风格，
// 只用于把「未识别的领取返回」拼进 report —— 那是一段会被人逐字读到的文本。
func pyDumps(v any) string {
	var b strings.Builder
	writePyJSON(&b, v)
	return b.String()
}

func writePyJSON(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case json.Number:
		b.WriteString(x.String())
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case string:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(x)
		b.WriteString(strings.TrimSuffix(buf.String(), "\n"))
	case []any:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writePyJSON(b, item)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writePyJSON(b, k)
			b.WriteString(": ")
			writePyJSON(b, x[k])
		}
		b.WriteByte('}')
	default:
		// Python 的 default=str 会把意外类型转成字符串再序列化。
		writePyJSON(b, fmtAny(v))
	}
}

// requestWithRetry 只重试网络不可达与 5xx；写操作调用方会传 retry=false。
func requestWithRetry(url string, headers map[string]string, method string, payload any, retry bool) (int, any) {
	if budgetLeft() <= 1 {
		return codeBudgetOut, map[string]any{"error": "已达本次运行时间预算，跳过剩余请求"}
	}
	code, body := doRequest(url, headers, method, payload, boundedTimeout())
	if !retry {
		return code, body
	}
	netUsed, srvUsed := 0, 0
	for {
		var delays []float64
		var used *int
		switch {
		case code == codeNoNetwork:
			delays, used = networkRetryDelays, &netUsed
		case code >= 500:
			delays, used = serverRetryDelays, &srvUsed
		default:
			return code, body
		}
		if *used >= len(delays) {
			return code, body
		}
		delay := delays[*used]
		*used = *used + 1
		if budgetLeft() <= delay+requestTimeout {
			return code, body
		}
		time.Sleep(time.Duration(delay * float64(time.Second)))
		code, body = doRequest(url, headers, method, payload, boundedTimeout())
	}
}

// post 默认不重试：抽奖/领奖等写操作若在服务端处理完成后才超时，重试会重复提交。
func post(url string, headers map[string]string, payload any, retry bool) (int, any) {
	return requestWithRetry(url, headers, http.MethodPost, payload, retry)
}

func get(url string, headers map[string]string) (int, any) {
	return requestWithRetry(url, headers, http.MethodGet, nil, true)
}

// dig 在可能被 data/result 包裹的响应里找字段，兼容信封结构。
func dig(obj any, key string) any {
	m, ok := obj.(map[string]any)
	if !ok {
		return nil
	}
	if v, ok := m[key]; ok && v != nil {
		return v
	}
	for _, k := range []string{"data", "result", "resp", "response"} {
		if inner, ok := m[k].(map[string]any); ok {
			if r := dig(inner, key); r != nil {
				return r
			}
		}
	}
	return nil
}

// truthy / orFirst 复刻 Python 的 or 语义（0、""、空容器都算 falsy）。
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err == nil && f != 0
	case float64:
		return x != 0
	case float32:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case []string:
		return len(x) > 0
	}
	return true
}

func orFirst(vals ...any) any {
	for _, v := range vals {
		if truthy(v) {
			return v
		}
	}
	return nil
}

func fmtAny(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return x.String()
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// strOr 对应 Python 的 `(x or "")` / `(x or "?")`。
func strOr(v any, def string) string {
	if !truthy(v) {
		return def
	}
	return fmtAny(v)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func asInt(v any, def int) int {
	f, ok := toFloat(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return def
	}
	return int(f)
}

// fmtCreditStr 复刻 Python fmt_credit 的 int(v)：只接受整数形态的值，
// "1.5"/"1e3" 这类数字串 int() 会抛 ValueError，必须原样回显而不是截断。
func fmtCreditStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return strconv.FormatInt(n, 10)
		}
		return x
	case bool:
		if x {
			return "1"
		}
		return "0"
	case json.Number:
		if f, err := x.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return strconv.FormatInt(int64(f), 10)
		}
		return x.String()
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return fmtAny(v)
		}
		return strconv.FormatInt(int64(x), 10)
	default:
		return fmtAny(v)
	}
}

// isIntNumber 对应 Python 的 `x == 10001` / `type(x) is int and x == 0`。
func isIntNumber(v any, val int) bool {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return err == nil && f == float64(val)
	case int:
		return x == val
	case int64:
		return int(x) == val
	case float64:
		return x == float64(val)
	}
	return false
}

// businessCodeZero 复刻 Python `type(code) is int and code == 0`：
// 字符串 "0"、浮点 0.0 / 1e0 都不算成功业务码。
func businessCodeZero(v any) bool {
	switch x := v.(type) {
	case json.Number:
		if strings.ContainsAny(x.String(), ".eE") {
			return false
		}
		n, err := strconv.ParseInt(x.String(), 10, 64)
		return err == nil && n == 0
	case int:
		return x == 0
	case int64:
		return x == 0
	}
	return false
}

func apiSuccess(code int, body any) bool {
	if code < 200 || code >= 300 {
		return false
	}
	if m, ok := body.(map[string]any); ok {
		if c, exists := m["code"]; exists && c != nil {
			return businessCodeZero(c)
		}
	}
	return true
}

func isHardFailure(code int) bool {
	return code >= 500 || code == codeNoNetwork || code == codeBudgetOut
}

func httpLabel(code int) string {
	switch code {
	case codeNoNetwork:
		return "网络不可达"
	case codeBudgetOut:
		return "时间预算耗尽"
	default:
		return fmt.Sprintf("HTTP %d", code)
	}
}

func firstInt(body any, key string, fallback any) int {
	if v := dig(body, key); v != nil {
		return asInt(v, 0)
	}
	return asInt(fallback, 0)
}

func fmtETA(arriveAt, serverNow any) string {
	a, ok1 := toFloat(arriveAt)
	n, ok2 := toFloat(serverNow)
	if !ok1 || !ok2 {
		return ""
	}
	left := a - n
	if math.IsNaN(left) || math.IsInf(left, 0) {
		return ""
	}
	if left <= 0 {
		return "，已到达待领取"
	}
	// Python round() 用「四舍六入五成双」，math.Round 是「四舍五入」，
	// 在 2.5 / 4.5 这类边界上会差一分钟，必须用 RoundToEven。
	minutes := int(math.RoundToEven(left / 60.0))
	if minutes < 60 {
		if minutes < 1 {
			minutes = 1
		}
		return fmt.Sprintf("，约 %d 分钟后回", minutes)
	}
	return fmt.Sprintf("，约 %.1f 小时后回", left/3600.0)
}

// clientToken 对应官方前端的 randomUUID，活动接口靠它做幂等去重。
func clientToken(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := fmt.Sprintf("%x", b)
	return fmt.Sprintf("%s-%s-%s-%s-%s-%s", prefix,
		h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func isNoChance(msg any) bool {
	m := strings.ToLower(strOr(msg, ""))
	if m == "" {
		return false
	}
	if strings.Contains(m, "insufficient") || strings.Contains(m, "not enough") {
		return strings.Contains(m, "chance") || strings.Contains(m, "balance")
	}
	return strings.Contains(m, "no chance")
}

func isUnknownTier(code int, body any) bool {
	if code != 400 {
		return false
	}
	m := strings.ToLower(strOr(dig(body, "msg"), ""))
	return strings.Contains(m, "tier") &&
		(strings.Contains(m, "unknown") || strings.Contains(m, "unsupported") || strings.Contains(m, "invalid"))
}

func isTierLocked(code int, body any) bool {
	if code != 403 {
		return false
	}
	m := strOr(dig(body, "msg"), "")
	return strings.Contains(m, "天数不足") || strings.Contains(m, "不足")
}

// isBusinessForbidden 识别 403 里的常态业务拒绝。
//
// 最典型的是 POST /buddy/travel/depart 返回 "no active buddy"：账号还没有
// 激活的 Buddy，派不出去。它跟登录失效、账号权限都无关，若按鉴权失败处理
// 会中止整轮成长中心，后面的任务领奖/补登/兑换/盲盒全部被跳过，还会把
// 已经成功的签到标成 needs_attention。
func isBusinessForbidden(code int, body any) bool {
	if code != 403 {
		return false
	}
	msg := strings.ToLower(strOr(dig(body, "msg"), ""))
	return strings.Contains(msg, "no active buddy") || isTierLocked(code, body)
}

func forbiddenMsg(body any) string {
	if msg := strOr(dig(body, "msg"), ""); msg != "" {
		return msg
	}
	return "活动条件未满足"
}
