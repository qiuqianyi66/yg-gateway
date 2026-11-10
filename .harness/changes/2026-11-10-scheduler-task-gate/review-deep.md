# 深度复审 — 098c0c1（P2-5 同族互斥）

口径：在 `doubao-coding-review-code` 之上补读 `references/review-invariants.md`（修复完整性 /
并发交错 / 生命周期）与 `~/.agents/skills/security-audit`（standard 深度），并对每个受门控
函数做**全仓调用点穷尽**（上一轮只核了 panel 的 5 个按钮入口，没核 panel 自动任务脚本）。

## 问题清单（Findings）

### [P3] 面板自动任务 runBlackCat 绕过 taskBlackcat 门控，同族仍可重入 — internal/panel/autotask_scripts.go:174

**触发条件（当前可达）**：
1. 23:00 排程 `RunBlackcatNow` 持 `taskBlackcat` 锁逐账号跑，每账号要做真实 glm-5.2 对话，
   整批持续数分钟；
2. 同一夜间窗口内，用户在任务面板对某账号点 `black_cat` 的自动完成 →
   `autotask.go:228` 的 per-account 锁只拦「两个面板动作」，不知道排程那趟，放行；
3. `runBlackCat`（autotask_scripts.go:163）先调 `BlackcatNeed` 读剩余次数——此刻排程正在
   填，读到的是**陈旧 need**，随后 `autotask_scripts.go:174` 直接调 `RunNightChats`，
   与 `blackcat.go:57` 是同一个计次上游接口。

**直接消费者看到的实际影响**：该账号当日夜间对话次数超过目标上限，多出来的是真实模型对话
（浪费配额，不是纯上报）；每次浪费量受 need 上限约束（通常 1–3 条），无数据损坏。

**现有保护为何挡不住**：
- `tryLockAccount`（autotask.go:228）保护域只有面板动作，不含 scheduler；
- 生产已启用 `concurrency_per_account=4`（config.json 实测），HTTP 层在途闸把**并行**压成
  FIFO 排队，但它不识别「同族第二趟」，挡不住串行的重复条数；
- `Pool.Acquire` 只在代理聊天路径（chatrun.go:590）用，养号调用不经过它。

**最小修复方向**（待确认后另开一批，不在本批顺手做）：
导出 `Scheduler.FamilyBusy(taskKind) bool`（读 `TryLock` 状态或单独 atomic 标记），
`runBlackCat` 入口先查，排程在跑就直接返回「排程正在补足，跳过」，不发 `BlackcatNeed`。

### 其余候选（终态）

| 候选 | 终态 / 证据 |
|---|---|
| 锁顺序死锁 | rejected：gate 全在函数首行，无嵌套同族锁；`RunStreakBonusNow` 无锁且仅 checkin 内调 |
| 各受门控函数的旁路调用 | 全仓穷尽（含 cmd/server、panel、scheduler）：除面板 5 按钮与 runBatch，仅本 P3 一条旁路；其余生产调用点为零 |
| 依赖 CVE / 硬编码密钥 | rejected：本批 go.mod 未动，无新依赖 |
| PII / 日志泄露 | rejected：跳过日志只含任务名，无 UID/token；R2 未破 |
| 新端点 / body 解析 / 提示注入面 | rejected：无新增 |
| 测试假通过 | rejected：空锁变异 5.02s 报红；但现有用例只覆盖 scheduler 内两趟，**没覆盖 panel 旁路**——这正是本 finding 的测试缺口，修复时须补 |

## 验证缺口

- 本 P3 尚未动态复现（静态路径 + 配置已闭合；真复现需要在 23:00 排程窗口内操作，属 L3→待实测）。
- `-race` 本机不可用（无 gcc），并发仍靠 CI race 作业。

**verdict：changes_required（1 个 P3，修复方案已给，等确认后分批实施）**
