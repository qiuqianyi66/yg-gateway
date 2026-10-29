# 2026-10-29-snapshot — 账号现态每日快照

> 状态：✅ 已落地（main 分支）。改动范围：scheduler（快照任务）+ cmd/server
> （装配）。未触碰并行会话工作区。

## 改了什么

1. **`internal/scheduler/snapshot.go`（新）**：`RunSnapshotNow()`——`Pool.List()`
   全量现态落盘 `<SnapshotDir>/YYYY-MM-DD.json`（json.MarshalIndent + tmp+rename
   原子写，防半截文件；MkdirAll 容错目录缺失）。`snapshotNow` 可替换时钟
   （测试钉住日期，防午夜翻转）。四态：内容是 observation（池直接观测现态）；
   曲线消费是后续轮次（unknown + requires 快照积累）。
2. **`internal/scheduler/scheduler.go`**：`taskSnapshot` 新任务类型 +
   `SnapshotHours`（默认 [3]）+ `SnapshotDir`（空=禁用）；`nextWake` 仅在
   SnapshotDir 非空时纳入时点（对既有测试零影响）；`runBatch` 派发到
   `RunSnapshotNow`。**不进 Reconfigure**（时点固定无需热改，签名零变化）。
3. **`cmd/server/main.go`**：`SnapshotDir: stateSibling(cfg.StateFile, "snapshots")`
   ——快照目录与 state.json 同风格（Docker volume 持久化路径），生产为
   `D:/yg-gateway/data/snapshots/`。
4. **`internal/scheduler/snapshot_test.go`（新）**：4 个专项测试——落盘内容
   （captured_at + accounts）、R1 无凭证、未配置跳过、nextWake 纳入、目录容错。

## 怎么验证的

- `go test ./internal/scheduler/ -run TestSnapshot -v`：4/4 PASS；
- `go test ./internal/scheduler/ ./cmd/server/ -count=1`：全量零回归；
- pre-commit 11/11 全绿（-Fast 时 go test 已单独跑过）。

## 怎么回滚

- 代码回滚：`git revert` 本次提交即可（taskSnapshot 移除、快照目录不再装配；
  未配置 SnapshotDir 时行为与回滚前逐字一致）。
- 运行期：本功能纯读 Pool + 写本地文件，无上游调用、无状态变更、无新出站；
  下线不改变任何路由/选号/记账行为。已落盘快照文件保留（只增不改）。

## 已知 unknown / 边界

- **历史存活曲线消费**：本版只落盘不消费。曲线/到期曲线的展示需要：① 快照
  积累 ≥2 天；② 前端或 benchmark 读取快照目录合成。标 unknown，
  requires = 快照积累 + 消费轮次。
- **时点固定**：默认 [3]（避开签到 9/21、旅行 9/21、活跃 10、保活 22、黑猫
  23、成长 1）。不进 Reconfigure——若未来需要热改时点再评估。
- **快照保留策略**：不清理旧文件（每日 ~几 KB，7 天量级无压力）；若未来长期
  运行需保留策略，另行决策。
