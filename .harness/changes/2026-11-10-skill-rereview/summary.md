# 2026-11-10-skill-rereview 变更摘要

需求：`docs/prd/REQ-2026-11-10-skill-rereview.md`
评审记录：`review.md`（v2，按 `doubao-coding-review-code` + `~/.agents/skills/expert-reviewer` 重做）
分级：L2（业务逻辑：会话容量淘汰的并发与成本形态）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `修改：internal/session/session.go` | 淘汰改批量（一次扫描 + 排序删最旧约 10%，至少 1 条）；淘汰清单改为**解锁后**镜像 `Store.DelBind`（对齐 `gcOnce` 的「锁内收集、锁外镜像」）；`evictOldestLocked` → `evictOldestBatchLocked`；新增 `sort` 导入 |
| `修改：internal/session/session.go`（v2 幂等） | `LoadFromStore` 不再镜像删除被淘汰者：冷启动只腾内存，不清理上层持久化绑定（否则每次重启永久丢一批） |
| `新增：internal/session/max_entries_test.go` | `TestRouterEvictsBatchAtSaturation`（饱和批量淘汰）、`TestLoadFromStoreDoesNotDeletePersistedBinds`（冷启动 delBinds==0） |
| `新增：.harness/changes/2026-11-10-skill-rereview/**` | 本次复检的 REQ + review.md + 摘要 |

**没做的（明写，免得下轮重提）**：
- 未给 reqlog 的 panic 加计数器（🟢2）：尚无真实 panic 观测，属无证据的加固。
- 未把 `defaultMaxEntries` 开成配置项（🔵1）：默认 3MB 上界够用，开配置属堆积。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| session 整包 + 覆盖 | `go test ./internal/session/ -count=3 -cover` | ok，98.1%（v1 时 97.9%，补冷启动用例后回升） |
| 静态检查 | `go vet ./internal/session` / `gofmt -l internal\session` | 退出码 0 / 无输出 |
| 体量棘轮 | `session.go` 行数 | 762 → 795，`maxFileLines` 1070 未触线 |
| 抖动复跑 | reqlog `-count=8` / session `-count=5` / growth `-count=3` | 全绿 |
| 全量门禁 | `powershell -NoProfile -File scripts/pre-commit.ps1` | 见日志 `%TEMP%\yg-precommit-rereview2.log` |

**负向探针**（各在 `%TEMP%` 副本做，做完删除）：
- `ygg-mut-batch`：批量淘汰 `n := len(all)/10` → `n := 1` → 报红 `Count=20 want 19`。
- `ygg-mut-load`：`LoadFromStore` 变异回「删镜像」→ 报红 `LoadFromStore 删了 6 条持久化绑定`（25 条装载 / 上限 20）。

## 三、未完成 / 待补（诚实清单）

- [ ] 批量比例 10% 是拍的值，无实测依据。`requires`：生产观测 `/status` 的 Count 锯齿幅度与
      新会话速率；若锯齿影响观感再调（属参数，不改结构）。
- [ ] 淘汰仍按 map 遍历 + 排序，单次成本 O(n log n)；n=20000 时约毫秒级，未跑 benchmark 证实。

## 四、怎么回滚

`git revert <提交>`：恢复单条淘汰与持锁镜像即可；行为差异只在饱和态的淘汰批量与锁内调用。
不涉及二进制/配置/凭证。

## 五、Learning

根因：上一轮把「加上限」当成一次性判断题（有/没有），漏了**饱和态的成本形态**——
上限生效的那一刻，淘汰路径才第一次开始跑；之前所有测试都只在未饱和区。
