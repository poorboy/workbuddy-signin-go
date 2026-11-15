# workbuddy-signin

WorkBuddy 每日签到与成长中心的**无 UI 命令行工具**，纯 Go 标准库实现（Python 版的功能等价移植）。

读取本机 WorkBuddy 桌面端已登录的凭据，自动完成签到领取，并按需执行成长中心里的旅行、任务、补登、连登兑换、盲盒等操作，最后输出一行 JSON 结果。

> 非官方工具，与腾讯 / WorkBuddy 官方无关。请遵守服务条款，风险自负。

## 功能

- **每日签到**：查询签到状态，未签才领取，已签则直接汇报今日收益与连签天数。
- **成长中心**：旅行礼物、任务接单与领奖、补登卡补登、连登兑换、盲盒抽奖、Buddy 能量盲盒。
- **稳健性**：所有网络请求受统一时间预算约束；读操作按退避重试，写操作不自动重试以避免重复提交；每个成长模块独立守护，单段异常不会吞掉已领取的奖励。
- **无人值守友好**：`silent*` 命令不写 stdout，只落盘日志；非交互环境自动跳过「按任意键退出」。
- **凭据安全**：只读本机会话文件；加密凭据交给客户端运行时在子进程中解密，密钥不出客户端进程；输出前统一脱敏。
- **零依赖**：仅使用 Go 标准库，`CGO_ENABLED=0` 可静态编译。

## 命令

```text
workbuddy-signin.exe [command]
```

不给参数时默认为 `auto`。

| 命令 | 说明 |
| --- | --- |
| `auto` | 签到 + 成长中心，结果打印到 stdout（默认） |
| `silent` | 同 `auto`，但不打印 stdout，只写日志 |
| `growth` | 只跑成长中心 |
| `silent-poll` | 轮询用的 `silent`，时间预算更短；空跑默认不落盘 |
| `silent-growth` | 轮询用的 `silent`，行为同 `silent-poll` |
| `status` | 只查询签到活动状态，不领取 |
| `claim` | 只领取每日签到 |
| `all` | 先 `status` 再 `claim` |
| `doctor` | 本地凭据自检（格式、加密类型、客户端运行时能力），**不联网** |

## 快速开始

### 前置条件

- 已安装并登录 WorkBuddy 桌面端（凭据由客户端写入本机）。
- 构建需要 Go 1.21 或更高版本。

### 构建

```bat
cd src
build.bat          REM 精简构建 + UPX 压缩（找不到 UPX 时保留精简版）
build.bat test     REM 先 go fmt / vet / test，再构建
build.bat noupx    REM 只做精简构建，输出 workbuddy-signin-unpacked.exe
```

构建脚本会依次在 `PATH`、常见安装目录、`tools\upx.exe` 中查找 UPX；也可用 `WORKBUDDY_UPX` 指定完整路径。产物默认写入 `src\workbuddy-signin.exe`。

不使用脚本时：

```bat
cd src
set CGO_ENABLED=0
go build -trimpath -buildvcs=false -ldflags="-s -w" -o workbuddy-signin.exe .
```

### 运行

```bat
workbuddy-signin.exe            REM 签到 + 成长中心，打印一行 JSON
workbuddy-signin.exe doctor     REM 先确认凭据能被正确读取
workbuddy-signin.exe silent     REM 定时任务用：只写日志
```

## 输出与日志

- 非 `silent*` 命令把**一行 JSON** 写到 stdout，便于调度脚本直接解析；提示信息只写 stderr。
- `silent*` 命令不写 stdout（无窗口运行时 stdout 可能是黑洞），结果直接进日志。
- 任何命令的结果为 `ERROR` 时，即使已经打印到 stdout，也会**额外落盘**一份日志，避免失败记录丢失。
- 日志默认写在可执行文件同目录的 `signin.log`，可用 `WORKBUDDY_SIGNIN_LOG` 改路径；自定义路径不可写时自动回退到可执行文件同目录。

示例：

```json
{"result":"CLAIMED","report":"成功领取 10 积分（连续 3 天，累计 120 积分）","credit":10,"streak_days":3,"total_credits":120,"growth":"...","growth_result":"GROWTH","needs_attention":false}
```

`needs_attention=true` 表示需要人工看一眼（失败、认证异常、网络不可达等）。

## 环境变量

| 变量 | 说明 |
| --- | --- |
| `WORKBUDDY_AUTH_FILE` | 指定凭据文件路径，覆盖自动发现；文件不存在会直接报 `NO_AUTH` |
| `WORKBUDDY_EXE` | 指定 WorkBuddy 客户端可执行文件，用于解密 `sym-v1` 加密凭据 |
| `WORKBUDDY_BUDGET_SECONDS` | 本次运行的时间预算（秒）。默认 420、上限 540；轮询类命令默认 180、上限 240。超出上限会被夹紧并在输出中带 `config_warning` |
| `WORKBUDDY_SIGNIN_LOG` | 自定义日志路径 |
| `WORKBUDDY_PAUSE` | `1/true/yes/on` 强制结束前等待按键；`0/false/no/off` 禁止等待。未设置时按是否为交互式控制台自动判断 |
| `WORKBUDDY_GROWTH_LOG_EMPTY` | 设为 `1` 时，`silent-poll` / `silent-growth` 的空跑也写日志 |
| `WORKBUDDY_UPX` | 构建时 UPX 可执行文件完整路径 |
| `GOOS` / `GOARCH` | 构建时交叉编译目标 |

