# 2026-10-28 structured-output-p2 回滚方案

## 触发条件（什么情况下必须回滚）

- 文档表述与真机事实不符：例如后续换版后上游不再认 `response_format`，README / REQ
  仍写「上游认 json_object（observation）」

## 回滚步骤

1. 纯文档改动：`git revert <commit>`（无二进制换版、无配置、无数据、无凭证影响）
2. 若根因是透传行为被后续代码改动破坏：另开 bugfix 恢复 `response_format` 透传并重跑
   `internal/upstream/payload_passthrough_test.go`，再重测（成本 ≈ 0.03 credit）

## 回滚后必须验证

- README「核心能力·模型与网关」表、REQ-INDEX、REQ-structured-output 状态与新事实一致
- `git log --oneline -1` 与工作区一致

## 已知风险 / 回滚不了的部份

- 真机一发是**单样本 observation（单模型 deepseek-v4-flash）**：结论只代表 2026-10-28
  时点的该模型行为；json_schema 形态与其余模型未测 = unknown，本文档不保证其行为
