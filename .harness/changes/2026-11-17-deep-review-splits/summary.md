# 2026-11-17 深审第二轮 + 降体量（4 文件拆分）

## 改了什么

### 降体量：纯搬移，无行为变化（逐函数原文复制，未改一行逻辑）

| 文件 | 前 | 后 | 拆出 |
|---|---|---|---|
| `internal/server/handler.go` | 895 | 682 | `handler_response.go`(83) + `handler_errpolicy.go`(167) |
| `internal/server/chatrun.go` | 831 | 611 | `chatrun_serve.go`(239) |
| `internal/panel/panel.go` | 880 | 710 | `panel_models.go`(192) |

拆分依据是「责任」而不是「行数」：

- `handler_response.go` — 无状态响应工具（`writeJSON` / `writeOpenAIError` / `flushWriter` / `wafCooldownBase`）。
  与 Handler 的装配、鉴权、路由无关。
- `handler_errpolicy.go` — 上游错误分类 → 账号处置的唯一映射点（冷却/禁用/负缓存/熔断）。
  策略密度高、注释比代码长，是全包最常被改的一段。
- `chatrun_serve.go` — 两个「直接写 ResponseWriter 并决定本次请求成败」的终结分支
  （`serveStream` 流式透传 / `serveNonStream` 聚合），与同文件剩下的选号/轮转责任不同。
- `panel_models.go` — 面板「模型与档位」只读视图，本包唯一与上游模型目录强耦合的部分
  （双域探测、四级上下文窗口查找、三值能力字段的条件透出）。

### 深审修复

- `internal/upstream/client.go`：`panic("unreachable")` → `return nil, 0, nil, fmt.Errorf(...)`。
  理由写进注释：即使「构造保证 chatPaths 非空」成立，该保证依赖不在本函数里的前提；
  空集时 panic 是不可恢复路径，而 7×24 常驻服务里 panic 会终止进程。
  改后**全仓生产代码零 panic**。

### 新增的 4 处 R10 论证注释

搬文件会让 `_ =` / `, _ :=` 行被 R10 检查当成「新增」（判据按 diff 新增行）。
这 4 处代码本身是老的、行为不变，补的是论证：

| 位置 | 论证要点 |
|---|---|
| `handler_response.go:38` `fl, _ := w.(http.Flusher)` | 断言失败 fl=nil，退化为只写不刷 |
| `handler_response.go:43` `raw, _ := json.Marshal(v)` | v 恒为本包构造的 map/struct，不含 chan/func |
| `handler_response.go:46` `_, _ = w.Write(raw)` | 写失败只可能是客户端已断开，无补救动作 |
| `panel_models.go:84` `id, _ := out[i]["id"].(string)` | id 是本函数刚写入的，失败只让 base 为空串 |

## 审计结论（读了，判定不改）

| 审计项 | 范围 | 结论 |
|---|---|---|
| R10 `_ =` 吞业务错误 | 全仓 59 处、生产 11 处 | 逐个读上下文，均有注释且失败可见 → 合规 |
| `resp.Body` 成对关闭 | 12 处 `.Do(` 对 15 处 `Body.Close` | 逐条核对全部成对，`openai_compat.go:60` 的 resp 所有权交调用方并被 defer 接住 → 无泄漏 |
| `panic(` | 全仓 | 生产代码仅 `client.go:763` 一处，已改错误返回 |
| `time.Sleep` | 全仓 29 处 | 全为风控节流（给上游留时间、防批量动作），不在 HTTP 请求路径；停机窗口 30s 远大于 1–10s 间隔 → 不改 |
| `http.Client` 构造 | 生产 3 处 | `alerting` 5s / `panel/login` 30s / `upstream` 可配且带同 host 重定向策略（SSRF 纵深）→ 合规 |

## 怎么验证的

| 命令 | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `gofmt -l ./cmd ./internal` | 无输出 |
| `go test ./... -cover` | exit 0，27 个包 ok，无 FAIL/panic |
| `scripts/check-boundaries.ps1` | ✅ 通过 |
| `scripts/check-depth.ps1 -Detail` | `maxFileLines 895 → 801`、`oversizeFiles 0`、`oversizeFuncs 0`、`minCoverage 78.5%` 持平 |
| `scripts/check-depth.ps1 -Update` | 棘轮收紧到 801 |
| `scripts/pre-commit.ps1` | 见提交记录 |

**已知边界**：本轮是结构搬移，不产生行为差异的证据；「行为不变」的依据是
（a）函数体逐行原文复制、（b）编译与全量单测通过、（c）`go vet`/`gofmt` 干净。
没有做逐字节二进制差分（Go 编译器本身不保证），不声称差分等价。

## 怎么回滚

`git revert <本轮 commit>`。四个新文件独立，回滚不需要联动改脚本。

## 残余 / 未做

- `internal/panel/app.js` 1640 行仍冻结（无浏览器级 E2E 兜底，拆文件风险大于收益）。
- `internal/scheduler/scheduler.go` 801 行现为最大文件，未拆。
- 文件体量之外：`oversizeFuncs 0`、覆盖率 78.5% 均已达门禁，继续拆是边际收益递减。
