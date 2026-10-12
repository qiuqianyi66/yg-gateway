# 后端选号优化（2026-10-12）— 执行记录

> 用户选定方向：深度优化轮第二轮候选之一「后端选号优化」（选项 backend_pick_opt）。
> 交付：ADR-005 两项决定 —— D-600 选号去写锁、D-601 EWMA 表现回灌（默认关，零回归）。
> 流程：探查（HANDOFF/PRD/ADR-003/pool 源码）→ 设计（evidence-locked）→ 最小改动 →
> 测试（牙齿守门）→ 文档 → 部署真机验证 → 提交。

---

## 一、改动清单

| 文件 | 改动 |
|---|---|
| `internal/pool/pick.go` | **D-600**：写锁→RLock；lastUsed/usedSeq/breakerProbeAt 原子写回；prune 移除；exploreMu 探索判定；**D-601**：routingWeightOf 接 median 参数 + ewmaWeightFactor |
| `internal/pool/entry.go` | 原子字段 + helper；EWMA 信号字段与喂入（NoteSuccess/NoteError/NoteFailures/RecordTokenUsage）；Status 新字段；参数与边界 const 集中 |
| `internal/pool/pool.go` | pickSeq → atomic.Uint64；exploreMu；SetEwma |
| `internal/pool/state.go` | 粘性路径 RLock + 原子写回；EWMA 喂入；status 透出 |
| `internal/pool/degrade.go` | NoteFailures 喂入 EWMA |
| `internal/pool/persist.go` | pruneAllLocked + flusher 静默期兜底清理 |
| `cmd/server/config.go` | EwmaConfig 段（enabled/alpha/min_samples，默认 0.1/5）、归一（alpha>1 报错） |
| `cmd/server/main.go` | SetEwma 启动装配 + 热重载 |
| 测试 | 更新：breaker_halfopen_test/pool_test/modelcooldown_test/config_test；新增：`internal/pool/ewma_test.go`（9 条） |

## 二、验证结果

- `go vet ./...`：干净（exit 0）
- `go test -timeout 300s ./...`：**全包 ok，exit 0**（含 internal/scheduler 14.4s、internal/server 10.4s 长测）
- EWMA 牙齿测试全绿：禁用回归（关=旧权重逐位一致）、冷启动中性、±15% 钳制+单调、
  延迟仅罚 >1.5× 池中位（-30% 封顶）、喂入契约、pick 权重偏移（r=0 选好号、r=末尾
  不饿死）、池中位下中位口径、/status 透出
- 数据竞争：本机无 gcc，`-race` 由 CI Linux race job 兜底（同 ADR-003 备注）

## 三、踩坑记录（供后续参考）

1. **并发测试自死锁**：EWMA pick 测试误写双重 `p.mu.Lock()`（sync.RWMutex 不可重入），
   表现是「test timed out」而非报错——排查时先怀疑实现死锁，实际是测试自身。
2. **喂失败信号会触发熔断**：测试喂 20 次 `NoteError` 想验证权重偏移，结果 3 连败触发
   熔断，坏号直接出池，断言全部"假通过"（选好号不是因为权重，是因为坏号不可选）。
   改为 5 成 5 败交替（峰值 fails=1 < 3），EWMA 因子才真正生效。
3. **闲置补偿干扰权重测试**：首轮选中的号 lastUsed 归零 idle，未用号拿满分 5.0 反超，
   r=0 会选到「未用号」——测试必须初始齐用（两号 lastUsed 同置 now）隔离该变量。
4. **中位数口径**：两候选取上中位会把基准抬到慢号头上（慢号不"慢于自己"）。
   改为**下中位** `(n-1)/2`：更保守认定慢号，且两候选场景取较小值。
5. **浮点边界**：`sr=1 → 1.15` 的精确比较受二进制不精确影响，断言用容差 ±1e-6。

## 四、部署与真机验证

见下（D: 实例构建+复制+启动同命令，BOM 安全写 config 开启 pool.ewma，/status 观测）。

## 五、未提交

本轮尚未 commit/push（待部署验证后一并提交）。
