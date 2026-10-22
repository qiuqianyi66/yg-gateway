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
| F-101 | **凭证 ACL 常态加固**：新建凭证后自动收紧（或面板显式提示重跑 `lock-auths.ps1`）——✅ 已落地（commit `01d76c4`，`internal/auth/lockdown.go` + `auth.go` 挂点 + `lockdown_test.go`，ADR-012） | P0 |
| F-102 | 补齐安全审计发现的高危项（待审计结论） | P0 |
| F-103 | 依赖漏洞门禁：`govulncheck` 进 CI 硬门禁——✅ 已落地（commit `962fdc8` 起 test job 即含 govulncheck 步骤、PR/push main 必跑、无 continue-on-error；现随 `9c68947` 合并在 `.github/workflows/release.yml` 的 test job） | P1 |

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
| F-201 | **停止打断式全量重绘**：5s 轮询不应重置 hover/tooltip/焦点态——✅ 已落地（commit `5ecfe71`，`accRenderSig`/`accountsSig` 数据签名短路 + 表内有焦点时跳过本轮渲染） | P1 |
| F-202 | 补齐高频运维能力（待前端调研结论）：导出、慢请求/失败聚焦、批量操作等 | P2 |
| F-203 | 窄屏细节：`.nav .sig` 在 ≤760px 溢出侧栏 | P2 |
| F-204 | **错误态就地重试**：失败视图在错误区内提供重试按钮，不要求滚回顶部——✅ 已落地（commit `acc7c0b`，`loadErrorHTML`/`bindRetry`，用量页 `app.js:3674`、积分构成页 `app.js:4202`，测试 `internal/panel/frontend_ux_test.go`） | P2 |

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
| F-301 | **热路径去重**：单个请求体的全文档 `json.Unmarshal` 从 7~8 次降到 2~3 次——✅ 已落地（首pass `73911e4`；RequestView 单次解析 `981b0a1`，`internal/session/requestview.go` + `requestview_test.go`） | P0 |
| F-302 | **热路径降锁**：去掉每次尝试的 `Pool.Status(uid)` 重锁调用——✅ 已落地（commit `73911e4`，改为 `Pool.RealmOf(uid)` 单字段查询，不再全池 RLock + 构造整个 Status） | P0 |
| F-303 | **reqlog 环形缓冲**：消除每请求 100 条 Event 的 O(n) 拷贝——✅ 已落地（commit `73911e4`，`recent [recentLimit]Event` + `recentHead` 写入位，无切片整体拷贝，`reqlog_test.go`） | P0 |
| F-304 | 404/405 返回 OpenAI 错误信封而非 text/plain 页面——✅ 已落地（兜底注册与 `notFound` 在 commit `73911e4`；牙齿测试 `notfound_test.go` 在 commit `600fc48`） | P1 |
| F-305 | 可观测性补齐：重试放大率、慢请求追踪——✅ 已落地（commit `5b3c245`，`yg_gateway_upstream_attempts_per_request` 直方图（整数分桶 1/2/3/4/6/8/+Inf，全局+model 标签）+ `attempts_total` 放大率分子 + `slow_requests_total`（阈值 `observability.slow_request_threshold_seconds` 默认 120s）；`internal/metrics/attempts.go` + `internal/server/retry_metrics.go`，零新增依赖） | P1 |
| F-306 | 断路器 half-open——✅ 已落地（commit `9489e98`，`internal/pool/breaker_halfopen_test.go`；ADR 职责由 **ADR-003 D-400** 覆盖，含 Given-When-Then 与回滚，无需新建 ADR） | P1 |
| F-307 | 全局准入控制 / 在途上限 429——✅ 已落地（commit `9489e98`，`internal/server/admission_test.go`、`internal/pool/globaladmission_test.go`；ADR 职责由 **ADR-003 D-401** 覆盖） | P2 |

> **落地状态汇总（2026-10-21 回填）**：以上每行注记均经 `git show <sha>` / `git log -S/-G` 独立实证（非转述）。映射：F-101→`01d76c4`；F-103→`962fdc8`（现 release.yml test job）；F-201→`5ecfe71`；F-204→`acc7c0b`；F-301→`73911e4`+`981b0a1`；F-302→`73911e4`；F-303→`73911e4`；F-304→`73911e4`（生产兜底）+`600fc48`（牙齿测试）；F-305→`5b3c245`；F-306/F-307→`9489e98`（ADR-003 D-400/D-401）。

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

> 落地状态（2026-10-21 回填）：此节旧问题已解决——首pass去重见 `73911e4`，RequestView 单次解析落地见 `981b0a1`（`internal/session/requestview.go`，配套 `requestview_test.go`、`hotpath_bench_test.go`）。原取证表格保留备查，行号为落地前快照。

### 最该先动的三处（已核对行号属实）

1. **`reqlog.go:193`** `r.recent = append([]Event{e}, r.recent...)` —— 每请求新建并整体拷贝 100 条 Event。
2. **`handler.go:838`** `if a, ok := h.cfg.Pool.Status(uid); ok && a.Realm != ""` —— 仅为取 realm，却走全池 `RLock` + `statusOf` 完整构造（含两次 sort）。
3. **`handler.go:764`** `reqHasImage := hasImagePart(body)` —— 无条件调用，但只在 `hintContext`（错误路径）被用到。

