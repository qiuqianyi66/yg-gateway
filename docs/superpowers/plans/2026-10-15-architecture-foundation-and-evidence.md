# YG Gateway 架构地基 + Evidence/Unknown 语义 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用低成本动作锁定 yg-gateway 现有正确的依赖方向，并把 rea 最核心的「观察/推断/未知三态分离」与「Unknown 消解链路」落地，替代原清单里 ROI 偏低的 MCP 主线。

**Architecture:** 分三层推进——(1) 依赖方向守卫脚本冻结现状（XS）；(2) 顶层 AGENTS.md 把 R8 等红线上提为硬约束并写入三态语义要求（XS）；(3) 新增 `internal/evidence/` 定义 observation/derivation/inference/unknown 四态标记，先接入成本分层与 EWMA 两个已知"推断值"，再把 Unknown 做成带 revision 的可消解实体。upstream 收窄为 interface 从 P0 降到 P1。

**Tech Stack:** Go 1.27.2，零新依赖（沿用项目哲学），PowerShell 脚本（Windows 宿主）

**Spec:** `docs/prd/REQ-yg-gateway-hotpath-optimization-2026-10-14.md`、`docs/adr/ADR-006-hotpath-streaming-observability.md`

## Global Constraints

- Go 工具链 ≥ 1.27.2（`C:\Program Files\Go\bin\go.exe`），`GOPROXY=https://goproxy.cn,direct`，`CGO_ENABLED=0`
- 零新依赖：`go.mod` 当前仅 `github.com/redis/go-redis/v9` 一个直接依赖
- 不改上游协议报文（R8 红线）：`wb2api-<ms>` conversationId、`chatcmpl-wb2api` 哨兵、UA 指纹一律不动
- 选号/冷却/熔断/EWMA 公式零改动
- 全量门禁：`go test ./...` 21 包全绿（18 ok + 3 no test files）、`go vet ./...` 无输出
- `gofmt -l ./cmd ./internal` 允许残留 3 个 pre-existing 漂移（config.go / httpauth/index.go / panel.go），**新文件必须干净**
- 每次改动建 `.harness/changes/{feat}/`，配 PRD + ADR

## Review Focus

1. **推断值被当成观察值展示**：`model_costs` 里 tier=收费、`ewma_success`、USD 折算价——这些是推断/派生，消费者（养号决策）需要知道置信度。缺失会导致"基于 2 个样本把号排除出选号"。
2. **没覆盖 ≠ 没有**：`supports_efforts` 为空时，档位信息是 unknown，不是"不支持"；`supportsImages` 缺失时同理。rea 原话：`Missing coverage is unknown, not absence`。
3. **依赖方向被未来改动破坏**：现状正确（server→pool 单向），但没有机器校验，一次随手 import 就会引入环。
4. **Unknown 无法消解**：「账号 tier 未知」现在只是个枚举值，没人跟踪"需要什么才能消解、消解了没"。
5. **半截流 vs 正常结束不可判定**：流式被掐断时客户端收到的字节与正常结束无区别标记。

---

### Task 1: 依赖方向守卫脚本

**Files:**
- Create: `scripts/check-boundaries.ps1`
- Modify: `.harness/rules/工程结构.md`（补"边界守卫"一节）
- Test: 手工运行验证（脚本本身即测试）

**Interfaces:**
- Consumes: `go list -deps` 输出、`go.mod` module 名 `yg-gateway`
- Produces: 退出码 0（通过）/ 非 0（违规），违规时打印 `违规: <from> -> <to>`

- [ ] **Step 1: 写脚本骨架**
  用 `go list -f '{{.ImportPath}} {{join .Imports " "}}' ./...` 取每个包的 import 列表，过滤出 `yg-gateway/internal/` 前缀的。

- [ ] **Step 2: 定义允许矩阵**
  白名单式（只允许下列方向，其余报错）：
  - `server` → 任意（顶层装配层）
  - `pool` → `auth`, `logfmt`
  - `upstream` → `auth`, `logfmt`, `session`
  - `session` → 无 internal 依赖（当前实测）
  - `panel` → 允许依赖 pool/upstream/reqlog/usage/scheduler（只读运维）
  - 禁止：`pool` → `server`、`upstream` → `panel`、任何包 → `server`（除 cmd）

- [ ] **Step 3: 运行验证当前仓库通过**
  Run: `pwsh -File scripts/check-boundaries.ps1`
  Expected: 退出码 0，输出 "边界检查通过"

- [ ] **Step 4: 故意制造违规验证脚本有牙齿**
  临时在 `internal/pool/` 某个文件加 `import "yg-gateway/internal/server"`，重跑：
  Expected: 退出码非 0，输出包含 `pool -> server`
  然后还原。

