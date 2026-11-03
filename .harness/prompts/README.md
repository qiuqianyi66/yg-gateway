# .harness/prompts/ — 提示词模板库

> 四个高频场景的**可直接粘贴**的提示词模板。设计口径：模板里的每一条约束都能在本仓
> 找到对应规则或机器检查（`AGENTS.md` / `.harness/rules/` / `scripts/`），
> 不写"放之四海皆准"的套话——套话对 agent 没有约束力。

## 索引

| 模板 | 什么时候用 | 对应流水线阶段 |
|---|---|---|
| [feat-dev.md](feat-dev.md) | 要做新功能 / 改业务逻辑 | 阶段 0 → 4（必要性 → 编码） |
| [code-review.md](code-review.md) | 改完了要评审、要独立复审 | 阶段 6 |
| [debug-triage.md](debug-triage.md) | 有 bug / 报错 / 行为不对但不知在哪 | 阶段 5 之后的返工环 |
| [perf-optimize.md](perf-optimize.md) | 慢、内存涨、覆盖率或体量棘轮报警 | 阶段 5 / 10 |

## 使用方式

1. 复制模板正文到会话开头，把 `<尖括号>` 占位符换成实际值（不换也能跑，agent 会先问）。
2. 模板默认**串行**：上一段的产出没落盘，不许进下一段。
3. 模板产出的文件落 `.harness/changes/{feat-name}/`（summary / review），
   需求落 `docs/prd/REQ-*.md`——与 `AGENTS.md`「变更追踪」一致。

## 维护纪律

- 模板里出现的**门禁命令**必须与 `scripts/pre-commit.ps1` 同源；脚本改了这里要同步，
  否则模板会教 agent 跑一条不存在的命令（这类漂移不报错，只误导）。
- 新增模板前先问：本仓是否**已经**有对应规则文档？有就引用，不复制（复制即漂移）。
