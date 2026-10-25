---
name: req-pick-provenance-2026-10-25
description: 选号决策溯源——每次选中落结构化理由（模式/成本层/探索/试探/兜底类别），Status 透出。
status: 主体落地（2026-10-25，面板展示待 app.js 解锁）
owner: maintainer
---

# REQ: 选号决策溯源（pick provenance）

## 必要性

- **真实问题**：选号器的决策依据（成本层、是否探索、是否 half-open 试探、走了哪条
  兜底）在 pick() 内全部算过即丢——事后无法回答「这个请求为什么落在 3 号而不是 5 号」。
  本仓亲历三次高价值瞬间（词表口径 bug 由投影测试抓、response_format 真相由链路实测
  抓、金样 blocked 由环境事实抓）共同指向：本仓最值钱的资产是「决策可审计」，但它只
  覆盖数据（metrics/reqlog/unknown），不覆盖**决策本身**。
- **不做会怎样**：静默——opsagent 想验证「402 矛盾号仍被兜底捞回」这类假设时无观察
  证据可用；面板运营者看到号被选中只知道结果不知道路径。
- **现状**：pick() 有零散日志（fallback WARN、explore 事件），无结构化留存。
- **对标**：magpie/CRS/one-api/LiteLLM 均无决策溯源层（2026-10-25 对标核实）——
  这是四态语义的自然延伸，也是同类没有的差异化面。
- **更便宜的等效手段**：无——数据已在 pick() 算过，只是没留下；留 string 字段成本
  一次原子 Store。

## 落地（2026-10-25）

- `internal/pool/pickreason.go`：不可变 `pickReason{Mode,Tier,Explored,Probe,Kind,At}` +
  `atomic.Pointer` 整体换（读锁路径安全，ADR-005 D-500 口径）。
- 四条选号路径各自落理由：weighted（tier/explore/halfopen-probe）、sticky（两个
  PickByUID 变体）、lru（top5 全刚用过）、earliest-expiry（kind 粗口径归一）。
- `Status.PickReason`（string，omitempty）：statusOf 透出，面板 overview/`/status` JSON
  自动携带（additive 字段，消费方按需读取；app.js 展示待 E2E 解锁）。
- 测试：四路径各自断言 + 从未选中为空串（pickreason_test.go）。

## Given-When-Then

- When 正常加权选中，Then Status.PickReason = `weighted tier=N [explore] [halfopen-probe]`。
- When 全冷却兜底，Then = `earliest-expiry kind=hard|soft|…`（粗口径）。
- When 粘性命中 / LRU 兜底，Then = `sticky` / `lru`。
- When 从未选中，Then 空串（不编造）。

## 后续（待解锁）

- reqlog 请求级归档带 pick_reason（handler 需透传，M 级）；
- opsagent 规则消费（如「earliest-expiry 且 kind=hard 的号被反复捞回」→ 假设登记）；
- 面板列展示（依赖 app.js E2E/瘦身解锁）。
