# 上游修复同步（P0 两条）+ 巡检机制 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把我方缺失的两条上游修复（空 `function.name` 剔除、`prompt_cache_key` 会话回退）按同源 patch 移植进来，并建立一个可复用的上游差异巡检脚本，使"上游修复及时搬进来"变成固定动作而不是等出事。

**Architecture:** 三条独立改动 + 一个巡检工具。修复 A（`tool_pairing.go`，+30 行）防"空 tool name 导致整会话每轮 503"；修复 B（`handler.go`，+10/-1）防"无 conversation_id 客户端共用一个前缀缓存槽"。两者都在我方代码里有逐字对应的落点（同源），属**适配移植**而非重写。巡检脚本用 `git` 无状态对比上游文件哈希，输出"上游已改而我方未跟"的清单。

**Tech Stack:** Go 1.27.2、PowerShell 5.1（宿主无 pwsh 7）、GitHub REST API（匿名，注意 60 req/h 限额）

**Spec:** 本计划即 spec。源头证据：
- 修复 A：`linguo2625469/workbuddy2api-panel@535e19e4dbdd0c611fad86226be7b529781c53c8`（2026-10-10）
- 修复 B：`linguo2625469/workbuddy2api-panel@20254d8290f13f5fc1b5f4702a9442dc20f3c5d6`（2026-10-08）

## Global Constraints

- Go 工具链 `C:\Program Files\Go\bin\go.exe`（≥1.27.2），`GOPROXY=https://goproxy.cn,direct`，`CGO_ENABLED=0`
- 零新依赖（`go.mod` 保持 4 个）
- **R8 红线**：上游协议数据不改名（`wb2api-<ms>`、`chatcmpl-wb2api`、UA 指纹）
- **R10**：错误不静默吞（禁 `_ =` 吞业务错误）
- **R15**：二进制替换前必须备份
- 门禁：`go test ./... -count=1` 全绿、`go vet ./...` 无输出、`gofmt -l` 新文件干净、`scripts/check-boundaries.ps1` 退出码 0
- 本机无 gcc，`-race` 不可用；并发正确性靠设计 + 单测（可用 `-count=N` 重复跑代替）
- 线上实例：`D:\yg-gateway`（端口 7863），改完必须真机冒烟

## Review Focus

1. **移植不要夹带副作用**：修复 A 的上游实现**刻意仍登记 `callIDs`**（保住 `hasTraffic` 语义），只在 `keepCalls` 一处否决。若"顺手优化"成"空 name 不登记 callIDs"，会让"空 name 且无结果"的脏条目**漏出站**——上游有专门的回归用例钉这一点。移植时必须保留该结构。
2. **判定面不要扩大**：`toolCallNameEmpty` 只认「`function` 对象存在、`name` 字段存在且为空白串」。缺 `function` 或缺 `name` 字段的占位条目不判脏（上游不因此拒参）。
3. **修复 B 只改 `ConversationID` 取值**：带 `conversation_id` 的客户端行为必须逐位不变；`sessKey` 为空时行为也必须不变（否则会影响粘性/轮级键）。
4. **巡检脚本要无状态**：不要缓存上游文件内容到仓库（会腐烂）；每次现拉现比，且要能离线降级（API 限流时不报假失败）。
5. **真机验证必须覆盖负例**：不只是"成功路径没坏"，还要构造空 tool name 的请求验证不再产生 11133。

---

### Task 1: 上游差异巡检脚本

为什么先做这个：你有很多本地增强（evidence/metrics/unknown），与上游同源于早期某点。**只有脚本化的差异清单才能回答"上游还有哪些修复我没搬"**——上一轮我手工只查了 4 个点，可能还有遗漏。

**Files:**
- Create: `scripts/check-upstream-drift.ps1`
- Create: `.harness/rules/UPSTREAM-SYNC.md`（记录巡检流程与"采纳/不采纳"判据）

**Interfaces:**
- Consumes: GitHub REST API（`/repos/{owner}/{repo}/git/trees/HEAD?recursive=1`、`/commits?per_page=N`、`/commits/{sha}`）
- Produces: 控制台报告（三分类：`上游新增文件` / `上游修改而我方未跟` / `我方新增`）+ 退出码（0 = 无未跟进项，1 = 有）

