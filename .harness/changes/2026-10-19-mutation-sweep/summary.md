# 变异核验 — 全清单复跑基线（A6 收口，2026-10-19）

- 日期：2026-10-19
- 分级：**L1**（新增核验资产 + 一处测试补齐；产品代码未改）
- 入口：`scripts/mutation/run-all.ps1`（本轮新增）
- 清单：`scripts/mutation/mutations/*.json` 共 **10 个 / 60 条变异**

## 一、结论

```
合计 60 条变异 / 10 个清单；异常清单 0 个
✓ 全部咬住        exit 0
```

| 清单 | 包 | 条数 | 结果 |
|---|---|---|---|
| `auth` | `internal/auth` | 6 | 6 咬住 |
| `h3` | `internal/auth` | 1 | 1 咬住（`SaveAtomic` 吞 rename 错误） |
| `cmdserver` | `cmd/server` | 7 | 7 咬住 |
| `pool` | `internal/pool` | 6 | 6 咬住 |
| `scheduler` | `internal/scheduler` | 4 | 4 咬住 |
| `session` | `internal/session` | 6 | 6 咬住 |
| `server` | `internal/server` | 6 | 6 咬住（S1–S6：空流/错误帧/断连/超大帧/错误策略/成功计量） |
| `panel-claim` | `internal/panel` | 8 | 8 咬住（本轮新增，A7/D3 判定与领奖口径） |
| `panel-autotask` | `internal/panel` | 7 | 7 咬住（本轮新增，终态分类） |
| `panel-queue` | `internal/panel` | 9 | 9 咬住（本轮新增，扫描/队列门控、去重、排序、夹取、回滚） |

## 二、复跑过程中抓到的三件事

### 2.1 `run-all.ps1` 自己的一个假事实（已修）

首版用 `@(Get-Content -Raw | ConvertFrom-Json)` 取条数，10 个清单**全部显示「1 条」**。
根因：PS 5.1 的 `ConvertFrom-Json` 把整个 JSON 数组当成**一个对象**写进管道（不展开），
`@()` 包一层得到 `count=1`，`$entries[0].file` 还在数组上取属性（返回多个值）。
改 `[array]$entries = ...` 强制展开。**这是「输出物在说谎」的典型：数字看起来合理，没人会去质疑。**

### 2.2 `cmdserver` D3 锚点漂移（已修）

`D3`（去掉「多 Key 与顶层 api_key 同值」拦截）的 `old` 仍是 B3-c 之前的
`if c.APIKey != "" && key == c.APIKey {`；`9823ebd` 改成归一化比较后锚点 **0 命中**，
driver 判「无效变异」。已更新为 `if c.APIKey != "" && key == strings.TrimSpace(c.APIKey) {`，
复跑 7/7 咬住。**教训：清单里的锚点是代码片段的副本，代码一改就会静默失效——只有全量复跑能发现。**

### 2.3 `pool` P5 真缺口（已补测试）

`P5`（去掉「在途占满」判定 ⇒ 同一账号可被并发超发）**存活**：把
`if p.inFlightFull(e) { continue }` 改成 `if false && ...` 仍然全绿。
说明这条生产里真实存在的并发保护**没有任何测试盯着**。

补 `internal/pool/inflight_pick_test.go`：
- `maxInFlight=1`，占满权重更高的 `u-full` → `Pick` 必须选 `u-free`；
- **对照**：`Release` 之后同一个号必须能被选中（证明排除来自在途，不是权重/健康度）。

## 三、复现

```powershell
powershell -NoProfile -File scripts/mutation/run-all.ps1 -Fresh     # 全量，约 40 分钟
powershell -NoProfile -File scripts/mutation/run-all.ps1 -Only pool  # 单清单
```

退出码：`0` = 全部咬住；`1` = 有存活或无效变异（表里会指出是哪个清单）。

## 四、诚实边界（不把这些写成「已穷尽」）

- 60/60 只说明**清单里这 60 条**都有测试盯着，**不证明**测试覆盖了全部分支。
  A6 原文的「穷尽」在字面上不可证；本轮的成果是**可复跑的基线 + 一条入口**，
  未纳入清单的分支仍属 **unknown**（按四态不写成「没有缺口」）。
- 已知还没被变异覆盖的面：`autotask` 的 expert chain / mp 事件细节、`server` 的 reqlog 分页与筛选、
  `pool` 的 EWMA 回灌路径。
- driver 的 `(cached) ⇒ 无效变异` 判据在 `-count=1` 下不会触发，本轮**未构造出真实缓存命中**，
  该分支属未覆盖（L-17 已按此标注）。