> 落地状态（2026-10-22 复核回填）：①②③均已在 commit `73911e4` 消除——①reqlog 环形缓冲
> `recentHead`；②realm 改 `Pool.RealmOf(uid)`（去全池 RLock + statusOf）；③带图探测改惰性
> `imageProbe` 闭包（成功路径不解析，仅错误 hint 首次求值并缓存），后由 `ee0cd92` 方法化为
> `chatRun.hasImage()`（`internal/server/chatrun.go:82-99`：`imageDone` 缓存，视图可用走
> `hasImageView` 共用 messages 子树、否则回落 `hasImagePart`）。原 unknown 已消解为 observation
> （`git log -S` 实证）。

### 已确认"做得好、不要动"（避免优化破坏既有正确性）

`resolveModel`（零分配最优）、超时不换号不罚号、内容类错误 fail-fast、11102 负缓存的半开语义、`usedSeq` 规避 Windows 时钟精度、transport 半死连接加固、取消传播链路、租约 CAS 无锁设计、面板 36 个 API 全部 `withAuth`。

---

## 五、前端调研结论（已回收，33 条，按 ROI 排序）

### 已在本轮修复（缺陷，轻量路径直接改）

| # | 缺陷 | 位置 | 后果 | 状态 |
|---|---|---|---|---|
| 36 | **Key 弹窗对 `input.value` 误用 `esc()`** | `app.js:1109-1110` | 名字含 `&` 的 Key 显示/保存被二次转义（`A&B`→`A&amp;B`→`A&amp;amp;B`），ID 参与鉴权匹配更严重 | ✅ 已修（commit `27e3c98`，含牙齿测试） |
| — | **密钥门 `onsubmit` 内联属性被 CSP 拦截** | `index.html:1480` | 回车提交密钥整页刷新 → 密钥丢失 → 401 循环 | ✅ 已修（commit `d38179a`，Playwright 验证） |

### P0（半天内可完成，收益最大）

| # | 缺陷 | 位置 | 方案 |
|---|---|---|---|
| 1 | 切视图静默丢弃未保存的配置改动 | `app.js:352` | `cfgDirty` 标志 + 确认拦截 + `#cfgNote` 显示「● 未保存」 |
| 2 | 启动时 overview 失败 = 全页静默空白 | `app.js:1603`/`524` | `booted` 标志；首屏失败插持久化错误条（复用 Key 页 `.key-load-error` 范式）——✅ 已落地（commit `acc7c0b`，首屏未拿到数据时表内插持久化错误条 + 就地重试，`app.js:866-876`，`.load-error` 范式） |
| 3 | 浏览器返回键/手改 hash 不工作 | `app.js:361` | 加 `hashchange` 监听，导航改 `location.hash` |
| 4 | Esc 只关两个 Key 弹窗，其余四个关不掉 | `app.js:1340-1345` | 统一 `VEILS` 列表，Esc 从后往前关当前 `.on`——✅ 已落地（commit `5ecfe71`，`VEIL_CLOSERS` 注册表 + Esc 从后往前关，`app.js:2157-2190`） |
| 5 | **5s 全量 `innerHTML` 重绘打断交互** | `app.js:1605`/`382` | 数据签名短路 + `activeElement` 在表内时跳过 |
| 7 | 窄屏 topbar 与任务队列行被裁掉 | `index.html:139`/`97` | `.topbar{flex-wrap}`；`#qcList` 包 `.tbl-wrap` |
| 8 | 「刷新」按钮实际是打上游刷余额 | `app.js:1584` | 拆成纯刷新 + 独立「刷新余额」（带确认） |
| 9 | 批量/长任务无进度无汇总（`console.log`） | `app.js:1688` | 结果渲染进 `#taskState`，复用 `.qprog` 进度条 |
| 10 | 切视图不回顶 | `app.js:345` | `go()` 加 `scrollTo({top:0})`（初始那次跳过）——✅ 已落地（commit `5ecfe71`，`skipGuard` 首跳跳过的 `window.scrollTo({top:0})`，`app.js:568`） |

### P1（无障碍与状态管理）

| # | 缺陷 | 方案 |
|---|---|---|
| 11 | 弹窗无 `role="dialog"`/focus trap/焦点回收 | 加 role + 15 行 trap + `lastFocus.focus()` |
| 12 | Toast 无 `aria-live`、无堆叠上限 | 加 `aria-live="polite"`；上限 4 条；错误改 8s |
| 13 | 删除/禁用无 undo，用原生 `confirm()` | 自绘确认弹窗；移除账号/删 Key 加 5s undo |
| 14 | **对比度不达 WCAG AA**（`--ink-3` 3.08–3.82） | 只改 4 个令牌值（`--ink-3`/`--warn`/`--ok` 明暗两套） |
| 15 | 无 `<main>`/无 `<h1>`/`<th>` 无 `scope` | 语义标签增补，CSS 保视觉 |
| 16 | 任务中心扫描结果一离开视图就丢 | `lastScan` 缓存 + 回视图时渲染并标注时间 |
| 17 | 配置页读取失败只有 3.6s toast | 顶部持久化错误块 + 失败时 disable 保存 |
| 18 | `change` 监听挂 window → 任何控件改动都重写主题图标 | 改 `matchMedia` 监听 |
| 19 | 轮询无 `AbortController`，慢响应覆盖新响应 | 各 loader 持 controller，新一轮 abort |
| 20 | 页面隐藏仍轮询 | `visibilitychange` 暂停/恢复——✅ 已落地（commit `7c1601f`，`app.js:2522`） |
| 21 | 死代码（`.srow` 等 19 行 CSS、重复行） | 清理 |

