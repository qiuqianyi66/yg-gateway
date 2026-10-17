# 变更记录 — B3：四个包的首轮独立变异核验（auth / session / scheduler / cmd-server）

- 来源：`docs/PLAN-open-items-2026-10-17.md` §9.2 **B3**
  （"`auth`/`session`/`scheduler`/`cmd/server` 四包从未做过独立变异核验"）
- 日期：2026-10-17
- 分级：**L0**（只做核验，不改生产代码；发现的问题作为新条目列出）
- 纪律：**变异一律在副本里做**（LESSONS L-2 Seen 2 后的硬要求）；副本排除 `.git`、`auths/`（R1：不复制凭证）、
  `data/` 与二进制，跑完删除并逐字节核对还原

## 一、做法

8 条变异（每包 2 条），全部改成"看起来合理、实际改变了判据"的形态；跑**该包自己的测试**
（`go test <pkg> -count=1`），红 = 测试有牙齿（CAUGHT），仍绿 = 漏网（MISSED）。
每条变异只替换文件中**第一处**锚点，跑完立刻用原文还原。

## 二、结果：4 CAUGHT / 4 MISSED

| 包 | 变异 | 结果 |
|---|---|---|
| `internal/auth` | `realm` 判定丢掉 `isGlobalDomain(a.Domain)` 兜底（只看显式 realm） | **CAUGHT** |
| `internal/auth` | 过期边界 `if a.ExpiresAt <= 0` → `< 0` | **MISSED** ⚠ |
| `internal/session` | TTL 过期判定 `now.Sub(...) > TTL` → `>= TTL` | **CAUGHT** |
| `internal/session` | TTL 默认值边界 `if cfg.TTL <= 0` → `< 0` | **CAUGHT** |
| `internal/scheduler` | 迟到阈值 `late <= wakeupLateThreshold` → `<` | **MISSED** ⚠ |
| `internal/scheduler` | 活动时段判空 `if len(activityHours) > 0` → `>= 0`（恒真） | **MISSED** ⚠ |
| `cmd/server` | config 零值默认判定 `== 0 {` → `== -1 {`（首处） | **CAUGHT** |
| `cmd/server` | `appVersion = "` → `appVersion = "9.9.9-` | **MISSED**（见 §四.4，属"由 CI 而非单测覆盖"） |

## 三、3 个真缺口（建议各补一个测试，每个约 30min）

| # | 缺口 | 为什么值得补 | 建议测试 |
|---|---|---|---|
| **B3-a** | `auth.ExpiresAt <= 0` 的边界：`ExpiresAt == 0` 与负数是否都算"无过期信息"？改成 `< 0` 测试仍绿 = 该边界无人断言 | 过期判定直接决定"这个号还能不能用"；边界错一位会让**刚刚过期的凭证**继续被当成有效 | 表驱动：`ExpiresAt ∈ {负数, 0, 未来, 过去}` 四态各断言一次 |
| **B3-b** | `scheduler` 的迟到阈值 `<=` vs `<`：恰好等于阈值时的行为无人断言 | 定时任务在"刚好迟到 N" 时是否顺延，影响养号任务的执行时间窗 | 冻结时钟，构造 `late == wakeupLateThreshold` 正好命中 |
| **B3-c** | `scheduler` 的各时段判空 `len(x) > 0` → `>= 0` 恒真仍全绿 = **空时段配置**这条路径没有任何测试 | 用户不填 `activity_hours` 时行为必须与"配了"不同，否则会在不该跑的时候跑任务 | 表驱动：每种 `*Hours` 为空 / 非空各跑一次，断言任务集合 |

## 四、需要如实说明的 4 件事

1. **4 条 MISSED 里只有 3 条是真缺口**：`appVersion` 那条不由单测保证——它由
   `release.yml` 的「断言二进制版本串」在发布路径上强制（跨编译矩阵里检查二进制含源码版本串），
   所以单测不管它是**设计如此**，不是缺口。这条要写进 B3 的结论里，免得下一个人以为要补单测。
2. **本次只做核验，没写新测试**：3 个缺口的测试属新增覆盖，按纪律应当各自跑一遍"改回原样要变红"的
   牙齿验证，单独一批做更干净（也避免把核验和补测混在一个提交里）。已作为 B3-a/b/c 记入规划。
3. **变异核验的覆盖边界**：8 条只代表"这 8 个判据已被/未被钉住"，**不代表四包已被穷尽核验**
   （与 `2026-10-16-t3-mutation` 的措辞一致：不把"N 条"说成"穷尽"）。
4. **副本与原文件逐字节核对**：`auth.go` 哈希一致（`True`）；副本目录已删除。
   变异未触碰真实工作区（`git status` 全程只有本变更自己的文件）。

## 五、回滚

纯文档变更，无代码；删除本目录即可。无生产影响。

## 六、下一步（推荐顺序）

1. 先补 **B3-c**（空时段配置——最可能真出问题的一条）
2. 再补 **B3-a**（过期边界，涉及"号能不能用"）
3. **B3-b**（迟到阈值）可与 B4a（夜猫子末次 4s 等待）合并成一批"调度时间语义"改动，一起出 PRD

## 七、收尾：B3-a 是**等价变异**，B3-b / B3-c 已补测试闭环

| # | 结论 |
|---|---|
| **B3-a** | **等价变异，无需测试**。`if a.ExpiresAt <= 0 { return true }` 改成 `< 0` 时，`ExpiresAt == 0` 会落到下一行 `time.Now().Add(within).Unix() >= 0` —— 恒为 true。两条分支输出相同，**任何测试都无法区分**。按变异测试惯例记为 equivalent mutant（不是测试缺口）。原记录里"3 个真缺口"应更正为 **2 个**。 |
| **B3-b** | 已闭环。`awaitWakeupGrace` 的判定抽成纯函数 `isOnTime(late)`（`late <= wakeupLateThreshold`，正好等于阈值算准点）——原实现 `time.Since(planned)` 持续增长，边界不可构造。新增 `TestIsOnTimeBoundary` 逐点钉住 0 / 毫秒 / 阈值−1ns / **阈值** / 阈值+1ns / 1min。牙齿：`<=` → `<` 时"阈值"那行 FAIL。 |
| **B3-c** | 已闭环。新增 `TestReconfigureKeepsWindowsWhenEmpty`：六个时段先配好，再用全空参数 `Reconfigure` 一次，断言全部保留；反向再传非空验证"更新仍然生效"。牙齿：`len(activityHours) > 0` → `>= 0` → FAIL。 |

**行为等价性证据**：抽 `isOnTime` 后 `go test ./internal/scheduler/ -count=1` 全绿（含既有的
`TestAwaitWakeupGraceZeroDelayWhenOnTime` / `TestAwaitWakeupGraceLateCatchupWaitsForNetwork`），
说明重构未改变行为——它们同时也是这次重构的安全网。