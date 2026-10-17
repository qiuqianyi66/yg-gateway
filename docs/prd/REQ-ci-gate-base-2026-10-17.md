# REQ — CI gate 的差分基准必须覆盖「一次推送的全部提交」（A1）

- 状态：**需求已确认**（用户 2026-10-17 批准 `docs/PLAN-open-items-2026-10-17.md` §6「今天」批次，A1 为首项）
- 日期：2026-10-17
- 分级：**L2**（业务逻辑：改的是「门禁检查什么」；门禁是最终闸门，改错会静默放宽全仓治理）
- 来源：`docs/PLAN-open-items-2026-10-17.md` §1 A1；`.harness/changes/2026-10-16-ci-gate/summary.md` §四.2
- 相关纪律：`开发流程规范.md` §五「本地与 CI 的分工」（第 147-149 行：push 用 `HEAD~1` 的口径）、
  `.harness/LESSONS.md` L-6（CI 里「本次改动」≠ 工作区 diff）

## 一、问题（F-1）

`.github/workflows/gate.yml` 在 **push 事件**下把差分基准固定成 `HEAD~1`：

```yaml
      - name: 解析 base（PR → origin/<base>；push → HEAD~1）
        env:
          BASE_REF: ${{ inputs.base_ref }}
        run: |
          if [ -n "$BASE_REF" ]; then ... else echo "ref=HEAD~1" >> "$GITHUB_OUTPUT"; fi
```

R10（新增静默忽略必须论证）与「本次改动文件」（gofmt 目标）都是按 `git diff <base>...HEAD` 算的。
于是**一次推送 N>1 个提交时，只有最后一个提交的新增行被检查**，前 N−1 个提交的 R10/gofmt 在 CI 里是盲区。

**这不是理论风险，是已经发生的事**：2026-10-16 那次 push 一次带了 6 个提交
（`ebbe018` → `9c68947`），CI 的 gate 实际只以 `HEAD~1`（`12fa651`… 的相对位置）为基准，
即只差分了最后一个提交。本地 `pre-commit.ps1` 用的是**工作区 diff**（六个提交都在），
所以「本地查过」——但 CI 的独立复核只覆盖了 1/6，而 CI 存在的意义正是「本地可能漏/可绕过」。

**为什么这不是「不重要的小事」**：`开发流程规范.md` §五把 CI `gate` 标为**最终闸门**、
「不可绕过」。基准算错让这句话变成「只对最后一个提交成立」。

## 二、目标与非目标

**目标（F-1）**：push 事件的差分基准 = **本次推送之前的 HEAD**（GitHub 事件里的
`github.event.before`），使一次推送的**全部**提交进入 R10 / 改动文件清单；无法确定基准时
**必须告警**，不得静默回落成「看起来查过」。

**非目标（明确不做）**：

- 不改 `scripts/pre-commit.ps1` 的任何判据（R10 形态、gofmt 目标、`-Base` 语义都不动）；
- 不改 PR 路径的基准（仍是 `origin/<base_ref>`）；
- 不动 `test` / `race` / `build` / `build-image` job；
- 不引入新 action、新依赖、新权限；
- 不顺手做事后比较（如「上次成功运行的 SHA」）——那需要额外 API 调用与状态管理，超出本条。

## 三、设计

### 3.1 基准来源优先级（F-1）

| 事件 | 基准 | 说明 |
|---|---|---|
| `pull_request` | `origin/<inputs.base_ref>` | 不变（GitHub 保证 `base_ref` 存在） |
| `push`（tag，`refs/tags/*`） | **上一个发版 tag**（`git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-ci' <tag>^`） | 新 tag 的 `before` 恒为 40 个 0，但基准**是可确定的**。**必须排除 `-ci` 演练 tag**（`release.yml` 支持「先演练再发版」，演练 tag 就贴在 `HEAD~1`，取到它会让窗口退化成 1 个提交）。取不到（首个 tag）→ `HEAD~1` + 告警 |
| `push`（branch） | `github.event.before` | **本次推送之前**的 HEAD → 覆盖推送里全部提交 |
| `push` 且 `before` 全是 0（新分支首推）或不在历史里（强推） | `HEAD~1` + **`::warning::`** | 无法确定基准时保留旧行为，但**必须留下告警**，不许静默 |
| `workflow_dispatch`（无 `before`） | `HEAD~1` + **`::warning::`** | 同上 |

