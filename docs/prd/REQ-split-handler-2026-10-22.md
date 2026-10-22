# REQ：拆分 internal/server/handler.go（R-202）

> 状态：已落地（2026-10-22）。纯机械同包拆分，行为零变化。
> 关联：PRD v2 改进项「工程债 P2——超大文件拆分」；基线 a0ec952；
> 与 R-201（upstream/client.go 拆分）同系列，本文件只碰 internal/server。

---

## 一、必要性（阶段 0，四个问题）

**Q1 要解决的真实问题是什么？**（实测，非"清单上写着"）

`internal/server/handler.go` 拆分前 **1558 原始行 / 1466 非空行**，是
`check-depth.ps1` 的 `maxFileLines` 棘轮峰值（R-201 拆完 client.go 后接棒）。
它已经被拆过一轮（chatflow.go / chatrun.go / logging.go 抽出），但剩余部分仍是
「HTTP 主壳 + /v1/models 整簇 + CORS + 错误提示簇 + 工具函数」混在一个文件里。
模型目录的缓存/组装逻辑（~310 行）与 chat 主壳毫无关系，任何动 /v1/models 的
改动都要在千行文件里翻找边界。

**Q2 不做会怎样？** 棘轮被钉死在 1466：任何往 handler.go 加一行都顶穿基线；
且 C 项协议漂移预案 / E 项多上游模板都要动 fallback 与 models 路径——与本拆分
天然 rebase 冲突，先拆后改是唯一不返工的顺序。

**Q3 现状是不是已经变了？** 已核实：拆分前（a0ec952）handler.go 非空行=1466，
与 R-201 -Update 后的棘轮基线逐位相等；其余生产文件无变化。

**Q4 有没有更便宜的等效手段？** 同包机械搬移：不改签名、不改调用点，
`go test ./...` 全绿即自证。成本 = 4 个新文件 + 一次棘轮 `-Update`。
明确不做函数级重命名与职责再分配。

**结论：做。** L2（位置移动，无接口/协议变化），不触发 ADR。

---

## 二、切分方案（Given-When-Then 验收）

| 新文件 | 迁出内容（原 handler.go 行段） | 非空行 |
|---|---|---|
| `cors.go` | allowHeader + reflectableCORSOrigin（原 :274-298） | 30 |
| `models_handler.go` | modelsCache 类型+四方法、models、fmtCreditsPrefix、nameRateSuffix、applyModelInfoFields、modelList、fetchGlobalModels、fetchDynamicModels、cachedModelsSnapshot（原 :606-917） | 309 |
| `helpers.go` | tokensPerSecond、promptTooLongMessage、usageCreditTotal、rotateBackoff、isUpstreamTimeout、minGenWindow 常量（原 :1054-1164） | 117 |
| `hint.go` | hasImagePart、hasImageView、hintContext、hintOf（原 :1477-EOF） | 87 |
| `handler.go`（保留） | Config/Handler/NewHandler/ServeHTTP/withAuth/authenticate/checkKeyConstraints/healthz/status/latencySnapshot/chatCompletions/applyErrorPolicy/fallbackForward/flushWriter/write* | **955** |

**禁区未动**（只做提取，内容零改）：NewHandler 注册块、withAuth/authenticate/
checkKeyConstraints、status、healthz、fallbackForward、chatCompletions 主体、
applyErrorPolicy。

**Given 拆分完成 / When 编译并跑全量测试 / Then**：
1. `go build ./...` 0 error、`go vet ./...` 无输出、`gofmt -l ./cmd ./internal` 无输出；
2. `go test ./...` 23 包：20 ok + 3 no test files，与基线逐包一致；
3. handler.go 非空行 **< 1000**（实测 955）；
4. check-boundaries 白名单不变（server 层 internal 依赖未增减）；
5. `check-depth.ps1 -Update` 收紧并连同 json 提交。

---

## 三、没做的（明写）

- 未拆 panel/autotask.go（~1244 非空，下一个峰值候选）。
- 未做函数级重命名；未改任何禁区函数内容。
- 未动 upstream / panel 任何文件（R-201 分支产物不重复碰）。
