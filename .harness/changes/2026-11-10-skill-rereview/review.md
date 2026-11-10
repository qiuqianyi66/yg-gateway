# 复检（v2，按本地深度技能库）— 2026-11-10 六个提交

技能：`doubao-coding-review-code`（defect-first，只报当前可触发且证据闭合的 P0–P3）
　　　+ `expert-reviewer`（用户 `~/.agents/skills` 版：6 维 + 影响度×置信度反向门限）
范围：`581af79..4376632`（6 提交 / 21 文件 / +1612 −18）
取代：v1（`.harness/skills` 四技能版）的 6 维评审，其 🟡/🟢/🔵 分级在本技能口径下多数不成立（见末节）。

---

## 问题清单（Findings）

### [P3] 冷启动把上层持久化镜像当垃圾清理 — `internal/session/session.go:133-145`

- **本次变化**：`cbfa12e` 给 `LoadFromStore` 加了容量检查，并把被淘汰的 key 收集进 `evicted`，
  解锁后统一 `Store.DelBind(k)`（该写法在 `LoadFromStore` 里是本批新引入）。
- **最小触发**：Store 启用（`config.json` 配了 Upstash）且 `LoadBinds` 返回的绑定数 > `MaxEntries`
  （默认 20000，可配更小）。此时装到第 20001 条起，每进一批就淘汰约 10%，这些 key 随即被从 Redis 删除。
- **直接消费者看到的结果**：`redisstore.DelBind` 删掉 `yg-gateway:bind:<key>`。下次冷启动 `LoadBinds`
  少一批绑定 → 那些会话重新选号，对话上下文落到另一个账号（粘性失效），且**每次重启再删一批**。
- **最小证据**：`session.go:134-145` 原文（`evicted = append(evicted, r.ensureCapacityLocked(key)...)`
  → `r.cfg.Store.DelBind(k)`）；副本变异实测（`%TEMP%\ygg-mut-load`，已删）：
  `LoadFromStore 删了 6 条持久化绑定：冷启动不该清理镜像`（25 条装载 / 上限 20 → 3 次淘汰 × 2 条）。
- **为何保护无效**：内存上限只约束内存（`ensureCapacityLocked` 已删 `r.entries`），
  `DelBind` 是额外的**破坏性**副作用，没有任何契约要求冷启动顺便清理 Redis。
- **最小修复**：`LoadFromStore` 只腾内存、不镜像删除（本批已改，`session.go:133-142`；
  新用例 `TestLoadFromStoreDoesNotDeletePersistedBinds` 钉住 delBinds==0）。

---

## 实际 base/head 与覆盖

- base `581af79` → head `4376632`；21 文件全读（生产代码两处：`session.go` 87 行改动、
  `reqlog.go` 27 行改动；其余为文档/治理 JSON/测试）。
- changed tests 按真实顺序审完：`growth/client_test.go`（768 行，10 组用例）、
  `session/max_entries_test.go`（含本批新增冷启动用例）、`reqlog/reqlog_test.go`（panic 隔离用例）。
- 重复运行查抖动：reqlog `-count=8`、session `-count=5`、growth `-count=3` 全绿（`0.8s~8.2s`）。

## 会改变 finding / verdict 的验证缺口

- **未跑 `-race`**：本机无 gcc（AGENTS.md 已记），并发证据只能来自 CI `release.yml`；本轮所有并发结论
  都是「锁内/锁外顺序 + 单测」级，不是竞态检测器级。
- **`unlock → DelBind` 窗口**（淘汰镜像期间该 key 被并发 `Bind` 重新绑定 → store 缺一条、内存有）：
  与 `Unbind`/`gcOnce` 的既有模式同类（pre-existing，且下次请求会重新 `SetBind` 自愈），不作为 finding。
- **panic 后 `w.buf` 半截**（`reqlog`）：`writeEvent` panic 时缓冲可能已有残片，下一次 flush 会写出
  一条坏行；`readFile` 对坏行 `continue`（`reqlog.go:746`），影响 = 丢 1 条事件，不扩散 → 不报。
- **`closeFile` panic 后 `w.file` 非 nil** → 后续归档全部失败（粘性失败）：panic 来源不可达（bufio/Close 不 panic）→ `gap`。
- **growth 用例依赖本地时区与"昨天"**：跨午夜有理论窗口不匹配；`-count=3` 未复现 → `gap`。

---

## 与 v1（`.harness/skills` 版）的口径差异（诚实记录）

| v1 结论 | 本技能口径 | 处置 |
|---|---|---|
| 🟡1「锁内 O(n) 扫描」 | 需「调用频率 + 每次成本 + 规模后果 + 缺主导保护」四项实测，本轮只有结构证据 → 不成立为 finding | 已按该口径改成批量淘汰（**属改进，不是缺陷修复**） |
| 🟡2「镜像在锁内调 Store」 | 正确性类，成立（已复现并已修） | 保留 |
| 🟡3「函数改名」/ 🟢2「panic 不计数」/ 🔵1「配置入口」 | 「不得报告」：命名、维护性、未证实加固 | 只作记录，不进 findings |

## Verdict

`pass` —— 唯一合格 finding（P3）已在本批修掉并补测试；其余候选均为 `gap` 或被 pre-existing 驳回。
