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

> **勘误（2026-10-16）**：本文早期版本写作「123 处，其中类型断言 6 处」——那是用
> 另一种模式（`_ = |, _ :=|, _ =`）在**人眼抽样**下的估计值，把类型断言严重少算
> （实测按行计数是 **73** 行）。数字不是规范的一部分，但**口径必须写死在命令上**，
> 否则下一次审计会在错误的分类上继续推理。

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

### 留待产品定夺（未改，避免单方面改口径）

| 位置 | 疑点 |
|---|---|
| `server/handler.go` 的 `usage.credit / total_tokens` 类型断言 | 零值兜底产物 `credit=0` 会被当真值写进成本台账（`NoteModelCost`），与判据④前提「值已被上游检查过」不符；收紧为 `c, ok := ...` 会改变计量口径 |
| `server/chatrun.go` / `logging.go` 的 `UsageCacheMissTokens` | miss 未知时记 0，与「全命中」不可区分；**必须两处同改**，只改一处会造成流式/非流式口径分叉 |
| `server/anthropic.go` 的 `json.Unmarshal(body, &peek)` | body 非法时下一步会报 400（判据③成立），但 `"stream":"true"` 类型错会被吞 → 静默走非流式分支 |
| `cmd/login/main.go` 的 `runPoll` 状态码分档 | 4xx 全归「登录未完成」，真 401/403 时用户按到天亮也不会成功且看不到上游状态码（`upstream-hint.md` 同族问题） |

