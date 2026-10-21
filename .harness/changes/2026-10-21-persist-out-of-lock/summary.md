# persist-out-of-lock 变更记录

> 需求来源：`docs/code-review/2026-10-20-four-dimensions.md` §一 [P2]
> 「`pool/persist.go` 持 `p.mu` 做两次全量 JSON 序列化 + 磁盘 IO + Redis 镜像」。

## 一、变更概览

| 项 | 内容 |
|---|---|
| 功能名 | persist-out-of-lock（落盘重活移出选号临界区） |
| 类型 | perf + test |
| 涉及范围 | `internal/pool/persist.go`、`internal/pool/pool.go` + 1 个新测试文件 |

## 二、问题

`p.mu` 是选号临界区（Pick/Release/Acquire 都走它）。旧 `saveLocked` 在**调用方已持
`p.mu`** 的情况下完成：

1. `stateOverviewLocked()` 全池遍历收集（持锁，合理）；
2. `json.MarshalIndent` 全量序列化（持锁，**不合理**——CPU 密集）；
3. `WriteFile` + `Rename` 原子写（持锁，**不合理**——磁盘 IO，慢到毫秒级）；
4. Redis 镜像再一次 `json.Marshal`（持锁，**不合理**——第二次全量序列化 + 网络 IO）。

调用方：flusher（每 5s）、`Flush`（退出）、`Remove`（管理面板，逐条删账号时）——
**Remove 是 O(N) 次持锁全量序列化**：面板连删 50 个账号 = 50 次全池 JSON + 50 次写盘
全部卡在选号临界区里。磁盘一慢（满卷/网络盘/容器 IO 限速），选号整体停摆。

## 三、方案：两段式 + 锁分层

拆成「持锁收集快照 → 解锁 → 锁外落盘」：

```
snapshotForSaveLocked()  持 p.mu：prune + 抢占 dirty + 收集 stateFile/ store
saveUnlocked(job)        持 saveMu（新）：序列化 + 原子写 + Redis 镜像
```

- **新增 `saveMu`**：只串行化「锁外落盘」这一段，**不阻塞选号**。多个调用方
  （flusher/Flush/Remove）可同时竞争进入，落盘彼此串行、互不交叉写文件。
- **`saveJob` 结构体**把快照与 store 一起传递。这不是顺手打包——`SetStore` 在
  `p.mu` 下写 `p.store`，若让 `saveUnlocked` 无锁去读，就是「无锁读 vs 持锁写」的
  理论数据竞争（`-race` 可证）。在持锁的收集点一并捕获，happens-before 天然成立。
  这是本次改动**自己引入**的隐患，自查中发现并消掉。
- **快照无别名**：`stateOverviewLocked` 产出全新 `stateFile`（`tokenUsage` 是值类型，
  指针字段 `BreakerUntil/DegradeUntil` 指向栈上局部拷贝，两张 map 全新分配），
  锁外序列化不与并发选号竞争。

### 调用点处置

| 调用点 | 频率 | 处置 |
|---|---|---|
| flusher（5s） | 热路径 | 两段式 |
| `Flush`（退出） | 热路径 | 两段式 |
| `Remove`（面板删账号） | 热路径（可连发） | 两段式；`defer Unlock` 改显式解锁 |
| `syncLocked`（SyncToDir） | **每次启动一次** | **保留 `saveLocked` 持锁落盘** |

`syncLocked` 保留持锁落盘不是偷懒，是两个理由的合力：① 它每次启动只跑一次
（`main.go:128-130`），不在热路径；② ADR-010 明确要求「删 + 落盘」在同一把锁内
完成、**中间没有可崩溃窗口**——两段式反而会在「已删内存、未落盘」之间露出一个
崩溃窗口，语义上更差。已在代码注释中写明，防止后人"顺手优化"。

## 四、测试

`internal/pool/persist_outoflock_test.go`（新增，3 例）：

1. **`TestSaveUnlockedRunsOutsidePoolLock`**：持 `p.mu` 调用 `saveUnlocked` 必须
   立即完成。正确实现只碰 `saveMu`；一旦回退成「落盘要拿 p.mu」，RWMutex 不可重入
   → 挂起 → 5s 超时失败。
   *为什么选持锁调用这个形态*：生产形态是「解锁后调用」，那个形态对正确/错误两种
   实现都畅通、**咬不住回退**；只有持锁调用能让错误的锁假设现形。
2. **`TestFlushConcurrentWithPickNoDeadlock`**：选号循环与 `Flush` 循环并发 150ms，
   要求双侧退出 + 文件内容完整（`saveMu` 串行化不出现写盘交叉损坏）。
3. **`TestRemovePersistsViaTwoStageSave`**：Remove 改显式解锁后持久化结果不变
   （被删账号不复活、保留账号不丢失）+ 不存在的 uid 返回 nil。

## 五、自检清单

- [x] `go build ./...` + `go vet ./...` 通过
- [x] `go test ./...` 全量通过（pool 包含 3 个新用例）
- [x] gofmt 干净
- [x] 牙齿验证：`saveUnlocked` 开头加回 `p.mu.Lock()`（模拟回退）→
      用例 1 精确 FAIL「持锁调用死锁（实现回退成锁内落盘了）」；还原 → PASS
- [x] 深度棘轮未回退（`persist.go`/`pool.go` 均非超限文件；基线 2058/5 持平）
- [x] pre-commit 10 检项全过
- [ ] **未跑 `-race`**：本机无 gcc（CGO 不可用），`go test -race` 无法构建。
      并发安全性当前依据是别名分析（快照无共享）+ 锁序论证（saveMu 持有者从不持
      p.mu，单向无死锁面），**未经竞态检测器实证**——这一点不作为"已验证并发安全"
      呈报，有 gcc 的环境应补跑。

## 六、Learning

1. **锁分层先于锁外移**：把重活移出锁之前，先回答"这段重活自己的互斥要什么"。
   本次的答案是 `saveMu`——它保护的是 `persistFails` 与文件写入的串行化，与选号
   状态无关。若直接把 `p.mu` 换成"无锁落盘"，两个 flusher 周期交叠时
   `persistFails` 就会数据竞争。**移出锁 ≠ 去掉锁，是换一把语义更窄的锁**。
2. **改动自己会引入新竞争**：两段式让 `saveUnlocked` 首次在无锁上下文读 `p.store`，
   而 `SetStore` 是持锁写——旧代码这段恰好在锁内所以从未暴露。凡是"把某个读操作
   移到锁外"，都要回头问一句：**这个字段有没有持锁的写者**。`stateFp` 构造后不可变
   （安全），`p.store` 可被 `SetStore` 写（不安全，已收进 `saveJob`）。
3. **性能属性的测试钉子只能钉住安全子集**：「选号不被落盘阻塞」的时延本身无法
   稳定断言（flaky），能钉的是它的安全面——不死锁、不竞争、不错内容。
   本次钉子 1（持锁调用必须完成）正是把"性能优化的安全前提"转成了可断言命题。
