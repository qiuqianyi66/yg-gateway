# ROADMAP：PRD v2 改进项落地路线图（2026-10-22）

> **定位**：本文件是并行执行的**合并基准（merge baseline）**。先于一切分支落定于 `main`；
> 所有并行分支自本文件的提交点开出；分支间文件所有权互斥（§7）；合入顺序见 §8。
>
> **事实源**（按优先级）：
> - `docs/research/YG-Gateway深度研究与产品PRD-v2.html`（§1.6 遗留清单、§2.2 需求分级、§3.5 优先级、§4.4 债务清单）
> - `docs/code-review/2026-10-20-four-dimensions.md`（§六 处理状态回填，含已修项落点 commit）
> - `AGENTS.md`（顶层硬约束）、`.harness/rules/*`（实施细则）
> - 实测代码基线（2026-10-22，仓库工作区 + `check-depth.ps1` 实测）
>
> **状态**：v1，待执行。执行入口 = §5 各项（REQ 粒度，含 Given-When-Then 验收）。

---

## 1. 行数口径声明（对齐 L-05）

全篇一律采用「**非空行**」口径（= `scripts/check-depth.ps1:86` 的统计口径，也是
`.harness/depth-baseline.json` 的 `maxFileLines` 口径）。四维度审查与 PRD 引用的
**原始行数**（如 client.go 2184）与口径有差，本文件以实测非空行为准，换算如下：

| 文件 | 原始行（审查/PRD） | 实测非空行（2026-10-22） | 备注 |
|---|---|---|---|
| `internal/upstream/client.go` | 2184 | **2058** | = 当前棘轮 `maxFileLines=2058`，已顶到硬顶 |
| `internal/server/handler.go` | 1491 | **1466** | |
| `internal/panel/autotask.go` | 1300 | **1244** | |
| `internal/usage/usage.go` | 1011 | 911 | `SnapshotWindow` 在 :666-895，全量深拷贝在 :674-679 |
| `internal/pool/pick.go` | — | 570 | 选号主函数 :62-314 |

其余 oversize 文件（`sse.go`、`panel.go`、`cmd/server/config.go` 等，棘轮
`oversizeFiles=5` 内的成员）**不在 PRD v2 债务清单**，本期不拆，记入 §9 待后续。

---

## 2. 已修项 / 明确不做（禁止重复实施）

### 2.1 已修项（全部已在 `origin/main`，实测 `3060b81`）

| 项 | 落点 | 依据 |
|---|---|---|
| usage 桶无界增长 | ADR-011，`c1f7de8` | 四维度 §六 #2 |
| R10 静默解析（blackcat/global_models/school） | `b9043a8`/`fa242e5`/`46729e8` | §六 #3a-3c |
| payload.go「静默 merge」 | **审查误判，未改**（`changed` 布尔为刻意设计） | §六 #3d |
| CheckRedirect 缺失 | `791ce96` | §六 #4a |
| CORS https-only | `bfdd1ca`（**刻意不做**前端源白名单，与 PNA 设计初衷冲突） | §六 #4b |
| 锁内全量序列化（saveMu 两段式） | `d21d5c2` | §六 #5 |
| **F-101 写回/启动自动收紧** | `01d76c4` + ADR-012；`internal/auth/auth.go:375`（SaveAtomic→LockdownFile）、`:441`（LoadDirReport 扫存量）、`internal/auth/lockdown.go:41-58`（icacls 本体） | 实测代码 |
| 并发正确性 | CI `race` job 实证（`00aa737` run 39558740374 绿） | §六 遗留 #2 |
| last-sleep 边界 | 2026-10-18 已修 | PRD §5.4 |
| 首调用上下文计数（F5） | `d4bf9b2`，`/status` 透出 task_metrics | 四维度 §六 遗留 #3 |

### 2.2 明确不做（边界否决，AGENTS.md + PRD §1.1/§4.1）

- 不是多租户计费/配额派发平台：**不做**充值、兑换码、OIDC、多语言（平台层运营多语言）。
- 不是通用 AI 网关：**不追求**接所有 provider；上游就是 CodeBuddy + 备用源。
  多上游接入模板（R-107）只做「OpenAI 兼容接入模板」抽象，不得动摇单上游绑定定位。
- 不是一次性分析工具：7×24 常驻服务，稳定性优先于功能广度。

