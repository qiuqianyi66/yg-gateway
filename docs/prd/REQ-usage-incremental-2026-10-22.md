# REQ：usage Snapshot 读路径优化 + 增量用量落库（R-205）

> 状态：v1（2026-10-22，本分支 `usage-incremental`）。P2，并案一个改动单。
> 受 ADR-009 版本门约束（文件版本 > 程序 → 拒绝启动）。

---

## 一、必要性（阶段 0，四个问题）

**Q1 要解决的真实问题是什么？**

两个实测热点（`internal/usage/usage.go`）：

1. **SnapshotWindow 锁内全量深拷贝**（:674-679）：面板每次拉用量视图，都在 `r.mu` 持锁下
   把全部桶（桶上限 400k × ~90B ≈ **36MB**）逐桶 `*b` 值拷贝一遍，解锁后再聚合 9 维累加器。
   桶越多，持锁越久——面板刷新期间所有 `Add()`（每次聊天请求一次）被堵。
2. **flush 每 30s 全量重写**（:402-442）：锁内拷贝全部桶 + monthly map，整体 marshal 成
   JSON（数十 MB）tmp+rename。与桶数成正比，大桶场景每 30 秒把同样的内容整体重写一遍。

**Q2 不做会怎样？** 面板刷新卡顿叠加在聊天请求上（个人网关低 QPS，当前不致命，但随账号/
模型/时间片增长是线性恶化）；磁盘 I/O 与 CPU 做大量重复无用功。不报错，静默损耗。

**Q3 现状是不是已经变了？** 桶上限 ADR-011（400k + rollup）立了之后，这两个热点的量级
从「几百桶」变成「可达 400k 桶」——深拷贝 36MB / 全量重写数十 MB 的代价随桶数线性放大。

**Q4 有没有更便宜的等效手段？** 有两个方向，都已采用，不做更重的：

- 读路径：**COW 发布**（拷出副本改完换指针）——Add() 每次一个小分配，换 Snapshot 锁内只取
  指针切片（~8B/桶）。比「RWMutex」便宜且无数据竞争；比「swap 整个 map」语义不变。
- 写路径：**WAL 侧车**（增量追加）——日常 flush 只追加脏桶，完整基线按阈值/Stop 才重写。
  不做 WAL 压缩合并器等更重件（个人网关，量小）。

**结论：做。** 纯同包重构 + 一个侧车文件，不改对外 wire 形状，不改 USD 四态语义。

---

## 二、需求项（Given-When-Then）

### GWT-1 读路径不再锁内全量深拷贝
- Given Recorder 已写入若干桶
- When 调用 `SnapshotWindow`
- Then 锁内只收集桶指针切片（不整桶值拷贝），聚合输出与深拷贝口径**逐字段一致**
- 验收：`go test ./internal/usage` 全绿（既有 Snapshot 用例 + 新增对照）；client 行为零变化

### GWT-2 日常 flush 增量落库（不每次全量序列化）
- Given Recorder 已写入脏桶
- When `flush(false)`（30s 周期）
- Then **不重写** `usage.json` 基线，只把本轮脏桶/脏 monthly 追加进 `usage.wal.jsonl`
- 验收：`TestWALIncrementalFlushNoBaseRewrite`——flush(false) 后基线文件不存在、WAL 有记录

### GWT-3 崩溃安全（基线 tmp+rename 原子 + WAL append durable）
- Given 已追加 WAL 后崩溃（含半截坏行）
- When 重启 `New(path)`
- Then 加载基线 + 回放 WAL（坏行跳过），数值完整恢复；基线重写仍走 tmp+rename 原子替换
- 验收：`TestWALBadLineSkipped`、`TestWALReplayTakesMax`

### GWT-4 基线阈值/Stop 重写 + 截断 WAL
- Given WAL 累计记录数达 `walCheckpointAfter=20000` 或 Stop/Save（force）
- When flush
- Then 重写完整 `usage.json` 基线，写完截断 WAL
- 验收：`TestWALForceCheckpointTruncates`

### GWT-5 旧格式可读（ADR-009 版本门，向后兼容）
- Given 磁盘上是旧 v6 `usage.json`（无 WAL）
- When 新版本启动
- Then 正常加载（版本 < 当前走备份 `.pre-upgrade-v<N>.bak` 逻辑不变），升级不丢数据
- 验收：`TestLoadLegacyV6StillWorks` + 既有 `TestLedgerVersionGate` 不回归

### GWT-6 回放合并 = 逐字段取大（消解回滚时序回退）
- Given 基线值比 WAL 记录新（旧版回滚后写过基线）
- When 回放 WAL
- Then 取大不覆盖（计数单调非减），不被旧 WAL 拉回旧值
- 验收：`TestWALReplayTakesMax`

### GWT-7 USD 折算四态语义不变
- When 本次重构
- Then `bucketUSD`（:114-129）一行不动；`kind=inference` 标注、字段省略（缺失≠0）语义保持

---

## 三、范围边界

**做**：`internal/usage/`（usage.go / cap.go / 新增 wal.go / wal_test.go）+ ADR-016 + 本文 + changes。

**不做**：
- 不改 `cmd/server/config.go`（并行分支在动）——落库开关如需要，记 summary「未完成」节
- 不改 `internal/server` / `internal/panel` / `internal/upstream`
- 不做 WAL 压缩合并器 / WAL 版本迁移工具（量小，不必要）
- 不改 wire 形状（`/status`、`/v1/models` 字段一律不动）

## 四、验证

`go test ./...`（20 ok + 3 no test files 基线口径）+ `go vet ./...` + `gofmt -l` 无输出 +
`pre-commit.ps1` 全量 10 项。

## 五、回滚

见 `.harness/changes/2026-10-22-usage-incremental/rollback.md`。

---

## 六、v2 修订（2026-10-22）：两点遗留消除

v1 落地后 summary 留了两个「未完成」节，本次闭环。原正文不动，只追加。

### 遗留① WAL delete op（消除折叠-崩溃-重启的源桶复活窗口）

原妥协（ADR-016 §三）：折叠把源桶并进目标桶后只写目标 upsert、源键无删除记录；若崩溃落在
「折叠后、下次基线上线前」，旧 upsert 记录会把源桶回放复活（短暂重复计数，下次基线自愈）。

**Given-When-Then**：

- G：基线有源桶 A（h:10）+ 目标桶 B（d:10）；折叠把 A 并进 B
- W：本轮 flush 写入 WAL = 先 delete(A)、后 upsert(B)
- T：崩溃重启回放后，A **不再复活**、B 合并值正确
- G：折叠后 A 键又被新流量重建（同批既有 delete(A) 又有 upsert(A)）
- W：顺序回放（delete 先行）
- T：A 被重建为新值（先删后建，不残留旧值）
- G：merge-max（旧 upsert）与 delete 同键共存
- W：顺序回放
- T：delete 是确定性终态（不用 merge-max 套 delete）

### 遗留② walCheckpointAfter 装配期配置化

原妥协：阈值是包常量 20000（wal.go:29），config.go 当时并行禁碰。

**Given-When-Then**：

- G：config.json 无 `usage.wal_checkpoint_after`
- W：启动
- T：阈值回落默认 20000（缺省零回归）
- G：配置 `<=0`
- W：启动
- T：回落默认 20000
- G：配置显式正数 N
- W：累计 WAL 记录达 N 后 flush
- T：触发完整基线重写并截断 WAL

**装配期**：启动读一次，改后重启生效；不进 livecfg 热快照；面板无 UI 编辑。

