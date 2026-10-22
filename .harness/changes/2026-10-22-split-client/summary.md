# 2026-10-22-split-client 变更摘要

需求：`docs/prd/REQ-split-client-2026-10-22.md`
分级：L2（业务逻辑位置移动；无接口契约 / 数据模型 / 上游协议变化）
ADR：不需要（无难逆转决策、无新权衡；切分边界按既有函数聚类）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `新增：internal/upstream/errors.go` | 原 client.go :27-608 整段搬入：ErrKind/Error、标记词表、Classify、ParseRetryAfter、ParseRateReset |
| `新增：internal/upstream/models_fetch.go` | 原 client.go :1201-1746 整段搬入：ModelInfo/dynModelEntry、FetchModels、v3/config 探测、efforts/modelRates 缓存 |
| `新增：internal/upstream/billing.go` | 原 client.go :1748-EOF 整段搬入：CreditPackages、UserResource*、DailyCheckin、truncate |
| `修改：internal/upstream/client.go` | 删去上述三段，保留 apiEnvelope/Client/New/端点路由/refresh/ChatStreamContext；import 按编译器反馈裁剪 |
| `新增：docs/prd/REQ-split-client-2026-10-22.md` | 需求 + 阶段 0 必要性 + GWT 验收 |
| `修改：.harness/depth-baseline.json` | check-depth `-Update` 收紧（maxFileLines 2058→558） |

**没做的（明写）**：未拆 handler.go；未做函数级重命名；未动 headers.go/payload.go/useragent*（R8）。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 构建 | `go build ./...` | 0 error |
| 静态 | `go vet ./...` | 无输出 |
| 格式 | `gofmt -l ./cmd ./internal` | 无输出 |
| 全量测试 | `go test ./... -count=1` | 20 ok + 3 no test files（cmd/credit、cmd/signin、cmd/trial），与基线逐包一致 |
| R8 报文 | upstream 包测试含 `r8_golden_test.go` | 随全量测试绿 |
| 深度棘轮 | `scripts/check-depth.ps1 -Update` | 见下 |

**行数对比（非空行口径，check-depth 口径）**：
- client.go：**2058 → 556**（raw 2184 → 612）
- 新文件：errors.go 558、models_fetch.go 540、billing.go 438

## 三、未完成 / 待补（诚实清单）

- [ ] 独立复审 review.md：由独立评审者后续写（本任务不写自评冒充凭据，L-16）。

## 四、怎么回滚

纯代码搬移 + 文档：`git revert <本提交>` 即可恢复 client.go 原状；无数据/状态变更，
不涉及 `auths/`、不涉及 state.json。

## 五、Learning

根因：单文件随功能累积到棘轮硬顶——今后每新增一簇职责，先问"是不是该新文件"。
