# 2026-10-22-split-handler 变更摘要

需求：`docs/prd/REQ-split-handler-2026-10-22.md`
分级：L2（业务逻辑位置移动；无接口契约 / 上游协议变化）
ADR：不需要（切分边界按既有函数聚类，无难逆转决策）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `新增：internal/server/cors.go` | 原 handler.go :274-298：allowHeader + reflectableCORSOrigin |
| `新增：internal/server/models_handler.go` | 原 handler.go :606-917：modelsCache + models/modelList/applyModelInfoFields/fetchGlobalModels/fetchDynamicModels |
| `新增：internal/server/helpers.go` | 原 handler.go :1054-1164：tokensPerSecond/promptTooLongMessage/usageCreditTotal/rotateBackoff/isUpstreamTimeout/minGenWindow |
| `新增：internal/server/hint.go` | 原 handler.go :1477-EOF：hasImagePart/hasImageView/hintContext/hintOf |
| `修改：internal/server/handler.go` | 删去上述四段；import 按编译器反馈裁剪；禁区函数内容零改 |
| `新增：docs/prd/REQ-split-handler-2026-10-22.md` | 需求 + 阶段 0 必要性 + GWT 验收 |
| `修改：.harness/depth-baseline.json` | check-depth `-Update` 收紧 |

**没做的（明写）**：未拆 autotask.go；未函数级重命名；未动禁区行（NewHandler/withAuth/authenticate/status/healthz/fallbackForward/chatCompletions/applyErrorPolicy）；未动 upstream/panel。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 构建 | `go build ./...` | 0 error |
| 静态 | `go vet ./...` | 无输出 |
| 格式 | `gofmt -l ./cmd ./internal` | 无输出 |
| 全量测试 | `go test ./... -count=1` | 20 ok + 3 no test files，与基线逐包一致 |
| 边界 | `scripts/check-boundaries.ps1` | 同包拆分无新增方向（server 层 internal 依赖未变） |

**行数对比（非空行口径）**：handler.go **1466 → 955**（raw 1558 → 1027）。
新文件：models_handler.go 309、helpers.go 117、hint.go 87、cors.go 30。

## 三、未完成 / 待补（诚实清单）

- [ ] 独立复审 review.md：由独立评审者后续写（L-16，不自评冒充凭据）。

## 四、怎么回滚

`git revert <本提交>` 即可恢复 handler.go 原状并删四个新文件；无数据/状态变更。

## 五、Learning

切分顺序的坑：文档注释块随声明走——isUpstreamTimeout 的收尾与 applyErrorPolicy 的长 doc 注释相邻，第一刀切进了注释中段，复核接缝行后修正。今后按「注释块头行」定位切片边界。
