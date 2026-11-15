// helper.go — 内嵌 JS 凭据助手及其子进程管理。
//
// 固定代码只在 argv 中，加密字段走 stdin；密钥不出客户端子进程，stdout 只返回
// 受约束的 JSON。所有管道与截止时间都有上限，原始助手输出不进入日志。
package main

import (
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"
)

const authHelperJS = `
'use strict';
const crypto = require('crypto');
const inputLimit = 65536;
const failure = reason => { throw {reason}; };
const object = x => x !== null && typeof x === 'object' && !Array.isArray(x);
function base64(value, length) {
  if (typeof value !== 'string' || value.length > inputLimit) failure('INVALID_FORMAT');
  const bytes = Buffer.from(value, 'base64');
  if (bytes.toString('base64') !== value || (length !== undefined && bytes.length !== length))
    failure('INVALID_FORMAT');
  return bytes;
}
function utf8(bytes) {
  const text = bytes.toString('utf8');
  if (!Buffer.from(text, 'utf8').equals(bytes)) failure('INVALID_FORMAT');
  return text;
}
function decode(value) {
  if (!object(value) || Object.keys(value).sort().join(',') !== '$wbEncrypted,envelope' || value.$wbEncrypted !== 1)
    failure('UNSUPPORTED_ENVELOPE');
  let envelope;
  try { envelope = JSON.parse(utf8(base64(value.envelope))); }
  catch (e) { failure(e.reason || 'INVALID_FORMAT'); }
  if (!object(envelope) || !Number.isInteger(envelope.suite)) failure('INVALID_FORMAT');
  if (envelope.suite !== 1) failure('UNSUPPORTED_ENVELOPE');
  if (Object.keys(envelope).sort().join(',') !== 'authTag,ciphertext,keyId,nonce,suite' ||
      typeof envelope.keyId !== 'string' || !/^[0-9a-f]{16}$/.test(envelope.keyId)) failure('INVALID_FORMAT');
  return {keyId: envelope.keyId, nonce: base64(envelope.nonce, 12),
    tag: base64(envelope.authTag, 16), ciphertext: base64(envelope.ciphertext)};
}
function nativeStorage() {
  try {
    const storage = process._linkedBinding('electron_browser_workbuddy_storage');
    if (typeof storage.loggerGet !== 'function') failure('RUNTIME_UNAVAILABLE');
    return storage;
  } catch (_) { failure('RUNTIME_UNAVAILABLE'); }
}
function decrypt(envelope) {
  let payload;
  try { payload = JSON.parse(nativeStorage().loggerGet()); }
  catch (_) { failure('RUNTIME_UNAVAILABLE'); }
  let key;
  let plaintext;
  try {
    if (!object(payload) || payload.version !== 1) failure('RUNTIME_UNAVAILABLE');
    let secret;
    try { secret = base64(payload.atRestSecretKey, 32); }
    catch (_) { failure('RUNTIME_UNAVAILABLE'); }
    const empty = secret.every(b => b === 0);
    secret.fill(0);
    if (empty) failure('RUNTIME_UNAVAILABLE');
    key = crypto.createHash('sha256').update(payload.atRestSecretKey, 'utf8').digest();
    payload = null;
    if (crypto.createHash('sha256').update(key).digest('hex').slice(0, 16) !== envelope.keyId)
      failure('KEY_MISMATCH');
    const lp = s => {
      const bytes = Buffer.from(s, 'utf8');
      const length = Buffer.alloc(4); length.writeUInt32BE(bytes.length);
      return Buffer.concat([length, bytes]);
    };
    const aad = Buffer.concat([Buffer.from('WB-AAD\0', 'ascii'), Buffer.from([1]),
      lp('WBEV1'), lp('sym-v1'), Buffer.from([0, 0, 0, 1]), lp(envelope.keyId), Buffer.from([2, 0, 0])]);
    try {
      const cipher = crypto.createDecipheriv('aes-256-gcm', key, envelope.nonce, {authTagLength: 16});
      cipher.setAAD(aad); cipher.setAuthTag(envelope.tag);
      plaintext = Buffer.concat([cipher.update(envelope.ciphertext), cipher.final()]);
    } catch (_) { failure('DECRYPT_FAILED'); }
    const token = utf8(plaintext);
    if (!token.length || token.length > 32768 || !/^[A-Za-z0-9._~+\/-]+=*$/.test(token))
      failure('INVALID_FORMAT');
    return token;
  } finally {
    if (key) key.fill(0);
    if (plaintext) plaintext.fill(0);
  }
}
let chunks = [], size = 0;
function reply(value) {
  process.stdout.write(JSON.stringify({version: 1, ...value}), () => process.exit(value.ok ? 0 : 1));
}
process.stdin.on('data', chunk => {
  size += chunk.length;
  if (size > inputLimit) reply({ok: false, reason: 'INVALID_FORMAT'});
  else chunks.push(chunk);
});
process.stdin.on('error', () => reply({ok: false, reason: 'HELPER_PROTOCOL'}));
process.stdin.on('end', () => {
  try {
    const request = JSON.parse(utf8(Buffer.concat(chunks))); chunks = [];
    if (!object(request) || request.version !== 1) failure('HELPER_PROTOCOL');
    if (request.operation === 'probe') {
      nativeStorage();
      if (!crypto.getCiphers().includes('aes-256-gcm')) failure('RUNTIME_UNAVAILABLE');
      reply({ok: true, electron: process.versions.electron || 'unknown'});
    } else if (request.operation === 'decrypt') {
      reply({ok: true, accessToken: decrypt(decode(request.value))});
    } else failure('HELPER_PROTOCOL');
  } catch (e) {
    const reasons = ['INVALID_FORMAT','UNSUPPORTED_ENVELOPE','RUNTIME_UNAVAILABLE',
      'KEY_MISMATCH','DECRYPT_FAILED','HELPER_PROTOCOL'];
    reply({ok: false, reason: reasons.includes(e.reason) ? e.reason : 'HELPER_PROTOCOL'});
  }
});
`

