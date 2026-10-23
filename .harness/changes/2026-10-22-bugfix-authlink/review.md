# 独立复审报告（L-16 第二轮，只读子代理）

> 本文件按 L-16 规则：**自评审 PASS ≠ 复审通过**，本报告由独立只读子代理另起一人完成。
> 不是对作者自评的复读——逐项对照 `git show` diff 与代码现状，并实跑机器检查。
> 分级沿用本仓习惯：🔴 P0/P1（阻断）、🟡 P2（建议改）、🔵 P3（可选项/记录）、🟢 通过。

- 复审对象：`c5a2221`（批次1 修复 X-1/X-2/X-3/X-4/F-4/A-1/A-2）+ `e4b5fdb`（深度棘轮恢复：handler 拆分 + cmd/credit 重构）
- 复审时间：2026-10-23
- 结论：**PASS**（🔴 0 / 🟡 0 / 🔵 2 / 🟢 全项）。无 P0/P1。

---

## 0. 实跑的机器检查（证据，非作者自述）

工具链绕行：`GOTOOLCHAIN=go1.27.2`、`GOFLAGS=-modfile=.git/tmpmod/go.mod`。

| 检查 | 命令 | 结果 |
|---|---|---|
| 变更包测试 | `go test ./internal/server/ ./internal/auth/ ./cmd/credit/` | ✅ 三包 `ok`（server 8.7s / auth 1.0s / credit 0.7s），退出 0 |
| 深度棘轮 | `scripts/check-depth.ps1` | ✅ 退出 0；maxFileLines 1089→1088、minCoverage 73.6%、oversizeFiles=2，无回退 |
| 依赖方向 | `scripts/check-boundaries.ps1` | ✅ 退出 0（文件搬移后 server→upstream/usage 方向仍在 allow 表） |
| gofmt | `gofmt -l` 10 个变更文件 | ✅ 无输出（无格式漂移） |

> 说明：工作树 `cmd/server/main.go`(M) 与 `main_test.go`（未跟踪）是并行 agent 在途改动，按约定未碰；pre-commit 的 gofmt 可能因它们漂移而红，**不属本次复审范围**，不计入结论。

---

## 1. 逐项核对修复正确性

### X-1 `/v1/messages` 非流式错误路径透传状态+body — 🟢 通过
- 代码：`anthropic.go:198-208`，`interceptWriter`（:331-346）把 `WriteHeader` 只记 `iw.status`、`Write` 只进 `buf`；修复后 `iw.status>=400` 时 `w.WriteHeader(iw.status)` + `w.Write(buf.Bytes())`。
- 正确性：chatCompletions 写错误信封经 iw 缓冲、真实 `w` 全程无 body；错误分支透传后状态码正确、信封不丢。对照成功分支（:210-218 解析 buf→转 Anthropic）无冲突。
- **牙齿（真钉住）**：`TestAnthropicNonStreamErrorFlushed` 用空池强制 chatCompletions 走 503 错误信封，断言「不是 200 空体」。把错误分支改回裸 `return` 即 FAIL。✓

### X-2 MaxBytesReader — 🟢 通过
- 代码：`anthropic.go:157-171`，`MaxBodyBytes>0` 时 `http.MaxBytesReader` 截断，`errors.As(err,&*http.MaxBytesError)` → 413。与 chatCompletions 入站上限同口径，补上 /v1/messages 的 DoS 缺口。
- **牙齿**：`TestAnthropicBodyLimit` MaxBodyBytes=32 vs 256 字节 → 断言 413。✓

### X-3 fallbackForward committed 终止级联 — 🟢 通过
- 状态机逐路径推演（`handler_fallback.go`）：
  - `compat.Do` 连接失败（未写任何字节）→ committed 仍 false → 返回 false → **继续级联**（语义保留）。
  - `w.WriteHeader(resp.StatusCode)` 后流式 io.Copy/非流式 ReadAll 失败 → committed=true → 返回 false → **break 终止级联**（不再拼 B 的流）。
  - 入口守卫 `if *committed return false` 为兜底，循环里 break 已保证不再进 forwardToOne。
- 逻辑自洽，没有「已写头还续写下一端点」的路径。
- **牙齿**：`TestFallbackCascadeStopsAfterCommit` 用真实 httptest server A 写一块 SSE 后 `Hijack+Close` 强制 io.Copy 读错误，断言端点 B 的 `bHit=false` 且响应不含 marker。去掉 committed 守卫即命中 B。✓（Hijack 走真实 server，非 ResponseRecorder，平台无关。）

### X-4 notFound known 表补 /v1/messages — 🟢 通过
- `handler.go:269` 补 `"/v1/messages":{"POST":true}`，与路由注册块（POST /v1/messages）一致。

### F-4 OpenAICompatEndpoint.Do 出站收口 — 🟢 通过（附 🔵-2）
- `openai_compat.go:53-60`：组装（NewChatRequest）+ 发出（Do）都收敛到 upstream 包，server 级联只编排不持 HTTP 客户端，R6 意图落实。

### A-1 SaveAtomic 错误串 UID8 — 🟢 通过
- `auth.go:323` 改 `logfmt.UID8(a.UID)`；auth→logfmt 在 boundary allow 表内（`auth→logfmt`），不越界。错误链会原样打日志，截断符合 R2。
- **牙齿**：`TestSaveAtomicErrorTruncatesUID` 断言 err 不含完整 UID 尾巴、含 UID8 前缀。✓

