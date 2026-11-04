# 变更：chatStream 全路径 404/405 撞 panic 修复 + 剩余留档项闭合评估

日期：2026-11-04
基线：`5ba554a`
状态：已实施，未部署（仅提交）

## 改了什么

### 修复潜伏 panic（P0-3 登记"观察未修"项）

`internal/upstream/client.go` chatStream（ChatStreamContext 循环）：上游对**全部**
候选路径回 404/405 时，循环耗尽撞 `panic("unreachable: chatPaths is never empty")`。
根因：`chatPaths` 恒非空只保证至少一条路径，不保证路径不全 404/405；404/405 分支
无条件 `continue`，最后一条也 continue → 循环正常结束落 panic 行。

修复：`(resp.StatusCode == 404 || resp.StatusCode == 405) && i < len(paths)-1` 才
continue；全部路径 404/405 时**落回 ≥400 返回分支**（读 body → Classify → 返回
带 Kind 信封的错误），行为与任意单路径 ≥400 一致，不再 panic。

验证：`chatpaths_404_test.go`（TestChatStreamAllPaths404NoPanic）——global 两路径全
404 → 请求 2 次返回 404 错误；cn 单路径 404 → 请求 1 次返回 404 错误。**先红后绿**：
临时还原无条件 continue 实跑触发 panic（client.go:738），恢复修复后绿。

### 剩余留档项闭合评估（结论，无代码改动）

| 项 | 结论 | 证据 |
|---|---|---|
| 429 五分类细化 | **不细化**：yg 13 类 kind 已覆盖 magpie 五分类决策维度，且冷却已按类分档（429+14018→ErrHardCredit 长冷却 / 429 其余→ErrSoftRate 短冷却 / 11102→ErrModelBlocked 模型级 / 402+hardMarkers→长冷却）。上游 429 body 可区分形态仅 14018 与 generic 两类，再细分无真实收益 | `upstream/errors.go:572-593`（429 先于 hardMarkers、14018 特判） |
| PII 确定性占位符 | **不适用**：yg `prompt_cache_key` 已是确定性哈希派生（`buildCacheKey` = sha256(uid\|conversation)[:16] + uid8 前缀，`upstream/cache_key.go:91-102`），无原文入键、同账号同会话稳定——即 magpie 目标形态。无请求归并机制依赖 PII | `upstream/cache_key.go`、`usage/usage.go:885`（定价缓存仅 realm+model） |
| min_request_interval 默认值 | **保持 0 关**（未知不签发）：无账号请求分布观测数据不编默认值；requires 7 天观测后按分位校准 | `cmd/server/config.go:411-412` |
| warm-up 错峰 | **不排期**：yg 已有每日签到 + token 保活（等价活跃保持）；无最低成本 Haiku 探测需求 | 规划 P2 |
| 三层锁 | **不排期**：单实例单进程无并发写凭证场景；多实例/CLI 并行切号时再引入 | 规划 P2 |

## 怎么验证的

- `go test ./... -count=1`：30 包全绿
- `go vet ./...`：无输出
- `gofmt -l ./cmd ./internal`：零输出
- `check-boundaries.ps1`：✅
- `check-depth.ps1`：✅（oversizeFiles=2，覆盖率 74%）

## 回滚

- 本提交 `git revert` 即可；404/405 修复回退后回到 panic 形态（旧行为），评估结论为纯文档不影响运行。
- 无配置、无外部副作用、无持久状态变化。
