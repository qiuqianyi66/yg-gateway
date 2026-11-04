# PLAN — magpie 可借鉴项落地 yg-gateway（2026-11-04）

> 规划基线：HEAD = `d1a2436`（2026-11-04 13:34）。
> 依据：`docs/reference/magpie-rea-full-2026-11-03.md`（全盘 REA 结论）+ 本规划对当前 HEAD 的只读代码锚定。
> 方法：doubao-coding-design-solution（Evidence-locked design）。每条推荐回链当前源码；未知项显式标注。

---

## 0. 最重要的认知更新：大多数「待办」已被 4fb5639 落地

4fb5639（11-02 ban-risk-reduction）已实现并合入 HEAD：

| 待办 | 状态 | 证据 |
|---|---|---|
| #11 per-account proxy | ✅ 能力已实现 | `internal/upstream/transport.go:116-165`（HTTP CONNECT + SOCKS5）、`client.go:154-179 clientFor` / `:183-211 chatClientFor`（proxyClients 双检锁缓存） |
| 🔵-1 进程级 Session-ID | ✅ 已实现 | `internal/upstream/headers.go:140-144 newProcessSessionID`（36 hex，每次启动换新）；`headers.go:130` 旧 purpose=session 已弃用 |
| #12 11102 三值白名单 | ✅ 逻辑已实现 | `internal/server/models_handler.go:79-121 modelCatalogVerdict`（Known/Absent/Unknown）；`chatrun.go:813-828`（仅 modelCatalogAbsent fail-fast，Unknown 继续换号） |
| min_request_interval | ✅ 已实现（默认关） | `cmd/server/config.go:405-412`（单账号/global 分档，热生效 SetMinRequestInterval，默认 0） |
| 养号/活跃保持 | ✅ 已有完整体系 | `internal/scheduler`：签到 / token 保活 / 旅行 / 黑猫独立排程 |

**结论：规划不做「从零抄 magpie」，只做当前 HEAD 的真实缺口。**

---

## 1. 真实缺口与优先级

### P0-1　#11 语义修复：代理失败静默回退直连（fail-open）★直接封号风险

**证据**：`internal/upstream/transport.go:135/154/162` 三处失败分支全部 `log.Printf("WARN ... 回退直连")`——
proxy URL 解析失败、SOCKS5 初始化失败、不支持 scheme 时，**静默改用直连 Transport 出站**。
后果：显式配置了代理的账号在代理配错时走共享出口 IP，多账号风控关联重新出现，且不报错（最危险形态）。

**方案**：
- `newTransportWithProxy` 增加失败语义参数（或返回 `(tr, error)`）：显式代理构造失败 → 返回错误，**不**静默降级直连。
- `clientFor` / `chatClientFor`：代理构造失败 → 该账号出站失败（报错驱动换号/账号级失败），且**不得缓存坏 client 进 proxyClients**（当前构造失败仍会缓存直连 client，:172-178 需先改）。
- 保持向后兼容：`a.ProxyURL == ""` 的直连路径（:155-157 / :184-190）**不动**。

**反例击穿**：配坏 proxy URL → 请求必须失败且错误可观测（`proxyconnect`/`socks` 错误冒泡到账号失败路径），而不是成功但走直连。

**测试（可证伪）**：①坏 URL（`://`）②不支持 scheme（`ftp://`）③SOCKS5 不可达——三用例均断言「请求失败 + 未走直连」。现有 `client_test.go:543` 直连回落测试保持绿。

**回滚**：若加 `proxy_fail_closed` 配置开关（建议加，默认 true）可热关；二进制级走 R15 deploy-prod.ps1。

---

### P0-2　/status 代理观测

**现状**：全仓无代理生效观测字段（grep `proxy_effective`/`proxy_active` 零命中）。magpie 自身 per-account 代理观测也是 unknown（REA 文档第④节）——所以 yg 目标 = **可见**，不是精确测量。

**方案**：/status 每账号输出 `proxy_url`（redactProxyURL 脱敏）+ 三态 `effective`：direct / proxy / proxy_failed。P0-1 的失败语义返回点即状态来源。

**验证**：/status JSON 字段存在且与 config 一致；配坏代理时显示 proxy_failed。

---

### P0-3　#12 e2e 补测：目录为空 + 11102 → 仍换号

**现状**：三值**逻辑**已实现且有 isKnownModel 单测（`models_catalog_state_test.go:11-49` 覆盖空目录/含/不含三态）；`chatrun.go:813-828` 的「Unknown 继续换号、Absent 才 fail-fast」分支**无 chatrun 级端到端测试**。

**方案**：补 chatrun 测试——stub 上游对所有号返回 11102 + 目录为空（modelCatalogUnknown）→ 断言继续轮转尝试换号，不 400。
（AGENTS.md 纪律：缺口与否定断言必须穷尽证据；这是上一会话 #12 登记的原始缺口。）

**验证**：新测试先红后绿；`go test ./internal/server/ -run 11102`。

---

### P1-1　出站节奏增强（RPM）：先配置后机制

