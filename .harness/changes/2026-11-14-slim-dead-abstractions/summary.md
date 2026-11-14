# 2026-11-14 精简：删除未接线的抽象层 + 合并重复护栏

## 阶段 0：四问

**1. 问题是什么？**
仓库里存在「写好了、测过了、但没有任何生产代码调用」的抽象层：`internal/quota` 整包、
`provider.Registry` 一族、`router` 的 Strategy/Rotate/Usage。它们看起来像在用，实际空转。
另一处是 `pick` 与 `pickNonCredits` 各自复制了一份同名护栏闭包，靠注释
「改一处必须同步另一处」约束——注释已经不同步。

**2. 为什么现在做？**
上一轮（`b4e591a`）已完成安全加固与 Pick 热路径，本轮用户诉求是「代码还是很多，继续打磨」。
死抽象是纯粹的读者成本：每次审查都要重新判断「这层到底生效没有」。

**3. 怎么判定「真死」？**
不采信 `deadcode` 单一输出（接口方法、测试专用 helper 全是误报）。逐条交叉验证：

| 候选 | 证据 | 结论 |
|---|---|---|
| `internal/quota` | `grep 'yg-gateway/internal/quota"'` → 只命中 `quota_test.go:6` | 真死 |
| `provider.NewRegistry` 等 | 唯一调用方 `provider_test.go`；`provider_wiring.go` 用普通 map | 真死 |
| `router.Rotate`/`Usage`/`Strategy`/`Mode` | `pick.go` 只用 `View` + `Order{}` | 真死 |
| `pool.floorBlockedForModel` | 仅 `softband_recent_test.go:68` 调用；生产用 `floorBlockedForRealmModel` | 真死（包装层） |
| `server.parseModelFromBody`/`uidPrefix`/`clientIPForLog` | 各自只被 `logging_test.go` 引用 | 真死（测试锚点） |
| `auth.LoadDir`/`GlobalEnabled`/`growth.IsBuddyTaskIncomplete`/`session.ExtractKey` 等 | 被测试或动态路径调用 | **保留**（误报） |

**4. 怎么保证不破坏行为？**
`router` 的删除有逐位等价证明：`pickNonCredits` 构造的每个 `router.View` 都写死
`Healthy: true`，`Order.Pick` 必返回下标 0；改成 `cands[0]` 后「map 迭代随机首个」的
选号顺序不变。护栏合并是逐字搬移，判定式与边界注释一并迁移到 `guards.go`。

## 改动

| 文件 | 动作 |
|---|---|
| `internal/quota/` (quota.go, quota_test.go) | 删除整个包（零 import） |
| `internal/provider/provider.go` | 删 `Descriptor`/`Registry`/`Validate`/`valid`/`Err*`，只留 `Kind`/`QuotaKind` 枚举 |
| `internal/provider/provider_test.go` | 删除（全部在测 Registry） |
| `internal/router/` | 删除整包（恒等抽象）；`pickNonCredits` 改 `cands[0]` |
| `internal/pool/guards.go` | **新增**：`pickGuards` 单一实现（realmOK / failureWindowBlocked / minIntervalBlocked / breakerHalfOpen） |
| `internal/pool/pick.go` | 两处各 4 个重复闭包 → 调 `g := p.guardsAt(...)`；删 `floorBlockedForModel` 包装 |
| `internal/pool/softband_recent_test.go` | 改调 `floorBlockedForRealmModel(..., "", ...)` |
| `internal/server/logging.go` | 删 `parseModelFromBody`/`uidPrefix`/`clientIPForLog`（及 `net`、`upstream` 两个随之无用的 import） |
| `internal/server/logging_test.go` | 删对应三段用例 |
| `scripts/check-boundaries.ps1` | allow 表移除 `quota` / `router` 条目 |

## 验证

