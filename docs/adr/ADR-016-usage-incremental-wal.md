# ADR-016 — usage 增量落库（WAL 侧车）+ Snapshot COW 读路径

- 状态：已采纳（2026-10-22）
- 相关：`docs/prd/REQ-usage-incremental-2026-10-22.md`；前置 `ADR-009`（版本门）、`ADR-011`（桶上限）
- 影响面：`internal/usage/usage.go`、`internal/usage/cap.go`、新增 `internal/usage/wal.go`

## 一、背景：两个随桶数线性放大的热点

桶上限 ADR-011 立了之后桶数可达 400k（×~90B ≈ 36MB 常驻）。两个热点随之放大：

1. **SnapshotWindow 锁内全量深拷贝**：面板每次拉视图，`r.mu` 持锁逐桶值拷贝（:674-679），
   持锁 ~36MB 拷贝期间堵死所有 `Add()`（每次聊天请求一次）。
2. **flush 每 30s 全量重写**：锁内拷贝全部桶 + monthly，整体 marshal 成数十 MB JSON
   tmp+rename——与桶数成正比，每 30 秒把同样内容整体重写一遍。

## 二、决策

### 2.1 读路径：COW（copy-on-write）发布

- `Add()` / Rollup 折叠**不再原地改已发布桶**：拷出副本改完，把新指针换进 `r.buckets[key]`。
- 已发布桶对象一旦进 map 即**不可变**；`SnapshotWindow` 锁内只收集 `[]*bucket` 指针切片
  （~8B/桶 vs ~90B/桶），锁外透过指针聚合——读到的是一个一致时点，**无数据竞争**。
- 代价：`Add()` 每次一个小对象分配（个人网关 QPS 低，GC 可忽略）。

### 2.2 写路径：WAL 侧车（增量追加）

- 日常 `flush(false)`：**不重写** `usage.json`，只把本轮脏桶/脏 monthly 追加进
  `usage.wal.jsonl`（JSONL，append durable）。Recorder 记 `walDirty` / `walMonthly` 脏集。
- 完整基线 `usage.json` 只在 **Stop/Save（force）或 WAL 记录数 ≥ walCheckpointAfter=20000**
  时重写一次，写完截断 WAL。崩溃安全：WAL append durable + 基线 tmp+rename 原子替换。
- 重启 = 加载基线 → 回放 WAL（坏行跳过）。

### 2.3 回放合并 = 逐字段取大

- 计数单调非减，WAL 回放按**逐字段取大**合并（`mergeBucketMax`）。
- 这同时消解「旧版回滚写了更新的基线、再升级被旧 WAL 覆盖」的时序回退（max 不退化）。

### 2.4 版本门与向后兼容

- `usage.json` 格式不变（fileVersion=6，wire 形状不动）；WAL 是**新侧车**。
- 旧二进制不认识 WAL：读主文件如常，不写不读 WAL。回滚到旧版后丢失 ≤ 一个 flush 周期
  （30s）的增量数据——可接受，本文如实记录。

## 三、权衡与代价（明说）

- `Add()` 从「原地自增、零分配」变成「每次小拷贝 + 换指针」。对个人网关低 QPS 可忽略；
  若未来高 QPS，这是读路径优化的对价，不是免费午餐。
- WAL 在两次基线之间保留全部脏桶全量当前值（非 delta-of-delta）——量小，不做压缩合并器。
- Rollup 折叠掉的桶没有 WAL「删除」记录：若折叠发生在两次基线之间、恰好崩溃重启，
  被折走的旧桶可能从旧 WAL 记录里「复活」到下一次基线前（短暂重复计数，下次基线自愈）。
  折叠只在容量压力（>400k）下触发，正常负载永不触达。
- 不改 `config.go`：walCheckpointAfter 是包内常量（保持配置面不膨胀）。

## 四、验收

| 用例 | 期望 |
|---|---|
| SnapshotWindow | 锁内只拷指针切片，输出与原深拷贝逐字段一致（既有 Snapshot 用例不回归） |
| flush(false) | 不写基线，只追加 WAL；重启回放后数值完整 |
| flush(force) | 重写基线 + 截断 WAL |
| WAL 坏行 | 跳过不炸，好行正常回放 |
| 旧 WAL vs 新基线 | 取大，不被旧记录拉回 |
| 旧 v6 主文件 | 正常加载，版本门逻辑不变 |

见 `internal/usage/wal_test.go`。

## 五、回滚

`git revert` 本提交即回到「全量深拷贝 + 全量重写」旧行为。旧二进制不读 WAL 侧车文件，
残留的 `usage.wal.jsonl` 会被旧程序忽略（下次旧版 force flush 重写基线后它仍在，但无害——
升级回来时回放取大，不会把旧值拉低）。数据零丢失。

## 六、修订记录（2026-10-22）

本 ADR §三登记的两条妥协已消除/被取代，不新增 ADR 编号（同主题增量修订）：

1. **§三第 3 条（折叠源桶复活窗口）已消除**：WAL 新增 delete 记录（`walRecord.D`）。
   折叠把源键并入目标桶时，源键写入 `walDeletes`，flush 时**先 delete、后 upsert** 同批追加；
   回放按 WAL 内顺序执行 delete（`delete(buckets, D)`），不用 merge-max 套删除。
   折叠-崩溃-重启场景源桶不再复活。同键同批 delete+upsert（折叠后又被新流量重建）时此序
   天然正确（先删后建）。
2. **§三第 4 条（不改 config.go）已被取代**：`walCheckpointAfter` 从包常量改为**装配期配置**
   `config.usage.wal_checkpoint_after`（`<=0` 回落默认 20000，零回归）。`usage.NewWithCheckpoint`
   注入；`New(path)` 保留为默认阈值兼容构造。装配期（启动读一次，改后重启生效；不进 livecfg）。
