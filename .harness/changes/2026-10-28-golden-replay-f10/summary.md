# 2026-10-28 golden-replay-f10

## 背景与必要性

F10（PLAN-frontend-functional-2026-10-27）：V3-9 `cmd/harnessrun` 就绪后的金样重放
T1–T3——验证「规则/工具变更后 agent 还能不能在本仓正确干活」。不做则 harnessrun 的
机器可判检查与金样重放协议停留在「写完没跑过」。

## 改了什么

- **机器检查**：`harnessrun check T1`（go test ./... 全绿：22 ok + 3 no test files /
  gofmt -l 干净 / 变更目录齐）与 `check T2`（pre-commit -Fast 通过 / REQ 必要性段存在）
  实跑通过；`harnessrun list / report` 实跑正常。
- **T3 文档漂移狩猎 3/3**（先实测后修正，证据=命令输出）：
  1. AGENTS.md「23 包：21 ok + 2 no test files」→ 实测 25 包：22 ok + 3 no test files
  2. README「单页八视图」+「面板（八视图）」+ 视图表 → 实测 app.js TITLES 九视图，
     改九视图并补「运维 Agent」行
  3. AGENTS.md「18 个源包条目」→ 实测 check-boundaries `$allow` 表 20 个源包 key
- **报告**：`docs/agent-eval/run-2026-10-28.md` 落档（机器检查 + 人工评分：T3=2，
  T1/T2 人工项未判——无注入任务，不伪造；总分不判，性质=工具就绪验证 + T3 专项）。
- PLAN-frontend-functional F10 行回填 ✅。

## 怎么验证的

- T1/T2 机器项经 harnessrun 实跑（原始输出见报告）；T3 每处先跑命令（go list / go test
  统计 / app.js TITLES grep / check-boundaries 计数）再改。
- 常驻层预算复算 **15821/15822**（差 -1，AGENTS 等价压缩生效，未超）。
- `pre-commit.ps1` 全量通过（提交前总闸，见提交记录）。

## 怎么回滚

单提交 `git revert`（纯文档改动，无二进制/配置/数据影响）。若未来包数/视图数/边界表
再变，重跑对应实测命令更新 AGENTS/README（T3 本就是为此设计的定期狩猎）。
