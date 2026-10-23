# bugfix-authlink 变更摘要（X-1/X-2/X-3/X-4/F-4/A-1/A-2）

日期：2026-10-22（代码落地）/ 2026-10-23（收尾提交）
分级：P1×1（X-1）+ P2×6 bugfix（全仓体检批次 1，server/auth/upstream 域）

---

## 一、改了什么

| 编号 | 文件 | 问题 | 修复 |
|---|---|---|---|
| X-1 (P1) | `internal/server/anthropic.go:182-195` | /v1/messages **非流式错误路径空 200**：interceptWriter 捕获了上游错误状态与 body，但错误分支只 `WriteHeader(iw.status)` 不写 body 缓冲 → 客户端拿到 200 空体，错误信封（含状态码/错误信息）被吞 | 错误分支把缓冲完整透传：`w.Header().Set("Content-Type","application/json"); w.WriteHeader(iw.status); w.Write(buf.Bytes())`（与 chatCompletions 错误信封口径一致）。测试 `TestAnthropicNonStreamErrorFlushed` 钉住状态码+body。 |
| X-2 | `internal/server/anthropic.go:151-168` | /v1/messages 裸 `io.ReadAll(r.Body)` 绕过 MaxBodyBytes（chatCompletions 有上限，messages 没有） | 套 `http.MaxBytesReader`（同 chatflow.go:82 口径），超限 413。测试 `TestAnthropicBodyLimit`。 |
| X-3 | `internal/server/handler.go:931-971` | fallback 级联续写同一 ResponseWriter：端点 A 已写头后失败，继续写端点 B 的流 → 客户端拿到 A+B 混合流 | `fallbackForward` 维护 `committed bool`；`forwardToOne` 在 WriteHeader 前置位（只在未提交时写头），已提交后失败终止级联。测试 `TestFallbackCascadeStopsAfterCommit`。 |
| X-4 | `internal/server/handler.go:267-273` | notFound known 表漏 `/v1/messages`（PUT/GET 会落 404 而非 405，且错误提示不带正确方法集） | known 表补 `"/v1/messages": {"POST": true}`。测试：notfound_test 加 PUT→405。 |
| F-4 | `internal/upstream/openai_compat.go:49-61` | 备用源转发用 `http.DefaultClient.Do`，出站 Do 落在 server 包（R6 灰区：出站未收编 upstream） | `OpenAICompatEndpoint` 新增 `Do(ctx, body, accept)` 方法（含 ctx 超时传递）；`forwardToOne` 改调 `compat.Do`。出站 Do 收口 upstream。 |
| A-1 | `internal/auth/auth.go:323` | SaveAtomic 错误串内嵌**完整 UID**（错误信息载体绕过 R2 脱敏） | 改 `logfmt.UID8(a.UID)`。测试 `TestSaveAtomicErrorTruncatesUID`。 |
| A-2 | `internal/auth/auth.go:413-421` | 读不出/解析失败的凭证文件**不进 ACL 兜底**（half-written 坏文件保留旧 ACL，安全姿态不收敛） | skipped 分支对坏文件调 `lockdownSkippedFile`（幂等、失败 WARN）。测试 `TestLoadDirReportLocksdownSkippedFiles`。 |

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 编译 | `go build ./...` | exit 0 |
| vet | `go vet ./internal/server ./internal/auth ./internal/upstream` | 无输出 |
| 相关包测试 | `go test ./internal/server ./internal/auth ./internal/upstream -count=1` | 三包 ok（9.9s/1.2s/3.0s） |
| 全量测试 | `go test ./... -count=1` | 23 包全绿（含 panel/usage/reqlog/scheduler） |
| gofmt | `gofmt -l ./cmd ./internal` | 无输出（干净） |

新增测试（实跑 PASS）：`TestAnthropicNonStreamErrorFlushed`、`TestAnthropicBodyLimit`、
`TestFallbackCascadeStopsAfterCommit`（server）；`TestSaveAtomicErrorTruncatesUID`、
`TestLoadDirReportLocksdownSkippedFiles`（auth）；notfound_test PUT→405 用例。

> **工具链绕行披露**：Windows Defender 反复隔离 go1.27.3 toolchain 的 go.exe（机器级误报，
> 本机非管理员）。本批次验证用 `GOTOOLCHAIN=go1.27.2` + `GOFLAGS=-modfile=.git/tmpmod/go.mod`
> （临时 modfile 仅把 go 指令降为 1.27.2，放在 `.git/` 不入库）完成；**go.mod 本体保持
> `go 1.27.3` 未动**。R14（≥1.27.2）语义满足；CI/用户侧恢复 1.27.3 后建议复跑全量门禁。

## 三、未完成 / 残留

- [ ] pre-commit 10/10：全量门禁由 OrganizeAgent 在合流后统一跑（同工具链绕行方式）。
- [ ] 独立复审 review.md（L-16 风格）由复审批次补充。

## 四、怎么回滚

`git revert` 本提交。无凭证/无 wire 字段变更；X-1 透传缓冲、X-3 committed 终止、F-4 Do 委托
均为局部结构改动，回滚无副作用。A-2 的 lockdownSkippedFile 是幂等 ACL 收紧，回滚后坏文件
回到旧 ACL 姿态（建议回滚前确认无半写文件在途）。

## 五、Learning

- interceptWriter 这类「先缓冲后决定」的包装，错误分支必须把缓冲**透传**给真实
  ResponseWriter——只写状态码不写 body 会产出「200 空体」，比错误本身更难排查。
- MaxBodyBytes 必须覆盖每个 body 读入口（chat/completions 与 /v1/messages 是两条入口，
  只护一条等于没护）。
- fallback 级联的写入安全点只有一个：**谁先写头谁独占响应**；已提交后的任何失败都不能再
  写。committed 布尔比「看错误类型猜」可靠。