- [ ] **Step 1: 写脚本骨架**

  参数：`-Upstream <owner/repo>`（默认 `linguo2625469/workbuddy2api-panel`）、`-CheckCommits <N>`（默认 40，检查最近 N 个提交涉及的文件是否已在本地跟进）。

  核心逻辑：
  1. 拉上游 tree，得 `path → blob sha` 映射
  2. 计算本地同名文件的 sha（`git hash-object <file>`，与 GitHub blob sha 同算法，**不依赖网络**）
  3. 分类输出：仅有上游 / 双方都有但 sha 不同 / 仅本地有
  4. 对"双方都有但 sha 不同"的文件，回溯最近 N 个 commit 里哪些改过它 → 输出"上游已改、我方未跟"的 commit 清单

- [ ] **Step 2: 处理 API 限流与离线**

  匿名 GitHub API 限 60 次/小时。脚本必须：
  - 只发 **1 次** tree 请求 + **1 次** commits 请求（不逐 commit 拉详情，除非用户加 `-Deep`）
  - 遇到 `403 rate limit` 时**打印明确提示并以退出码 2 退出**（区别于"有差异"的 1），绝不把限流报成"无差异"
  - 支持 `-Offline`：只用本地已有的 `.git` 与上一次报告缓存（若存在）做对比

- [ ] **Step 3: 运行并产出清单**

  Run: `powershell -NoProfile -File scripts/check-upstream-drift.ps1`
  Expected: 输出三分类清单。**把"上游已改、我方未跟"的条目记下来**——这是本计划 Task 2/3 之外的额外发现，追加到 `.harness/rules/UPSTREAM-SYNC.md` 的待办表。

- [ ] **Step 4: 写 UPSTREAM-SYNC.md**

  内容：巡检命令、判据（**采纳**：fix/ 类、安全类、防会话死亡类；**不采纳**：与我方定位冲突的，如"移除入站 body 上限"、"api_key 为空即放行"、纯上游业务变更）、以及"不要逐字照搬上游注释风格"的说明（我方注释应写自己的实测依据，不复制对方的实测数字）。

- [ ] **Step 5: 提交**

  ```bash
  git add scripts/check-upstream-drift.ps1 .harness/rules/UPSTREAM-SYNC.md
  git commit -m "chore: 上游差异巡检脚本 + 同步判据"
  ```

### Task 2: 修复 A — 剔除空 function.name 的 tool_call

**为什么**：上游流式响应在 tool_call name 分片到达前异常结束（含 200 空流）时，聚合器产出 `name:""` 条目 → 客户端写进历史 → 下轮出站被上游判 `11133 tool_calls[i].function.name must be a non-empty string` → **整条会话从此每轮 503 no_healthy_account，只能手动清空历史**。这是会话级永久故障，优先级高于任何新功能。

**Files:**
- Modify: `internal/upstream/tool_pairing.go`（+1 import，+~25 行）
- Modify: `internal/upstream/tool_pairing_test.go`（+5 个子用例）

**Interfaces:**
- Consumes: 现有 `cleanupOrphanToolCalls(messages []any) ([]any, bool)`
- Produces: `toolCallNameEmpty(tc map[string]any) bool`（包内私有）

- [ ] **Step 1: 写失败的测试**

  在 `tool_pairing_test.go` 加 `TestCleanupOrphanToolCallsEmptyName`，5 个子用例（照上游语义，但注释用我方口径）：
  1. **空 name 带结果** → 调用与结果成对剔除，assistant 消息保留
  2. **空 name 且无结果** → 仍走孤儿路径删除（**这是防"顺手优化"的回归钉桩**：若改成"空 name 不登记 callIDs"，`hasTraffic` 会变 false 而提前 return，脏条目反而漏出站）
  3. **纯空白 name（`"   "`）** → 与空串同判
  4. **批内混合** → 只剔脏项，健康项不动（`[c1 脏, c2 健康]` → 只剩 `assistant(c2)` + `tool(c2)`）
  5. **缺 function / 缺 name 字段** → 不判脏，`changed` 为 false（不扩大打击面）

- [ ] **Step 2: 运行确认失败**

  Run: `go test ./internal/upstream/ -run TestCleanupOrphanToolCallsEmptyName -count=1`
  Expected: FAIL（`toolCallNameEmpty` 未定义）