### 新功能（按 ROI）

**高价值低成本**：#22 CSV 导出（全站无 Blob/download）· #23 筛选状态持久化 + URL 同步 · #24 一键复制诊断（脱敏）· #25 表头排序 + 列控制 · #28 骨架屏 · #33 空态补 CTA

**高价值中成本**：#26 请求记录详情抽屉（取代 title tooltip）· #27 图表交互（图例开关/HTML tooltip/点击下钻）· #29 命令面板 Cmd+K · #30 通知中心 · #31 账号池批量选择 · #32 虚拟滚动/分页

### 信息架构专项

- 首屏**缺**「今天失败数 / 成功率」（运维第一关心）→ 建议加第 3 张 hero。
- **积分构成页最严重**：每账号一个 box 各含整张 7 列表（12 账号 = 12 张表堆叠）→ 改单表 + 账号列。
- 模型表几百行无分页；请求记录 1000 行 × 10 列全量 DOM。
- 运行日志是**唯一控制住长度**的视图（限高 460/420）。

### ⚠️ 隐性约束（必须遵守）

`frontend_test.go` 用 `indexOf` 按函数名**切片**喂 `node/vm`（见 `app.js:3122-3130`、`3264-3268`、`3328-3332` 三处专门注释）。任何新增**顶层语句/常量**必须避开这几个切片区：
`[PK_ACCOUNT_COLORS, renderExpiryDistribution)`、`[PK_DEFAULT_DETAIL_LIMIT, renderPackages)`、`[expBatches, loadExpiry)`、`[requestLogText, fmtBytes)` —— 否则测试 ReferenceError。

---

## 五之二、安全审计最终结论（主 agent 亲自核查，替代失败的 subagent）

| 面 | 结论 |
|---|---|
| 依赖漏洞 | `govulncheck ./...` **零漏洞** ✅ |
| 鉴权覆盖 | 面板 36 个 `/panel/api/*` **全部** `withAuth`；仅 `index`/`app.js` 公开 ✅ |
| 多 Key 约束绕过 | 逐条验证：缺 model / 类型非法 / null / realm 前缀 / `models:[]` **全部挡住**，错误码不泄漏 Key 身份 ✅ |
| XSS | 60 处 `innerHTML` 扫描，24 处"未 esc 插值"经判读**全是内部计算值**（数字/CSS 类名）✅ |
| 路径穿越 | UID 白名单 `[A-Za-z0-9_-]` ≤64 ✅ |
| 凭证 ACL（Windows） | **发现未加固（高危）→ 已执行 `lock-auths.ps1` 修复**，复验仅 `<user>`+`SYSTEM` ✅ |
| 入站 body 上限 | 默认 64MB 恒开 + 牙齿测试 ✅ |
| 资源有界 | `usage` maxBuckets 400k + Rollup；session TTL+GC；reqlog 定长 100 ✅ |
| goroutine/定时器泄漏 | Pool/session/usage/monitorBody 关闭路径**均完整** ✅ |
| SSRF | 上游 base URL 全为**编译期常量**，仅 config.json（本机管理员）可覆盖 ✅ |
| 响应头 | 面板+API 统一 CSP/nosniff/DENY/no-referrer/COOP/CORP ✅（`/v1/*` 无这些头，但返回 JSON 无 XSS 面） |
| 错误响应 | 不回传凭证；两处 `err.Error()` 为请求侧/上游解析错误，有意透传便于排查 ✅ |

**结论：未发现新的高危漏洞**（唯一高危是部署态凭证 ACL，已修）。

---

## 六、待 YG 拍板的决策点（阶段 2 的 D-编号输入）

1. **优先级排序**：安全（F-101/F-102）／前端（F-201）／后端（F-301~303）先做哪个？
2. **本轮范围**：只做"低风险立即做"清单，还是连 ADR 项（F-306/F-307、选号去写锁、EWMA 权重、分位数）一起？
3. **观测依赖**：是否接受为分位数引入 `hdrhistogram`（否则用固定分桶自实现）？
4. **选号去写锁**（唯一全池串行点）：收益最高但改动面中等，是否立项？

---

## 七、下一步

调研回收 → 补全本文档 → **等 YG 确认** → 阶段 2（细化方案 + D 编号决策）→ 阶段 3（任务拆分 ≤4h）→ 阶段 4-5（编码 + 牙齿测试）→ 阶段 6（评审）→ 部署验证。
