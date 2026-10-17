# 变更记录 — CI gate 的差分基准改为「push 事件的 before」（A1，L2）

- 日期：2026-10-17
- 分级：**L2**（改的是「门禁检查什么」；门禁是最终闸门，改错会静默放宽全仓治理）
- 需求：`docs/prd/REQ-ci-gate-base-2026-10-17.md`（F-1）
- 计划：`docs/PLAN-open-items-2026-10-17.md` §1 A1（用户 2026-10-17 批准「今天」批次）
- 评审：`review.md`（同目录，六维 + 独立 reviewer 二轮）

## 一、改了什么

| 文件 | 改动 |
|---|---|
| `.github/workflows/gate.yml` | base 解析改为「PR → `origin/<base_ref>`；push → `github.event.before`；`before` 不可用 → `HEAD~1` **并 `::warning::`**」；`base_ref` 输入的描述同步 |
| `.harness/rules/开发流程规范.md` | §五 两处口径同步：`go-binaries.yml` → `release.yml`（合并后已不存在）；`push 用 HEAD~1` → `push 用事件 before`，并写明「不传 base / 传错 base 会变成 0 文件或只查最后一个提交的假绿」 |

不改 `scripts/pre-commit.ps1`（判据一行没动）、不改 PR 路径、不动其它 job、无新依赖/新权限。

### 1.1 设计要点

- **判据留在门禁自己手里**：`workflow_call` 继承调用方的 `github` 上下文，所以 `gate.yml` 直接读
  `github.event.before`。若让调用方各自算，`release.yml` 与将来的调用方就会各写一遍、各自写错
  —— 与「门禁不能复制成两份」同源。
- **不可用时必须告警而不是静默降级**：`before` 为 40 个 0（新分支首推）、不在历史里（强推/改写）、
  或事件里没有（`workflow_dispatch`）→ 回落 `HEAD~1` + `::warning::`，把「早期提交可能漏检」
  写在 CI 日志里。旧实现的「静默回落」正是这次要消灭的形态。

## 二、怎么验证的

### 2.1 base 解析：把 `run:` 块**原样抽出**用本机 bash 跑五分支

不手抄逻辑：从 `gate.yml` 按缩进抽出 24 行脚本（含空行）写到探针克隆里执行。
> 抽取本身踩了个小坑：字面量块里的**空行**不含 10 个空格，第一版循环在空行处提前结束、只抽到 7 行。
> 修法是把空行也计入、只在「非空且缩进不足」时停。

| # | 输入 | 期望 | 实测 |
|---|---|---|---|
| 1 | `BASE_REF=main`（PR） | `origin/main` | ✅ `ref=origin/main` |
| 2 | `BEFORE_SHA=<HEAD 的真 sha>`（branch push） | 该 sha | ✅ `ref=953806e62bea…` |
| 3 | `BEFORE_SHA=40 个 0`（新分支首推） | `HEAD~1` + 告警 | ✅ `ref=HEAD~1` + `::warning::` |
| 4 | 无 `BEFORE_SHA`（dispatch） | `HEAD~1` + 告警 | ✅ `ref=HEAD~1` + `::warning::` |
| 5 | `BEFORE_SHA=deadbeef×5`（强推，不在历史） | `HEAD~1` + 告警（点名该 sha） | ✅ `ref=HEAD~1` + `::warning::` |
| 6 | `GIT_REF=refs/tags/v9.9.9`（tag，仓库无更早可达 tag） | `HEAD~1` + 告警 | ✅ `ref=HEAD~1` + `::warning::tag push 且找不到上一个 tag` |
| 7 | `GIT_REF=refs/tags/v0.0.9`（tag，存在更早 tag） | 上一个 tag，**不告警** | ✅ `ref=v2.0.0`（它取到的是仓库真实存在的更早 tag） |

### 2.1.1 为什么给 tag 单独一支（而不是一律告警）

GitHub 对**新建 ref**（含新 tag）给的 `before` 恒为 40 个 0。若把 tag 也归进「before 不可用 → 告警」，
**每次发版都会刷一条 warning**——warning 刷多了就没人看，等于把「不许静默降级」变成「习惯性忽略」。
而 tag 的基准**是可确定的**：上一个 tag（`git describe --tags --abbrev=0 <tag>^`），
这与 `release.yml` 的 publish job 算 compare 链接同口径。所以：能算的就算（不告警），
真算不出（首个 tag）才告警。

### 2.2 牙齿：旧口径会让真实违规**整轮绿**

在 `%TEMP%\ci-base-probe`（仓库的本地克隆，**绝不触及真仓库**）造两个提交：
`probe A` = 在 `internal/logfmt/logfmt.go` 末尾加一处**未论证**的静默忽略
（`n, _ := probeR10Two()`，gofmt 已干净、可编译）；`probe B` = 一处无关文档改动。
模拟「一次推 2 个提交」，用**同一份** `pre-commit.ps1` 跑两种 base：

```
旧口径 base=HEAD~1 （= 一次推 2 个提交时的旧行为）   exit=0
  --- 检查 2/7：R10 静默忽略（新增行）
    [OK]   新增行无未论证的静默忽略（检查了 0 个生产文件）   ← 假绿：0 个文件

新口径 base=HEAD~2 （= 推送的 before，覆盖全部提交）  exit=1
  --- 检查 2/7：R10 静默忽略（新增行）
    [FAIL] internal/logfmt/logfmt.go:171  n, _ := probeR10Two()
             → 新增的静默忽略点缺解释注释。补一行「// 忽略：<原因>，不影响 <什么>」
```

这条同时是 **LESSONS L-6 的同族复发形态**：旧口径的判定结果不是「查错了」，而是
「**检查了 0 个文件**」——和 CI 工作区干净时 R10 空转一模一样。

### 2.3 本地门禁

`powershell -NoProfile -File scripts/pre-commit.ps1`（全量）→ 见 §四 提交前实测。
本变更无 `.go` 改动，gofmt/R10 的「改动文件」为空属预期（不是假绿：这两项的输入本来就是 0 个文件）。

## 三、影响分析（含**预期的**风险）

- 门禁**变严**：历史上多提交 push 里被漏检的行，可能第一次被查出来 → 本轮及后续 push 若变红，
  先看「是不是本来就该红」。
- **CI 时间不变**（`fetch-depth: 0` 已有；只是 `git diff` 范围变大）。
- 强推/改写历史时退回旧行为 + 告警：这是**明说的边界**，不是修复不彻底。要更稳需要「上次成功运行的
  SHA」这类外部状态，属另一件事（已留在 `PLAN-open-items` 的观察项里，不在 A1 范围）。

## 四、怎么回滚

```powershell
git revert <本变更的提交>
```

回滚后 push 事件重新固定 `HEAD~1`（多提交仍漏检）。生产侧无动作——这是 CI 编排，不影响运行时。
**回滚前先确认**：下一轮要覆盖多提交 push，就还得把这条改回来。

## 五、执行中的裁决

- **把 base 判据留在 `gate.yml` 而不是调用方**：调用方式将来会增加，策略必须单点。
  ——代价（若判断错）：`gate.yml` 多 17 行 bash，读的人要多看一眼。
- **只对「无法确定基准」告警而不失败**：让 `workflow_dispatch` 与新分支首推仍可跑。
  ——代价（若判断错）：这两种场景下早期提交仍可能漏检（日志里有 warning，但不会红）。
- **不顺手加「上次成功运行 SHA」**：需要额外 API 调用与状态，且会引入「首次运行」的鸡生蛋问题。
  ——代价（若判断错）：强推场景只能退化到 `HEAD~1`。