- [ ] **Step 3: 实现**

  `tool_pairing.go`：
  1. 加 `"strings"` 到 import
  2. `cleanupOrphanToolCalls` 收集阶段加 `nameBadIDs := map[string]bool{}`；在**已有的** `callIDs[id] = true` 之后追加 `if toolCallNameEmpty(tc) { nameBadIDs[id] = true }`（注意：**不改变**已登记的 `callIDs`/`hasTraffic`）
  3. `keepCalls` 循环加 `if nameBadIDs[id] { continue }`
  4. 文件末加 `toolCallNameEmpty`：仅当 `function` 是 `map[string]any` **且** `name` 存在且 `strings.TrimSpace(name) == ""` 时返回 true

  > 注意判据必须是 `name, ok := fn["name"].(string); if !ok { return false }`——不能写成 `strings.TrimSpace(strField(tc, "name")) == ""`（那会把"字段缺失"也判脏，扩大打击面）。

- [ ] **Step 4: 运行确认通过**

  Run: `go test ./internal/upstream/ -count=1`
  Expected: PASS（含既有全部用例——上游该修复的既有测试"零改动通过"）

- [ ] **Step 5: 提交**

  ```bash
  git add internal/upstream/tool_pairing.go internal/upstream/tool_pairing_test.go
  git commit -m "fix(upstream): 剔除空 function.name 的 tool_call，防整条会话 11133 死锁"
  ```

### Task 3: 修复 B — 无 conversation_id 时 prompt_cache_key 回退会话 key

**为什么**：`cache_key.go` 现在用 `buildCacheKey(uid, conversationID)`，`conversationID` 为空时哈希输入退化成 `uid + "|"` ——即 **`wb2a-<uid8>-<定值>`，每个账号一个常量**。同一账号下所有不带 `conversation_id` 的客户端（Anthropic / Claude Code 流量）于是共用一个上游前缀缓存槽，互相覆盖 → 前缀命中不稳定、常整段 miss。

**Files:**
- Modify: `internal/server/handler.go`（行 ~1123，+10/-1）

**Interfaces:**
- Consumes: 已在行 936 定义的 `sessKey`（`session.ExtractKeyOptView`，作用域覆盖修改点）、`session.ResolveConversationIDView(&reqView)`
- Produces: 无新导出符号；仅改变 `upstream.ChatMeta.ConversationID` 的取值

- [ ] **Step 1: 写失败的测试**

  在 `internal/server/` 加或用既有 handler 测试框架加一个用例：**入站 body 不带 conversation_id 时，出站 `prompt_cache_key` 必须由 sessKey 派生**。

  实现要点：用两个**不同会话**（不同 system/首条 user）但**同一账号**的请求，断言两者出站的 `prompt_cache_key` **不同**；再断言同一会话重复请求时 `prompt_cache_key` 相同。

  > 若既有测试框架不便捕获出站 body，可在 `internal/upstream` 层加一个针对 `promptCacheKeyFrom(obj, uid, conversationID)` 的用例，断言 `conversationID=""` 与 `conversationID="sess-key"` 产出不同键——**这更简单且同样能钉住回归**。

- [ ] **Step 2: 运行确认失败**

  Run: `go test ./internal/server/ -run CacheKey -count=1`（或对应新测试名）
  Expected: FAIL

- [ ] **Step 3: 实现**

  `handler.go` 行 ~1123 改为：

  ```go
  // ConversationID 透传客户端会话 id。**无 conversation_id 的客户端**（Anthropic /
  // Claude Code 流量）回退到 sessKey —— 否则上游 prompt_cache_key 退化成
  // 「每账号一个常量」，同账号下所有客户端共用一个前缀缓存槽、互相覆盖导致整段 miss。
  // 只影响 prompt_cache_key 取值：带 conversation_id 的客户端逐位不变，
  // sessKey 为空时行为也不变。
  convID := session.ResolveConversationIDView(&reqView)
  if convID == "" {
      convID = sessKey
  }
  chatMeta := upstream.ChatMeta{ConversationID: convID}
  ```

  > 注意用 `ResolveConversationIDView(&reqView)`（我方 view 版本），不是上游的 `ResolveConversationID(body)`。