---

## 3. 文档内部口径裁定（两份冲突的处理）

1. **F-101 优先级**：§4.4/§3.5 = **P0**（「R1 唯一人工兜底缺口」）；§2.2 需求分级表 = P1
   （来源「增强（遗留 P0 债务）」）。**裁定：P0** —— 与 AGENTS.md R1 安全红线优先级一致，
   且 §3.5 明示「P0 立即」。执行按 P0 走，但本项剩余工作量小（纯面板提示，§5 R-101）。
2. **面板多语言/主题**：§1.1「不做多语言」（语境 = 多租户平台 i18n 运营）vs README 路线图
   「面板多语言/主题完善」。**裁定：边界否决指平台层；面板 UI 语言属本地 UX，但本期不做**，
   记入 §9 待用户裁定，避免边界漂移。
3. 其余无冲突。

---

## 4. 决策项（阶段 0 预判，已实测，不派发分支）

### D-204 pick.go 选号优化 —— 预判「不立项」（必要性评审留痕随本文件提交）

- **实测证据**（`internal/pool/pick.go:62-314`）：收集候选 **O(n)**（:110-131）、maxCredits
  扫描 O(n)（:142-146）、costTier 预计算 O(n)（:175-182，ADR-003 已把三次 map 查找收成一次）、
  中位延迟 O(n)（:224）；**排序仅作用于候选子集**（:250 `sort.SliceStable`，k = 数十量级），
  权重在排序前预计算（:213-215 注释明说避免比较器内现算），:268-270 截断 top5 后 :294 加权抽签。
- **结论**：四维度审查原判「O(n log n)」**不成立于全池排序**；PRD 建议「可缓存预选列表或降为
  近似」——当前已是近似（top5 短名单），且 top5 并列公平 + 稳定兜底是**有意设计**。排序换
  「最大权重单次扫描」会丢掉该语义。**进一步优化空间很小**。
- **决策**：阶段 0 必要性评审**不立项**。触发重开条件：账号池 >500 且实测每请求选号
  >1ms（需观测证据，不允许凭推断重开）。
- **留痕**：`.harness/changes/2026-10-22-pick-necessity-review/summary.md`（随本文件提交）。

---

## 5. 落地项清单（REQ 粒度）

> 每项执行时：①在 `docs/prd/` 建对应 REQ 文档（**必须含「一、必要性（阶段 0，四问）」
> 章节**，格式参照 `docs/prd/REQ-harness-engineering-2026-10-19.md`，阶段 0 检查
> `check-req-necessity.ps1` 会校验）；②建 `.harness/changes/{feat}/`（summary + rollback +
> review，L2+ 独立复审）；③ADR（标注「需要」者，编号 **ADR-013 起按合入顺序取号**，
> 当前最大 ADR-012）；④编码 + 测试同提交（L-11）；⑤pre-commit 全绿。
>
> 编号规则：R-10x = P1 及功能增强；R-20x = 工程债。

### R-101 F-101 面板显式提示（P0）· 分支 `panel-features` · 无需 ADR

- **现状**：写回自动收紧 ✅（`auth.go:375`）、启动兜底 ✅（`auth.go:441`）；**面板无任何
  ACL 提示**（全仓 grep `internal/panel` 下 lock-auths/icacls/ACL/权限收紧 = 0 命中）。
  PRD §4.4：「新建凭证写回后自动收紧（已有）+ 面板显式提示 + 启动兜底扫历史文件」。
- **REQ 文档**：`docs/prd/REQ-f101-panel-acl-notice-2026-10-22.md`
- **changes 目录**：`.harness/changes/2026-10-22-f101-panel-notice/`
- **Given-When-Then 验收**：
  - Given 面板凭证管理区加载，When 用户查看，Then 展示「凭证已自动收紧 ACL（仅当前用户+SYSTEM）」说明文案。
  - Given 新增/编辑凭证成功写回，When 面板操作完成，Then 显式提示已自动收紧（或提示手动兜底路径）。
  - Given 收紧失败路径（`auth.go:376` WARN 不阻断），When 发生，Then 面板提示可人工运行 `lock-auths.ps1`（若实现只读状态端点）。
  - Given 状态端点（如实现），When 查询，Then 必须幂等只读，不执行任何收紧/写操作。
