---
last_updated: 2026-10-12
status: 待确认（阶段 1 产出，需 YG 拍板后进阶段 2）
owner: YG
modification_level: L2~L3（含热路径重构选项与 ADR 候选）
research: 本轮三份调研（安全审计 / 前端效果 / 后端与路由）
---

# YG Gateway 深度优化轮 — 需求分析（PRD）

> 背景：多 API Key 派发（一期）+ 面板 Key 管理（二期）已交付并上线。
> 本轮目标：**安全加固为头等大事 + 前端体验增强 + 后端与网关路由深层优化**。
> 流程：十阶段流水线，阶段 1 需求须经用户确认后才进阶段 2。

---

## 一、范围边界

### 做什么（三个方向）

| 方向 | 目标 |
|---|---|
| **A. 安全（头等）** | 消除已发现的高危项，补齐可观测的加固缺口 |
| **B. 前端体验** | 修复已验证的体验缺陷，补齐运维面板"标配"能力 |
| **C. 后端/路由** | 热路径去重与降锁、调度与熔断更深一层、可观测性补齐 |

### 不做什么（明确排除）

1. **不引入重量级依赖**：Prometheus / OTel / hdrhistogram 是否引入需单独拍板（项目哲学"最少依赖"，当前仅 12 模块）。
2. **不改上游协议报文**（R8 红线）。
3. **不碰凭证链路承重墙**（除非安全审计要求）。
4. **不做多租户计费 / 配额**（沿用 PRD-二期排除项）。

---

## 二、需求（F 编号）

### A. 安全（P0，头等）

| F-编号 | 需求 | 优先级 |
|---|---|---|
| F-101 | **凭证 ACL 常态加固**：新建凭证后自动收紧（或面板显式提示重跑 `lock-auths.ps1`） | P0 |
| F-102 | 补齐安全审计发现的高危项（待审计结论） | P0 |
| F-103 | 依赖漏洞门禁：`govulncheck` 进 CI 硬门禁 | P1 |

**F-101 验收（Given-When-Then）**
- Given 面板新增一个账号后 / When 检查 `auths/*.json` 的 ACL / Then 仅当前用户 + `SYSTEM` 有权限，**不含** `BUILTIN\Users` 或 `Authenticated Users`
- Given 服务运行中 / When 加固凭证 ACL / Then 服务读取不受影响（面板 200、选号正常）

**F-101 现状证据（已实测，非推断）**
```
加固前：BUILTIN\Users Allow ReadAndExecute；Authenticated Users Allow Modify
加固后：<domain>\<user> Allow FullControl；NT AUTHORITY\SYSTEM Allow FullControl
```

### B. 前端体验（P1）

| F-编号 | 需求 | 优先级 |
|---|---|---|
| F-201 | **停止打断式全量重绘**：5s 轮询不应重置 hover/tooltip/焦点态 | P1 |
| F-202 | 补齐高频运维能力（待前端调研结论）：导出、慢请求/失败聚焦、批量操作等 | P2 |
| F-203 | 窄屏细节：`.nav .sig` 在 ≤760px 溢出侧栏 | P2 |
| F-204 | **错误态就地重试**：失败视图在错误区内提供重试按钮，不要求滚回顶部 | P2 |

**F-204 验收**
- Given 用量页读取失败 / When 查看错误区 / Then 错误区内**就地**有「重试」按钮，点击即重新拉取
- Given 积分构成页读取失败 / When 同上 / Then 同上

**F-204 现状证据**（已核对）：`app.js:1315` 仅 Key 页有 `btnKeyRetry`；`app.js:3174-3175`（积分页）与 `app.js:2725-2726`（用量页）失败时只渲染文本，重试依赖 header 的 `btnPk`/`btnUsage`。

**F-201 验收**
- Given 用户悬停在某账号行的 tooltip 上 / When 下一轮 5s 轮询完成 / Then tooltip **不消失**，行内交互态保持
- Given 用户正在表格内选中文本 / When 轮询 / Then 选区不被清除

**F-201 现状证据**：`app.js` `refreshVisible()` → `loadOverview(true)` → `renderAccounts()` 走 `tb.innerHTML = ...` 全量重绘，5s 一次。

**F-203 现状证据**（iPhone 14 视口 390px 实测）：文档级横向溢出 **0px**（无破版）；溢出元素集中在 `.nav`（刻意 `overflow-x:auto`）与其 `.sig`。

### C. 后端与路由（P0/P1）

| F-编号 | 需求 | 优先级 |
|---|---|---|
| F-301 | **热路径去重**：单个请求体的全文档 `json.Unmarshal` 从 7~8 次降到 2~3 次 | P0 |
| F-302 | **热路径降锁**：去掉每次尝试的 `Pool.Status(uid)` 重锁调用 | P0 |
| F-303 | **reqlog 环形缓冲**：消除每请求 100 条 Event 的 O(n) 拷贝 | P0 |
| F-304 | 404/405 返回 OpenAI 错误信封而非 text/plain 页面 | P1 |
| F-305 | 可观测性补齐：重试放大率、慢请求追踪 | P1 |
| F-306 | 断路器 half-open（**需 ADR**） | P1 |
| F-307 | 全局准入控制 / 在途上限 429（**需 ADR**） | P2 |

