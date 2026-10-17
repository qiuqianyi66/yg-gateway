# 变更记录 — 四个包补独立变异核验（PLAN B3）

- 日期：2026-10-17
- 分级：**L1**（纯核验：不改生产代码，产出是证据 + 一份可复用的驱动脚本说明）
- 计划：`docs/PLAN-open-items-2026-10-17.md` §10.2 B3（`auth` / `session` / `scheduler` / `cmd/server`
  四包「从未做过独立变异核验」）
- 纪律依据：LESSONS **L-2（已升为硬规则，Seen 2）**——变异一律在 `%TEMP%` 副本里做，
  先断言 `go env GOMOD` 指向副本，每轮还原，收尾再断言副本干净

## 一、必要性

前面几轮反复引用「测试有牙齿」「按 L-2 做了变异验证」，但**只有 `upstream` 与 `panel` 做过独立复验**
（`.harness/changes/2026-10-16-t3-mutation/`）。这四包一直是「自述」——
即「我认为这些测试能咬住 bug」，没有任何证据。而它们覆盖的正是**最不能出错的面**：

| 包 | 为什么不能只靠自述 |
|---|---|
| `internal/auth` | 凭证解析、realm 路由、原子写；R1（凭证不出 `auths/`）的落点 |
| `internal/session` | 聚合键与会话粘性；键算错 → 不同轮次串味 |
| `internal/scheduler` | 定时编排；边界算错 → 忙循环或永不触发 |
| `cmd/server` | 配置默认值与校验；**入站 body 上限、read_timeout、多 Key 约束**都在这里 |

## 二、怎么做的（可复现）

1. `git clone` 仓库到 `%TEMP%\mut-b3`（**不碰真实工作区**——这正是 L-2 升规则的原因：
   上一轮的变异就留在原地，差点把 `chatrun.go` 的一个真实修复静默回退）。
2. 驱动脚本 `%TEMP%\mut-run.ps1`（本轮产出，未入库：它是一次性核验工具，不是产品代码）：
   - **先断言 `go env GOMOD` 指向副本**，不指向就拒绝继续；
   - **先跑基线**，未变异时必须绿（否则「红=咬住」的推理不成立）；
   - 每个变异先自检「替换真的发生了」（替换 0 次 = 无效变异，必须报出来，不能算咬住）；
   - 每个变异跑完立刻用原文还原；
   - 收尾 `git status` 断言副本干净。
3. 每个包 4–6 个**语义变异**（不是改格式）：都取「有人顺手简化/写反」会真实发生的形态。

## 三、结果

| 包 | 变异数 | 咬住 | 存活 | 无效变异 |
|---|---|---|---|---|
| `internal/auth` | 6 | **6** | 0 | 0 |
| `internal/session` | 5 | **5** | 0 | 0 |
| `internal/scheduler` | 4 | **4** | 0 | 0 |
| `cmd/server` | 7 | **7** | 0 | 0 |
| **合计** | **22** | **22** | **0** | **0** |

逐条（含各自被哪个测试咬住）：