- **依赖**：无。**文件域**：`internal/panel/`（`app.js`/`index.html`/面板 handler，**不含** `autotask.go`）。
- **验证**：`go test ./internal/panel` + pre-commit 全绿 + 面板截图检查文案与布局。

### R-106 OpenRouter 费率面板展示（P2）· 分支 `panel-features`（与 R-101 同分支、独立 changes 目录）· 无需 ADR

- **现状**：USD 折算**后端已实现**（`internal/usage/usage.go:99 USDPrice`、`:465 Agg.USDValue`
  （omitempty）、`:664 pricing`、`:731 bucketUSD`；`cmd/server/config.go:19/153/511 defaultPricing`）；
  AGENTS.md 四态表明确此为 **kind=inference**（OpenRouter 公开价折算）。**缺面板展示**。
- **REQ 文档**：`docs/prd/REQ-openrouter-savings-display-2026-10-22.md`
- **changes 目录**：`.harness/changes/2026-10-22-openrouter-savings-display/`
- **Given-When-Then 验收**：
  - Given usage 聚合含 `USDValue`，When 面板加载用量区，Then 展示折算金额并带「估算（inference）」标注。
  - Given `USDValue` 字段缺失（四态 unknown，omitempty 省略），When 展示，Then 显示「未估算」而非 0（**禁止 `!field` 判否**）。
  - Given 后端字段已存在，When 仅前端改动，Then 不改变任何 wire 形状（四态省略语义不变）。
- **依赖**：无（后端已具备）。**文件域**：`internal/panel/`（同上，**不含** `autotask.go`）。
- **验证**：`go test ./internal/panel` + pre-commit + 面板截图（含缺失态与有值态两种渲染）。

### R-102 协议漂移预案（P1）· 分支 `protocol-drift` · **需要 ADR（L3 上游协议）**

- **现状**：全仓无任何上游接口版本快照/漂移检测（grep `drift|protocolVersion|snapshot|probe|fingerprint`
  无命中，probe 仅模型目录三路 UA 探测）。已有缓解：模型目录四级查找（`model_catalog.go:4-11`）、
  fallback 级联（`server/handler.go:1031-1037` + `:1348 fallbackForward`）、13 种 ErrKind
  错误分类单一来源、`r8_golden_test.go` 被动钉死报文。
- **REQ 文档**：`docs/prd/REQ-protocol-drift-2026-10-22.md`
- **changes 目录**：`.harness/changes/2026-10-22-protocol-drift/`
- **Given-When-Then 验收**：
  - Given 首次运行，When 探测成功，Then 将上游响应特征（conversationId 前缀 `wb2api-<ms>` 格式、`chatcmpl-wb2api` 哨兵、UA 握手、模型端点响应形状）写入**协议快照**（带观测时间戳，四态：快照缺失 = unknown）。
  - Given 快照已存在，When 周期/按需比对发现特征不符，Then 判定漂移并记录 WARN（不得静默吞，R10）。
  - Given 漂移已判定，When `/status` 被查询，Then 暴露漂移状态字段（四态：无漂移证据 = unknown/正常，缺失 ≠ 无漂移）。
  - Given 漂移状态，When 选号/转发，Then 降级提示可被下游消费（本项只做**观测层**，不自动改出站报文）。
  - Given 任何实现，When 触碰报文，Then **禁止**改 `headers.go` UA 指纹 / `payload.go` 哨兵（R8，`r8_golden_test` 不得回归）。
- **依赖**：无前置；**文件域**：`internal/upstream/` 新增文件（如 `protocol_snapshot.go`/`drift.go`，
  **不得改** `client.go`/`headers.go`/`payload.go` 现有函数与报文）+ `internal/server/handler.go`
  **仅** `/status`（:514）区与路由注册（:218-239）区 + `cmd/server/config.go` **仅**新增 Drift 配置结构。
- **验证**：单测（快照写入/比对/漂移判定/状态暴露）+ `r8_golden_test` 回归 + pre-commit 全绿。

### R-107 多上游接入模板（P2）· 分支 `protocol-drift`（与 R-102 同分支、独立 changes 目录）· **需要 ADR（L3 架构）**

