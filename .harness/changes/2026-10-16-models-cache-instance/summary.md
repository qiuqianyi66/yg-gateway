# 变更记录 — 模型目录缓存搬出包级全局（N3：T5 的结构性根治）

- 日期：2026-10-16
- 计划：`docs/superpowers/plans/HANDOFF-2026-10-16-ci-closed.md` §3 N3
- 前置：T5（`.harness/changes/2026-10-16-t5-sandbox/summary.md`）——那份治的是**症状**，本份治的是**成因**
- 级别：L2（重构生产代码的状态归属；不改对外行为、不改依赖方向、不新增依赖）

## 一、问题

`internal/server` 的 `/v1/models` 用例在 `-count=3` 下红（`-count=1` 全绿）。T5 定位到真因是
**进程内包级全局**的动态模型目录缓存（`ids` + 10min TTL + 5min 失败负缓存）：

- `TestMain` 的沙箱隔离的是文件系统（HOME/cwd/TEMP），**隔离不了进程内变量**；
- 任何「假上游返回空目录/500」的用例都会把负缓存写成 `now` → 同一二进制内后续
  （含 `-count>1` 的下一轮）所有 `/v1/models` 用例被短路成空列表；
- T5 的修法是**每个用例首行清一次缓存**——那是**约定**。新增用例忘清就复发，
  而复发症状是「组装逻辑的用例红成环境问题」，极难归因。

本变更把它变成**结构**：缓存是 `Handler` 的实例字段，每个 `NewHandler` 自带一份空缓存，
隔离由构造保证，不靠纪律。

## 二、改了什么

### 2.1 生产代码

| 文件 | 改动 |
|---|---|
| `internal/server/handler.go` | 新增类型 `modelsCache`（`mu` + `ids` + `fetched` + `lastFail`）与四个方法：`snapshot()`（TTL 内快照）/ `inFailCooldown()` / `noteFail()` / `store(infos)`；`Handler` 新增字段 `modelsCache modelsCache`；**删除包级 `var dynamicModelsCache`**；`fetchDynamicModels` 改用实例缓存；`cachedModelsSnapshot()` 由**包级函数**改成 `*Handler` 方法；`hintContext` 的调用点随之改 `h.cachedModelsSnapshot()` |
| `internal/upstream/global_models.go` | 仅注释：把「语义参照 CN 侧 `handler.dynamicModelsCache`：1h TTL」改成 `handler.modelsCache`：10min TTL（原名已不存在、TTL 数字也已过时——两处都是陈旧引用） |

**行为等价性**（逐条对照，零回归）：

- 命中判据不变：`len(ids) > 0 && since(fetched) < TTL`。改前 `fetchDynamicModels` 的命中分支
  与 `cachedModelsSnapshot()` 用的就是**同一条**判据（都是「非空 + 未过期」），现在两者共用
  `snapshot()` 一个实现——原来是两处各写一遍。
- 判定顺序不变：先查成功缓存，未命中才查失败负缓存（顺序会影响「成功缓存已过期但负缓存仍热」
  时的返回值，本变更保持原顺序）。
- `store()` 仍清空 `lastFail`；`noteFail()` 仍只写负缓存、**不** `NoteError`（不跨界惩罚 chat 熔断器）。
- `snapshot()` 返回的仍是**共享只读切片**（不复制），与改前一致；调用方只读。
- 成功/失败路径的上游调用序列不变（负缓存命中时 0 次探测、过期后 1 轮 = 2 次探测）。

### 2.2 测试代码（`internal/server/handler_test.go`）

- **删除** `resetDynamicModelsCache()` 及其 5 个调用点（`TestModelsEndpoint` / `TestModelsDynamic` /
  `TestModelsDynamicFallsBackToStatic` / `TestModelsFetchFailurePenalizesAccount` /
  `TestModelsNegativeCacheOnFetchFailure`）。全仓 grep 该名字**已为零命中**。
- `TestModelsDynamic` 里读缓存条数的三行加锁直读 → 改用生产方法 `h.cachedModelsSnapshot()`。
- `TestModelsNegativeCacheOnFetchFailure` 的「时间旅行」（把 `lastFail` 拨回 10 分钟前）→
  抽成测试助手 `ageModelsFailCooldown(h)`（**唯一**一处需要碰缓存内部的用例）。
