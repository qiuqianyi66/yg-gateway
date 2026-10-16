# 变更总结 — 工程深度治理 Task 4/5/6 收口（2026-10-16）

> 承接 `docs/superpowers/plans/2026-10-15-engineering-depth-hardening.md`（Task 1–3 已完成）。
> 本次把**剩余三块**做完：Task 5 覆盖率补强 → Task 4 错误处理审计收尾 → Task 6 规范固化为机器检查。
> 约束不变：零新功能、零新依赖（`go.mod` 直接依赖仍为 1 个）、不改上游协议报文数据。

## 一、改了什么

### Task 5 覆盖率补强（8 个提交中的 4 个测试提交）

**先补覆盖、再用变异测试证牙齿**。补覆盖由并行 agent 完成（每包一个新测试文件、只写测试），
随后对 4 个包做**变异测试**：在 `%TEMP%` 的一次性副本里改生产代码，看测试是否 FAIL。
实测出漏网行为后逐条补断言——这一步是本轮最有价值的部分，它证明「覆盖率涨了」不等于
「行为被钉住了」。

| 包 | 覆盖前 | 覆盖后 | 目标 | 变异实证 |
|---|---|---|---|---|
| upstream | 56.8% | **89.5%** | ≥70% | 两个 agent 分工（解析/目录族 + HTTP 序列族） |
| panel | 13.6% | **95.5%** | ≥40% | handler 层 31 测试 + autotask 层 120 测试 |
| logfmt | 33.3% | **100%** | +10pp | 变异 2/5 → 补牙后 8/8 全捕获 |
| prompt | 41.3% | **93.7%** | +10pp | 变异 3/5 → 5 个顶层字段补齐后全捕获 |
| redisstore | 16.8% | **98.9%** | +10pp | 变异 3/5 → 8/8（含自建假 RESP2 服务器） |
| cmd/login | 24.8% | **99.1%** | +10pp | 变异 1/5 → 11/11 |
| auth | 68.8% | **99.2%** | +10pp | 33 变异实验 / 27 killed（存活 2 条经证明语义等价） |
| session | 67.2% | **98.8%** | +10pp | 10/10 |
| scheduler | 67.5% | **98.5%** | +10pp | 逐条对应 |
| cmd/server | 48.6% | **72.3%** | +10pp | 纯函数与 normalize 不变量 |

**最低包覆盖率：13.6% → 71.9%**（棘轮已抬高，此后不许回退）。

### Task 4 错误处理审计收尾

判据见 `.harness/rules/ERROR-HANDLING.md`（本轮更新）。131 处忽略点已**全部过判据**。
修掉 5 处「真吞错误」：

| 位置 | 问题 | 修法 |
|---|---|---|
| `internal/panel/autotask.go` `runAutoAll` | 进度回读错误被 `_ =` 吞掉 → `after` 为 nil 时**自动领奖静默跳过**，与「任务不存在」不可区分 | 捕获 error + WARN（含脱敏 `logfmt.Label`）；返回值格式不变 |
| `internal/panel/taskcenter.go` `runGrowthQueued` | 同一语义的第二处实现（必须同改，否则口径分叉） | 同上 |
| `cmd/credit/main.go` | `filepath.Glob` 错误被吞 → 报「0 个账号」且 exit 0，把**配置写错伪装成没有账号** | stderr + `os.Exit(1)` |
| `internal/reqlog/reqlog.go` `closeFile` | 轮转/停机时 `_ = w.buf.Flush()` 会静默丢掉**最多 64KB 审计日志** | 改走 `flush()`，失败经 `setErr` 进 `stats().LastError` |
| `cmd/server/main.go` 停机 | `_ = store.Close()` ——「最后一笔 Redis 镜像没写完」完全不可见 | 包 `if err != nil { WARN }` |

另给 5 处「容易被误读为疏漏」的忽略点补了判据注释（`pool/persist.go`、`upstream/model_catalog.go`
的 MkdirAll 走判据③；`redisstore`、`reqlog` 的 Close 与 main 停机 Close 走判据②）。