| ID | 包 | 变异（都是真实可能发生的 bug） | 咬住它的测试 |
|---|---|---|---|
| A1 | auth | `NeedsRefresh` 丢掉提前量 → 到期才刷新 | `TestAuthNeedsRefreshBoundary` |
| A2 | auth | 边界 `>=` 改 `>` | `TestAuthNeedsRefreshBoundary` |
| A3 | auth | `ResolveRealm` 默认 cn 翻 global → CN 凭证被路由到国际版 | `TestBackfillRealmDomain` |
| A4 | auth | `realmLocked` 去掉 domain 推断 → 按域名判定的 global 号被降级 | `TestRealmDomainFallback` |
| A5 | auth | `realmLocked` 去掉逃生门 → 纯 CN 部署锁被绕过 | `TestAuthSetGlobalEnabledEscapeHatch` |
| A6 | auth | 未知过期（`ExpiresAt<=0`）改成不刷新 → 永不主动刷新 | `TestNeedsRefresh` |
| S1 | session | `TurnKeyView` 认错角色（user→assistant） | `TestCSSessTurnKeyViewBranches` |
| S2 | session | 键丢掉消息位置索引 → 不同轮次撞键 | `TestCSSessTurnKeyViewBranches` |
| S3 | session | 空签名时继续往前找（注释明确禁止） | `TestCSSessTurnKeyViewBranches` |
| S4 | session | `Stream()` 对缺失字段判 true | `TestCSSessRequestViewNilSafeAccessorsAndZeroValue` |
| S5 | session | 默认 TTL 30m → 3m | `TestCSSessNewDefaultsAndNilStoreFallback` |
| C1 | scheduler | `nextFire` 边界 `!After` → `Before`（整点立刻重排 → 忙循环） | `TestNextFire` |
| C2 | scheduler | 取最晚而非最早时点 | `TestNextWakeTravelDisabled` |
| C3 | scheduler | 过去的时点不加一天 → 返回过去时刻 | `TestNextWakeActivityIndependent` |
| C4 | scheduler | `includeDisabledInTasks` 恒 true → 保号任务覆盖禁用号 | `TestRunActivityNowSkipsDisabledAndNoToken` |
| D1 | cmd/server | 入站 body 上限 64MB 默认 → 0（**DoS 防护静默关闭**） | `TestMaxBodyBytesConfig` |
| D2a | cmd/server | Default() 里 ead_timeout 300s → 0s | TestServerReadTimeout |
| D2b | cmd/server | 
ormalize() 里缺省回落 300s → 0s | TestServerReadTimeout |
| D3 | cmd/server | 去掉「多 Key 与顶层 api_key 同值」拦截（约束可被另一把绕过） | `TestAPIKeysDuplicateKeyFailsFast` |
| D4a | cmd/server | Default() 里归档保留 7 → 70 天 | TestLoggingDefaults |
| D4b | cmd/server | 
ormalize() 里缺省回落 7 → 70 天 | TestLoggingDefaults |
| D5 | cmd/server | 去掉保留 id 拦截（归因错乱） | `TestAPIKeysReservedID` |

## 四、这些结果**不能**说明什么（边界，别读成「测试完美」）

1. **0 存活 ≠ 测试无缺口**。这 20 个变异是**我读完代码后挑的**，而测试多半也是照着同一批分支写的
   ——存在**选择偏差**：容易想到的变异，往往也正是当年写测试时盯着的那几行。
   真正的缺口更可能在「没人想到的分支」（错误路径的组合、并发交错、边界的时间精度）。
2. **样本不是穷尽**。四个包合计 **297** 个测试函数（实测 `go test ./<pkg>/ -list '.*'`：auth 47 / session 53 / scheduler 98 / cmd/server 99），本轮只打了 **22** 个变异点——约 1/13。
   PLAN 里的 **A6（变异核验穷尽度）仍然开着**，它的处置是「改到哪条分支就补哪条」，不是一轮做完。
3. **只到包内单测层**。不覆盖跨包整合、真实上游报文、Windows 文件语义——那些有另外的证据
   （CI 的 `race`、排练脚本、生产实弹）。
4. **A3/S5/D2 这类「改了就大面积变糟」的变异被咬住，不代表运行期不会被别的路径绕过**。
   变异核验回答的是「测试能不能发现这个改动」，不是「系统有没有这个 bug」。

## 五、可复用的做法（下一轮 A6 直接照做）

```powershell
# 1) 建副本并断言（L-2 硬规则）
git clone --quiet <repo> "$env:TEMP\mut-b3"
powershell -NoProfile -File "$env:TEMP\mut-run.ps1" -MutsFile "$env:TEMP\muts-<pkg>.json" -Pkg 'internal/<pkg>'
```
变异清单是 JSON：`[{id,file,desc,old,new}]`；`old` 用**逐字**片段（含缩进），
未被替换到就判「无效变异」而不是「咬住」——这一条是这套方法的命门。

## 六、回滚

本轮**没有改生产代码**（副本已 `git status` 断言干净），故无回滚动作。
若要把驱动脚本入库（供 A6 复用），那是另一次改动。

## 七、复审

见 `review.md`。

## 八、第二轮独立复审（3 🟡 / 2 🔵）与它的三个「更难变异」

复审由独立 agent 做，被明确要求**不要采信我的 20 个变异**、自己另挑。结果它自选的 3 个**全部存活**
——这是本轮最有价值的发现，也是我「0 存活」这个标题最需要的纠正。

