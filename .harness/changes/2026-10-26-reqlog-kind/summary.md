# 2026-10-26 reqlog-kind（V3-8：reqlog 加 kind 维度）

## 背景与必要性

ROADMAP v3 P1 项（V3-8），magpie 实跑研究的吸收落地：它的 usage.jsonl 用
ep/kind/source 三维度记账（连 model_list 探测都记），我们的 reqlog 无法区分
「聊天流量/探测流量/养号流量」——V3-10 agent 流量画像的地基缺口。

动工前研究收口（实证，非转述）：

- reqlog 事件构造点全仓唯一：`internal/server/logging.go` 的 `requestTrace.event()`，
  且 `Handler.ServeHTTP` 只对 `POST /v1/chat/completions` 建 trace（handler.go）。
- **探测流量不进 reqlog**：模型目录探测走 `Upstream.FetchModels`（panel 直连上游），
  opsagent canary 是进程内合成观察（`CanaryRound` 无 HTTP 调用）。
- **养号流量不进 reqlog**：autotask 剧本（runChat5 等）经 `internal/upstream`
  直连上游，不经过网关自身 mux。
- 结论：本变更交付「维度 + 词表 + chat 打标 + 归档过滤」；probe/farming 是
  给未来记录方的占位词表值（REQ 语义口径已写明现状边界）。

## 改了什么

1. `internal/reqlog/reqlog.go`：`KindChat/KindProbe/KindFarming` 词表常量；
   `Event.Kind`（`json:"kind,omitempty"`，缺失=未知，wire 三态）；`Filter.Kind`
   （封闭词表**精确**匹配，与自由文本字段的包含匹配相区分，注释已声明这一例外）。
2. `internal/server/logging.go`：`requestTrace.event()` 打 `reqlog.KindChat`，
   注释钉住「路径判断改了这里必须跟着改」。
3. `internal/panel/panel.go`：`/panel/api/request_logs` 接受 `kind` 查询参数
   （后端查询能力；app.js 冻结不动，UI 等 V3-6）。

## 怎么验证的（GWT 兑现）

- G1 打标：`TestRequestMetricsRecordsStream` 断言事件 `Kind==chat`（流式；
  构造点唯一，非流式同路径）。
- G2 历史行兼容 + G3 词表占位：`TestArchiveFilterByKind`——无 kind 的旧行不被
  `kind=chat` 命中、`kind=farming` 回零行、不筛 kind 两行可见、旧行读回空串、
  wire 上零值不产出 `"kind":""` 键。
- G4/G5：panel `TestPanelHandlersRequestLogsLimitAndFilters` 扩展——`?kind=chat`
  只回打标行、`?kind=probe` 回空、既有过滤断言未改一行。
- 门禁：三包 gofmt 零漂移 + vet + test 全绿（reqlog 0.1s / server 8.6s /
  panel 44.7s）；pre-commit 全量见提交记录。

## 怎么回滚

单提交 revert。`kind` 字段 omitempty，回滚后新归档行不再携带该字段，既有
归档里多出的 `kind:"chat"` 键会被旧代码忽略（Unmarshal 丢弃未知字段）——
双向兼容，无迁移需求。

## 诚实记录

- 词表值 `probe`/`farming` 当前无入账来源（占位）：这不是「探测/养号流量已
  被记录」，测试钉的是「过滤回零行」。后续任何记录方入账时须显式打标，不许
  裸字符串。
- 面板 UI 未加 kind 筛选（app.js 4378 冻结，V3-6 E2E 解锁后随瘦身边际补）。
- 独立复审：本变更未做（子 agent 配额不稳，环境约束见 PLAN-next §3）；
  自查已覆盖 G1–G5，按 L-16 本条不冒充独立 review，待补入口=本文件。
