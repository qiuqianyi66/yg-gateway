# 评审记录 — R-205 usage Snapshot COW + 增量 WAL（usage-incremental）

- 日期：2026-10-22
- 复审身份：独立只读复审（子代理，L-16）。对象：commit `0dd9f02`。
- 分级：L2（业务逻辑/持久化，ADR-016）。**本轮为第 1 轮独立复审。**

## 结论

**PASS-WITH-NOTES**（0 🔴 / 0 🟡 / 2 🟢 / 2 🔵）

## 逐条 GWT 证据（REQ-usage-incremental / ADR-016）

| GWT | 代码级证据 |
|---|---|
| 增量 WAL（日常只追加脏桶，不重写全量） | `wal.go`：appendWAL 追加本轮脏桶；基线只在 Stop/Save/超阈值（walCheckpointAfter=20000）重写 + truncateWAL。usage.go flush 改「WAL 追加 + 阈值才重写」。main 上 `go test ./internal/usage` 绿。 |
| 崩溃安全（加载基线→回放 WAL） | usage.go load() 加基线缺失分支 + 末尾 replayWAL；基线走 tmp+rename 原子替换。 |
| 回滚时序不退化（逐字段取大回放） | `mergeBucketMax`：Req/Err/PT/CT/TT/LatMs/TPS/CR/CH/CM 等逐字段取大（wal.go:125-174）；旧 WAL 记录不拉回新基线（TestWALReplayTakesMax）。 |
| 坏行跳过不炸 | replayWAL：json.Unmarshal 失败 continue（:104-107）；TestWALBadLineSkipped。 |
| R10 无业务错误静默吞 | 全仓 grep `internal/usage/{wal,usage,cap}.go` 无 `_ =` / `_, _ =`；appendWAL 写盘失败均 log.Printf 显式记录（观测层，下轮 flush 补写），非 `_ =` 吞。 |
| wire 形状不变 | summary 明写不动 /status、/v1/models 字段；bucketUSD 四态折算一行未动。 |

## 发现的问题 / 应披露项

| 级别 | 问题 | 证据 / 处置 |
|---|---|---|
| 🟢 | **折叠复活窗口**：Rollup 折叠掉的桶无 WAL delete op；崩溃落在「折叠后、下次基线上线前」时，被折走的旧桶从旧 WAL 记录短暂复活，下次基线自愈。 | summary 未完成节如实登记。判据：仅容量压力 >400k 桶触发、低概率极端时序。当前不做（需 WAL delete op 或折叠失效记录）。**接受为已知低概率局限。** |
| 🟢 | walCheckpointAfter=20000 是包内常量，无 config 开关（本分支禁碰 config.go） | summary 登记 requires 并行 config 分支合并后再议。接受。 |
| 🔵 | appendWAL 内 `bw.Write`/`bw.WriteByte` 未单查错误，靠 `bw.Flush()` 统一捕获并 log | bufio.Flush 返回错误即暴露缓冲内全部写失败，无静默吞；可接受。 |
| 🔵 | 回放只覆盖桶 + monthly 两类记录 | walRecord 二选一结构清晰；无其它记录类型需回放。记录备查。 |

## 对后续建议

无需返工。唯一待办：折叠复活窗口若在高压生产实测中出现，再立 REQ 加 WAL delete op；否则保持当前「下次基线自愈」语义。
