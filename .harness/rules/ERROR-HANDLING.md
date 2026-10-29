# ERROR-HANDLING.md — 静默忽略的判据

> R10「错误不静默吞」的可执行定义。本文件回答一个问题：**什么情况下 `_ =` 是
> 合理的，什么情况下它是掩盖问题。**

---

## 一、为什么需要判据

全仓生产代码有 **123 处** `_ =` / `, _ :=`。逐条都"处理"会产生噪音 diff，也无从
判断对错；但若允许随意 `_`，R10 就成了一句空话。

**判据的作用是让"忽略"成为一个需要论证的决定**，而不是默认操作。

---

## 二、实测分布（2026-10-16 复核，生产代码，不含测试）

**口径**（唯一权威命令，见 §四；换命令必须重测并更新本表）：

| 类别 | 行数 | 典型形态 |
|---|---|---|
| 类型断言 `x, _ := v.(T)` / `x, _ = v.(T)` | **73** | `mi.CanDisableThinking, _ = r["canDisableThinking"].(bool)` |
| 写响应 / 清理 `_, _ = w.Write(...)`、`_ = f.Close()`、`Flush`、`os.Remove` | **16** | 写失败无法补救、Close 失败不影响主流程 |
| JSON 序列化/解析 `_, _ := json.Marshal(...)`、`_ = json.Unmarshal(...)` | **18** | 解析失败留下零值，消费方按"空值"处理 |
| **其他** | **27** | 逐条判定，已全部过判据（见 §六） |
| 合计 | **131** | 按包：upstream 最多，其后 server / panel |

> **勘误（2026-10-16）**：早期版本写作「123 处，类型断言 6 处」——人眼抽样
> 严重少算（实测按行计数 **73** 行）。数字不是规范，但**口径必须写死在命令上**，
> 否则下次审计会在错误的分类上继续推理。

---

## 三、判据

### 可忽略（保留 `_`，但必须有紧邻注释）

| # | 判据 | 例 |
|---|---|---|
| ① | **写响应后无法补救**：客户端已断开或已收部分字节，重试无意义 | `_, _ = w.Write(respBody)`、`_ = writeRaw(...)` |
| ② | **清理路径**：`Close`/`Remove`/`Flush` 失败不影响主流程与持久化正确性 | `_ = f.Close()`、`_ = client.Close()`、`_ = w.buf.Flush()` |
| ③ | **错误会在紧邻的下一步被检出**：忽略它不会让任何失败"消失" | `_ = os.MkdirAll(dir, ...)` 紧跟 `os.WriteFile` 且后者错误被完整处理——MkdirAll 失败必然导致 WriteFile 失败并上报 |
| ④ | **值已被上游检查过**：如刚判过非 nil，或类型断言有零值兜底 | 类型断言、`raw, _ := json.Marshal(...)`（结构体必然可序列化） |

### 不可忽略（必须处理：返回 / 记录 / 降级）

| # | 判据 | 为什么危险 |
|---|---|---|
| ① | **影响持久化正确性**：`SaveAtomic`/`os.WriteFile`/账号落盘 | 进程重启后数据丢失，且**不报错** |
| ② | **影响状态机推进**：pool 状态迁移、冷却/熔断计数、会话粘性绑定 | 状态与实际不符，后续决策基于错误状态 |
| ③ | **影响出站内容**：body 改写、提示词注入、header 构造 | 发给上游的东西与预期不符，可能触发风控 |
| ④ | **影响计量**：usage 记账、token/credit 统计 | 成本视图失真，且失真不可见 |

### 灰色的处置顺序

遇到不确定的：**先问"忽略它，失败会以什么形式被看见？"**
- 答不上来 → 按不可忽略处理（至少加一行 WARN）
- 答得上来且合理（如③）→ 可忽略，但补注释写明这条推理

---

## 四、审计执行方式

```powershell
# 导出全部忽略点（生产代码，排除测试）
Get-ChildItem -Recurse -Filter *.go -Path internal,cmd |
  Where-Object { $_.Name -notmatch '_test\.go$' } |
  Select-String -Pattern '_ = |, _ :=|, _ ='
```

**不追求给 123 处全补注释**——那会产生噪音 diff，且多数（类型断言 / `w.Write`）
自明。只补**容易被误读为疏漏**的那些：pool 状态迁移附近、持久化路径附近、
以及任何 `_ = <标识符>` 形态（见下）。

### 特别标记：`_ = <标识符>` 形态是异味

`_ = someVar`（丢弃一个**已有变量**）与 `_ = f()`（丢弃返回值）性质不同：

- `_ = f()` 是"我不关心这个返回值"——正当。
- `_ = someVar` 通常意味着**某个变量被算了但没用上**，编译器会报 unused，于是用
  `_ =` 压下去。**这掩盖的是"这段逻辑白算了"或"结果本该被使用"**。

实测样本（已修）：`internal/panel/autotask.go` 的专家召唤链里 `ok, fail := 0, 0`，
`fail` 累加了 3 处失败却从未使用，末尾 `_ = fail` 抑制检查——后果是**召唤失败对
运维完全不可见**（返回值只报成功数，看起来全成功）。修法：加 WARN 日志暴露失败，
不改返回值格式（对调用方零行为变更）。

---

## 五、给未来改动的检查清单

改动涉及错误处理时，逐条自问：

1. 我忽略的这个错误，**失败时谁会知道**？答不上来就不能忽略。
2. 我写的 `_ =` 是丢弃返回值还是压制 unused？后者几乎总是错。
3. 注释有没有说清"为什么可忽略"？只说"忽略"不算。
4. 这条路径在**进程重启 / 高并发 / 上游异常**三种情况下还成立吗？

