# 深度优化轮（2026-10-12）— 阶段 1 需求分析进展

> 用户要求：① 先做需求分析 ② 列举主要代码规范 ③ 安全列为头等大事 ④ 前端效果增强 ⑤ 后端与网关路由深层优化。
> 流程：按 `.harness/rules/开发流程规范.md` 十阶段推进，阶段 1 需求未经用户确认不得进阶段 2/4。

---

## 已完成（本轮）

### A. 代码规范清单 ✅
产出 `.harness/rules/CODE-STANDARDS-CHECKLIST.md`：红线 15 条速查 + 命名/错误/并发/安全/前端/测试/流程/结构/自检清单/Commit 规范。

### B. 安全核查（头等大事）

**已执行并修复**
1. **凭证 ACL 未加固（高危，已修）**
   - 发现：`D:\yg-gateway\auths\*.json` 实际 ACL 含 `BUILTIN\Users: ReadAndExecute` +
     `Authenticated Users: Modify` —— 本机任意登录用户可读明文 accessToken/refreshToken。
   - 处置：执行 `lock-auths.ps1`（README 强制要求「每次加完账号都要跑」）。
   - 复验：ACL 仅剩 `秋千易\yanha` + `SYSTEM`；服务不受影响（面板 200）。
   - ⚠️ 后续：新建凭证会重新带上继承 ACL，**每次添加账号后必须重跑**。建议面板添加账号后自动触发或提示。

**已核查无问题**
- `govulncheck ./...`：**零漏洞**。
- 面板 API 鉴权覆盖：36 个 `/panel/api/*` 端点**全部**走 `withAuth`；仅 `index`/`app.js` 静态公开（设计如此，HTML 无秘密）。
- XSS 面：扫描 60 处 `innerHTML`，24 处"未过 esc 的插值"经人工判读**全部是内部计算值**（数字/CSS 类名/已 esc 片段），无用户输入未转义。
- 资源有界：`usage` 有 `maxBuckets=400_000` + Rollup；`session` 有 TTL 30min + GC 5min；`reqlog.recent` 定长 100。
- goroutine/定时器：Pool flusher、session GC（含 stop channel 竞争修复）、usage、monitorBody 关闭路径**均完整无泄漏**。

**待安全审计 subagent 交叉验证**（已派发）
- 越权/多 Key 约束绕过、SSRF、DoS、响应头。

### C. 后端与网关路由调研 ✅（已回收，含文件:行号证据）

**热路径最大项：单个请求体被完整 `json.Unmarshal` 7~8 次**
| 位置 | 用途 |
|---|---|
| handler.go:686 | `peek{Stream,Model}` |
| handler.go:694 | `modelRaw` |
| handler.go:702 | `ToolNameAllowlist` |
| handler.go:744 | `session.ExtractKey` |
| handler.go:764 | `hasImagePart`（**成功路径用不到**，仅错误 hint） |
| logging.go:83 | `parseModelFromBody` |
| handler.go:901 | `ResolveConversationID` |
| 出站 | `prompt.Rewrite` + `payload.Prepare` + `rewriteModel` |

**实测基准**（已跑，40 轮 messages + tools，i7-14650HX）：

| 指标 | 现状 6 次解析 | 单次解析 | 收益 |
|---|---|---|---|
| ns/op | 1,676,043 | 294,601 | **5.7×** |
| B/op | 292,622 | 113,618 | **省 61%** |
| allocs/op | 782 | 52 | **少 93%** |

**最该先动的三处（低风险、改动小）**
1. `reqlog.go:193` `append([]Event{e}, r.recent...)` — 每请求拷贝 100 条 Event → 改环形缓冲（零分配）。**已核对行号属实**。
2. `handler.go:838` `Pool.Status(uid)` 仅为取 realm，却走全池 RLock + `statusOf` 完整构造（含两次 sort）→ 改用轻量取 realm。**已核对行号属实**。
3. `handler.go:764` `hasImagePart(body)` 无条件调用 → 惰性化（成功路径省一次全文档解析）。**已核对行号属实**。

**其余低风险项（19 条，见调研原文）**：合并 body 解析、`rewriteModel` 改字节替换（避免破坏上游前缀缓存键序）、pick 内 prune 惰性化、`costTier` 三处重复计算缓存、`session.touch` Redis 写节流、`chatStatsReader` 零分配、`usage` 小时串缓存+分片锁、Rollup 分批、404/405 OpenAI 信封兜底、bufio 池化、`idleTick` 1s→5s、兜底探测令牌桶、轮转总时间预算、`endpoint` 预拼接、`ErrServer` 轮转预算=1。

**需 ADR（8 条）**：断路器 half-open、选号去写锁（唯一全池串行点）、全局准入控制 429、EWMA 成功率/延迟权重、分位数直方图（现只有均值）、排水式优雅停机、Prometheus 导出、session availableSet 快照化。

**可观测性缺口**：无分位数（痛点恰在长尾）、无重试放大率（attempts/completed，数据已在手）、无慢请求追踪。

**已确认做得好、不要动**：`resolveModel`（零分配最优）、超时不换号不罚号、内容类错误 fail-fast、11102 负缓存的半开语义、`usedSeq` 规避 Windows 时钟精度、transport 半死连接加固、取消传播链路、租约 CAS。

### D. 前端效果调研 ⏳（已派发，待回收）

---

## 待用户拍板的决策点（阶段 2 输入）

1. **优先级**：安全加固（凭证+half-open）／前端体验／后端性能，先做哪个？
2. **范围**：本轮只做「低风险立即做」19 条，还是连 ADR 项一起？
3. **ADR 项取舍**：half-open、选号去写锁、全局准入控制 这三项收益最高但改动面中等，是否立项？
4. **依赖**：分位数是否接受引入 `hdrhistogram` 新依赖（项目哲学是"最少依赖"，当前仅 12 个模块）？

---

## 下一步

安全/前端调研回收后 → 补全 PRD → **等用户确认** → 阶段 2（方案 + D 编号决策）→ 阶段 3（任务拆分 ≤4h）→ 阶段 4-5（编码+测试）→ 阶段 6 评审 → 部署验证。