- **新增** `TestModelsCacheNotSharedAcrossHandlers`：故意不做任何重置，
  先让一个 Handler 撞 500 并断言它**真的进了负缓存**（探测次数停在 2，防「空转的假绿」），
  再让另一个 Handler 打 `/v1/models` 必须拿到完整目录。

## 三、怎么验证的

### 3.1 RED —— 新用例在旧结构下必须是红的

```
go test ./internal/server/ -run TestModelsCacheNotSharedAcrossHandlers -count=1 -v
--- FAIL: TestModelsCacheNotSharedAcrossHandlers (0.00s)
    handler_test.go:1209: 另一个 Handler 被前一个 Handler 的失败负缓存污染：拿到 0 个模型（期望 3）
```

这条失败本身就是本变更**反向变异**的证据：把缓存退回包级共享，用例立刻红——不需要另造变异体。
（前置断言「探测次数 == 2」在同一次运行里通过，说明第 1 步确实把负缓存写进去了，
第 2 步的失败不是空转。）

### 3.2 GREEN —— 验收项逐条

| 验收（计划 §3 N3） | 结果 |
|---|---|
| `go test ./internal/server/ -count=3` 全绿 | ✅ `ok yg-gateway/internal/server 24.934s`（全包 3 轮） |
| 测试里不再需要 `resetDynamicModelsCache()`（grep 应只剩定义处） | ✅ **零命中**：函数连定义一起删掉，残留注释里的名字也改写掉了（见 §五 裁决） |
| 新增用例天然隔离（写一条故意不 reset 的用例证明不会互相污染） | ✅ `TestModelsCacheNotSharedAcrossHandlers`；models 子集 `-count=3` 下该用例 3/3 PASS |
| 全量 `go test ./... -count=1` | ✅ 20 包 ok + 3 包无测试文件 |
| `go build` / `go vet` / `gofmt -l ./cmd ./internal` | ✅ exit 0 / 无输出 / **无输出**（过程中出现过一次漂移，当场修掉——见 §五） |
| 依赖方向未变 | ✅ `check-boundaries.ps1` 通过（本变更不新增跨包引用：`modelsCache` 用的 `upstream` 已在既有白名单内） |

### 3.3 未验证的部分（明说）

- **`-race` 未跑**：本机无 gcc，`-race` 由 CI 独占（`go-binaries.yml` 的 `race` job）。
  并发正确性的保证来自设计：`modelsCache` 的 `ids`/`fetched`/`lastFail` 三字段**只经四个方法读写**，
  每个方法内成对加解锁；`snapshot()` 释放锁后返回共享切片，与改前语义一致（调用方只读）。
- **`Handler` 现在内含 `sync.RWMutex`（不可复制）**：`go vet` 未报（无复制点）；`Handler` 一直是
  `*Handler` 传递。若将来有人想按值传递 `Handler`，vet 会在那里报出来。

## 四、怎么回滚

```powershell
git revert <本变更的提交>
```

影响面：`internal/server/handler.go` / `handler_test.go` / `internal/upstream/global_models.go`（注释）。
回滚后包级全局与「每个用例清缓存」的约定一并回来（CI 仍会是绿的，因为用例里的重置调用也一起回滚）。
生产侧无动作：进程内缓存不是持久状态，重启即清空。

## 五、执行中作出的裁决

- **连函数定义一起删，而不是只删调用**：计划写的是「grep 该函数应只剩定义处」，但留一个没有
  调用者的清缓存函数就是**死代码**，它还会让人以为「这个缓存仍然需要手动清」。
  删定义 + 改写两处引用它的注释（改成「每个用例首行手动清一次缓存」这类描述性文字）后
  grep 零命中。**代价（若判断错）**：想恢复「手动清缓存」这种测试手法时要重写这三行。
- **顺手修了 `internal/upstream/global_models.go` 的一处陈旧注释**：它引用的符号名已不存在、
  TTL 数字（1h）也已过时。只改注释、不改该文件任何逻辑。**代价（若判断错）**：无——注释不参与行为。
- **gofmt 漂移当场修**：编辑测试文件时留下的注释段落分隔（godoc 列表后需要空注释行）被
  `gofmt -l` 报出。按本仓格式零容忍纪律，先修再继续，没有让它进提交。
