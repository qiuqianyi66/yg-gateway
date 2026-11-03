# 模板：性能优化（profile → 瓶颈 → 优化 → 验证）

> 用法：整段粘贴，替换 `<...>`。适用于"慢 / 内存涨 / 棘轮报警"。
> 纪律先行：**先量化再动手**。没有基线数字的"优化"无法证伪，等于改风格。

---

目标：**<哪个指标 / 哪个端点 / 哪条链路>** 当前 **<基线数字 + 测量方式>**，
期望 **<目标数字>**。约束：**<延迟预算 / 内存预算 / 不许改的契约>**。

## 第 1 段：立基线（必须先有数）

- 从**已有度量**取数，别新造一套：
  - 请求级：`internal/reqlog` 归档（`/panel/api/*` 现算的聚合）
  - 账号级：`/status` 的 EWMA / 分位数（**派生**值，注明"分桶插值估算，非精确值"）
  - 网关级：`internal/bench`（M1 选号效果 / M2 池健康度 / M3 路由准确率，纯离线可复现）
- 记下**测量命令 + 原始输出**。同一命令跑两次数字应接近，否则先查噪声源。

> 四态纪律：基线是 observation（实测）；"优化后应该更快"是 inference，不许写进结论。

## 第 2 段：找瓶颈（profile 或等价证据）

- 有 profile 就用 profile；本机无 gcc、`-race` 由 CI 独占，别在本地硬跑竞态检测
- 没有 profile 时，用**可观测证据**定位：耗时分布、锁等待、上游请求量、分配次数
- 热路径优先级（实测体量最大的生产文件）：
  `upstream/sse.go` → `usage/usage.go` → `server/handler.go` → `panel/panel.go` →
  `server/chatrun.go` → `pool/state.go`
- 结论要写成机制："每次请求多一次 `json.Unmarshal` 全量 body"，
  不是"这里可能慢"

## 第 3 段：优化（最小、可回滚）

- 一次只改**一个**瓶颈，改完立刻测——多处同改会让归因失效
- 优先顺序：去掉重复工作 > 减少分配 > 换数据结构 > 并发（并发最后，它引入 R9 风险）
- 并发改动必须能回答：goroutine 谁回收（R9）、ctx 取消路径、锁是否覆盖 IO
- **禁止**的手段：放宽 `scripts/check-depth.ps1` 阈值、`-Update` 掩盖回退、
  去掉日志/校验换速度、改上游报文形态（R8）

## 第 4 段：验证（三个都要）

1. **指标**：同一条测量命令重跑，贴前后原始输出
2. **正确性**：`go test ./...` 全绿；性能改动必须带一条能证伪的测试（回退改动应红）
3. **棘轮**：`check-depth.ps1`（文件/函数体量、覆盖率）与 `check-context-budget.ps1`
   未回退；指标**确实变好**时才 `-Update` 收紧，并把 json 一起提交

```powershell
go test ./...
powershell -NoProfile -File scripts/check-depth.ps1
powershell -NoProfile -File scripts/pre-commit.ps1
```

## 收尾

落 `.harness/changes/<feat>/summary.md`：

- 基线 → 目标 → 实测（三段数字 + 命令）
- 瓶颈机制（为什么慢，含 file:line）
- 改了什么、放弃什么（若为速度牺牲了可读性，写清代价与重评条件）
- 回滚方式（单提交可 revert；若动了数据结构，写迁移往返）