| 级别 | 问题 | 处置 |
|---|---|---|
| **🟡** | **我写的「79 个测试函数」是错的**（实测 **297**：auth 47 / session 53 / scheduler 98 / cmd/server 99）。分母错一个量级，会把「抽样稀」说轻 | 已改 297 并附实测命令。**根因**：这个数字我没测就写了——正是 L-15「否定/数量断言必须穷尽证据」的同类 |
| **🟡** | **「9 个变异直接打在安全面上（含 R1 凭证原子写）」不可溯源**：22 个变异里**没有一条碰 `SaveAtomic`**，R1 完全没被覆盖 | 已改：只列实际存在的安全面变异（body 上限 / read_timeout / 多 Key 约束 / 保留 id / realm 路由 / 逃生门）；R1 现在由**新增的常驻测试**覆盖（见 B3-a） |
| **🟡** | 它自选的 3 个更难变异 **3/3 存活**，且都是安全/产品定位面 | 见下表，1 条已修成常驻测试，2 条登记为 B3-b / B3-c |
| 🔵 | 驱动脚本与变异清单只在 `%TEMP%`，会随临时目录消失，证据不可复算 | 已入库：`scripts/mutation/mutation-check.ps1` + `scripts/mutation/mutations/*.json`（5 份） |
| 🔵 | 脚本用 `Replace` 替换**全部**匹配，只挡了「0 次」，没挡「>1 次」→ 锚点不唯一时无法归因 | 已加唯一性护栏（命中 ≠1 即判无效）。**这条当场见效**：D2/D4 的锚点各命中 2 处（`Default()` 与 `normalize()` 同值），原来那次「5/5」其实是**同时改两处**；已拆成 D2a/D2b/D4a/D4b 用唯一锚点重跑，两层各自被咬住 |

### 8.1 复审自选的 3 个变异与结论

| ID | 变异 | 结果 | 缺口感 |
|---|---|---|---|
| **B3-a** | `auth`：`SaveAtomic` 把 `return os.Rename(...)` 改成 `_ = os.Rename(...); return nil`（凭证落盘失败静默当成功，R10） | **存活**（原有 4 个 SaveAtomic 测试全打在写 tmp 一侧，rename 返回值零断言） | ✅ **已修**：新增常驻测试 `TestSaveAtomicPropagatesRenameFailure`（把目标路径做成非空目录 → rename 必失败）。真仓库未变异 PASS；副本里施加该变异后 FAIL，克隆干净 |
| **B3-b** | `session`：把「空闲号优先」双段策略的 `pool2 := idle` 改成 `pool2 := uids`（新会话可能挤同一号，与「养号友好」定位冲突） | **存活** | ⏳ **仍开**。复审判定：`internal/session` 三个包内测试都不看分配分布，`git grep 空闲|idle|分散` 在测试里 0 命中 |
| **B3-c** | `cmd/server`：重复 Key 检测比较**未归一化**的 `k.Key`（顶层 `api_key` 不 trim，`api_keys[].key` 会 trim）→ 带空格的同值 Key 绕过约束 | **存活** | ⏳ **仍开**。复审还发现一个坑：`Load()` 会连跑两次 `normalize()`，第二次比较时 key 已被 trim，**把缺口盖住了**；要用走单次 normalize 的 `ParseConfig`（面板保存路径）才暴露 |

**这两条开着的缺口不是产线 bug**（未变异时行为正确），是**测试缺口**：在这两处做一次「顺手简化」的重构，CI 会静默放行。B3-a 的修法已经示范了成本（一个 ~30 行测试）。

## 九、更正后的操作口径

```powershell
# 驱动脚本已入库；变异清单在 scripts/mutation/mutations/
git clone --quiet <repo> "$env:TEMP\mut-b3"
powershell -NoProfile -File scripts/mutation/mutation-check.ps1 `
  -Clone "$env:TEMP\mut-b3" -MutsFile scripts/mutation/mutations/auth.json -Pkg internal/auth
```
脚本内置四条护栏：L-2 的 `go env GOMOD` 断言、基线必须先绿、锚点必须唯一、每个变异跑完立刻还原
并在收尾断言副本干净。