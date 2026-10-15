---
last_updated: 2026-10-15
status: 实施中（YG 授权 Native 执行）
owner: YG
modification_level: L2（新增 domain 包 + 依赖守卫 + 顶层约束文档）
research: morluto/rea 源码深读（AGENTS.md / architecture.mermaid / tool-design.md / evidence-workflows.md）
---

# YG Gateway 架构地基 + Evidence/Unknown 语义 — 需求分析（PRD）

> 背景：豆包基于 rea 源码深读给出了一份移植清单。本轮复核后确认清单方向对，
> 但**误读了 rea 一半**（把 MCP 当主线），且**漏了 rea 最值钱的东西**：
> observation / derivation / inference / unknown 的四态分离，以及 Unknown 作为
> 带 revision 的一等公民实体。
>
> 本 PRD 记录复核结论、修正后的范围，以及本轮实际实施的需求项。

---

## 一、复核结论：原清单的三处偏差

| 原清单项 | 复核结论 |
|---|---|
| P0-1 抽 `UpstreamClient` interface | 方向对但**定位错**：`internal/upstream` 已存在且是事实 provider 层，不是"新建"而是"收窄"。且它**零用户可见收益、风险最高** → 从 P0 降到 P1 |
| P0-2 依赖方向守卫「pool 不许 import server」 | **该约束现已成立**（实测 `server→pool` 单向）。脚本的价值是**冻结现状**而非"防止将来变坏"，工作量应下调为 XS |
| P2 全部（MCP 骨架/effects/codegen/admission） | rea 的 MCP 是**结果**不是**原因**。yg-gateway 是 7×24 服务，不是一次性分析工具，MCP 的 ROI 在此远低于 Evidence/Unknown → 整体延后 |

**实测依赖方向（本轮基线，脚本即冻结它）**：
```
pool     -> auth, logfmt
server   -> alerting, auth, httpauth, livecfg, logfmt, metrics, pool,
            prompt, redisstore, reqlog, session, upstream, usage
upstream -> auth, logfmt, session
session  -> （无 internal 依赖）
```

---

## 二、rea 最值钱、原清单漏掉的东西

### 漏 1（核心）：观察 / 推断 / 未知 的三层分离

rea `AGENTS.md` 首句：*"Distinguish observations, derivations, inferences, and unknowns"*。
每个工具返回体强制带 `coverage` + `limitations`，且 **"Missing coverage is unknown, not absence"**。

**yg-gateway 现在大量把 inference 当 observation 展示**：

| 现有展示 | 实际语义 | 风险 |
|---|---|---|
| `accounts[].model_costs` 的 tier=收费 | **推断**（少量实测 credit 样本外推） | 基于 2 个样本把号排除出选号 |
| `/status` 的 `ewma_success` | **派生**（EWMA 平滑值，非真实成功率） | 当成真实成功率看 |
| `usage` 的 USD 折算价 | **推断**（按 OpenRouter 公开价折算） | 面板标注了"参考价"，但 `/metrics`/`/status` 未标 |
| 「该号不可用」 | **混合**（硬观察 402 / 推断的 tier 判定混在一起） | 无法区分真故障与推断 |
| `supports_efforts` 为空 | **未知**（精简目录未返回），现被当"不支持" | 客户端不给选档位 UI |
| `supportsImages` 缺失 | **未知**，现被当"放行" | 发图不拦不提示 |

### 漏 2：Unknown 是一等公民，不是面板一个视图

rea 里 unknown 是**带 revision 的实体**：`record_unknown` / `update_unknown`（revision 不匹配要重读、不能盲重试）/ `list_unknowns`，且**消解只能由合格的观察证据完成**。

yg-gateway 现状：unknown 只是个隐式枚举值（如 `tier=1 未知`），**没人跟踪"需要什么才能消解、消解了没"**。

### 漏 3-6（次要）
- `verify` 断言式验证（rea `verify_reconstruction`）：yg-gateway 的等价性守门测试应升级为可对外调用的 verify 能力
- `limitations` / `coverage` 作为返回体必带字段（`/status` 的 p95 是分桶插值估算，未说明）
- `finalization` 优雅终止语义（流式被掐断 vs 正常结束不可判定）
- `.agents/skills/*/references/` 分层（yg 的 11 个 SKILL.md 都是单文件）

---

## 三、本轮需求（F 编号）

| F-编号 | 需求 | 优先级 |
|---|---|---|
| F-801 | 依赖方向守卫脚本，冻结现有单向依赖，违规退出非 0 | P0 |
| F-802 | 顶层 `AGENTS.md`：Product Direction + Boundary Contracts（R8 上提）+ 三态语义硬要求 | P0 |
| F-803 | `internal/evidence/` 四态标记（observation/derivation/inference/unknown），零依赖 domain 层 | P0 |
| F-804 | 成本分层 + EWMA 接入四态，`/status` 输出带 provenance | P1 |
| F-805 | `internal/unknown/` 消解实体（带 revision），`/status` 暴露 unknowns | P1 |
| F-806 | skills description 加"什么时候跳过" + references 分层 | P1 |
| F-807 | （延后）upstream 收窄为 interface | P2 |

**F-801 验收**
- Given 当前仓库 / When 跑 `pwsh -File scripts/check-boundaries.ps1` / Then 退出码 0
- Given 在 `internal/pool/` 加 `import "yg-gateway/internal/server"` / When 重跑 / Then 退出码非 0 且输出含 `pool -> server`

**F-803 验收**
- Given `Observe(402,"status")` / When 序列化 / Then JSON 含 `"kind":"observation"`
- Given `Infer("charged","2 samples",2)` / Then `Kind==inference` 且 `Samples==2`
- Given `Unknown("no call")` / Then `Kind==unknown`

**F-804 验收**
- Given 某账号某模型 1 条 credit 观测 / When 查 `/status` / Then `model_costs[].provenance.kind=="inference"`、`samples==1`
- Given 0 条观测 / Then `kind=="unknown"`
- Given `ewma_samples < ewma_min_samples` / Then `/status` 该字段 `kind=="unknown"`（**不输出数字**）
- Given 达到阈值 / Then `kind=="derivation"`

**F-805 验收**
- Given 登记一条 unknown / When 用**过期** revision 调 Resolve / Then 返回错误（不能盲重试）
- Given 用正确 revision + 证据 ID / Then 消解成功，Revision 递增

---

## 四、不做什么

1. **不动 README.md / internal/panel/app.js / HANDOFF.md 里他人的未提交改动**。
2. 不动选号/冷却/熔断/EWMA **公式**（ADR-005 零改动）。
3. 不改上游协议报文（R8）。
4. 本轮**不实现 MCP**（延后）。
5. 不改任何落盘格式（state.json / usage.json / 归档 JSONL），无数据迁移。

---

## 五、非功能约束

1. 零新依赖（`go.mod` 不变）。
2. 21 包全绿 + `go vet` 干净；新文件 `gofmt` 干净。
3. 每项配"有牙齿"的测试（还原 bug 必须 FAIL）。
4. 建 `.harness/changes/architecture-foundation-evidence/`。
