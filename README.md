# workbuddy-signin（Go 版）

WorkBuddy 每日签到与成长中心的**无 UI 命令行工具**，纯 Go 标准库实现。

本项目是 [135790135790a/WorkBuddy-](https://github.com/135790135790a/WorkBuddy-)（Python 原版）的 Go 重写版。重写的出发点很直接：**不想折腾 Python 运行环境**——不用装 Python 3、不用找 `pythonw.exe`、不用管 venv / 依赖 / 脚本绝对路径，构建出一个静态 exe 后复制到任意目录就能跑，直接挂进系统计划任务即可。

> 已保留与 Python 版一致的核心能力：每日签到、成长中心全套、加密凭据解密、命令集、环境变量、JSON 输出、日志格式和凭据发现顺序。
>
> 未移植 Python 版的托盘、HTML 报告、消息推送、历史/心跳/自愈/监控等可选增强。**如果你需要这些功能，请使用[原版](https://github.com/135790135790a/WorkBuddy-)。**

读取本机 WorkBuddy 桌面端已登录的凭据，自动完成签到领取，并按需执行成长中心里的旅行、任务、补登、连登兑换、盲盒等操作，最后输出一行 JSON 结果。

> 非官方工具，与腾讯 / WorkBuddy 官方无关。请遵守服务条款，风险自负。

## 为什么用 Go 重写

Python 原版的核心逻辑其实只依赖标准库，但「跑起来」仍然绕不开运行环境：

- **部署成本**：每台机器都要有 Python 3；计划任务里要写清 `python.exe` / `pythonw.exe` 和脚本的绝对路径。
- **静默运行**：Windows 后台任务要用 `pythonw.exe` 才不弹黑框，路径找错、Python 来源不同都会导致任务静默失败。
- **环境漂移**：系统 Python、微软商店 Python、conda、venv 混在一起时，`python` 到底指哪个经常要现场排查。
- **分发成本**：原版是「脚本 + 运行时」；Go 版是「一个 exe」，复制、备份、换机都更简单。

Go 版的具体做法：

- 仅使用 **Go 标准库**，`CGO_ENABLED=0` 静态编译，无 DLL / 运行时依赖。
- 默认构建会做 `-s -w` 精简并可选 UPX 压缩，产物约 **1.8 MB**。
- 源码约 4.4k 行 Go（其中约 1.3k 行测试），用 `httptest` 做离线端到端回归，不访问真实网络。
- 行为尽量与 Python 版对齐：命令名、环境变量、`result` / `report` / `needs_attention` 字段、`[时间] {JSON}` 日志格式、凭据路径、加密信封（`sym-v1` / suite 1）协议都保持一致。
- Windows 专属细节也用原生方式重写：共享模式读取被客户端占用的凭据文件、按注册表探测客户端安装位置、隐藏解密子进程窗口。

## 与原版的功能对照

| 能力 | Python 原版 | 本 Go 版 |
| --- | --- | --- |
| 每日签到 | ✅ | ✅ |
| 成长中心（旅行 / 任务 / 补登 / 兑换 / 盲盒 / Buddy） | ✅ | ✅ |
| 明文与 `sym-v1` 加密凭据 | ✅ | ✅ |
| 命令集、环境变量、JSON 输出、日志格式 | ✅ | ✅ 对齐（`silent-growth` 是 `silent-poll` 的旧名，同样支持） |
| 时间预算与读操作退避重试 | ✅ | ✅ |
| 运行环境 | Python 3 | **单个静态 exe，无需 Python** |
| 第三方依赖 | 标准库；托盘等增强另需 pywin32 等 | 仅 Go 标准库 |
| 系统托盘常驻 | ✅ | ❌ |
| HTML 报告 / 签到总览 / 连签分享卡片 | ✅ | ❌ |
| 消息推送（企业微信 / 钉钉 / 飞书 / Bark / Telegram / Webhook / 邮件） | ✅ | ❌ |
| 签到历史 / 运行心跳 / 登录态自愈 / 接口失效监测 | ✅ | ❌（只保留退出码与日志） |
| 一键安装脚本（Windows PowerShell / macOS launchd 模板） | ✅ | ❌（自行创建计划任务，见下文示例） |
| 跨平台 | Windows / macOS / Linux | 源码支持三平台；构建脚本为 Windows 批处理，其它平台用 `go build` |

一句话选择建议：

- **只想每天稳定签到、不想装 Python** → 用本仓库。
- **想要托盘、报告、推送、历史面板** → 用 Python 原版。

## 功能

- **每日签到**：查询签到状态，未签才领取，已签则直接汇报今日收益与连签天数。
- **成长中心**：旅行礼物、任务接单与领奖、补登卡补登、连登兑换、盲盒抽奖、Buddy 能量盲盒。
- **稳健性**：所有网络请求受统一时间预算约束；读操作按退避重试，写操作不自动重试以避免重复提交；每个成长模块独立守护，单段异常不会吞掉已领取的奖励。
- **无人值守友好**：`silent*` 命令不写 stdout，只落盘日志；非交互环境自动跳过「按任意键退出」，也可用 `--no-pause` 强制立即退出。
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

### 执行完立即退出：`--no-pause`

非 `silent` 命令在双击运行、且 stdin/stdout 都连着控制台时，会在结束前提示「按任意键退出」，方便查看结果。放到计划任务里时，用 `--no-pause` 可以跳过这一步，执行完立刻退出：

```bat
workbuddy-signin.exe auto --no-pause
workbuddy-signin.exe --no-pause status
```

开关可以写在命令前后，并接受这些别名：

```text
--no-pause  --nopause  -no-pause  nopause  nopase
```

- `silent*` 命令本来就**不会**暂停，加不加 `--no-pause` 都一样。
- 优先级：`--no-pause` > `silent*` > `WORKBUDDY_PAUSE` 环境变量 > 交互控制台检测。
- 即使设置了 `WORKBUDDY_PAUSE=1`，`--no-pause` 也会强制跳过等待。
- 开关只影响结束前的按键等待，不影响结果输出和日志。

## 从 Python 版迁移

### 命令对照

两版命令一一对应，把 `python signin.py` 换成 `workbuddy-signin.exe` 即可：

| Python 版 | Go 版 |
| --- | --- |
| `python signin.py` | `workbuddy-signin.exe` |
| `python signin.py auto` | `workbuddy-signin.exe auto` |
| `python signin.py silent` | `workbuddy-signin.exe silent` |
| `python signin.py growth` | `workbuddy-signin.exe growth` |
| `python signin.py silent-poll` | `workbuddy-signin.exe silent-poll` |
| `python signin.py silent-growth` | `workbuddy-signin.exe silent-growth` |
| `python signin.py status` | `workbuddy-signin.exe status` |
| `python signin.py claim` | `workbuddy-signin.exe claim` |
| `python signin.py all` | `workbuddy-signin.exe all` |
| `python signin.py doctor` | `workbuddy-signin.exe doctor` |

### 配置对照

- **环境变量同名同义**：`WORKBUDDY_AUTH_FILE`、`WORKBUDDY_EXE`、`WORKBUDDY_BUDGET_SECONDS`、`WORKBUDDY_SIGNIN_LOG`、`WORKBUDDY_GROWTH_LOG_EMPTY` 都可直接沿用。
- **日志格式相同**：每行 `[YYYY-MM-DD HH:MM:SS] {JSON}`，`result` / `report` / `needs_attention` 等字段与原版一致。
- **凭据路径相同**：自动探测顺序与原版一致，不需要重新登录；加密凭据仍由本机 WorkBuddy 客户端运行时解密。
- **退出码语义相同**：`0` 成功，`1` 需要关注，`2` 配置或凭据问题。

迁移时注意：

1. Go 版日志默认写在**可执行文件所在目录**的 `signin.log`；如果 exe 放在只读目录，请用 `WORKBUDDY_SIGNIN_LOG` 指定可写路径。
2. 原版的 `install-windows.ps1` 不能直接复用（它指向 `pythonw.exe`），需要把计划任务的目标改为 exe，见下一节。
3. 两个版本可以并存，但不要同时运行两套定时任务——虽然签到接口幂等、不会重复领积分，但会产生重复请求和两份日志。
4. 托盘、报告、推送等增强不在本仓库内；需要这些能力时继续使用 Python 原版。
5. `--no-pause` 是 Go 版新增的命令行开关；原版靠 `silent*` 命令避免暂停。

## 快速开始

### 前置条件

- 已安装并登录 WorkBuddy 桌面端（凭据由客户端写入本机）；Linux 上登录过 CodeBuddy CLI 即可。
- 直接使用现成的 exe 时，**无需安装 Python，也无需安装 Go**。
- 只有从源码构建才需要 Go 1.21 或更高版本。

### 下载预编译产物

不想自己编译时，从 [Releases](https://github.com/poorboy/workbuddy-signin-go/releases) 下载对应平台的压缩包，解压后直接运行 `workbuddy-signin`（Windows 为 `.exe`）。压缩包内含 `README.md` 和 `LICENSE`，并附带 `SHA256SUMS.txt` 供校验。

| 平台 | 架构 | 压缩包 |
| --- | --- | --- |
| Windows | x64 | `workbuddy-signin-v0.1.0-windows-amd64.zip` |
| Windows | ARM64 | `workbuddy-signin-v0.1.0-windows-arm64.zip` |
| macOS | Intel | `workbuddy-signin-v0.1.0-darwin-amd64.tar.gz` |
| macOS | Apple Silicon | `workbuddy-signin-v0.1.0-darwin-arm64.tar.gz` |
| Linux | x64 | `workbuddy-signin-v0.1.0-linux-amd64.tar.gz` |
| Linux | ARM64 | `workbuddy-signin-v0.1.0-linux-arm64.tar.gz` |
| Linux | ARMv7 | `workbuddy-signin-v0.1.0-linux-armv7.tar.gz` |

macOS / Linux 解压后如无执行权限，先 `chmod +x workbuddy-signin`。所有产物均为 `CGO_ENABLED=0` 静态编译，未做 UPX 压缩（避免跨平台兼容性和杀软误报），体积约 5.6–6.4 MB。

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

发布全部平台（交叉编译 + 打包 + 校验和）：

```powershell
powershell -ExecutionPolicy Bypass -File .\src\build-release.ps1 -Version v0.1.0
```

产物写入 `dist\`：`bin\` 下是原始可执行文件，根目录是按平台打包的 `.zip` / `.tar.gz`、`SHA256SUMS.txt` 和 `BUILD-INFO.txt`。`dist/` 已在 `.gitignore` 中忽略，发布时作为 Release 附件上传即可。

### 运行

```bat
workbuddy-signin.exe                    REM 签到 + 成长中心，打印一行 JSON
workbuddy-signin.exe doctor             REM 先确认凭据能被正确读取
workbuddy-signin.exe silent             REM 定时任务用：只写日志
workbuddy-signin.exe auto --no-pause    REM 要 stdout 但不等待按键
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
| `WORKBUDDY_PAUSE` | Go 版新增。`1/true/yes/on` 强制结束前等待按键；`0/false/no/off` 禁止等待。未设置时按是否为交互式控制台自动判断。命令行 `--no-pause` 优先级更高 |
| `WORKBUDDY_GROWTH_LOG_EMPTY` | 设为 `1` 时，`silent-poll` / `silent-growth` 的空跑也写日志 |
| `WORKBUDDY_UPX` | 构建时 UPX 可执行文件完整路径 |
| `GOOS` / `GOARCH` | 构建时交叉编译目标 |

## 凭据发现顺序

未设置 `WORKBUDDY_AUTH_FILE` 时，依次查找：

1. Windows：`%LOCALAPPDATA%\CodeBuddyExtension\Data\Public\auth\workbuddy-desktop.info`
2. macOS：`~/Library/Application Support/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info`
3. Linux：`$XDG_DATA_HOME/CodeBuddyExtension/Data/Public/auth/Tencent-Cloud.coding-copilot.info`（默认在 `~/.local/share` 下）
4. `~/.config/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info`
5. `~/.workbuddy/auth/workbuddy-desktop.info`

Windows 上若凭据是加密格式，会按常见安装目录与注册表卸载信息查找 `WorkBuddy.exe`；macOS 会检查 `/Applications/WorkBuddy.app` 等位置。

## 工作原理

### 1. 定位并校验凭据

按上一节的顺序找到 `workbuddy-desktop.info`，读取后校验 `auth` / `account` 结构和 token 格式。Windows 上用共享模式打开文件，容忍客户端刷新 token 时的短暂独占；读取失败会按 `0/1/2/3/5/8/13` 秒退避重试，并区分「文件被占用」和「访问被拒绝」。

### 2. 处理加密凭据（如需要）

- 明文 token：直接使用。
- `sym-v1` / suite 1 字段信封：从本机 WorkBuddy 可执行文件启动一个隐藏的 Electron 子进程（`ELECTRON_RUN_AS_NODE=1`），把内嵌的 JS 助手作为 `-e` 参数传入，待解密的信封走 stdin，结果只从 stdout 返回。
- 派生密钥、明文 token 都只存在于该子进程内存中；Go 侧只接收解密后的 token，并在输出前统一脱敏。子进程有 10 秒超时和输入/输出长度上限，原始助手输出不会写进日志。

### 3. 构造请求

Endpoint 取凭据里的 `auth.endpoint`，缺省为 `https://copilot.tencent.com`；请求头包括 `Authorization: Bearer <token>`、`X-User-Id`，以及存在时的 `X-Enterprise-Id` / `X-Tenant-Id` / `X-Domain`。

签到接口：

- `POST /v2/billing/meter/checkin-activity-status`：查询今天是否已签。
- `POST /v2/billing/meter/daily-checkin`：领取签到积分。

成长中心（`/v2/activity/growth`）依次处理：

1. 旅行：`/buddy/travel/status` → 已到达则 `/buddy/travel/claim` → 空闲则 `/buddy/travel/config` 选地点并 `/buddy/travel/depart`。
2. 任务：`/tasks` 查列表 → `/tasks/accept` 批量接单 → `/tasks/{code}/claim` 领奖。
3. 补登：`/streak` 查补登卡 → `/heatmap` 找当月零分日期（排除已补登日期）→ `/makeup-cards/use`，每轮最多补一天。
4. 连登兑换：`/redeem/summary` 查三档状态 → `/redeem` 领取 7/14/28 天奖励。
5. 盲盒：`/lottery/chances` 查次数 → `/lottery/draw` 抽奖。
6. Buddy 盲盒：`/buddy/quota` 查能量与可开数量 → `/buddy/open`。

每个模块单独 `recover` 守护：某一段异常只写入汇报，不会吞掉已经领到的奖励；认证/权限被拒才会中止整轮。

### 4. 重试与时间预算

- 网络不可达：按 5 / 15 / 30 / 60 / 90 秒退避重试。
- 服务端 5xx：按 3 / 10 秒重试。
- 补登、抽奖、领奖等成长中心写操作**不重试**，避免服务端已处理但客户端超时导致重复提交。
- 两个签到接口例外：状态查询是只读的，领取接口本身幂等（当天重复领取只会返回「已签」），因此允许重试。
- 所有请求受 `WORKBUDDY_BUDGET_SECONDS` 约束，预算不足时主动收尾，把剩余项留到下一轮；单个请求超时上限 30 秒。

### 5. 输出

成功或失败都收敛成一行 JSON；`needs_attention` 标记需要人工关注的异常。`silent*` 命令不写 stdout，只追加日志；结果为 `ERROR` 时两条通道都走，避免失败记录丢失。

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

## 定时任务与无人值守

原版提供 `install-windows.ps1` 一键创建任务；Go 版没有安装脚本，但把任务目标从 `pythonw.exe signin.py` 换成 exe 即可。建议沿用原版的两条任务：

- **每日签到**：每天 00:05 跑 `silent`。
- **成长中心轮询**：每 4 小时跑 `silent-poll`，领回旅行礼物、补派 Buddy，并顺带补签（未签才签）。

### Windows：PowerShell 创建计划任务

```powershell
# 1. 把 exe 放到固定目录
$exe = "C:\Tools\workbuddy-signin.exe"

# 2. 公共设置：错过触发后补跑；执行时限要大于对应的时间预算
$signinSettings = New-ScheduledTaskSettingsSet `
  -StartWhenAvailable `
  -ExecutionTimeLimit (New-TimeSpan -Minutes 10)

$pollSettings = New-ScheduledTaskSettingsSet `
  -StartWhenAvailable `
  -ExecutionTimeLimit (New-TimeSpan -Minutes 5)

# 3. 每天 00:05：签到 + 成长中心，静默写日志
Register-ScheduledTask -TaskName "WorkBuddyAutoSignin" -Force `
  -Action (New-ScheduledTaskAction -Execute $exe -Argument "silent") `
  -Trigger (New-ScheduledTaskTrigger -Daily -At 00:05) `
  -Settings $signinSettings `
  -Description "WorkBuddy 每日签到与成长中心"

# 4. 每 4 小时：补签 + 成长中心轮询
Register-ScheduledTask -TaskName "WorkBuddyGrowthPoll" -Force `
  -Action (New-ScheduledTaskAction -Execute $exe -Argument "silent-poll") `
  -Trigger (New-ScheduledTaskTrigger -Once -At (Get-Date).Date `
    -RepetitionInterval (New-TimeSpan -Hours 4) `
    -RepetitionDuration (New-TimeSpan -Days 3650)) `
  -Settings $pollSettings `
  -Description "WorkBuddy 成长中心轮询"

# 5. 确认任务已创建
Get-ScheduledTask -TaskName "WorkBuddy*" | Format-Table TaskName, State
```

卸载：

```powershell
Unregister-ScheduledTask -TaskName "WorkBuddyAutoSignin" -Confirm:$false
Unregister-ScheduledTask -TaskName "WorkBuddyGrowthPoll" -Confirm:$false
```

注意事项：

- 任务默认以**当前用户、仅在登录时运行**，这通常也是读取本机凭据最省事的方式。改成「不管用户是否登录都运行」时，可能因用户配置文件 / DPAPI 上下文不同而读不到凭据。
- 请使用登录 WorkBuddy 的**同一个 Windows 账号**注册任务；遇到「拒绝访问 / 0x80070005」时，用管理员 PowerShell 重试。
- 执行时限要大于时间预算：签到任务 10 分钟（预算上限 540 秒）、轮询任务 5 分钟（上限 240 秒），各留一点启动和收尾时间。
- 日志默认写在 exe 同目录的 `signin.log`；exe 放在只读目录时用 `WORKBUDDY_SIGNIN_LOG` 指定可写路径。

### macOS / Linux

原版的 launchd 模板仍然适用，只需把 `ProgramArguments` 改成 exe 路径和 `silent-poll`，不再需要 Python 路径。Linux 也可以直接用 cron：

```cron
# 每天 00:05 签到 + 成长中心
5 0 * * * /path/to/workbuddy-signin silent
# 每 4 小时轮询
0 */4 * * * /path/to/workbuddy-signin silent-poll
```

### 通用建议

- 无人值守一律用 `silent` / `silent-poll`：不依赖 stdout，也不会等待按键。
- 如果确实要跑 `auto` / `status` 并把结果留在 stdout，加 `--no-pause`，避免计划任务卡在按键提示上。
- 如果沿用原版的 WorkBuddy 自动化（模式 A），把提示词里的 `python signin.py auto` 换成 exe 的绝对路径即可，不再需要 Python。
- 失败不会抛出异常，靠退出码和日志判断；建议监控 `signin.log` 中 `needs_attention` 为 `true` 的记录。
- 轮询任务默认只在**真的领到东西或出错**时写日志；想逐轮查看就设 `WORKBUDDY_GROWTH_LOG_EMPTY=1`。
- 在受限环境（自动化沙箱、安全软件拦截）里可能读不到凭据，优先改为系统计划任务运行。

## 项目结构

```text
.
├─ README.md                   # 本文件
├─ LICENSE                     # MIT（含原项目版权声明）
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
   ├─ build.bat                # Windows 精简构建 + UPX
   ├─ build-release.ps1        # 全平台交叉编译 + 打包发布
   └─ go.mod                   # 模块定义（无第三方依赖）
```

## 开发与测试

```bat
cd src
go fmt ./...
go vet ./...
go test ./... -count=1
```

测试覆盖命令分发、时间预算、签到各分支、成长中心各模块、凭据解析与加密信封校验、输出与暂停行为等；端到端用例通过本地 `httptest` 服务模拟接口，不访问真实网络。当前共 6 个测试文件、38 个测试函数，全部离线运行。

## 安全与隐私

- 只读取本机客户端已有的登录凭据，不修改、不上传。
- 加密凭据（`sym-v1` 字段信封）只在客户端运行时子进程中解密，派生密钥与明文不会写入日志或输出。
- 请求 token 在输出前统一替换为 `[REDACTED]`。
- `doctor` 只做本地检查，不验证服务端登录状态。

## 致谢与许可

- **原项目**：[135790135790a/WorkBuddy-](https://github.com/135790135790a/WorkBuddy-)，作者 [88lin](https://github.com/88lin)，MIT License。本项目的命令设计、接口行为、凭据处理思路和部分文案参考了该项目。
- **本项目**：独立的 Go 重写实现，同样以 MIT 协议发布，详见 [LICENSE](LICENSE)。再分发时请保留原项目的版权声明。
- **无隶属关系**：签到接口来自对 WorkBuddy 桌面端的逆向分析，可能随客户端更新失效。本项目与腾讯 / WorkBuddy 官方无关，使用风险自负。

如果原版更新了接口或修复了兼容性问题，欢迎提 Issue / PR 同步到 Go 版。也建议给[原项目](https://github.com/135790135790a/WorkBuddy-)点个 Star。