- **现状**：fallback 仅**单** OpenAI 兼容端点白名单（`cmd/server/config.go:89-102 Fallback` 结构）；
  `handler.go:1348 fallbackForward` 硬编码原样透传；无 provider 抽象接口/SPI。
- **REQ 文档**：`docs/prd/REQ-multiupstream-template-2026-10-22.md`
- **changes 目录**：`.harness/changes/2026-10-22-multiupstream-template/`
- **Given-When-Then 验收**：
  - Given 配置多个备用端点，When CodeBuddy 账号池选号耗尽，Then 按配置顺序级联（不再单端点白名单）。
  - Given 接入新 OpenAI 兼容备用源，When 遵循接入模板，Then 仅需新增配置 + 实现接口方法，不碰 CodeBuddy 报文。
  - Given CodeBuddy 主链路，When 运行，Then 行为与响应形状不变（回归测试）。
  - Given 边界约束，When 设计，Then 明确「不做接所有 provider」——模板仅覆盖 OpenAI 兼容端点，README 同步更新。
- **依赖**：R-102 同分支并案（共用 config.go 与 handler.go 区域）。**文件域**：`internal/upstream/` 新增
  adapter 文件 + `internal/server/handler.go` **仅** `fallbackForward`（:1348）区 + `cmd/server/config.go`
  **仅** `Fallback` 结构区（:89-102）+ 路由注册区 + README。
- **验证**：单测 + fallback 级联集成测试 + pre-commit。

### R-201 工程债：client.go 拆分（P2）· 分支 `debt-split-client` · 无需 ADR

- **现状**：2058 非空行 = 当前棘轮 `maxFileLines` 硬顶。函数聚类（实测）：错误分类层 :47-720、
  Client 配置/端点层 :722-897、会话/鉴权层 :898-1103、聊天主链路 :1104-1355、模型目录层 :1356-1783、
  积分/账单层 :1784-2158、签到/杂项 :2159-2184。
- **REQ 文档**：`docs/prd/REQ-split-client-2026-10-22.md` · **changes**：`.harness/changes/2026-10-22-split-client/`
- **Given-When-Then 验收**：
  - Given 拆分完成，When 全量测试，Then 行为零变化（测试与改动同提交，L-11）。
  - Given 新文件，When 门禁，Then gofmt 无输出、`check-boundaries` 白名单不变（同包拆分，禁止新增跨包依赖方向）。
  - Given client.go 行数下降，When `check-depth.ps1 -Update`，Then 棘轮**只收紧**（maxFileLines 必须 ≤ 现 2058 且低于其余文件上限的合理值；禁止 -Update 掩盖回退）。
  - Given R8 报文文件，When 执行，Then **不得触碰** `headers.go`/`payload.go`（`r8_golden_test` 不回归）。
- **依赖**：无。**文件域**：`internal/upstream/client.go` + 同包新增文件（建议 `errors.go`/`billing.go`）。
- **验证**：`go test ./...`（23 包）+ pre-commit 全绿 + depth 棘轮收紧。

### R-202 工程债：handler.go 拆分（P2）· 分支 `debt-split-handler` · 无需 ADR

- **现状**：1466 非空行；已拆过 `chatflow.go`（391 行）/`chatrun.go`（781 行）/`logging.go`（646 行）。
  剩余职责簇：models HTTP 簇（modelsCache :632-658 / modelList :778 / applyModelInfoFields :716 /
  fetchGlobalModels :865 / fetchDynamicModels :882）、CORS（:275-291）、hint 簇（:1480-1552）。
- **REQ 文档**：`docs/prd/REQ-split-handler-2026-10-22.md` · **changes**：`.harness/changes/2026-10-22-split-handler/`
- **Given-When-Then 验收**：同 R-201 前三条（行为零变化 / 门禁全绿 / 棘轮收紧）。
- **禁区（留给 R-102/107/108 分支）**：NewHandler 注册块（:205-260）、withAuth/authenticate/
  checkKeyConstraints（:350-490）、status（:514）、fallbackForward（:1348）、chatCompletions 主体（:919）——
  **只做提取，不改这些区域内容**（若提取涉及行移动，仅限被提取簇本身）。
- **文件域**：`internal/server/handler.go` 上述簇 → 新增 `models_handler.go`/`cors.go`/`hint.go`。
- **验证**：`go test ./internal/server` + `go test ./...` + pre-commit + 棘轮收紧。