- [ ] **Step 5: 更新工程结构文档 + 提交**
  `.harness/rules/工程结构.md` 补一节说明守卫脚本位置与允许矩阵来源。
  ```bash
  git add scripts/check-boundaries.ps1 .harness/rules/工程结构.md
  git commit -m "chore: 加依赖方向守卫脚本，冻结现有单向依赖"
  ```

### Task 2: 顶层 AGENTS.md

**Files:**
- Create: `AGENTS.md`（仓库根）
- Modify: `README.md` 不动（有他人的未提交改动，禁止触碰）

**Interfaces:**
- Consumes: `.harness/rules/*.md` 现有三份规范、README 的定位段
- Produces: 仓库根硬约束文档，供所有 agent 会话首读

- [ ] **Step 1: 写 Product Direction 段**
  从 README 提炼定位：单二进制零依赖 / 养号友好 / 安全加固 / 可审计治理。写成"本项目是什么、不是什么"。

- [ ] **Step 2: 写 Boundary Contracts 段**
  把 R8（上游协议数据不改名：`wb2api-<ms>`、`chatcmpl-wb2api`、UA 指纹）等红线上提为顶层硬约束，明确列出。

- [ ] **Step 3: 写三态语义段（本轮新增的核心要求）**
  明确：对外暴露的事实必须区分 observation / derivation / inference / unknown。列出当前已知的推断值清单（成本 tier、EWMA、USD 折算价、`supports_efforts` 空、supportsImages 缺失）。

- [ ] **Step 4: 提交**
  ```bash
  git add AGENTS.md
  git commit -m "docs: 顶层 AGENTS.md，红线上提 + 三态语义要求"
  ```

### Task 3: `internal/evidence/` 四态标记

**Files:**
- Create: `internal/evidence/kind.go`
- Create: `internal/evidence/fact.go`
- Create: `internal/evidence/kind_test.go`

**Interfaces:**
- Produces:
  - `type Kind string`，常量 `KindObservation` / `KindDerivation` / `KindInference` / `KindUnknown`
  - `type Fact struct { Value any; Kind Kind; Basis string; Samples int }`
  - `func Observe(v any, basis string) Fact`
  - `func Derive(v any, basis string, samples int) Fact`
  - `func Infer(v any, basis string, samples int) Fact`
  - `func Unknown(basis string) Fact`
  - `func (f Fact) MarshalJSON() ([]byte, error)` — 输出带 `kind`/`basis`/`samples`
  - `func UnknownOf(basis string) Fact` — 供"没覆盖"场景

- Consumes: 无（纯 domain 层，零依赖）

- [ ] **Step 1: 写失败测试**
  ```go
  func TestFactKinds(t *testing.T) {
      o := evidence.Observe(402, "upstream status")
      if o.Kind != evidence.KindObservation { t.Fatal() }
      i := evidence.Infer("charged", "2 credit samples", 2)
      if i.Kind != evidence.KindInference || i.Samples != 2 { t.Fatal() }
      u := evidence.Unknown("no call yet")
      if u.Kind != evidence.KindUnknown { t.Fatal() }
  }
  ```

- [ ] **Step 2: 运行确认失败**
  Run: `go test ./internal/evidence/ -run TestFactKinds -v`
  Expected: FAIL（包不存在）

- [ ] **Step 3: 实现 `internal/evidence/kind.go` 与 `fact.go`**
  按 Interfaces 块的签名实现。`MarshalJSON` 输出 `{"value":...,"kind":"...","basis":"...","samples":N}`。

- [ ] **Step 4: 补 JSON 序列化测试**
  断言 `Observe` 编出的 JSON 含 `"kind":"observation"`。

- [ ] **Step 5: 运行确认通过**
  Run: `go test ./internal/evidence/ -v`
  Expected: PASS

- [ ] **Step 6: 提交**
  ```bash
  git add internal/evidence/
  git commit -m "feat(evidence): 观察/推断/未知四态标记（domain 层，零依赖）"
  ```

### Task 4: 成本分层与 EWMA 接入四态

**Files:**
- Modify: `internal/pool/state.go`（成本台账 `modelCosts` 相关）
- Modify: `internal/pool/pick.go`（EWMA 权重读取处）
- Modify: `internal/server/handler.go`（`/status` 的 `accounts[]` / `latency` 段）
- Test: `internal/pool/cost_evidence_test.go`（新建）、`internal/server/status_evidence_test.go`（新建）

**Interfaces:**
- Consumes: Task 3 的 `evidence.Fact` / `Observe` / `Infer` / `Unknown`
- Produces:
  - `/status` 的 `accounts[].model_costs[]` 每项带 `provenance`：`{"kind":"inference","samples":N,"basis":"实测 credit"}`
  - `/status` 的 `accounts[].ewma_success` 带 `provenance`：`{"kind":"derivation","basis":"EWMA alpha=..."}`
  - EWMA 样本 < `ewma_min_samples` 时 `kind` 为 `unknown` 而非输出数字

