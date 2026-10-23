# depth-ratchet-restore 变更摘要（handler.go 拆分 + cmd/credit 覆盖补足）

日期：2026-10-23
性质：全仓 bug 体检收尾的**棘轮回退修复**（pre-commit check-depth 9/10 红的两项根因）

---

## 背景（为什么有这次改动）

pre-commit 总闸 10 项跑到 9/10 通过，仅 check-depth 报两项回退：

1. **oversizeFiles 2 → 3**：`internal/server/handler.go` 非空行 1014（阈值 1000）。
   根因：批次 1（c5a2221，X-3 committed 级联终止 + F-4 出站 Do 收口）给 handler.go
   净增 +32 行，把文件推过 1000 行棘轮线。
2. **minCoverage 73.3% → 2.9%**：`cmd/credit` 覆盖率 2.9%。
   根因：批次 4b（bb1ef68，D-04 坏文件报错）给 cmd/credit 新建了 main_test.go，
   包从「0.0% 覆盖率豁免」变成有测试但覆盖不足的最低包，直接拉低全局最低覆盖率。

脚本原文：「严禁通过放宽阈值或 -Update 掩盖回退」——故两项都按「修复方向」处理：
拆分超长文件、补齐测试覆盖率，**不碰基线文件**。

## 一、改了什么

### 1. handler.go 拆分（oversizeFiles 回 2）

| 文件 | 改动 |
|---|---|
| `internal/server/handler.go` | 把 fallbackForward/forwardToOne（原 927-1040）整体**搬出**到新文件；删除不再使用的 `bytes`/`io` import。逻辑零改动。 |
| `internal/server/handler_fallback.go`（新增） | 承载 fallbackForward/forwardToOne 及自身 import（bytes/context/encoding/json/io/log/net/http/strings/time + upstream/usage），文件头注明拆分原因与「逻辑零改动，纯文件搬移」。 |

拆分后 handler.go 非空行 1014 → **~900**（阈值内），oversizeFiles 回落到基线 2。

### 2. cmd/credit 覆盖补足（minCoverage 2.9% → 73.5%+）

| 文件 | 改动 |
|---|---|
| `cmd/credit/main.go` | ① 抽 `run(args, authDir, fetch, stdout, stderr) int`：main 的全部流程（glob/排序/坏文件/缺 token/fetch/汇总/JSON/-pretty）搬入，fetch 与输出 writer 参数化，main 只做 os 取参委托——**测试可注入假 fetch + 缓冲捕获**；② 抽 `fetchUserResourceAt(base, af, client)`：base 与 HTTP client 参数化（httptest 可注入假上游），`fetchUserResource` 保留原签名委托；③ 抽 `billingPathFor(base)`（global 无 /v2 前缀的判定独立成函数）；④ `printPretty` 收 io.Writer。退出码/输出通道语义与原 main 完全一致。 |
| `cmd/credit/main_test.go` | 保留 TestLoadAuthFileBroken（D-04）；新增：TestBillingBaseFor、TestBillingPathFor、TestFetchUserResourceAt（httptest：成功聚合 + TotalDosage 提升 size + global 路径 + Authorization 头）、TestFetchUserResourceAtErrors（500/code!=0/坏 JSON）、TestFetchUserResourceAtDoError（连接失败）、TestRunFullFlow（4 文件混合场景：排序/坏文件/缺 token/fetch 失败/成功汇总/Error 行）、TestRunPrettyAndGlobError（-pretty 输出 + glob 元字符失败退出码 1）。 |

重构后 cmd/credit 覆盖率 **2.9% → 86.1%**（`go test ./cmd/credit -cover` 实测）。

## 二、怎么验证的

| 验证 | 结果 |
|---|---|
| `go build ./cmd/... ./internal/server` | exit 0 |
| `go test ./cmd/credit -cover -count=1` | ok，86.1% of statements |
| `go test ./internal/server -count=1` | ok（fallback 拆分无回归） |
| `gofmt -l ./cmd/credit ./internal/server` | 无输出 |
| `scripts/check-depth.ps1`（自跑全量 cover） | **✅ 未回退**：oversizeFiles 2、minCoverage 73.5%（≥73.3% 基线）；maxFileLines 1088 与基线一致 |

> 工具链：Windows Defender 误杀 go1.27.3 toolchain，本会话用
> `GOTOOLCHAIN=go1.27.2` + `GOFLAGS=-modfile=.git/tmpmod/go.mod`（临时 modfile 不入库）
> 完成验证；go.mod 本体保持 `go 1.27.3` 未动。

## 三、未完成 / 残留

- [ ] pre-commit 10/10 总闸重跑（本改动提交后）。
- [ ] 独立复审 review.md（L-16 风格）另行补充。

## 四、怎么回滚

`git revert` 本提交。fallback 块回到 handler.go 原位（oversizeFiles 回 3——若后续要维持
1000 行棘轮，需继续拆分而非回滚）；cmd/credit 回到不可测形态（覆盖率回 2.9%）。
**回滚会重新触发 check-depth 两项回退**——仅当确有回滚必要时执行，并接受棘轮复红。