**现状**：`min_request_interval` 已存在但默认 0 关；全池被限时选最早恢复者已有（4fb5639）。
**magpie 对照**：60s+1s 滑动窗 RPM 硬闸 + 429 五分类分档退避 + 零成本 /v1/models 探测复活。

**候选比较**：
- A（建议先做）：给 yg 设合理 `min_request_interval` 默认值（待校准参数，requires 观测账号请求分布），文档 + 面板展示生效值。
- B（P2 独立项）：实现 magpie 式 rpmTransport 滑动窗硬闸——改动大，需单独设计。

429 分类：yg 现有 kind 体系（6004 rate_limit / 11102 model_unavailable，`pool/entry.go:171`）已分主类，是否需要细化到 magpie 五类（频率/额度/月度/单模型）→ 评估后定，不预设。

---

### P1-2　🟢-2 收敛：clientFor / chatClientFor 去重

**证据**：`client.go:154-179` 与 `:183-211` 结构完全相同（代理构造 + proxyClients 双检锁缓存），仅 HTTP client 构造参数不同（120s 总时长 vs 无总时长）。

**方案**：抽私有构造器 `clientForProxy(a, timeout, cacheKey)`，两个入口调用；`chatHTTP()`（:235-240）保留回落语义。

**验证**：现有测试保持绿；新增测试断言代理缓存 key 隔离（`chat:` 前缀）不变。

---

### P1-3　面板鉴权组合拳（对照 REA 文档 P1 建议）

**现状**：面板有 api_key 访问门（历史记忆），但 cookie/HttpOnly/常量时间/BREACH padding/allowlist 细节**未核实** → 标 **unknown**，requires 读 `internal/panel` 鉴权实现（index.go/security）。

**方案（确认现状后）**：随机 key + HttpOnly SameSite=Lax cookie + 常量时间比较（R5 `internal/httpauth` 已有）+ gzip 随机 padding 防 BREACH + allowlist fail-closed + 不信 XFF。

---

### P2（留档不排期）

- **🟢-4 模型快照复制**：已确认消除（2026-11-04 全仓 grep 原始输出：
  `git grep -i -E "copyModels|deepcopy|cloneModels|snapshot.*(copy|clone)|copy.*snapshot" -- '*.go'`
  仅命中 `internal/session/coverage_test.go:510-512`（会话快照独立副本测试，session 域，
  非 upstream 模型目录）；`internal/upstream` 下 Copy/copyModels/deepCopy/cloneModels
  零命中。**结论：upstream 模型快照复制已被 4fb5639 消除，无需再修**。
- **warm-up 错峰**：yg 已有每日签到 + token 保活（等价活跃保持），增量有限；仅当需要「最低成本 Haiku 探测」时才评估，当前不做。
- **三层锁（进程内 RWMutex + 跨进程 .renew.lock）**：单实例单进程当前无并发写凭证场景；未来多实例/CLI 并行切号时再引入。
- **PII 确定性占位符**：请求归并/缓存键稳定性的长期项。

---

## 2. 执行顺序与依赖

```
P0-1 代理 fail-closed ──► P0-2 /status 观测（依赖 P0-1 的状态来源）
P0-3 #12 e2e 补测（独立，可并行）
P1-1 节奏配置（依赖 P0-2 观测基线）
P1-2 🟢-2 去重（独立）
P1-3 面板鉴权（requires 先读 panel 现状）
```

第一批动手：P0-1 + P0-3（并行，互不依赖）。P0-2 紧随 P0-1。

## 3. 变更纪律（仓库硬约束）

- 每次改动建 `.harness/changes/{feat}/`：REQ（Given-When-Then）+ summary + 回滚说明。
- 门禁：`go test ./...` / `go vet ./...` / `gofmt -l` 零输出 / `check-boundaries.ps1` / `check-depth.ps1`（棘轮只许收紧）；提交前 `pre-commit.ps1`。
- 出站行为改动（P0-1）用 deploy-prod.ps1 换版（备份 → 冒烟 → 失败回滚）。

## 4. 待校准/未知清单

| 项 | 状态 | requires |
|---|---|---|
| 🟢-4 快照复制现状 | **已消除**（2026-11-04 全仓 grep 确认，见 P2 节） | — |
| 面板鉴权细节 | **已实施**（P1-3，b544037：Bearer 鉴权审计 + api() 强制鉴权机制化） | — |
| min_request_interval 默认值 | **已校准：保持 0 关**（2026-11-04，8 天 5745 请求观测：生产账号 0 次 429、无风控事件、忙时亚秒并发真实存在，数据支撑 0 关即正确默认；分析见 .harness/changes/2026-11-04-min-interval-calibration/） | — |
| 429 是否需要五分类细化 | **已评估：不细化**（2026-11-04 闭合：13 类 kind 已覆盖 magpie 五分类决策维度，429 可区分形态仅 14018 与 generic 两类；见 .harness/changes/2026-11-04-chatpaths-404-panic/summary.md） | — |
| PII 确定性占位符 | **不适用**（2026-11-04 闭合：prompt_cache_key 已确定性哈希派生，无请求归并依赖 PII） | — |
