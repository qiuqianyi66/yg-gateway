---
name: bug-diagnose
description: 难 bug 诊断总入口——先建 tight feedback loop（一条命令稳定变红），再按 6 阶段排查。当用户报 bug/报错/慢/异常且还不知道是哪个领域时触发；定位后分派到 upstream-fault。
type: flow
---

# Bug 诊断（bug-diagnose）

难 bug 的 6 阶段纪律。**Phase 1 是本体，其余是机械动作**。没有能稳定复现的命令，不许 theorize——读代码猜根因是本技能要防的失败模式。

## 触发条件

- 用户说"这个 bug 帮我看看 / 报错了 / 慢 / 崩 / 不对 / 打不通"
- 还不知道是哪个领域（网关转发 / 上游 / 面板 / 连接层）
- 间歇性 flake、两个已知 good 状态间的 regression
- 不触发：已知是哪类问题（直接走 upstream-fault）、纯新功能开发、单点文案错

## Phase 0: 网关特定快速分诊（建 loop 前先过）

本项目历史 bug 集中在四层，Phase 1 建 loop 前先快速排除：

1. **环境层（实时防护 / 网络）**：症状是「新构建的 exe 跨命令消失」→ 实时防护删除新二进制，
   对策是构建+swap+启动放同一条命令。症状是「连上游超时」→ 查 `transport.go` 的
   Dial/TLS 超时、DNS、本机出网（GOPROXY 用 goproxy.cn，proxy.golang.org 本机不可达）。
2. **连接层（半死连接）**：症状是「偶发卡 60s / http2 timeout」→ 查 TLSNextProto 是否
   真禁 h2（空 map 而非 nil）、keepalive 15s、ResponseHeaderTimeout。
3. **数据模型层（state.json / 凭证）**：症状是「重启后账号状态错 / 积分归零」→ 查
   state.json 落盘、auths/*.json 的 ACL（Windows 0o600 不剥离继承 ACL）。
4. **可观测性层（旁路静默降级）**：症状是「数据没落库但日志没报错」→ 查旁路
   （reqlog/usage/埋点）的 catch 是不是把落盘失败吞了。

四层都排除不掉，再进 Phase 1 建 loop。

## Phase 1: 建 tight feedback loop（最重要）

**这是整个技能。** 有一条命令在这个 bug 上稳定变红，bisection/假设/插桩都只是消耗它。

按顺序试，第一个成功的就是 loop：

1. 失败测试（`go test ./internal/... -run TestXxx`，打在能碰到 bug 的 seam 上）
2. curl / PowerShell 打本地网关（`http://127.0.0.1:7863/healthz`、`/v1/models`、chat）
3. `go test` 表驱动带 fixture，stdout 对 snapshot
4. 前端切片测试（node vm 跑 app.js 切片）
5. Replay 捕获的真实请求（reqlog 归档里的 payload 存盘，隔离重放）
6. Throwaway harness（最小子集，一个函数触发 bug）
7. Property/fuzz（"有时错"→ 跑 1000 个随机输入找失败模式）
8. Bisection（两个状态间回归 → `git bisect run`）

### Tighten loop

更快？信号更锐？更确定？2 秒确定的 loop 是 debug 超能力；30 秒 flaky 跟没有一样。

### 非确定 bug

目标不是"干净复现"而是**提高复现率**。loop 触发 100×、并行、加压、缩时序窗口。

### 真建不起来 loop

停下来说。列试了什么。问用户要复现环境 / 脱敏捕获物（HAR / 日志 dump / 录屏）。
**没有 loop 不许进 Phase 2。**

## Phase 2-6（机械动作）

- **Phase 2 复现 + Minimize**：缩小到最小场景仍红，每个元素 load-bearing
- **Phase 3 排序假设**：3-5 个可证伪假设（"如果 X 是因，改 Y 会让 bug 消失"），先给用户看
- **Phase 4 插桩**：一次一个变量，日志打 `[DEBUG-xxxx]` 标签，禁"全打日志再 grep"
- **Phase 5 修 + 回归测试**：只在有正确 seam 时写；无 seam 本身是发现（标给 arch-deepen）
- **Phase 6 清理**：DEBUG 标签删干净、throwaway 删除、commit 写清哪个假设对

## 纪律

- 所有命令/输出/捕获物先脱敏：密钥写 `<REDACTED>`，别把 token 贴出来
- 定位到上游后走 `upstream-fault`；定位到面板走 `panel-frontend`
- 修完走 retro：这次什么环境问题导致 debug 难？该加什么自动化检查？

## 检查清单

- [ ] Phase 1 有已跑过的 red-capable 命令（秒级、确定、无人值守）
- [ ] 四层快速分诊已排除（环境/连接/数据/可观测）
- [ ] 3-5 个可证伪假设已排序
- [ ] 回归测试在正确 seam
- [ ] DEBUG 标签全删、commit 写清真假设
