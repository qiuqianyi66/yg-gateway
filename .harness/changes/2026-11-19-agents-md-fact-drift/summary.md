# 按 neat-freak 的方法核了一遍自己的规则文件

日期：2026-11-19
上一提交：`d0f945d`

## 起因

调研 `KKKKhazix/khazix-skills` 时读到 `neat-freak` 的一条做法：**规则文件里写明、又能
机械核对的约定，要核对它有没有被遵守**——「规则引用了已经退役的路径或命令」要当场核实，
不能假设它还对。

于是拿它验了我自己的 `AGENTS.md`。

## 查出的一处真实漂移

`AGENTS.md` 写：

> `go test ./...`（**31 包：26 ok + 5 无测试**）

实测：

```
go list ./...  → 32 个包
无 *_test.go 的：cmd/harnessrun、cmd/signin、cmd/trial、internal/provider  → 4 个
go test ./... -count=1  → ok 28 个包，FAIL 0
```

**32 包 / 28 ok / 4 无测试**，与文档写的 31 / 26 / 5 三项全错。

这条的危害是 `neat-freak` 说的那句：「下一个会话读到这些，会照着错的前提干活，而且
不会怀疑」。比如看到「5 个包没有测试」，就会去补一个其实已有测试的包的覆盖率。

## 修法

只改数字，不加解释性文字。第一次改时我把 4 个包名也写进去了，结果
`check-context-budget.ps1` 直接报超预算（**14805 / 14805 是硬压线，一个字符的余量都没有**）。
改回纯数字替换后，diff 是 `1 insertion / 1 deletion`，预算仍是 14805 / 14805 压线通过。

## 验证

| 检查 | 结果 |
|---|---|
| `go list ./...` | 32 个包 |
| 无测试文件的包 | 4 个（与修正后的数字一致） |
| `go test ./... -count=1` | **28 ok / 0 FAIL** |
| `check-context-budget.ps1` | **未超预算，14805 / 14805** |

## 顺带记录：三个仓库的判断

用户给了三个仓库要评估，这里存一份结论免得下次重新调研。

| 仓库 | 判断 | 理由 |
|---|---|---|
| `KKKKhazix/human-writing` | **不装** | 与 `doubao-human-signal` 目标重叠（都去 AI 味）。唯一增量是 `check_prose.py` 机器检查，但它查的是中文散文的咨询黑话（赋能/抓手/底层逻辑）与翻案腔，放进技术文档会大面积误报——「链路」「对齐」「方法论」在本仓库的文档里是正经术语 |
| `KKKKhazix/AIHOT` | **不装，无关** | 热点聚合 API，与网关无交集。需要查 AI 行业动态时直接调它公开的匿名端点即可，不必装 skill |
| `KKKKhazix/khazix-skills` | **不整包装，吸收三条机制** | `leader` 与 `goal-contract` 重叠、`neat-freak` 与 `.harness/` 治理重叠、`hv-analysis` 已在用户偏好里、`storage-analyzer` 与 `doubao-pc-optimizer` 重叠。装包就是屎山 |

三条值得吸收的机制：

1. **编辑不是记录员**（`neat-freak`）：「收尾后这些文件通常变短或持平。如果只是变长了，
   多半是在追加，没有在对齐」。本仓库的 `.harness/changes/` 有 20+ 个只增不减的目录，这是
   记录员行为。但 `changes/` 是变更追踪的审计证据、有固定位置和明确用途，属于
   「有用途的历史」，不是该删的噪音。
2. **明卷 + 暗卷**（`leader`）：验收命令写进任务书（明卷），另留 2–3 条执行者看不见的
   抽查在会话侧（暗卷）；**执行者不能自己批卷**。这与 `verification-before-completion`
   的「委派出去的工作要自己验」同源，但它把跨界的做法写成了可执行的分配方式。
3. **交汇判断**（`hv-analysis`）：「只有年表和参数对照表、没有交汇，这份报告就没有完成」。
   用户偏好里已有横纵分析法，这条判据是新增的那半句。

## 验证与能力边界

- 已实现：`AGENTS.md` 的包数数字修正
- 已验证：`go list` / `go test` / `check-context-budget` 三条都实跑
- 未验证：`AGENTS.md` 里其余可机械核对的约定（如 R1–R15 红线是否有对应脚本）没有逐条核

## 回滚

```powershell
git revert --no-edit 7658d64
```