### R-203 工程债：autotask.go 拆分（P2）· 分支 `debt-split-autotask` · 无需 ADR

- **现状**：1244 非空行，41 个函数，~25 个 `runXxx` 养号任务剧本（:743-1119）+ MP 任务领取簇（:222-411）。
- **REQ 文档**：`docs/prd/REQ-split-autotask-2026-10-22.md` · **changes**：`.harness/changes/2026-10-22-split-autotask/`
- **Given-When-Then 验收**：
  - Given 拆分完成，When 全量测试，Then 行为零变化（同提交，L-11；autotask 相关测试全绿）。
  - Given 剧本注册，When 重构，Then 建议表驱动注册（`map[string]func(*Panel,*auth.Auth)(string,error)`）而非逐函数散落——若采用，需保持剧本名/路由不变。
  - Given 门禁，When 运行，Then gofmt 无输出 + check-boundaries 不变 + 棘轮收紧。
- **文件域**：`internal/panel/autotask.go` → 新增 `internal/panel/autotask_*.go`（**不得触碰** panel 其他文件）。
- **验证**：`go test ./internal/panel` + pre-commit + 棘轮收紧。

### R-205 工程债+功能：usage Snapshot 优化 + 增量用量落库（P2，并案一个改动单）· 分支 `usage-incremental` · **需要 ADR（持久化格式，受 ADR-009 版本门约束）**

- **现状**：`SnapshotWindow`（:666-895）在 :674-679 **锁内全量深拷贝**全部桶再聚合；落盘
  `flush()`（:402-442）**全量序列化**（:411-413 拷贝全部桶 + :416-421 拷贝 monthly map）后
  tmp+rename 原子替换，30s 周期；**无增量/WAL/append-only**。桶上限 400k + rollup（ADR-011）。
  USD 折算（:731 bucketUSD）在 SnapshotWindow 内。
- **REQ 文档**：`docs/prd/REQ-usage-incremental-2026-10-22.md` · **changes**：`.harness/changes/2026-10-22-usage-incremental/`
- **Given-When-Then 验收**：
  - Given 大桶，When 调用 Snapshot，Then 读路径不再全量深拷贝桶（快照增量/引用计数等，行为语义不变）。
  - Given 新落库格式，When 启动加载，Then ADR-009 版本门校验；**旧格式可读**（向后兼容，升级不丢数据）。
  - Given flush 周期，When 数据持续增长，Then 写路径增量聚合（不再每次全量序列化全文件）。
  - Given USD 折算，When 迁移重构，Then 保持 **kind=inference** 标注与字段省略语义（缺失 ≠ 0）。
- **依赖**：ADR-009/ADR-011 语义约束。**文件域**：`internal/usage/` 全部（`usage.go` + 同包新文件）+ ADR。
- **验证**：`go test ./internal/usage`（含持久化往返/格式版本测试）+ `go test ./...` + pre-commit。

### R-108 功能：Key 级 IP 白名单（P2）· 分支 `key-ip-whitelist` · **需要 ADR（数据模型扩展 L3）**

- **现状**：`httpauth.Key`（`index.go:18-31`：ID/Name/Digest/Models/Realm/RPM/MonthlyTokenQuota/Group）
  与 `APIKeyConfig`（`cmd/server/config.go:418-428`）均**无 IP/CIDR 字段**；全仓无 Key→来源 IP 限制
  （现有 whitelist 均为模型白名单 / 受信代理 CIDR / 国家白名单，语义不同）。
- **REQ 文档**：`docs/prd/REQ-key-ip-whitelist-2026-10-22.md` · **changes**：`.harness/changes/2026-10-22-key-ip-whitelist/`
- **Given-When-Then 验收**：
  - Given Key 配置了 IP/CIDR 白名单，When 请求来源不在名单，Then 拒绝（fail-closed，403 或既有错误分类语义）。
  - Given Key 未配置该字段，When 请求，Then 行为完全不变（空 = 不限，向后兼容）。
  - Given 受信代理场景，When 取来源 IP，Then 复用 `trusted_proxy.go` 已防伪造的 XFF 判定，不重复实现。
  - Given 配置解析，When CIDR 非法，Then 启动 fail fast（对齐 `normalizeAPIKeys` 语义，禁止静默忽略）。