## 凭据发现顺序

未设置 `WORKBUDDY_AUTH_FILE` 时，依次查找：

1. Windows：`%LOCALAPPDATA%\CodeBuddyExtension\Data\Public\auth\workbuddy-desktop.info`
2. macOS：`~/Library/Application Support/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info`
3. Linux：`$XDG_DATA_HOME/Tencent-Cloud.coding-copilot.info`（默认 `~/.local/share`）
4. `~/.config/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info`
5. `~/.workbuddy/auth/workbuddy-desktop.info`

Windows 上若凭据是加密格式，会按常见安装目录与注册表卸载信息查找 `WorkBuddy.exe`；macOS 会检查 `/Applications/WorkBuddy.app` 等位置。

## 退出码

| 码 | 含义 |
| --- | --- |
| `0` | 成功（含「今日已签过」「活动未开启」等正常状态） |
| `1` | 有需要关注的问题：网络、超时、认证被拒、业务失败、成长中心部分失败 |
| `2` | 配置或凭据问题、未知命令、未捕获异常 |

## 结果值

`result` 字段常见取值：

| 值 | 含义 |
| --- | --- |
| `CLAIMED` | 本次签到领取成功 |
| `ALREADY` | 今日已签过 |
| `INACTIVE` | 签到活动未开启 |
| `GROWTH` | 成长中心执行结果 |
| `AUTH_READY` | `doctor` 通过本地凭据检查 |
| `NO_AUTH` | 找不到凭据文件 |
| `NO_SESSION` | 凭据里缺少登录会话 |
| `AUTH_ERROR` / `AUTH_REJECTED` / `AUTH_BUSY` | 本地凭据处理失败 / 服务端 401 / 凭据文件被占用或拒绝 |
| `FORBIDDEN` | 服务端 403，多为权限或活动条件不满足 |
| `NETWORK` / `TIMEOUT` | 网络不可达 / 时间预算耗尽 |
| `UNKNOWN` | 未识别的接口返回 |
| `ERROR` | 其它失败 |

## 定时任务建议

- 用 `silent`（或轮询场景用 `silent-poll` / `silent-growth`），避免交互等待和 stdout 黑洞。
- 计划任务的执行时限应大于 `WORKBUDDY_BUDGET_SECONDS`，否则进程可能先被系统杀掉。
- 失败不会抛出异常，靠退出码与日志判定即可；建议同时监控 `signin.log` 中 `needs_attention` 为 `true` 的记录。
- 在受限环境（自动化沙箱、安全软件拦截）里可能读不到凭据，优先改为系统计划任务运行。

## 项目结构

```text
.
├─ README.md
├─ .gitignore
├─ workbuddy-signin.exe        # 构建产物（已忽略）
└─ src/
   ├─ main.go                  # 命令分发、时间预算、结果输出
   ├─ signin.go                # 签到主流程与日常流程组装
   ├─ growth.go                # 成长中心各模块
   ├─ api.go                   # HTTP、重试、JSON 取值与格式化工具
   ├─ auth.go                  # 凭据发现、会话校验、请求头构造
   ├─ auth_windows.go          # Windows：共享读、错误码分类、注册表发现
   ├─ auth_other.go            # 非 Windows 平台的对应实现
   ├─ helper.go                # 内嵌 JS 凭据助手与子进程管理
   ├─ emit.go                  # stdout / 日志唯一输出通道
   ├─ pause.go                 # 交互式结束前暂停
   ├─ pause_windows.go         # Windows 按键实现
   ├─ pause_other.go           # 其它平台按键实现
   ├─ *_test.go                # 单元与端到端测试
   ├─ build.bat                # 精简构建 + UPX
   └─ go.mod                   # 模块定义（无第三方依赖）
```

## 开发与测试

```bat
cd src
go fmt ./...
go vet ./...
go test ./... -count=1
```

测试覆盖命令分发、时间预算、签到各分支、成长中心各模块、凭据解析与加密信封校验、输出与暂停行为等；端到端用例通过本地 `httptest` 服务模拟接口，不访问真实网络。

## 安全与隐私

- 只读取本机客户端已有的登录凭据，不修改、不上传。
- 加密凭据（`sym-v1` 字段信封）只在客户端运行时子进程中解密，派生密钥与明文不会写入日志或输出。
- 请求 token 在输出前统一替换为 `[REDACTED]`。
- `doctor` 只做本地检查，不验证服务端登录状态。