### A-2 lockdownSkippedFile — 🟢 通过
- `auth.go:405-422`：读不出/解析失败的坏凭证文件也过 ACL 兜底；LockdownFile 幂等 icacls、失败只 WARN 不阻断启动，与正常文件 F-101 同策略。
- **牙齿**：`TestLoadDirReportLocksdownSkippedFiles` 替换 `lockdownRunner`/`lockdownOS="windows"`（**不真跑 icacls，平台无关**），断言坏文件进入 lockdown 调用记录。✓

### e4b5fdb：handler 拆分 + cmd/credit 重构 — 🟢 通过
- **拆分零行为改动**：`fallbackForward/forwardToOne` 整体搬到 `handler_fallback.go`，逐行比对 diff 逻辑一致；imports 搬齐（bytes/context/json/io/log/http/strings/time/upstream/usage），`go build` 随测试通过证明无悬空 import。目的是把 handler.go 压回棘轮（1014 行超阈值→拆分而非 -Update 放宽，方向正确）。
- **credit 重构可测性**：`fetchUserResourceAt(base,af,client)`、`billingPathFor`、`run(args,authDir,fetch,stdout,stderr)`、`printPretty(w,...)` 全部参数化；`main()` 退成薄委托（env→authDir、调 run、非 0 才 os.Exit）。
  - 行为等价核对：`pretty` 判定、glob 失败写 stderr+退出1、非 pretty `Fprintln` 带换行、pretty 四行输出——与原 main 逐条一致。
  - **牙齿**：`TestRunFullFlow` 覆盖 坏文件/无 token/fetch 成功/fetch 失败/汇总/排序/Error 行；`TestRunPrettyAndGlobError` 覆盖 -pretty 与 glob 元字符失败退出码；`TestFetchUserResourceAt(+Errors,+DoError)` 覆盖聚合与三条错误分支。2.9%→86.1% 的覆盖提升与这些用例相符。

---

## 2. 红线触碰核查

- **R10 不静默吞错**：anthropic 错误 flush 的 `if _,werr:=w.Write(...);werr!=nil{}` 空体——注释明确「客户端已断连，无补救动作」，属可接受的终态吞错；forwardToOne 的 io.Copy/ReadAll 错误均 `log.Printf + return false`，不伪装成功。✓
- **R1 凭证不出 auths/**：本次未改任何落盘字段；credit 命令读 auths 但不写状态/日志 token（测试断言 Authorization=Bearer tok 仅在假上游收到）。✓
- **R2 日志脱敏**：A-1 修复即把错误串 UID 截 8 位。✓
- **R5 密钥比较**：未触碰比较点。✓
- **R6 出站集中**：F-4 把备用源出站收进 upstream；panel/login 设备登录直连不在本批次范围。✓

---

## 3. 发现的问题（无 P0/P1，无需阻断）

### 🔵 P3-1（X-1 错误信封形状）：/v1/messages 错误响应体是 OpenAI 形状，非 Anthropic 形状
- 证据：`anthropic.go:202-206` 直接 `w.Write(buf.Bytes())`，而 buf 是 chatCompletions 写的 OpenAI 错误信封 `{"error":{...}}`；Anthropic 客户端期望 `{"type":"error","error":{"type":...}}`。
- 定级理由：本次真正的 bug 是「200 空体」，已修对（状态码正确 + 有 body）；错误体形状不匹配是**遗留的边缘体验问题**，不是回归。Anthropic SDK 对解析失败的错误体一般回退展示状态码。
- 建议（可选）：错误分支把信封转成 Anthropic error shape，或在端点文档注明 /v1/messages 错误体为 OpenAI 形状。

### 🔵 P3-2（F-4 传输层）：Do 仍用 http.DefaultClient，未走 upstream 加固 transport
- 证据：`openai_compat.go:59` `http.DefaultClient.Do(req)`；upstream 包正常出站走 `transport.go` 的自定义 client（禁 HTTP/2、超时装配）。
- 定级理由：旧 forwardToOne 本来就用 `http.DefaultClient.Do`，F-4 只是搬迁、**未引入回归**；且备用源是用户自配的 OpenAI 兼容端点（非 CodeBuddy），禁 HTTP/2 的风控要求不适用。
- 建议（可选）：后续若要统一，可让 `Do` 复用 upstream 的加固 client 构造，而非全局 DefaultClient。

---

## 4. L-16 专项：本机绿是否来自环境巧合？

逐条查新测试是否依赖本机环境（L-16 第 2 条抓的就是这类）：
- fallback 级联用**真实 httptest server + Hijack**，不依赖本机网络/端口巧合。✓
- auth ACL 测试**替换 lockdownRunner**，不真跑 icacls（跨 OS 可复现）。✓
- credit 测试全走 `httptest.NewServer` 假上游，不打真实计费域。✓
- 无「本机 gitconfig / 本机目录权限恰好掩盖缺陷」的迹象。

---

## 5. 复审结论

批次1 七个修复（X-1/2/3/4、F-4、A-1/2）与深度棘轮恢复（文件拆分 + credit 重构）**均正确落地，新增测试有牙齿（每个都给出「改动即 FAIL」的反例路径），机器检查全绿且非环境巧合**。两处 🔵 为可选改进，不阻断合入。**本轮判 PASS。**