---

## 六、2026-10-16 全量审计结论（131 处已全部过判据）

除类型断言 / 写响应 / 纯 Marshal 三类自明项外，**48 个"需要判断"的点已逐条读上下文
并过判据**（每条读前后 ≥25 行、跨函数读调用方）。结论：

### 已修（真吞错误，判据"不可忽略"）

| 位置 | 问题 | 修法 |
|---|---|---|
| `internal/panel/autotask.go` `runAutoAll` 回读处 | 进度回读错误被 `_ =` 吞掉 → `after` 为 nil 时**自动领奖静默跳过**，与「任务不存在」不可区分（判据④ 计量） | 捕获 `aerr`，WARN 日志（含脱敏 `logfmt.Label`）；返回值格式不变 |
| `internal/panel/taskcenter.go` `runGrowthQueued` 回读处 | 同上（队列路径同一语义被复制了一份 → 两处必须同改，否则口径分叉） | 同上 |
| `cmd/credit/main.go` `filepath.Glob` | Glob 错误被吞 → 报「0 个账号」且 exit 0，把**配置写错伪装成没有账号**（判据④） | 打 stderr + `os.Exit(1)` |
| `internal/reqlog/reqlog.go` `closeFile` | 轮转/停机时 `_ = w.buf.Flush()` 会**静默丢掉最多 64KB 审计日志**（判据④ 持久化完整性） | 改走 `w.flush()`（失败经 `setErr` 进 `stats().LastError`，面板可见） |
| `cmd/server/main.go` 停机 | `_ = store.Close()` ——超时放弃排空时「最后一笔 Redis 镜像没写完」完全不可见 | 包 `if err != nil { log.Printf WARN }` |

> 本轮同时在 `panel/autotask.go` 修掉了上一轮的 `_ = fail`（压制 unused 变量）——
> 那类形态不是"忽略返回值"，是"结果本该被使用"，见 §四。

### 已补注释（容易被误读为疏漏的位置）

`internal/pool/persist.go`（MkdirAll → 判据③）、`internal/upstream/model_catalog.go`
（MkdirAll → 判据③）、`internal/redisstore/redisstore.go`（Ping 失败后的 Close → 判据②）、
`internal/reqlog/reqlog.go`（文件句柄 Close → 判据②）、`cmd/server/main.go`（停机 Close）。

**注释形式统一为**：`// 忽略：<原因>，不影响 <什么>（判据<编号>）`。

### 判定为可忽略（未改，抽样理由）

- `names, _ := c.fetchGlobalModelsOnce(a)`、`msgs, _ = mergeAdjacentToolCalls(msgs)`、
  `_, _ = a.BackfillRealm()`：第二返回值分别是 `infos` / `changed` / `string`，**不含
  error** —— 看到 `, _ :=` 不要想当然认为丢了错误，先看签名。
- `raw, _ := json.Marshal(结构体)`、`jar, _ := cookiejar.New(nil)`、`fl, _ := w.(http.Flusher)`：
  判据④（必然可序列化 / 无错误 / 类型断言零值兜底）。
- `x, _ := io.ReadAll(resp.Body)`：后续解析失败会被上报，错误不会消失（判据③）。

### 曾经的「留待产品定夺」——**四项已全部实施**（2026-10-17 核对）

> 这张表原先写着「未改，避免单方面改口径」，但四条都已随
> `docs/prd/REQ-usage-unknown-admission-2026-10-16.md` 的 **P1–P4** 落地。表格长期不更新会
> 让下一个会话以为「还没做」而重复立项——所以这里按实测改写，并给每条的落点与验证方式。

| 原疑点 | 现状 | 落点 | 语义变化 | 还缺什么 |
|---|---|---|---|---|
| `server/handler.go` 的 `usage.credit / total_tokens` 类型断言 | **P1 已收紧** | `usageCreditTotal`（`handler.go:1021-1035`）返回 `ok` 标志；零值兜底不再被当真值写进 `NoteModelCost` | 计量口径**已改变**（这是当初要求「产品定夺」的点，已定） | — |
| `server/chatrun.go` / `logging.go` 的 `UsageCacheMissTokens` | **P2 已两处同改** | `chatrun.go:363-370`（流式）与 `:453-460`（非流式）都改成「miss 未知 → 只打 `cache_miss_unknown` 标记，不写 0」；`reqlog.go:84-88` 落字段 | miss 未知 vs 全命中**已可区分** | 产线尚**从未观测到** `cache_miss_unknown`（6916 行 reqlog 里 0 次）→ 见 `PLAN-open-items` 的 B1 |
| `server/anthropic.go` 的 `json.Unmarshal(body, &peek)` | **P3 已改** | `anthropicStreamIntent`（`anthropic.go:26-40`）用 `json.RawMessage` + 二次解析，类型非法时返回**诊断 note** 而不是静默走非流式 | 「不知道客户端要什么」不再伪装成「知道」 | 只单测覆盖，未真机跑 |
| `cmd/login/main.go` 的 `runPoll` 状态码分档 | **P4 已改** | `pollFailureMessage`（`main.go:237-253`）四档：0/5xx、401/403、404、其它 4xx；且 4xx 只报状态码、不回显上游原文 | 真 401/403 会明确说「继续等待不会成功」 | 只单测覆盖，未真机跑（D2 问的是「401 是否该走上游错误档」这个更细的语义，与本条不重叠） |

**判断口径不变**：以上四条不构成静默吞错先例——共同点是错误被**转换成用户/记账可用的
信息**（ok 标志、unknown 标记、诊断 note、可执行文案），而非消失。新增 `_ =` 仍受 R10 约束。