**F-301 验收**
- Given 一个典型聊天请求（多轮 messages + tools） / When 处理 / Then 全文档 JSON 解析次数 ≤3（可用计数断言或基准回归）
- Given 大 body（≥256KB） / When 与优化前对比 / Then ns/op、B/op、allocs/op 均显著下降（基准已建立，见 §四）

**F-302 验收**
- Given 一次上游尝试结束 / When 记录用量 / Then 获取 realm 不触发全池 `RLock` 与完整 `statusOf` 构造（不含两次 sort）

**F-303 验收**
- Given 连续记录 N 条事件 / When 每条 `Record` / Then **零**切片整体拷贝（环形缓冲，allocs/op 显著下降）

---

## 三、非功能约束

- **红线零违反**：R1/R2/R3/R5/R8 等 15 条不得破（尤其 R8：上游协议数据不得改名；`rewriteModel` 改字节替换正是为保护前缀缓存键序）。
- **不改上游出站报文**：任何优化不得改变发往上游的字段与顺序。
- **向后兼容**：`usage.json` 若升版本，旧数据须无损（沿用 v5 的"未归因"兼容范式）。
- **零新增依赖**（除单独拍板的观测组件）。
- **测试有牙齿**：每条修复须验证"还原 bug 后 FAIL"。

---

## 四、影响分析与源码取证（关键行号已亲自核对）

### 热路径重复解析（F-301）

| 位置 | 用途 | 是否成功路径必需 |
|---|---|---|
| `handler.go:686` | `peek{Stream,Model}` | 是 |
| `handler.go:694` | `modelRaw`（Model 原始类型） | 是 |
| `handler.go:702` | `ToolNameAllowlist` | 是 |
| `handler.go:744` | `session.ExtractKey` | 是 |
| `handler.go:764` | `hasImagePart` | **否**（仅错误 hint 用） |
| `logging.go:83` | `parseModelFromBody` | 是 |
| `handler.go:901` | `ResolveConversationID` | 是 |
| 出站 | `prompt.Rewrite` + `payload.Prepare` + `rewriteModel` | 是 |

**实测基准**（i7-14650HX，40 轮 messages + tools，基准文件 `internal/server/body_parse_bench_test.go`）：

| 指标 | 现状（6 次解析） | 单次解析 | 收益 |
|---|---|---|---|
| ns/op | 1,676,043 | 294,601 | **5.7×** |
| B/op | 292,622 | 113,618 | **省 61%** |
| allocs/op | 782 | 52 | **少 93%** |

### 最该先动的三处（已核对行号属实）

1. **`reqlog.go:193`** `r.recent = append([]Event{e}, r.recent...)` —— 每请求新建并整体拷贝 100 条 Event。
2. **`handler.go:838`** `if a, ok := h.cfg.Pool.Status(uid); ok && a.Realm != ""` —— 仅为取 realm，却走全池 `RLock` + `statusOf` 完整构造（含两次 sort）。
3. **`handler.go:764`** `reqHasImage := hasImagePart(body)` —— 无条件调用，但只在 `hintContext`（错误路径）被用到。

### 已确认"做得好、不要动"（避免优化破坏既有正确性）

`resolveModel`（零分配最优）、超时不换号不罚号、内容类错误 fail-fast、11102 负缓存的半开语义、`usedSeq` 规避 Windows 时钟精度、transport 半死连接加固、取消传播链路、租约 CAS 无锁设计、面板 36 个 API 全部 `withAuth`。

---

## 五、待补充（调研回收后填入）

- 安全审计 subagent 的完整结论（越权 / SSRF / DoS / 响应头）。
- 前端调研的完整结论（信息架构 / 空加载错误态 / 可访问性 / 缺失的高价值功能）。

---

## 六、待 YG 拍板的决策点（阶段 2 的 D-编号输入）

1. **优先级排序**：安全（F-101/F-102）／前端（F-201）／后端（F-301~303）先做哪个？
2. **本轮范围**：只做"低风险立即做"清单，还是连 ADR 项（F-306/F-307、选号去写锁、EWMA 权重、分位数）一起？
3. **观测依赖**：是否接受为分位数引入 `hdrhistogram`（否则用固定分桶自实现）？
4. **选号去写锁**（唯一全池串行点）：收益最高但改动面中等，是否立项？

---

## 七、下一步

调研回收 → 补全本文档 → **等 YG 确认** → 阶段 2（细化方案 + D 编号决策）→ 阶段 3（任务拆分 ≤4h）→ 阶段 4-5（编码 + 牙齿测试）→ 阶段 6（评审）→ 部署验证。
