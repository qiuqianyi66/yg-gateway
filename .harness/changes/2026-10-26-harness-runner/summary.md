# 2026-10-26 harness-runner（V3-9：金样任务评分辅助工具）

## 做了什么

新增 `cmd/harnessrun` 命令行工具，自动化 GOLDEN-TASKS 里**机器可判**的评分点检查：

- `harnessrun list`：列出 5 个金样任务（T1-T5）及一句话描述。
- `harnessrun check T1`：检查 T1 的机器可判项——go test ./... 是否绿、gofmt 是否干净、变更目录是否齐全。
- `harnessrun check T2`：检查 T2 的机器可判项——pre-commit.ps1 是否通过、REQ 文档是否含必要性段。
- `harnessrun check T5`：跑 deploy-prod.rehearsal.ps1 并报告结果。
- `harnessrun report`：生成 `run-YYYY-MM-DD.md` 报告模板，机器检查结果留占位、人工评分留空。

## 为什么这么设计

**设计考虑（安全性/可维护性/扩展性）**：

- **安全性**：不做自动启动 agent 会话——agent 评测是人工/外部 agent 来跑的，工具只做评分辅助，不越界。
- **可维护性**：单文件 main.go（~200 行），任务定义是硬编码切片（5 个任务不会经常变）。
- **扩展性**：加新任务就在 tasks 切片里加一项；加新机器可判检查就在 cmdCheck 里加分支。

**边界（诚实记录）**：
- T3（文档漂移狩猎）、T4（否定断言纪律）主要靠人工评分，工具只列出来提醒，不做机器检查。
- 报告模板是骨架，人工评分需要自己填进去。

## 怎么验证的

- `go build ./cmd/harnessrun/` → 通过
- `go run ./cmd/harnessrun list` → 正常输出 5 个任务

## 怎么回滚

删掉 `cmd/harnessrun/` 和本变更目录即可。
