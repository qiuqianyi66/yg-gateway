# REQ：上游协议漂移观测预案（R-102，P1）

> 状态：v1（2026-10-22）。观测层已落地，见 `.harness/changes/2026-10-22-protocol-drift/summary.md`。
> ADR：`docs/adr/ADR-013-protocol-drift-observer.md`。
> 本文定位：需求 + 验收 + 后续判据。只做**观测层**——不改写出站报文（R8 红线）。

---

## 一、必要性（阶段 0，四个问题）

**Q1 要解决的真实问题是什么？**（实测，非"清单上写着"）

CodeBuddy 上游响应形状一旦静默变更，**网关不报错**：选号照常跑、请求照常透传，
但模型目录端点（v3/config）可能形状变了、目录条数悄悄缩水——面板与客户端拿到的
`/v1/models` 悄悄变少。这类漂移没有任何错误码可挂（上游返回 200，只是内容变了），
只能靠「播种基线 → 周期比对」主动发现。此前全仓**没有任何**上游接口版本快照/漂移
检测（已 grep version/drift/snapshot/probe/fingerprint，零命中于观测层）。

**Q2 不做会怎样？** 静默漂移、不报错、运维无感知。与本仓已赔过的 doc-drift /
cross-file-reconcile 同类：不报错的偏差比报错的故障更危险。

**Q3 现状是不是已经变了？** 部分缓解已存在：模型目录四级查找（`model_catalog.go`）、
fallback 级联（`handler.go`）、13 种 ErrKind 单一来源、`r8_golden_test.go` 被动钉死报文。
但这些都是**故障处置**，不是**漂移发现**——没有「记录基线、比对形状」这一环。

**Q4 有没有更便宜的等效手段？** 有边界：不改写出站、不改主链路代码（文件域约束：
upstream 仅新增文件；handler.go 仅 /status/fallbackForward/路由注册三区）。
所以喂点用**周期探测 goroutine**（复用既有 `FetchModels`，用池里一个可用 CN 号），
观测器自包含在新文件 `internal/upstream/drift.go`，主链路零侵入。

**结论：做，且只做观测层。** 明确不做：自动降级/自动切备用（那是处置，不是观测）；
不改 `headers.go`/`payload.go`/`useragent*` 的任何报文形态（R8）。

---

## 二、验收（Given-When-Then）

| # | GWT |
|---|---|
| 1 | Given drift.enabled=true 且池里有可用 CN 号，When 启动后首轮探测成功，Then 把目录条数写入快照文件（data/ 下 protocol-snapshot.json，带 version + observed_at），日志 `snapshot seeded` |
| 2 | Given 快照已播种，When 探测发现目录条数较播种值缩水 ≥50%，Then state=drift、drift_reason=catalog_shrink、WARN 日志 |
| 3 | Given 快照已播种，When 探测连续失败，Then state=drift、drift_reason=catalog_probe_failing、WARN 日志（R10 不静默吞） |
| 4 | Given 已 drift，When 连续 2 次正常观测，Then 自愈回 state=ok（防抖，不反复翻牌） |
| 5 | Given drift 模块启用，When GET /status，Then 响应含 `protocol_drift` 键（四态：state=unknown 时键在值为 unknown，**不是省略键**；未启用模块时省略整个键 = 外层 unknown） |
| 6 | Given 快照文件缺失/版本不符，When Load，Then 回到 unknown 等重新播种，**不报错、不沿用错误基线** |
| 7 | Given 任何漂移观测动作，Then `r8_golden_test.go` 不回归（出站报文零改动） |

---

## 三、明确不做（反膨胀锚点）

- 不自动切备用/自动降级（观测与处置分离，本 REQ 只到观测）。
- 不观测 SSE 帧内容（sse.go/chatrun.go 不在可改文件域；目录端点形状已足够做漂移告警）。
- 不接面板 UI（/status 暴露即够，面板展示另议）。