- **边界**：非多租户 B 端管控；本期 **config.json 驱动**（APIKeyConfig 字段），面板管理记入 §9 待后续。
- **文件域**：`cmd/server/config.go` **仅** APIKeyConfig 区（:410-430）+ `internal/httpauth/`
  （Key 结构 + 解析）+ `internal/server/` 新增中间件文件 + `handler.go` **仅** withAuth/authenticate/
  checkKeyConstraints 区（:350-490）+ ADR。
- **验证**：`go test ./internal/httpauth ./internal/server` + 集成测试 + pre-commit。

---

## 6. 依赖与合入顺序

- **无前置依赖**（可就绪即合）：R-101、R-106、R-201、R-203、R-205。
- **R-202（handler 拆分）先于 R-102/107（fallback/status/注册区）与 R-108（withAuth 区）合入**：
  降低三路同文件（`handler.go`）合入冲突——但**不阻塞并行开发**：各分支均从 §8 基线提交点开出，
  合入时按 §8 顺序走，冲突由合入方（组织层）按分区约定裁决。
- **R-102/107 同分支并案**（共享 config.go 与 handler.go 区域）；**R-108 与 R-102/107 在
  config.go 上按区域分区**（Fallback/Drift 新区 vs APIKeyConfig 区）。
- **合入顺序建议**：① R-202 先合 → ② R-101/106、R-201、R-203、R-205 就绪即合 → ③ R-102/107
  与 R-108 最后合（其间若 R-202 已合入，冲突面最小）。

---

## 7. 文件所有权表（并行硬约束，合并时逐项核对）

| 分支 | 独占文件域 | 禁止触碰 |
|---|---|---|
| `panel-features`（R-101/R-106） | `internal/panel/app.js`、`index.html`、面板 handler（非 autotask） | `autotask.go`、`internal/upstream/*`、`internal/server/*`、`internal/usage/*`、`cmd/server/config.go` |
| `debt-split-client`（R-201） | `internal/upstream/client.go` + 同包新文件；`depth-baseline.json` | `headers.go`/`payload.go`（R8）、`internal/server/*`、`internal/panel/*` |
| `debt-split-handler`（R-202） | `internal/server/handler.go` 的 models/CORS/hint 簇 + 新文件；`depth-baseline.json` | 注册块/withAuth/status/fallbackForward/chatCompletions 主体区域；`internal/upstream/*` |
| `debt-split-autotask`（R-203） | `internal/panel/autotask.go` + 新文件；`depth-baseline.json` | panel 其余文件、`internal/server/*` |
| `protocol-drift`（R-102/R-107） | `internal/upstream/` 新增文件；`handler.go` 的 status/fallbackForward/注册区；`cmd/server/config.go` 的 Fallback 区 + 新增 Drift 配置区；README | `client.go` 现有函数与报文文件（R8）、`internal/panel/*`、APIKeyConfig 区 |
| `usage-incremental`（R-205） | `internal/usage/` 全部；ADR | `internal/server/*`、`internal/panel/*`、`internal/upstream/*` |
| `key-ip-whitelist`（R-108） | `cmd/server/config.go` 的 APIKeyConfig 区；`internal/httpauth/`；`internal/server/` 新增中间件 + withAuth 区；ADR | config.go 的 Fallback/其他区、`internal/panel/*`、`internal/upstream/*` |

> 三个分支共享 `internal/server/handler.go`（R-202 提取簇 / R-102+107 fallback-status 区 /
> R-108 withAuth 区）——**区域互斥**是硬约束；任何分支不得越过自己区域的边界行。
> `depth-baseline.json` 的 `-Update` 操作只允许由**实际降低对应指标**的分支执行，合入顺序
> 靠后时以最新 main 上的值为基（禁止抬升）。

---

## 8. 合入验收（每分支，缺一不可）

1. 该分支工作区内 `powershell -NoProfile -File scripts/pre-commit.ps1` **全绿**（10 项：
   gofmt / R10 / PS 引号 / 垃圾文件 / build+vet / test-cover / boundaries+depth / 必要性段 /
   check-harness / check-context-budget；`-Fast` 只跳 test）。
2. 变更追踪齐全：REQ（含阶段 0 四问）+ `.harness/changes/{feat}/`（summary + rollback + review，
   L2+ 独立复审）+ ADR（标注「需要」者，编号 ≥ ADR-013 且不冲突）。
