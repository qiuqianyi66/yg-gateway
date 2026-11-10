# 评审记录 — 2026-11-10 五个提交按 skills 库复检

技能：`main-dev/expert-reviewer`（6 维）+ `main-dev/unit-test-write`（牙齿）+ `main-dev/unit-test-ci`（门禁）
范围：`git diff 581af79..HEAD`（5 提交 / 13 文件；生产代码 `internal/session/session.go`、`internal/reqlog/reqlog.go`）
结论：🔴 0 ｜ 🟡 3（本批已清零）｜ 🟢 3 ｜ 🔵 1

---

## 一、逐维结论

| 维 | 结论 | 依据 |
|---|---|---|
| 正确性 | 通过（🟡3 修复后） | session 三条插入路径都过容量检查；`ensureCapacityLocked` 只对新 key 腾位；reqlog `step` 只包动作不包生命周期 |
| 性能 | 发现 🟡1 并修 | 单条淘汰 = 饱和后每次插入一次 O(n) 全表扫描且持全局写锁 |
| 安全 | 通过 | 无机密入日志：reqlog panic 消息只打 `%T` 类型（R2）；无新增入站读取、无路径拼接、无 XFF 相关改动 |
| 一致性 | 发现 🟡2/🟡3 并修 | 镜像删除偏离 `gcOnce` 的「锁内收集、锁外镜像」；函数名与批量语义不符 |
| 可维护性 | 通过 | 注释讲 why（上限的字节量级、批量淘汰的理由、panic 隔离的边界）；测试名即语义 |
| 架构合规 | 通过 | 无新增跨包依赖（session→redisstore 既有方向）；R9 无新 goroutine；R10 reqlog 失败进 LastError 而非吞掉 |

## 二、findings

### 🟡1 session 饱和后单条淘汰，把 O(n) 扫描摊在每次插入上（已修）

- 证据：`ensureCapacityLocked` 原实现对每个新 key 在 `len(entries) >= limit` 时调用一次 `evictOldestLocked`，
  后者 `for k, e := range r.entries` 全表取最小 `lastActive`，且整段在 `r.mu` 写锁内。
  稳态（cap=20000 已满）下**每一条新会话**都是一次 2 万次比较，唯一 conversationId 的高频流量会把
  它变成锁竞争热点。
- FIX：改为批量淘汰（一次扫描 + 排序，删最旧的约 10%，至少 1 条），成本摊到约 10% 的新增上。
- See：`.harness/skills/main-dev/expert-reviewer/SKILL.md` 性能维度（锁竞争）。

### 🟡2 淘汰镜像在持 `r.mu` 时调外部存储（已修）

- 证据：原 `evictOldestLocked` 内 `r.cfg.Store.DelBind(oldestKey)`；对照 `gcOnce`（同文件）
  是「锁内收集 expiredKeys → 解锁 → 镜像删除」，即本仓既有约定是**镜像不在锁内做**。
  Upstash.DelBind 走 `goWrite`（取 `submitMu` 并起 goroutine），持锁调用把路由锁与存储锁序耦合。
- FIX：淘汰函数返回被删 key 列表，三处调用点（`ResolveForModel` 慢路径 / `Bind` / `LoadFromStore`）
  在解锁后统一镜像。`ResolveForModel` 用「解锁 → 镜像」的 defer 保序，保留单点解锁。
- See：`internal/session/session.go` `gcOnce` 既有模式。

### 🟡3 函数名与语义不符（已修）

- 证据：`evictOldestLocked` 改为批量后名字仍暗示单条。
- FIX：改名 `evictOldestBatchLocked`（3 处引用），文档同步。

### 🟢1 growth 用例把 `nightChatGap=4s` 钉进断言

- 有意为之：该用例的目的就是证明「注入实现收到的是**生产默认值**而非被顺手改小的值」。
  代价是调参要同时改测试——已写进 `REQ-2026-11-10-growth-own-tests` 的验收条款，属可接受。

### 🟢2 reqlog 只保留最后一次 LastError，重复 panic 不计数

- `step` 每次 panic 只是覆盖 `lastErr`，面板看不到「每秒都在 panic」的频率。
- FIX 建议（未做）：加 `atomic` 计数器进 `ArchiveStats`；本轮不做，因为尚无真实 panic 观测，
  属"没有证据的加固"。

### 🟢3 批量淘汰后 `Count` 会低于上限（20 → 19）

- `/status` 上会看到锯齿；这是批量摊销的正常代价，LRU 语义不变，不改。

### 🔵1 `defaultMaxEntries` 无配置入口

- 已在 `REQ-2026-11-10-session-max-entries` 的「不做」清单登记：默认 3MB 上界够用，
  开配置项属配置堆积。

## 三、验证（修复后）

| 验证 | 命令 | 结果 |
|---|---|---|
| session 整包 | `go test ./internal/session/ -count=1 -cover` | ok，coverage 97.9% |
| 批量淘汰用例 | 新增 `TestRouterEvictsBatchAtSaturation`（20 条满载 → 插第 21 条 → Count=19，k00/k01 消失） | PASS |
| 门禁 | `powershell -NoProfile -File scripts/pre-commit.ps1` | 见 summary（日志 `%TEMP%\yg-precommit-rereview.log`） |

## 四、自检

- [x] 6 维全覆盖
- [x] 🔴 零残留、🟡 清零（本批修复）
- [x] 每条 finding 含 FIX 与出处
- [x] 评审记录已落盘
