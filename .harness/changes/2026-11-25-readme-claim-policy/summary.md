# 2026-11-25-readme-claim-policy 变更摘要

> 类型：**L0 纯文档**（README 口径声明）；无代码/行为/契约/数据变化；按 L0 写简版，无 PRD。
> 基线：该 6 行段是工作区**既有未提交文档**（前序会话写入），本轮只补记录并提交，不改内容。

## 改了什么

- `README.md`「模型与协议」之后新增「模型/provider 声明口径」：
  - 凡写成「已验证 / 支持」的模型、provider 或协议，必须带实测日期 + 实测值；没实测过的只能标 illustrative，不得当成可用性承诺。
  - 当前唯一逐项实测项写明为 `json_object`（2026-10-28，deepseek-v4-flash）；示例配置里的 `glm-*` / `deepseek-*` / `doubao-*` 与 Ark provider 标 illustrative，可用性以运行时 `/v1/models` 为准。
  - 上游协议（CodeBuddy）的持续验证由日常运行与日志承担，不声明具体版本号。
- 新增本 summary。

## 为什么

防止 README 把「配置示例」写成「可用性承诺」，与四态纪律（observation / derivation / inference / unknown）以及「结论前穷尽证据」一致。纯文档，不改变任何对外行为。

## 验证

- `powershell -NoProfile -File scripts/pre-commit.ps1 -Fast`：**exit 0**（92.3s，11 项检查；`check-harness` 11/11 OK，含 prompts 索引与引用断链）。
- `git diff --check`：无空白错误（提交前执行）。
- 纯文档，无代码/测试/配置变化；未跑全量 `go test`（`-Fast` 按设计跳过，L0 无需）。

## 未完成 / 待补（诚实清单）

- [ ] **未逐条审计** README 现有「已验证 / 支持」声明是否都满足本口径。`requires`：逐条对照实测记录或测试证据；本段只立规则，不宣称现有声明全部合规。
- [ ] 文中「当前唯一逐项实测项是 json_object」是**当前态断言**，未重新核对 2026-10-28 之后新增的实测项。`requires`：重新跑一次 README 声明清单。

## 回滚

`git revert <本提交>`。纯文档，无代码/配置/数据迁移；不影响运行时。
