# REQ：拆分 internal/upstream/client.go（R-201）

> 状态：已落地（2026-10-22）。纯机械同包拆分，行为零变化。
> 关联：PRD v2 改进项「工程债 P2——超大文件拆分」；基线 a0ec952。

---

## 一、必要性（阶段 0，四个问题）

**Q1 要解决的真实问题是什么？**（实测，非"清单上写着"）

`internal/upstream/client.go` 是全仓最大生产文件：**2184 原始行 / 2058 非空行**，
顶着 `check-depth.ps1` 的 `maxFileLines=2058` 棘轮硬顶（`.harness/depth-baseline.json`，
2026-10-17 实测生成）。一个文件里塞了七类互不相关的职责：错误分类、Client 装配与端点
路由、token 刷新、聊天主链路、模型目录拉取、积分账单、签到。后果不是"难看"，而是
**下一个要动这些簇的改进项（协议漂移预案 P1、账单口径调整）都得在 2000+ 行里翻找边界，
且与本拆分天然 rebase 冲突**。

**Q2 不做会怎样？** 棘轮被钉死：任何往 client.go 加一行的改动都会顶穿 `maxFileLines`
基线触发 check-depth 红——不是"文件太大不好读"这种软痛，是**机器门禁直接变红**的硬痛。
且协议漂移预案（P1）与本拆分若先后做在同一文件上，必有一方返工。

**Q3 现状是不是已经变了？** 已核实：拆分前（a0ec952 工作区实测）client.go 非空行=2058，
与棘轮基线逐位相等；其余生产文件无变化（check-depth 实测 oversizeFiles=5）。无重复劳动。

**Q4 有没有更便宜的等效手段？** 有且更便宜的路径只有"什么都不做、等下次红了再说"——
但那会把 P1 协议漂移改动和拆分挤在同一窗口，必然冲突。本方案是同包机械搬移：
不改导出签名、不改类型、不动调用点，`go test ./...` 全绿即自证行为零变化。
成本 ≈ 3 个新文件 + 一次棘轮 `-Update`。

**结论：做。** 纯 L2（业务逻辑位置移动，无接口/数据模型/上游协议变化），不触发 ADR
（无难逆转决策、无新权衡——切分边界按既有函数聚类，方案唯一）。

---

## 二、切分方案（Given-When-Then 验收）

| 新文件 | 迁出内容（原 client.go 行段） | 行数（原始） |
|---|---|---|
| `errors.go` | ErrKind/Error、全部标记词表、Classify、ParseRetryAfter、ParseRateReset（原 :27-608） | 595 |
| `models_fetch.go` | ModelInfo/dynModelEntry、FetchModels、fetchEnterpriseModels/fetchV3Models/fetchV3ConfigModelMap、efforts/modelRates 缓存（原 :1201-1746） | 563 |
| `billing.go` | CreditPackages、UserResource*、enterpriseResource、respAccount/packageRemainUsed、DailyCheckin/IsAlreadyCheckin、truncate（原 :1748-EOF） | 454 |
| `client.go`（保留） | apiEnvelope、Client 结构体、New、端点路由助手、doJSON、refreshGroup/token 刷新、ChatStreamContext | 612 |

**Given 拆分完成 / When 编译并跑全量测试 / Then**：
1. `go build ./...` 0 error、`go vet ./...` 无输出、`gofmt -l ./cmd ./internal` 无输出；
2. `go test ./...` 23 包：20 ok + 3 no test files，与拆分前逐包一致；
3. `internal/upstream/client.go` 非空行 **< 1000**（实测 556）；
4. 导出符号签名/类型/常量零变化——`r8_golden_test.go`（R8 报文钉死）自然通过即自证；
5. `check-boundaries.ps1` 白名单不变（同包拆分不产生新跨包方向）；
6. `check-depth.ps1` 指标真实变好后 `-Update` 收紧并连同 json 提交。

**Given 拆分中 / When 误改任何导出符号 / Then**：编译失败或既有测试（2.9s 的 upstream
包测试含 96KB coverage_parse_test + r8_golden）立即红——本方案零手工语义改动，
全部为整段行搬移。

---

## 三、没做的（明写，免得下轮重提）

- 未拆 server/handler.go（1558 行）——下一个大文件候选，天然独立切片，不在本 REQ。
- 未做函数级重命名/职责再分配——本 REQ 只做"按既有聚类搬文件"。
- 未动 headers.go / payload.go / useragent*（R8 红线）。