- [ ] **Step 4: 运行确认通过 + 全量回归**

  Run: `go test ./internal/server/ ./internal/upstream/ -count=1 && go test ./... -count=1`
  Expected: 全绿

- [ ] **Step 5: 确认无副作用**

  逐项确认（可 grep 断言）：
  - 带 `conversation_id` 的请求：`ResolveConversationIDView` 非空 → `convID` 原值 → 行为不变
  - `sessKey == ""` 的请求：`convID` 仍为 "" → `buildCacheKey(uid, "")` 行为不变
  - 粘性/轮级键：`sessKey`/`turnKey` 的使用点（行 1126-1131）**未被改动**

- [ ] **Step 6: 提交**

  ```bash
  git add internal/server/handler.go internal/upstream/cache_key_test.go
  git commit -m "fix(server): 无 conversation_id 时 prompt_cache_key 回退会话 key"
  ```

### Task 4: 门禁 + 部署 + 真机负例验证

**Files:**
- Modify: 无（部署动作）
- 产出：`D:\yg-gateway\yg-gateway.exe`（替换）、备份 `yg-gateway.exe.pre-sync.bak`

- [ ] **Step 1: 跑完整门禁**

  ```powershell
  go build ./... ; go vet ./... ; go test ./... -count=1
  gofmt -l ./cmd ./internal          # 只允许 pre-existing 漂移
  powershell -NoProfile -File scripts/check-boundaries.ps1
  ```
  Expected: 21+ 包全绿、vet 空、边界退出码 0

- [ ] **Step 2: 重复跑并发相关包（代替 -race）**

  Run: `go test ./internal/pool/ ./internal/upstream/ ./internal/server/ -count=3`
  Expected: 全绿（本机无 gcc，用重复执行替代竞态检测）

- [ ] **Step 3: 部署（R15 备份 + 单命令构建复制启动）**

  ```powershell
  Copy-Item D:\yg-gateway\yg-gateway.exe D:\yg-gateway\yg-gateway.exe.pre-sync.bak -Force
  Get-Process yg-gateway -EA SilentlyContinue | Stop-Process -Force
  # 构建 + 复制 + 启动放同一条命令（跨命令空档是实时防护下手时机）
  ```

- [ ] **Step 4: 冒烟（成功路径）**

  `/healthz` 200 → `/v1/models` 200 → 流式与非流式各一发 200。

- [ ] **Step 5: 冒烟（修复 A 的负例——最关键）**

  构造一个带空 `function.name` 的 tool_call 历史，发一轮请求：

  ```json
  {"model":"cn:deepseek-v4.1-flash","messages":[
    {"role":"user","content":"q"},
    {"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"","arguments":"{}"}}]},
    {"role":"tool","tool_call_id":"c1","content":"r1"},
    {"role":"user","content":"继续"}
  ],"stream":false}
  ```

  Expected: **200**（网关在出站前剔除了脏条目），而不是 `503 no_healthy_account`。
  修复前该请求会命中上游 11133 并（在旧分类下）轮转全池。

- [ ] **Step 6: 冒烟（修复 B 的行为差异）**

  发两个**不同会话**（不同首条 user）、**不带 conversation_id** 的请求，从日志或上游 usage 确认 `prompt_cache_key` 不同；同一会话重复发确认相同。

- [ ] **Step 7: 确认账号未被误伤**

  `/status` 检查所有账号 `disabled=false`、`cooling=false`、`consecutive_fails=0`。

---

## 执行顺序与依赖

```
Task 1（巡检，独立，先做——可能发现更多待搬项）
   ↓
Task 2（修复 A）──┐
Task 3（修复 B）──┴─→ Task 4（门禁 + 部署 + 真机验证）
```

Task 2 与 Task 3 相互独立，可并行；Task 4 依赖两者都完成。

## 预期产出

1. `scripts/check-upstream-drift.ps1` + `.harness/rules/UPSTREAM-SYNC.md` —— 把"同步上游"从临时动作变成固定流程
2. 两条会话级/缓存级修复落地 + 各自的钉桩测试
3. 真机负例验证记录（空 tool name 请求从 503 变 200）
4. 追加到 `.harness/changes/` 的变更总结
