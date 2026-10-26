# REQ-reqlog-kind-2026-10-26 — reqlog 加 kind 维度（ROADMAP v3 V3-8）

## 必要性（阶段 0）

1. **真实问题**：reqlog 事件只有一个来源（chat 流量），无法区分探测流量和养号流量——后续要做 reqlog 归因、成本分摊、账号健康度计算时，所有事件混在一起，无法按种类切分。
2. **不做会怎样**：后续加 probe/farming 流量时，只能靠事后猜测每条日志是什么类型，无法做精确的按种类统计和限流。
3. **现状是否已变**：ROADMAP v3 V3-8 明确列了这个需求，已落地。
4. **更便宜手段**：直接在内存快照 + JSONL 归档里加一个 kind 字段，封闭词表，成本最低。

**结论：做**（已落地）。

- 状态：✅ 已落地（2026-10-26）
- 来源：[ROADMAP-prd-v3](../roadmap/ROADMAP-prd-v3-2026-10-25.md) V3-8（借鉴 magpie
  usage.jsonl 的 ep/kind/source，实跑研究见 `docs/reference/magpie-deep-read-2026-10-25.md` §四）
- 变更目录：`.harness/changes/2026-10-26-reqlog-kind/`

## 需求

reqlog 事件（内存快照 + JSONL 归档）增加 `kind` 调用种类维度，封闭词表：

| 值 | 语义 | 现状 |
|---|---|---|
| `chat` | 下游聊天流量（/v1/chat/completions） | **当前唯一入账来源** |
| `probe` | 探测流量（健康/目录/canary 类合成调用） | 词表占位，尚无入账方 |
| `farming` | 养号流量（面板 autotask 任务动作） | 词表占位，尚无入账方 |

**语义口径（四态纪律）**：

- `kind` 缺失 = **未知**（V3-8 之前的历史归档行没有该字段），消费方按三值处理，
  禁止把空串读成 `chat`——与 `/v1/models` 的「字段省略 = 未知」同源，wire 上用
  `omitempty` 表达（缺失不产出 `"kind":""`）。
- `Filter.Kind` 是封闭词表的**精确**匹配（`Filter` 其余字符串字段仍是包含匹配）；
  历史行不被 `kind=chat` 命中（那是把未分类读成已分类）。
- **现状边界（研究实证，勿臆断）**：探测流量（`Upstream.FetchModels`、opsagent
  CanaryRound 合成观察）与养号流量（autotask 经 `internal/upstream` 直连上游）
  **目前都不经过 reqlog**——reqlog 只记下游 chat completions。本变更是 V3-10
  agent 流量画像的地基：先把维度立起来，后续记录方（探测/养号入账、面板展示）
  各自兑现自己的 kind。

## Given-When-Then 验收

- **G1 打标**：When 下游 POST `/v1/chat/completions` 落账，Then 事件
  `kind=chat`（流式与非流式同口径）。
- **G2 历史行兼容**：When 归档含 V3-8 之前的行（无 kind 字段），Then 读回
  `Kind` 为空串，不被 `Filter{Kind:"chat"}` 命中，不筛 kind 时照常可见；
  wire 序列化不产出 `"kind":""`。
- **G3 词表占位**：When 按 `kind=probe` / `kind=farming` 过滤，Then 回零行
  （尚无入账来源），不误命中其他行。
- **G4 面板查询**：When `GET /panel/api/request_logs?kind=chat`，Then 只回
  显式打标 chat 的行。
- **G5 零回归**：When 不传 kind 参数，Then request_logs 行为与 V3-8 之前完全
  一致（全部既有过滤/limit 语义不变）。

## 落点

- `internal/reqlog/reqlog.go`：Kind 词表常量 + `Event.Kind`（omitempty）+
  `Filter.Kind`（精确匹配）。
- `internal/server/logging.go`：chat 路径事件构造点打 `reqlog.KindChat`。
- `internal/panel/panel.go`：`/panel/api/request_logs` 接受 `kind` 查询参数。
- 测试：`reqlog_test.go`（G2/G3）、`logging_test.go`（G1）、
  `coverage_handlers_test.go`（G4/G5）。
- 面板 UI 不在本变更（app.js 冻结 4378 行，等 V3-6 E2E 解锁后再动）。
