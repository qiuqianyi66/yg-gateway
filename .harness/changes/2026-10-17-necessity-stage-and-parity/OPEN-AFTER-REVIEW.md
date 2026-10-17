# 五次复审的结论与「仍未实现/未验证」清单

- 生成于 2026-10-17；对象：`docs/PLAN-necessity-review-2026-10-17.md` 与阶段 0 门禁那条改动线
- 用途：**交给下一个 agent 接手**。每条都给可执行命令，不需要读历史。

## 一、五轮结论一览

| 轮次 | 谁审 | 对象 | 结论 | 修在哪个提交 |
|---|---|---|---|---|
| 1 | 我自己（六维自评） | `5717e20` | PASS（**已被推翻，保留在 review.md §一**） | — |
| 2 | 独立只读子代理 | `5717e20` | **FAIL**（0🔴 / 3🟡 / 5🟢 / 6🔵） | `3286f07` |
| 3 | 独立只读子代理 | `3286f07` | **FAIL**（0🔴 / 1🟡 / 3🟢 / 4🔵） | `f74b01e` |
| 4 | 独立只读子代理 | `f74b01e` | **FAIL**（0🔴 / 1🟡 / 2🔵） | `38d86cd` |
| 5 | 独立只读子代理 | `38d86cd` | **未产出**（agent 中途崩，无结论） | 无 |

完整逐轮记录：`.harness/changes/2026-10-17-necessity-stage-and-parity/review.md`

### 已被独立确认修好的（不用再碰）

`core.quotePath` 静默跳过（🟡-2）、前端棘轮漏 `index.html`（🟡-3）、`-Update` 保留 `_comment`、
`pre-commit` 检查 8 退出码三态分支文本、`summary.md` 的「7 份→11 份」、REQ 的 baseline 两键、
`GIT_TRACE` 噪音不混入文件列表、quotePath 五种组合（未跟踪 / staged / 补段 / staged-only / index 缺段+删盘）。

## 二、仍未实现或未验证的（8 条，按该被做的顺序）

### 1. 第 5 轮复审的结论缺失 ← 最该先补

`38d86cd` 之后**没有任何独立结论**。第 5 轮 agent 崩了，我自己验的两条证据是：
orphan base → exit 2 且 `fatal: orphan...HEAD: no merge base` 可见；全量门禁 8 项 exit 0（53.9s）。

```powershell
# 复核命令（在 %TEMP% 里造 no-merge-base，别动仓库）
$p = Join-Path $env:TEMP 'rev5'; New-Item -ItemType Directory -Force -Path "$p\scripts","$p\docs\prd" | Out-Null
Copy-Item C:\Users\yanha\Desktop\yg-gateway\scripts\check-req-necessity.ps1 "$p\scripts\" -Force
cd $p; git init -q -b main; git config user.email t@t; git config user.name t
"# x" | Set-Content docs\prd\REQ-a.md; git add -A; git commit -qm base
git checkout -q --orphan orphan; git commit -qm o; git checkout -q main
powershell -NoProfile -File scripts\check-req-necessity.ps1 -Base orphan   # 期望 exit 2 + 打印 fatal 文本
```

### 2. `deploy-prod.rehearsal.ps1` 从未跑过（B4b 的唯一未覆盖面）

B4b（launcher 回收）只做了函数级验证 + `-DryRun`。演练脚本覆盖 6 场景 / 53 断言。

```powershell
powershell -NoProfile -File scripts/deploy-prod.rehearsal.ps1    # 在 %TEMP% + 7864 沙箱跑，不碰生产
```

### 3. 当前工作区是**红**的：前端棘轮 `4378 → 4387`

`internal/panel/app.js` 被另一会话加了 9 行（非空行 4387），`check-depth` 报
`REGRESSED: maxFrontendLines 4378 → 4387`。二选一：
拆掉那 9 行，或 `powershell -NoProfile -File scripts/check-depth.ps1 -Update` **并在提交说明写理由**。

### 4. PS 5.1 + 遗留 ANSI 代码页（ACP≠65001）未实测

`check-req-necessity.ps1` 读文件已加 `-Encoding UTF8`，但只在 ACP=65001 的机器上验过。
在 ACP=936 的机器上造一个中文名、无 BOM 的 REQ，跑脚本应 exit 1 而不是乱码误判。

### 5. `gate.yml` 的 base 改动与检查 8 都没在真 CI 上跑过

本地模拟过 `-Base origin/main`、`-Base orphan`、`quotePath=true`，但 GitHub Actions runner 未跑。
下一次 push 的 CI 结果即验证（看 `gate` job 日志里的 `base source = push-before` 与「检查 8/8」）。

### 6. `dubious ownership` / `bad object` 两条 git 失败路径只按同根因记为**推断**

未实跑（沙箱改不了目录属主）。若要坐实：在 `%TEMP%` 造一个属主不同的仓库副本触发
`detected dubious ownership`，确认脚本走 exit 2 并打印 git 原文。

### 7. 检查 8 的 `exit 2` 分支端到端仍未覆盖

`pre-commit.ps1` 在「无法枚举改动文件」处就先 `exit 2`（第 109 行附近），
所以**检查 8 自己的 exit-2 分支实际走不到**（第 4 轮复审 🔵-1）。已改好那句误导性提示，
但「端到端验证检查 8 的 exit 2」仍只能桩驱动。要真覆盖：让 `-Base` 一个 rev-parse 通过但
diff 会失败的 ref（no merge base 分支），确认 `pre-commit` 的报错文案指向 no-merge-base 而不是别的原因。

### 8. `B3-a` 是**等价变异**，别再补测试

`auth.ExpiresAt <= 0` 改成 `< 0` 时，`0` 会落进下一行恒真分支，两种实现输出相同 → 任何测试都区分不了。
若有人再跑变异核验会再次看到它 MISSED，**这是预期的**，不是缺口。

## 三、边界（别踩）

- 仓库里有**并发会话**在改代码（今天出现过 `internal/panel/app.js`、`internal/server/chatrun.go` 等）。
  提交前 `git status` 分清归属，**只用显式路径 `git add -- <文件>`**，别 `git add -A`。
- 变异核验**只许在 `%TEMP%` 副本里做**（LESSONS L-2 Seen 2 后的硬规则）。
- 生产换版一律走 `scripts/deploy-prod.ps1`，不要手工分步（`AGENTS.md` §部署纪律 + 退出码表）。
