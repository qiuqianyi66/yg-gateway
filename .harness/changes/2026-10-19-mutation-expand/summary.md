# 变异核验 — 第二批扩面（EWMA / reqlog / mp / expert，2026-10-19）

- 日期：2026-10-19
- 分级：**L1**（核验资产 + 三处测试补齐；产品代码未改）
- 新增清单：`pool-ewma`（5）、`server-reqlog`（6）、`panel-mp`（4）、`panel-expert`（4）共 **19 条**
- 入口：`powershell -NoProfile -File scripts/mutation/run-all.ps1`

## 一、首轮：15 咬住 / **4 存活**

| 清单 | 首轮 | 存活项 |
|---|---|---|
| `pool-ewma` | 4 / 5 | **EW2** 失败事件懒初始化改 0 |
| `server-reqlog` | 4 / 6 | **RQ1** 归档默认 200→20、**RQ2** 上限 1000 不再夹取 |
| `panel-mp` | 4 / 4 | — |
| `panel-expert` | 2 / 4 | **EX1** 召唤失败不计 fail、**EX3** 部分失败不 WARN |

## 二、四条存活的共性：都是「静默」

| 缺口 | 为什么此前绿 | 补的断言 |
|---|---|---|
| EW2 | 既有 EWMA 用例都是「先成功再失败」，`samples==0` 的失败路径从未走过 | `TestEwmaFirstFailureIsOnlyOneStep`：首个事件即失败 → `sr == 0.9`（不是 0） |
| RQ1 | 没有任何用例用 `limit<=0` 读归档，200 这个默认值没人钉 | `TestArchiveDefaultLimitAndMaxCap`：1005 条归档、`limit=0` → 恰好 200 条 |
| RQ2 | 没有超过 1000 的读取用例 | 同上：`limit=100000` → 恰好 1000 条（`defaultReadMax`） |
| EX1/EX3 | `fail` 只进日志；返回值只报成功数，**看起来像正常完成**，所以没人断言日志 | `TestExpertChainPartialFailureIsLogged`：全部召唤失败时日志必须含 `ok=0 fail=2 type=agent` |

这四条都属同一类风险：**行为没变、可见性没了**。变异核验能抓出来，是因为它改的正是「计数/日志/上限」这类不影响返回值的行。

## 三、补测试后：19/19 咬住

```
pool-ewma      咬住 5 / 存活 0 / 无效 0
server-reqlog  咬住 6 / 存活 0 / 无效 0
panel-mp       咬住 4 / 存活 0 / 无效 0
panel-expert   咬住 4 / 存活 0 / 无效 0
```

全清单合并复跑（含前一批 10 个清单，`run-all.ps1 -Fresh`）：

```
合计 79 条变异 / 14 个清单；异常清单 0 个
✓ 全部咬住        exit 0
```

| 包 | 清单 | 条数 |
|---|---|---|
| `internal/auth` | `auth` + `h3` | 6 + 1 |
| `cmd/server` | `cmdserver` | 7 |
| `internal/pool` | `pool` + `pool-ewma` | 6 + 5 |
| `internal/scheduler` | `scheduler` | 4 |
| `internal/session` | `session` | 6 |
| `internal/server` | `server` | 6 |
| `internal/reqlog` | `server-reqlog` | 6 |
| `internal/panel` | `panel-claim` + `panel-autotask` + `panel-queue` + `panel-mp` + `panel-expert` | 8 + 7 + 9 + 4 + 4 |

## 四、复现

```powershell
powershell -NoProfile -File scripts/mutation/run-all.ps1 -Fresh
powershell -NoProfile -File scripts/mutation/run-all.ps1 -Only panel-expert
```

## 五、仍然没被覆盖的面（继续按 unknown 记）

- `server` 的 reqlog **写入侧**（轮转/保留天数/丢弃计数）目前只有 RQ 里的读取侧断言；
- `panel` 的 `DesktopChatWithExpert` 真实 chat 链路（本清单只覆盖召唤上报失败）；
- `pool` 的 `RecordTokenUsage` 延迟样本边界（EW5 覆盖了罚分封顶，未覆盖「首个延迟观测直接赋值」以外的序列）。