| 检查 | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `go test ./...` | 26 包全绿（`provider` 已无测试、`router`/`quota` 已删） |
| `scripts/check-boundaries.ps1` | ✅ 通过 |
| `scripts/check-depth.ps1` | 未回退；覆盖率 75.5% ≥ 棘轮 74.7% |
| 体量 | 生产 158→157 文件 / 41546→41180 行（−366）；测试 242→239 文件 / 63256→62872 行（−384） |

## 回滚

单次 commit 全量回退即可：`git revert <sha>`。删除的包与工厂函数在 git 历史里完整保留，
ADR-026 仍是它们的设计依据。

## 未做（显式记录）

- 未拆 `sse.go`(1112) / `main.go`(1127) / `usage.go`(1069) 与 7 个 >120 行函数——仍在冻结清单，需单独立项。
- 未动 `session` 的 legacy/view 双实现（`deriveKey` 生产零调用但承担与 `deriveKeyView` 的逐位互校），删除会拆掉防漂移的安全网。
- 覆盖率棘轮已随本批 `-Update` 收紧（74.7% → 75.0%，同时 `oversizeFiles` 2 → 1）。

---

# 第二批（同会话续做）：main() 拆装配块

## 为什么接着做

第一批删完死抽象后，`git log --name-only -20` 显示 **`cmd/server/main.go` 是近 20 次提交里
改动最频繁的文件（8 次）**——比任何业务文件都高。原因不是它逻辑复杂，而是 `main()` 是一个
**483 行的单函数**：pool 的 17 个 `Set*` 注入、upstream 的四层超时/UA/global 注入、调度启停的
启动横幅、HTTP server 构造与优雅停机，四类互不相干的职责串成一条直线。改「上游空闲超时」
得先读懂「签到横幅什么时候打印」。

判定依据（improve-codebase-architecture 的删除测试）：这四块**搬走之后**，`main()` 剩下的
仍然是一条可读的装配流，复杂性没有搬去别处变成十层调用——是「集中」而不是「搬家」，所以值得做。

## 改动

新增 `cmd/server/main_wiring.go`（179 非空行），把四块搬出：

| 函数 | 原位置 | 删除行 → 插入行 |
|---|---|---|
| `applyPoolConfig(p, cfg)` | main() 194–211 | 19 → 3 |
| `applyUpstreamConfig(up, cfg)` | main() 257–297 | 42 → 4 |
| `logScheduleBanner(cfg)` | main() 321–358 | 39 → 2 |
| `serveHTTP(ctx, cfg, h, p) error` | main() 561–601 | 41 → 5 |

纪律：`main_wiring.go` 只做字段装配与进程编排，不含业务判断；`serveHTTP` 返回 error 而不是
在内部 Fatalf（Fatalf 会跳过 main 里全部 defer：usage 落盘、requestLog.Close）。

搬运期修掉一个真实错误：首版把 `cfg.DegradeCooldownDur` 写成 `cfg.Pool.DegradeCooldownDur`
（该字段在 Config 顶层，`Pool` 里只有 `DegradeCooldown string`）——编译期即红，已修正。

## 数字（口径：非空行，LESSONS L-5）

| 指标 | 前 | 后 |
|---|---|---|
| `main()` 行数 | 483 | **354**（−27%） |
| `cmd/server/main.go` 非空行 | 1070 | **942**（退出 >1000 名单） |
| `check-depth` oversizeFiles | 2 | **1**（只剩 sse.go 1070） |
| 生产非空行总量 | 38657 | 38708（**+51**，为 main_wiring.go 的函数级 why 注释） |
| `>120` 行函数数 | 7 | 7（main 仍 354 行，未进名单；继续拆会让 30+ 局部变量变参数传递，判定收益为负，主动停止） |

## 验证

| 检查 | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `go test ./...` | 26 包全绿 |
| `gofmt -l cmd internal` | 无输出 |
| `check-depth.ps1 -Update` | 棘轮抬高 `oversizeFiles` 2→1、`minCoverage` 74.7→75.0 |
| 评审 | `.harness/changes/2026-11-14-slim-dead-abstractions/review.md`（6 维自评审，🔴/🟡 零残留；**独立复审缺失已显式记录**） |