> **为什么不把 tag 也直接告警**：每次发版都刷一条 warning 会训练人忽略 warning，
> 等于把「不许静默降级」变成「习惯性忽略」。tag 的基准**能**算出来，就该算出来；
> 只有真的算不出来（首个 tag）才告警。

### 3.2 为什么把判据留在 `gate.yml` 而不是让调用方传

`workflow_call` 的被调用 workflow 继承调用方的 `github` 上下文（含 `github.event`），
所以 `gate.yml` 可以直接读 `github.event.before`。把基准策略留在门禁自己手里，
调用方（现在是 `release.yml`，将来可能更多）就无法各自算错——这与「门禁不能复制成两份」同源。

### 3.3 已知边界

- **`before` 是否一定在本地历史里**：`fetch-depth: 0` 已取全历史，正常推送下存在；
  强推/改写了历史时可能不存在 → 代码用 `git cat-file -e "<sha>^{commit}"` 探测后回落。
- **`before` 为 0 值**：GitHub 在「新建分支的首次推送」时给 40 个 0，
  直接当基准会让 `git diff` 报错 → 显式识别并回落。
- **不改工作流触发条件**：`paths` 过滤仍可能让某些 push 根本不触发 gate，
  那是另一个问题（不在 A1 范围内），已在 `PLAN-open-items` 保留观察。

## 四、验收（Given-When-Then）

**F-1-a 单提交 push 行为不变**
- Given 一次推送只含 1 个提交
- When gate 解析基准
- Then `ref` = 该推送的 `before` = 等价于 `HEAD~1`；R10 与改动文件清单与改动前一致

**F-1-b 多提交 push 全覆盖（本条的核心）**
- Given 一次推送含 N≥2 个提交
- When gate 解析基准
- Then `ref` = 推送前的 HEAD；**第一个提交**引入的未论证静默忽略也会让 gate 红

**F-1-c 无法确定基准时必须告警**
- Given `before` 为 40 个 0 / 不在历史里 / 事件里没有 `before`
- When gate 解析基准
- Then `ref=HEAD~1` **且**输出 `::warning::`（不允许静默假装查过）

**F-1-d PR 路径不变**
- Given `pull_request` 事件且 `base_ref` 非空
- Then `ref=origin/<base_ref>`

**F-1-e tag push 用上一个发版 tag 作基准**
- Given `push` 事件且 `github.ref` 以 `refs/tags/` 开头，且存在更早的**发版** tag
- Then `ref` = 上一个发版 tag（**不告警**）；`-ci` 演练 tag 即使更近也不得被选中
- Given 找不到更早的发版 tag（首个 tag）
- Then `ref=HEAD~1` **且** `::warning::`

**F-1-f 基准解析结果不得为空**
- Given 任何事件形态
- Then `steps.base.outputs.ref` 非空；若为空则脚本 `::error::` + 退出码 1
  （因为 `-Base ""` 会让下游变成「检查 0 个文件」的假绿）

## 五、影响分析

| 面 | 影响 |
|---|---|
| 门禁强度 | 变**严**：多提交 push 的早期提交从「不查」变「查」 |
| CI 时间 | 无（`fetch-depth: 0` 已有；只是 `git diff` 的范围变大） |
| 失败面 | 变宽：历史上被漏检的行可能第一次被查出来 → **本轮要预判并接受这个结果** |
| 权限 / 依赖 | 无变化 |
| 规则文档 | `开发流程规范.md` §五 第 139/148 行两处写着 `go-binaries.yml` 与 `push 用 HEAD~1`，**必须同步**（规范 §六：治理数字/口径漂移当场修） |

## 六、实测证据

见 `.harness/changes/2026-10-17-ci-gate-base/summary.md` §二（8 个分支的 bash 实跑、
空 ref 护栏的变异验证、负向探针）与 `review.md`（六维自评 + 独立复审二轮）。