- [ ] **Step 1: 写失败测试（成本分层）**
  断言：给某账号 1 条 credit 观测后，`model_costs` 的 provenance `kind=="inference"`、`samples==1`；0 条观测时 `kind=="unknown"`。

- [ ] **Step 2: 运行确认失败**

- [ ] **Step 3: 改 `internal/pool/state.go` 成本台账**
  `NoteModelCost` 记录时同步累计样本数；`modelCosts` 查询接口返回带 `evidence.Fact` 的结构。

- [ ] **Step 4: 写失败测试（EWMA）**
  断言：`ewma_samples < min_samples` 时 `/status` 该字段 `kind=="unknown"`；达到阈值后 `kind=="derivation"`。

- [ ] **Step 5: 改 `internal/pool/pick.go` + `internal/server/handler.go`**
  `/status` 输出层包 `evidence.Fact`。选号逻辑本身**不改**（ADR-005 公式零改动）。

- [ ] **Step 6: 运行全量测试确认无回归**
  Run: `go test ./... -count=1`
  Expected: 21 包全绿

- [ ] **Step 7: 提交**

### Task 5: Unknown 消解实体（带 revision）

**Files:**
- Create: `internal/unknown/registry.go`
- Create: `internal/unknown/registry_test.go`
- Modify: `internal/server/handler.go`（`/status` 暴露 `unknowns` 段）

**Interfaces:**
- Consumes: Task 3 的 `evidence.KindUnknown`
- Produces:
  - `type Unknown struct { ID, Question, Requires string; Evidence []string; Revision int }`
  - `func (r *Registry) Record(question, requires string, ev ...string) Unknown`
  - `func (r *Registry) Resolve(id string, rev int, evidenceID string) (Unknown, error)` — revision 不匹配返回 stale 错误，调用方须重读
  - `func (r *Registry) List() []Unknown`

- [ ] **Step 1: 写失败测试**
  ```go
  func TestUnknownResolveStaleRevision(t *testing.T) {
      r := unknown.NewRegistry()
      u := r.Record("账号 u1 的 tier", "一次带 credit 的真实调用")
      if _, err := r.Resolve(u.ID, u.Revision+1, "ev-1"); err == nil {
          t.Fatal("stale revision 必须报错，不能盲重试")
      }
  }
  ```

- [ ] **Step 2: 运行确认失败**

- [ ] **Step 3: 实现 `internal/unknown/registry.go`**
  mutex + map，Revision 单调递增。

- [ ] **Step 4: 在成本分层的 unknown 路径登记**
  当某 (账号,模型) 无任何 credit 观测时，登记一条 Unknown：`"该号该模型成本未知"` / `"需要一次上游返回 credit 的调用"`。成功后由调用方 Resolve。

- [ ] **Step 5: `/status` 暴露 `unknowns` 数组**

- [ ] **Step 6: 全量测试 + 提交**

### Task 6: 上游收窄为 interface（原 P0-1，降至 P1）

**Files:**
- Create: `internal/upstream/codebuddy/adapter.go`
- Modify: `internal/upstream/client.go`（抽 interface）
- Modify: `internal/server/handler.go`、`internal/pool/pick.go`（改依赖 interface）

**Interfaces:**
- Produces: `type UpstreamClient interface { ChatStreamContext(...) ; RefreshTokenContext(...) ; FetchModels(...) }`
- Consumes: 现有 `*upstream.Client` 的方法集

> ⚠️ 本任务**零用户可见收益、风险最高**（动两个 2000+ 行文件的核心类型）。开始前需 YG 明确确认接受该投入。

- [ ] **Step 1: 定义 interface**（只含 handler/pool 实际用到的方法）
- [ ] **Step 2: 让 `*Client` 显式实现断言 `var _ UpstreamClient = (*Client)(nil)`**
- [ ] **Step 3: handler/pool 字段类型改 interface**
- [ ] **Step 4: 全量测试 + 提交**

### Task 7: skills description 重写 + references 分层

**Files:**
- Modify: `.harness/skills/*/SKILL.md`（11 个）
- Create: `.harness/skills/*/references/*.md`（按需）

- [ ] **Step 1: 每个 SKILL.md 的 description 加"什么时候跳过"**
  例：`request-analysis` 补"不要在纯 HTTP 网关改动时触发"。
- [ ] **Step 2: frontmatter 对齐 name/description/version**
- [ ] **Step 3: 主文件超过 300 行的拆 references/ 子目录**
- [ ] **Step 4: 提交**

---

## 执行顺序

```
Task 1 (XS) → Task 2 (XS) → Task 3 (S) → Task 4 (M) → Task 5 (M)
                                                    ↘ Task 7 (S)
                                          Task 6 (M, 需确认) 独立
```

Task 1+2 合计不到半天，建议立即执行。Task 3-5 是 rea 核心漏项补齐。Task 6 需你先确认。