func runAuthHelper(exe string, request map[string]any) (map[string]any, error) {
	payload := map[string]any{"version": 1}
	for k, v := range request {
		payload[k] = v
	}
	data, err := json.Marshal(payload)
	if err != nil || len(data) > authInputLimit {
		return nil, newAuthError("INVALID_FORMAT", "")
	}
	timeout := math.Min(authHelperTimeout, budgetLeft())
	if timeout <= 0 {
		return nil, newAuthError("HELPER_TIMEOUT", "")
	}

	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "NODE_") || strings.HasPrefix(upper, "ELECTRON_") ||
			strings.HasPrefix(upper, "WORKBUDDY_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "ELECTRON_RUN_AS_NODE=1")

	cmd := exec.Command(exe, "-e", authHelperJS)
	cmd.Env = env
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, newAuthError("RUNTIME_UNAVAILABLE", "")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, newAuthError("RUNTIME_UNAVAILABLE", "")
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, newAuthError("RUNTIME_UNAVAILABLE", "")
	}
	if err := cmd.Start(); err != nil {
		return nil, newAuthError("RUNTIME_UNAVAILABLE", "")
	}

	outC := make(chan []byte, 1)
	errC := make(chan []byte, 1)
	writeC := make(chan error, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(stdout, authOutputLimit+1))
		outC <- b
	}()
	go func() {
		b, _ := io.ReadAll(io.LimitReader(stderr, 8193))
		errC <- b
	}()
	go func() {
		_, e := stdin.Write(data)
		_ = stdin.Close()
		writeC <- e
	}()

	var outB, errB []byte
	var writeErr error
	allDone := make(chan struct{})
	go func() {
		outB = <-outC
		errB = <-errC
		writeErr = <-writeC
		close(allDone)
	}()
	select {
	case <-allDone:
	case <-time.After(time.Duration(timeout * float64(time.Second))):
		_ = cmd.Process.Kill()
		select {
		case <-allDone:
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Wait()
		return nil, newAuthError("HELPER_TIMEOUT", "")
	}

	waitErr := cmd.Wait()
	if len(outB) > authOutputLimit || len(errB) > 8192 || writeErr != nil {
		return nil, newAuthError("HELPER_PROTOCOL", "")
	}
	exitCode := 0
	if waitErr != nil {
		exitErr, ok := waitErr.(*exec.ExitError)
		if !ok {
			return nil, newAuthError("RUNTIME_UNAVAILABLE", "")
		}
		exitCode = exitErr.ExitCode()
	}

	parsed, err := decodeJSON(outB)
	if err != nil {
		return nil, newAuthError("HELPER_PROTOCOL", "")
	}
	reply, ok := parsed.(map[string]any)
	if !ok {
		return nil, newAuthError("HELPER_PROTOCOL", "")
	}
	if version, ok := jsonInteger(reply["version"]); !ok || version != 1 {
		return nil, newAuthError("HELPER_PROTOCOL", "")
	}
	if okValue, isBool := reply["ok"].(bool); isBool && !okValue && exitCode == 1 {
		reason, _ := reply["reason"].(string)
		if _, known := authReasons[reason]; known {
			return nil, newAuthError(reason, "")
		}
		return nil, newAuthError("HELPER_PROTOCOL", "")
	}
	okValue, isBool := reply["ok"].(bool)
	if !isBool || !okValue || exitCode != 0 {
		return nil, newAuthError("HELPER_PROTOCOL", "")
	}

	operation, _ := request["operation"].(string)
	switch operation {
	case "decrypt":
		if len(reply) != 3 || !validToken(reply["accessToken"]) {
			return nil, newAuthError("HELPER_PROTOCOL", "")
		}
		if _, exists := reply["accessToken"]; !exists {
			return nil, newAuthError("HELPER_PROTOCOL", "")
		}
	case "probe":
		electron, isStr := reply["electron"].(string)
		if len(reply) != 3 || !isStr || !electronRe.MatchString(electron) {
			return nil, newAuthError("HELPER_PROTOCOL", "")
		}
	default:
		return nil, newAuthError("HELPER_PROTOCOL", "")
	}
	return reply, nil
}