**修正判据文档的口径失真**：旧版 §二 写「类型断言 6 处」是抽样估计，实测按行计数是 **73 行**
（总 131 行）。现已把唯一权威测量命令写死在文档里。

### Task 6 规范固化为机器检查

- 新增 `scripts/pre-commit.ps1`：一次跑 gofmt 零漂移（仅本次改动文件）+ R10 判据
  （新增静默忽略必须带解释注释；`_ = <标识符>` 压制 unused 一律拒绝）+ `go build` + `go vet` +
  `go test ./...` + `check-boundaries` + `check-depth`。`-Fast` 跳测试，`-Install` 装 git hook。
- `.harness/rules/编码规范.md` 新增**第九节「体量与结构」**：函数 ≤120 代码行、文件 ≤1000 行、
  嵌套 ≤4、新增包级可变全局必须注释、新文件 gofmt 零容忍，并写明**棘轮语义**（只许收紧）。
- `.harness/rules/开发流程规范.md` 第五节把该脚本列为「提交前必跑总闸」（与部署前备份同级）。

## 二、怎么验证的

1. **四件套**：`go build ./...` / `go vet ./...` / `gofmt -l ./cmd ./internal`（空）/ `go test ./... -count=1`（20 包 ok）。
2. **治理门禁**：`check-boundaries.ps1` 退出 0；`check-depth.ps1` 未回退，`-Update` 抬高 `minCoverage`。
3. **牙齿（硬证据，不是自述）**：
   - 4 个包做了变异测试，存活者逐条判定为「语义等价」或「不可达」并写进提交 body；
   - `scripts/pre-commit.ps1` 本身做了反向验证：故意制造 gofmt 漂移 + `_ = <标识符>` + 未论证忽略
     → 退出码 1 且逐条点名；还原后文件 hash 与原始一致。
4. **收口门禁**：提交后再跑一次 `scripts/pre-commit.ps1` 全量 → 退出码 0。
5. **零生产代码改动**（除 Task 4 的 5 处修复 + 注释）：覆盖率补强的每个 agent 都自检
   `git diff -- <包>` 为空，未改 `go.mod`/`go.sum`。

## 三、怎么回滚

| 层 | 回滚 |
|---|---|
| 覆盖率测试 | 删掉各包新增的 `coverage_*.go` / `fake_resp_test.go`（纯增量，无生产依赖） |
| Task 4 修复 | `git revert 2ada6ed`；两处 panel WARN 是纯增量，单独回退也不影响行为 |
| Task 6 | `git revert c68155a`（脚本与两份规范章节）；`scripts/pre-commit.ps1 -Install` 若已装，
  删 `.git/hooks/pre-commit` |
| 棘轮 | `git revert 30c9b12` 会把基线降回 13.6%——**不建议**，除非同时删掉新增测试 |

## 四、遗留（已知、未做，避免默默带过）

1. **flake**：`go test ./internal/server/ -count=3` 的 `TestModelsEndpoint` 测试间全局状态污染
   （与本次改动无关，`-count=1` 全绿）；`TestEwmaLatencyPenalizesSlowInPick` 时间敏感偶发。
2. **测试时长**：panel 包单轮 44–60s（autotask 的 mp 上报路径含 0–10s 随机反作弊抖动，非可注入）。
3. **`runAutoAll` 与队列路径对「达标未领」处理不一致**（前者跳过不领、后者入队去领）——
   由 panel agent 报出，属产品语义，未单方面改。
4. **`internal/session` 的 legacy 派生分支**已不在热路径上，其覆盖率计入 98.8% 但生产可达语句
   约 89%（agent 主动声明，勿对外把 98.8% 说成热路径覆盖率）。
5. **`cmd/login` 的 4xx 全归「登录未完成」**、`server/anthropic.go` 的 `stream` 类型错被吞——
   均属「改口径」而非「补可观测性」，已写入 ERROR-HANDLING.md 的「留待产品定夺」表。
6. **上游协议相关的真问题**（均只记录、未改）：`RunNightChats` 硬编码 4s sleep 不可注入；
   `reqlog`/`redisstore` 的停机超时在高积压下不够；`DesktopChatWithExpert` 每 Read 只推进一个候选。
