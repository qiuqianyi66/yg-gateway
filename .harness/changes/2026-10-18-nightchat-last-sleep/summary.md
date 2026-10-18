# 变更记录 — 夜猫子末次对话后不再空等 4s（B4a / A-1，L1）

- 日期：2026-10-18
- 分级：**L1**（时序微调，无接口/报文变化）
- 需求：`docs/prd/REQ-nightchat-last-sleep.md`

## 一、改了什么

| 文件 | 改动 |
|---|---|
| `internal/upstream/blackcat.go` | `RunNightChats` 循环内 `c.sleepNightChat()` → `if i < need-1 { c.sleepNightChat() }`；注释改为「只拉相邻两次对话的间距」 |
| `internal/upstream/blackcat_test.go` | `TestNightChatsGapIsInjectable` 改为表驱动 `need ∈ {1,2,3}`：断言等待次数 == `need-1`、每次时长 == 4s、注入生效（耗时 < 1s） |

## 二、为什么（一句话）

末次对话之后再等 4s 没有下一个请求需要拉开间距，每个夜猫子任务白付 4s；间隔时长本身与风控相关，**只删末次，不动时长**。

## 三、怎么验证的

- `go test ./internal/upstream/ -run NightChat -v` → 5/5 PASS（`need=1/2/3` 三个子用例 + 生产构造不得预置注入）。
- 牙齿：把 `if i < need-1` 改回无条件 `c.sleepNightChat()` → `need=1` 子用例红（等待 1 次 want 0）；
  改成 `i < need-2` → `need=3` 子用例红（等待 1 次 want 2）。副本实测，见 §五。
- 全量门禁：`scripts/pre-commit.ps1` 8 项全绿（见提交记录）。

## 四、回滚

`git revert <本提交>` → 回到「含末次等待」的旧行为。

## 五、牙齿实跑记录（`%TEMP%\mut-nightchat` 副本，与工作区同源）

| 变异 | 结果 |
|---|---|
| M1 `if i < need-1` → `if true`（旧行为：无条件等待） | **红 3/3**：`need=1` 等待 1 次 want 0、`need=2` 2 want 1、`need=3` 3 want 2 |
| M2 `i < need-1` → `i < need-2`（少等一次） | **红 2/3**：`need=2` 0 want 1、`need=3` 1 want 2 |
| 复原 | 绿 |

过程记录（新坑，已加进下文纪律）：第一次变异「全绿」是假绿——`go test` 在**会话根目录**跑，
不是副本；副本改完必须显式 `workdir` 到副本目录再跑，否则读的是原树 + 缓存。
