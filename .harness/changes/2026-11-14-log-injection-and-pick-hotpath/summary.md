# 变更记录 — 日志注入防护 + Pick 热路径优化（2026-11-14）

## 0. 必要性（阶段 0 四问）

1. **要解决的真实问题**
   - 日志注入（观察，可复现）：`POST /v1/chat/completions` 的 `model` 是客户端完全可控的
     JSON 字符串，`"model": "a\n2026-11-14 INFO: 账号已禁用"` 经 `json.Unmarshal` 解出
     真实换行后直接进 `log.Printf` / 表格流水行 → 伪造出第二条审计日志行。昵称（来自
     `auths/<uid>.json`）、请求 ID、上游错误 body 同理。
   - 选号热路径开销（实测）：`BenchmarkPick46Accounts` 7.4µs/op、12 allocs/op，是每请求
     必经路径上最贵的单点（对比认证 101ns / VerifyBearer 152ns）。
2. **不做会怎样**：日志注入会污染审计与排障依据（伪造"账号已禁用""余额耗尽"之类记录，
   误导后续操作），且不报错；Pick 开销在 46 账号规模已占每请求的主要 CPU，池子越大越贵。
3. **现状是否已变**：日志此前只有 JSON 落盘路径安全（`json.Marshal` 转义），文本日志与
   面板环形缓冲两条路径无任何单行化；Pick 此前用 `sort.SliceStable`（reflect 实现）+
   无预分配切片。
4. **更便宜的等效手段**：无。日志侧逐调用点清洗必然漏（17+ 处 `log.Printf` 带外部字符串，
   新增一处就漏一处），只能在唯一写出口兜底；Pick 侧三个改动都是纯机械替换，无行为差异。

结论：**做**。

## 1. 改了什么

### A. 日志注入防护（安全）

| 位置 | 改动 |
|---|---|
| `internal/logfmt/logfmt.go` | 新增 `OneLine(s)`：C0 控制字符与 DEL 压平为空格，无控制字符时零分配 |
| `internal/logfmt/oneline_writer.go`（新） | `OneLineWriter`：消息级兜底，压平每条日志内部的换行、保留末尾分隔符 |
| `cmd/server/main.go` | `log.SetOutput` 与 `SetChatLogOutput` 包一层 `OneLineWriter`（**唯一写出口**，覆盖现在与将来的所有日志调用） |
| `internal/server/logging.go` | 表格流水行的 model / 账号标签 / requestID / clientIP / UA 逐个 `logfmt.OneLine`（直接 `Fprintf` 路径不经 log 包，需单独清洗） |

**语义边界**：只压平不删除、不截断——原文仍在同一行可 grep；`Truncate` 的契约
（"原串 TrimSpace 后的前缀"，有 fuzzy 测试锁定）**未改动**，两者职责分离。

### B. Pick 热路径优化（算法）

| 位置 | 改动 | 依据 |
|---|---|---|
| `internal/pool/pick.go` | `sort.SliceStable` → `slices.SortFunc` | 比较器是严格全序（cost1k → w → UID，UID 唯一），稳定与不稳定排序结果逐位相同；`SliceStable` 走 reflect.Swapper |
| 同上 | `cands` 按 `len(p.byUID)` 预分配 | 原 `var cands []*entry` 逐次 append，46 账号触发 6-7 次扩容分配 |
| 同上 | `eligible` 改固定数组 `[5]*entry` | 至多 len(cands)≤5 个元素，免堆分配 |

## 2. 怎么验证

**测试（全部带牙齿）**

- `internal/logfmt/oneline_test.go`：`TestOneLinePinsNewline`（\n/\r/\t/\x00/DEL 各压平）、
  `TestOneLineNeverEmitsNewline`。还原 `OneLine` 为恒等函数 → 必 FAIL。
- `internal/logfmt/oneline_writer_test.go`：消息体压平、干净消息零拷贝直通、任意输入
  换行数 ≤1、nil writer 静默消费。
- `internal/server/logging_oneline_test.go`：`TestChatRowModelCannotForgeSecondLine`
  （model 含换行 → 表格行仍 1 行）、`TestChatRowExtraFieldsCannotForgeSecondLine`。
  去掉 `logChatRowEx` 里的 `logfmt.OneLine` → 两条必 FAIL（已实测：第一条在修复前
  FAIL、修复后 PASS）。
- `internal/pool` 全量测试通过（`-run Pick` 与全包 `go test ./internal/pool` 均 ok），
  证明排序替换零行为差异。

**实测（本地，`-benchtime=2s -count=3`）**

| 指标 | 改前 | 改后 | 变化 |
|---|---|---|---|
| Pick46 ns/op | 7.4µs（7175-7578） | 4.4-4.7µs（4404-4654） | **-39%** |
| Pick46 allocs/op | 12 | 5 | **-58%** |
| OneLine 干净路径 | — | 9.5ns / 0 alloc | 零分配快速路径 |
| OneLine 含控制字符 | — | 40ns / 1 alloc | 只在真需要时分配 |

**门禁**：`scripts/pre-commit.ps1 -Fast -AllFiles -Base v2.1.0` 11 项全过（gofmt、R10、
build/vet、全量测试、边界、深度棘轮、必要性、治理体检、上下文预算、R6）。

## 3. 怎么回滚

三处独立可回退，互不依赖：
- 日志：删掉 `main.go` 两行 `OneLineWriter` 包装即恢复原样（`OneLine` 函数保留无副作用）。
- 排序：把 `slices.SortFunc` 块换回 `sort.SliceStable` 三元比较。
- 分配：`cands`/`eligible` 恢复原声明。

## 4. 未做（明确登记，不扩范围）

- `candTier`/`candCost` 两条平行切片仍各分配一次（可合并进 `weighted` 再原地过滤，
  预计再省 2 次分配、<0.3µs）——收益递减且触及探索层判定逻辑，留作后续单独立项。
- `chatrun.go` / `handler_provider.go` 等 17 处 `log.Printf` 的逐点清洗**未做**：
  已由写出口的 `OneLineWriter` 覆盖，逐点清洗属于重复劳动（保留调用点原样便于阅读）。
