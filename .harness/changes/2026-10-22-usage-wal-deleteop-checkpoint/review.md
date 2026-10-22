# 评审记录 — R-205 遗留：WAL delete op + walCheckpointAfter 配置化（usage-wal-deleteop-checkpoint）

- 日期：2026-10-22
- 复审身份：独立只读复审（子代理，L-16）。对象：commit `49de3b2`。
- 分级：L2（业务逻辑/持久化，ADR-016 §六同主题修订，不新增编号）。
- 本次复审正是对前次 usage-incremental（`0dd9f02`）review 披露两点遗留的**修复验收**。

## 结论

**PASS-WITH-NOTES**（0 🔴 / 0 🟡 / 2 🟢 / 1 🔵）

## 逐条 GWT 证据（REQ §六 v2）

### 遗留① WAL delete op

| GWT | 代码级证据 |
|---|---|
| 折叠 A→B，flush 先 delete(A) 后 upsert(B)，崩溃重启 A 不复活、B 合并值正确 | `cap.go:126/189` 两处折叠（hour→day、day→ancient）源键 `r.walDeletes[from/key]`；`usage.go:446-457` flush **先遍历 walDeletes 追加 `walRecord{D:k}`，再遍历 walDirty upsert**；`wal.go:119-122` replay `delete(buckets, rec.D)` 顺序处理。测试 `TestWALDeleteRemovesFoldedSource` PASS（独立 `-count=1` 实跑）。 |
| 折叠后 A 又被新流量重建（同批 delete(A)+upsert(A)）→ 顺序回放先删后建 | flush delete 先、upsert 后；replay 同序。测试 `TestWALDeleteThenUpsertRecreates` PASS。头注释（wal.go:24-30）写明「同键同批 delete+upsert 时此序天然正确：先删后建」。 |
| merge-max（旧 upsert）与 delete 同键共存 → delete 是确定性终态，不套 merge-max | `wal.go:119` delete 分支**不调 mergeBucketMax**，直接 `delete(buckets, rec.D)`（注释「删除是确定性操作，不是取大」）。测试 `TestWALDeleteCoexistsWithMergeMax` PASS。 |

### 遗留② walCheckpointAfter 装配期配置化

| GWT | 代码级证据 |
|---|---|
| config 缺省 → 回落默认 20000（零回归） | `usage.go NewWithCheckpoint`：`if n <= 0 { n = walCheckpointAfterDefault(20000) }`；`New(path)=NewWithCheckpoint(path,0)`。测试 `TestCheckpointDefaultFallback` PASS。 |
| config `<=0` → 回落默认 | 同上 `<=0` 分支覆盖。 |
| config 显式正数 N → 达 N 触发基线重写并截断 | `usage.go:459 needBase := force \|\| r.walCount >= r.walCheckpointAfter`（实例字段，非包常量）；测试 `TestCheckpointThresholdTriggersBaseRewrite` PASS。 |
| 装配期：启动读一次，不进 livecfg，面板无 UI | `main.go:327 newUsageRecorder(usagePath, cfg.Usage.WalCheckpointAfter)` 启动期传入；`config.go` Usage 节新增 `WalCheckpointAfter int json:"wal_checkpoint_after"`，仅在构造 Recorder 时读，未挂 livecfg 快照。 |

### 红线 / 纪律核对

| 项 | 结论 |
|---|---|
| R10 无 `_ =` 吞业务错 | diff 无 `_ =`；appendWAL 写盘失败仍 log.Printf 观测层记录（既有语义）。 |
| R8 报文字符串未动 | 纯 usage 包内部 + config/main 装配，未触 upstream/headers。 |
| 四态语义 | 本改动无 wire 字段变更（`wal_record.D` 是内部 WAL JSON，非对外 API）；未引入 `!field` 判否。config 字段缺省=默认，非否定语义。 |
| 依赖方向 | usage 包**未 import cmd/config**；main.go 把 int 值传进 usage.NewWithCheckpoint（叶子包方向正确）。 |
| 文件域 | 仅 usage/usage.go|wal.go|cap.go|wal_test.go + cmd/server/config.go|main.go|startup_guards_test.go + 文档；未碰 panel/upstream/server/httpauth。 |
| gofmt / 测试 | `gofmt -l internal/usage cmd/server` 无输出；`go test ./internal/usage` ok（6 用例含新增 5 个独立 `-count=1` 复跑全 PASS）。 |

## 发现的问题

| 级别 | 问题 | 证据 / 处置 |
|---|---|---|
| 🟢 | **跨版本降级-再升级场景源键仍可能短暂复活**（已知披露项）：delete op 只修「同版本窗口」；若旧版回滚后写了更新基线、新版再升级，跨版本顺序权威不成立。 | ADR-016 §2.4 ≤30s 语义内，下次基线自愈。任务书指定披露，**接受为已知局限**，不在本次修复范围。 |
| 🟢 | **appendWAL I/O 失败时 delete 记录丢失**：flush 在锁内已 `r.walDeletes = make(...)`（usage.go:476）清空，再锁外 `appendWAL`（:482）；若 appendWAL 写盘失败，delete 记录不在下轮补写（与既有 walDirty upsert 同一耐久性边界，非本提交引入）。极端磁盘故障下崩溃，复活窗口理论回归。 | wal.go:46-47 注释「写盘失败仅记日志，下轮 flush 会补写」对 delete 并不严格成立（已清空）。但属 R-205 既有观测层语义，且 appendWAL 失败是罕见 I/O 故障。**接受，记录备查**；若要根治需把「待补写」与「已落盘」分开记账。 |
| 🔵 | `walCount += len(recs)` 把 delete 行也计入阈值累计 | delete 行体积极小、计数正确；阈值触发略偏快但语义无损。非问题。 |

## 对后续建议

无需返工，两点遗留均闭环且测试独立复跑通过。唯一可跟进项：若生产实测出现 appendWAL 写盘失败告警，再考虑把 WAL 待补写集合与已落盘记账分离；当前耐久性边界可接受。