3. 文件域符合 §7，无越界触碰；合入 `main` 后全仓 `pre-commit` 再跑一遍全绿。
4. 合并冲突时按 §7 分区裁决；若 R-202 已先合入，其余分支合入前应基于最新 main 重放
   （rebase 或 merge main），不允许覆盖他人已合入的分区内容。

---

## 9. 记录在案（本期不做，防漂移）

| 项 | 说明 | 状态 |
|---|---|---|
| 面板多语言/主题 | README 路线图；与 §1.1 边界张力待裁定 | 待用户裁定 |
| 重置密钥时自定义新密钥 | README 路线图第 1 条，未进 PRD v2 债务清单 | 待排期 |
| 加字段三处同步（entry/state/persist） | ADR-010 已部分缓解；R-108 顺带验证数据模型扩展成本 | 观察 |
| fallback 仅单端点白名单 | 并入 R-107 一并解决 | 本期消化 |
| redisstore 消费风格不一致 | §1.5 可扩展性债 | 待排期 |
| 全局开关靠约定隔离 | §1.5 可维护性债（reqlog 等） | 待排期 |
| `sse.go`/`panel.go`/`config.go` 等其余 oversize 文件 | 不在 PRD v2 债务清单（棘轮内 5 个 oversize 文件） | 待后续 |
| Key 级 IP 白名单的面板管理 | R-108 本期 config 驱动，面板 UI 待后续 | 后续 |

---

## 10. 执行状态跟踪

| 分支 | 编号 | 优先级 | 状态 | 合入 commit | changes |
|---|---|---|---|---|---|
| `panel-features` | R-101/R-106 | P0/P2 | ✅ 已合入（2026-10-22） | `581b2d5`（分支提交 `e269633`） | `2026-10-22-f101-panel-notice/`、`2026-10-22-openrouter-savings-display/` |
| `debt-split-client` | R-201 | P2 | ✅ 已合入（2026-10-22） | `2738a3a`（分支提交 `b85ea96`） | `2026-10-22-split-client/` |
| `debt-split-handler` | R-202 | P2 | ✅ 已合入（2026-10-22，首个，快进） | `955790f` | `2026-10-22-split-handler/` |
| `debt-split-autotask` | R-203 | P2 | ✅ 已合入（2026-10-22） | `eb9bb05`（分支提交 `137b711`） | `2026-10-22-split-autotask/` |
| `protocol-drift` | R-102/R-107 | P1/P2 | ✅ 已合入（2026-10-22） | `f0ecf44`（分支提交 `009f724`） | `2026-10-22-protocol-drift/`、`2026-10-22-multiupstream-template/` |
| `usage-incremental` | R-205 | P2 | ✅ 已合入（2026-10-22） | `16063ef`（分支提交 `0dd9f02`） | `2026-10-22-usage-incremental/` |
| `key-ip-whitelist` | R-108 | P2 | ✅ 已合入（2026-10-22） | `e30b40d`（分支提交 `5f222a2`） | `2026-10-22-key-ip-whitelist/` |

**ADR 编号定案**（两分支独立起号曾撞 `ADR-013`，已修正，提交 `183910c`）：
`ADR-013`=key-ip-whitelist、`ADR-014`=protocol-drift-observer、`ADR-015`=fallback-cascade、
`ADR-016`=usage-incremental-wal；终态 `docs/adr/` 001-016 连续无重号（check-harness 复核通过）。

**合入后总体验证**（main `2e249af`）：
- pre-commit 10/10 全绿（含主仓真实边界检查；worktree 内 6/7 分支的 check-boundaries 因
  `.git` 指针文件环境不兼容按 §8 记环境失败、已由主仓复核）
- 深度棘轮 `-Update` 收紧：maxFileLines 2058→**1089**、oversizeFiles 4→**2**、minCoverage 72.5→**73.3%**
- 治理自检 ERRORS:[]（ADR 16 篇无重号、REQ 29 篇合规、73 个 changes 目录 summary 齐备）
- 7 分支全部 0 冲突合入；合入顺序实际为：R-202（快进）→ client → autotask → panel → key-ip → protocol-drift → usage

> 本表由组织层（本次并行执行的组织者）在合入时回填，作为对账清单。
