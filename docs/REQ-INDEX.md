# REQ 索引（需求验收档案导航）

> [`prd/`](prd) 目录下每个 `REQ-*.md` 是**一次性需求 + Given-When-Then 验收记录**，属治理
> 历史资产，落地后不改写、不删除（该目录命名被 `check-harness` 限定为只能放 `REQ-*.md`，
> 故本索引导出到 `docs/` 根）。本文件只做**导航与状态归类**，不复制 REQ 内容。
>
> **状态权威**：规划与合流对账看 [`roadmap/ROADMAP-prd-v2-2026-10-22.md`](roadmap/ROADMAP-prd-v2-2026-10-22.md)；
> 事实以代码 + `git log` 为准；本表「状态/落点」均有 commit 或在仓产物实证，未核实不写「已完成」。
> 最近核对：2026-10-23（main `169d0a0`）。

图例：✅ 已落地（合入 main 且在仓可验） · 🟡 主体落地/frontmatter 仍标待终审 · ⛔ 明确不做或阻塞

---

## A. 产品与架构 PRD（2026-10 上中旬总纲）

| REQ | 主题 | 状态 / 落点 |
|---|---|---|
| [multi-api-key-2026-10-11](prd/REQ-yg-gateway-multi-api-key-2026-10-11.md) | 多 API Key 派发（v2） | ✅ frontmatter 自述已完成，T0~T7 + 终审 6 项已合并 |
| [panel-key-management-2026-10-11](prd/REQ-yg-gateway-panel-key-management-2026-10-11.md) | 面板 Key 管理页（v1） | ✅ 卡片+弹窗，已合并 main |
| [panel-spec-2026-10-11](prd/REQ-yg-gateway-panel-spec-2026-10-11.md) | 面板体验与工程规范 | 🟡 主体已落地（前端 P0 多项带测试）；frontmatter 仍「待确认」 |
| [security-hardening-2026-10-11](prd/REQ-yg-gateway-security-hardening-2026-10-11.md) | 入站边界与路由安全加固 | 🟡 主体已落地（ACL/body 上限/XFF/常量时间比较）；frontmatter 仍「待确认」 |
| [architecture-foundation-evidence-2026-10-15](prd/REQ-yg-gateway-architecture-foundation-evidence-2026-10-15.md) | 架构地基 + Evidence/Unknown 语义 | 🟡 核心落地：`internal/evidence`、`internal/unknown` 在仓 |
| [hotpath-optimization-2026-10-14](prd/REQ-yg-gateway-hotpath-optimization-2026-10-14.md) | 热路径优化轮 | ✅ 已授权实施（去重解析/降锁/环形缓冲等，见 `73911e4`） |
| [deep-optimization-2026-10-12](prd/REQ-yg-gateway-deep-optimization-2026-10-12.md) | 深度优化轮（F-101/103/2xx/3xx） | ✅ F 项逐条 git 实证回填，含选号去写锁 `387c936`、hasImage 惰性化 `73911e4` |

## B. 计量 / usage 语义（四态纪律）

| REQ | 主题 | 状态 / 落点 |
|---|---|---|
| [usage-unknown-admission-2026-10-16](prd/REQ-usage-unknown-admission-2026-10-16.md) | unknown 不得冒充 0（写台账准入） | ✅ P1~P4 落地（ADR-007） |
| [usage-bucket-cap-2026-10-20](prd/REQ-usage-bucket-cap-2026-10-20.md) | usage 桶数硬上限（修无界增长） | ✅ 在仓（Rollup/桶 cap 实证） |
| [usage-incremental-2026-10-22](prd/REQ-usage-incremental-2026-10-22.md) | Snapshot COW 读路径 + 增量 WAL（R-205） | ✅ `0dd9f02` / 合并 `16063ef`（ADR-016） |
| [f5-metric-counter-2026-10-20](prd/REQ-f5-metric-counter-2026-10-20.md) | F5 度量计数器（消解 token unknown） | ✅ `d4bf9b2`，`/status` 透出 task_metrics |
| [openrouter-savings-display-2026-10-22](prd/REQ-openrouter-savings-display-2026-10-22.md) | OpenRouter 费率折算面板三值化（R-106） | ✅ 分支 `e269633` / 合并 `581b2d5` |

## C. 工程治理 / CI / 部署 / 面板（2026-10 中后旬批次）

| REQ | 主题 | 状态 / 落点 |
|---|---|---|
| [deploy-prod-script-2026-10-16](prd/REQ-deploy-prod-script-2026-10-16.md) | 换版固化为 `scripts/deploy-prod.ps1` | ✅ 脚本 + 备份/回滚/冒烟在仓 |
| [deploy-launcher-cleanup-2026-10-17](prd/REQ-deploy-launcher-cleanup-2026-10-17.md) | 回收 launcher，失败换版不留孤儿 | ✅ deploy-prod 内含；排练 `deploy-prod.rehearsal.ps1` 在 |
| [ci-gate-base-2026-10-17](prd/REQ-ci-gate-base-2026-10-17.md) | CI gate 差分基准覆盖整次推送 | ✅ `.github/workflows/gate.yml` 解析 before/base |
| [image-buildplatform-2026-10-17](prd/REQ-image-buildplatform-2026-10-17.md) | 镜像阶段挪到 BUILDPLATFORM | ✅ `Dockerfile:9/10/16` |
| [necessity-stage-and-frontend-ratchet-2026-10-17](prd/REQ-necessity-stage-and-frontend-ratchet-2026-10-17.md) | 阶段 0 必要性入规范 + 前端体量棘轮 | ✅ `check-req-necessity.ps1` + 前端行数棘轮 |
| [precommit-single-test-and-doc-debt-2026-10-17](prd/REQ-precommit-single-test-and-doc-debt-2026-10-17.md) | 全量测试只跑一次 + 覆盖率口径 + 文档债 | ✅ pre-commit 复用单份 cover |
| [harness-engineering-2026-10-19](prd/REQ-harness-engineering-2026-10-19.md) | harness 治理层工程化 | ✅ 多项落地（F1 检项/ADR 编号/F5 组等，见其 §八跟踪） |
| [apikey-normalized-dup-2026-10-17](prd/REQ-apikey-normalized-dup-2026-10-17.md) | 顶层 api_key 与多 Key 同口径查重 | ✅ 随多 Key 落地 |
| [tasks-met-unclaimed-claim-2026-10-18](prd/REQ-tasks-met-unclaimed-claim-2026-10-18.md) | 「达标未领」统一为「去领奖」 | ✅ 面板 claim 入口在 |
| [nightchat-last-sleep](prd/REQ-nightchat-last-sleep.md) | 夜猫子末次对话后不空等 4s | ✅ 见 REQ 内验收（B4a） |

## D. PRD v2 路线图项（2026-10-22，七分支全部合入）

> 对账明细与 ADR 编号定案见 [ROADMAP §10](roadmap/ROADMAP-prd-v2-2026-10-22.md)；ADR-013~016。

| REQ | 主题（编号） | 状态 / 落点 |
|---|---|---|
| [f101-panel-acl-notice-2026-10-22](prd/REQ-f101-panel-acl-notice-2026-10-22.md) | F-101 面板显式提示（R-101，P0） | ✅ `e269633` / `581b2d5` |
| [key-ip-whitelist-2026-10-22](prd/REQ-key-ip-whitelist-2026-10-22.md) | Key 级源 IP 白名单（R-108） | ✅ `5f222a2` / `e30b40d`（ADR-013；config 驱动，面板 UI 待后续） |
| [protocol-drift-2026-10-22](prd/REQ-protocol-drift-2026-10-22.md) | 上游协议漂移观测（R-102，P1） | ✅ `009f724` / `f0ecf44`（ADR-014；仅观测层） |
| [multiupstream-template-2026-10-22](prd/REQ-multiupstream-template-2026-10-22.md) | 多上游接入模板 + fallback 级联（R-107） | ✅ 同 protocol-drift 分支（ADR-015） |
| [split-client-2026-10-22](prd/REQ-split-client-2026-10-22.md) | 拆 `upstream/client.go`（R-201） | ✅ `b85ea96` / `2738a3a` |
| [split-handler-2026-10-22](prd/REQ-split-handler-2026-10-22.md) | 拆 `server/handler.go`（R-202） | ✅ `955790f` |
| [split-autotask-2026-10-22](prd/REQ-split-autotask-2026-10-22.md) | 拆 `panel/autotask.go`（R-203） | ✅ `137b711` / `eb9bb05` |

---

## 待办 / 不做（不在本索引「已落地」内）

- [routing-improvements-2026-10-25](prd/REQ-routing-improvements-2026-10-25.md)：选号路由改进——
  **已立项待执行**：P1 credit_floor 软带（×0.5 降权重，硬拦底线不退）+ P2 usage 半衰期
  可选策略（默认 idle 零回归）。来源：magpie 源码级深读（reference/magpie-deep-read）。
- [growth-package-split-2026-10-24](prd/REQ-growth-package-split-2026-10-24.md)：upstream 拆出
  growth 域子包——**立项待执行（等条件）**，耦合事实与候选方案已记录，解锁动作见文末。
- [structured-output-2026-10-24](prd/REQ-structured-output-2026-10-24.md)：response_format
  结构化输出——**P1 锚定透传已落地**（payload_passthrough_test.go）；P2 上游实测待真机一发。
- [opsagent-2026-10-24](prd/REQ-opsagent-2026-10-24.md)：网关内置运维 agent——
  **MVP 已落地（2026-10-24，默认 off）**：观察-建议闭环 + unknown/evidence 复用 +
  动作白名单护栏（ADR-017）；后续阶段（面板集成 / cost-explore 白名单开放）见 REQ §解锁动作。
- **F-102** 新一轮安全审计结论：阻塞在外部输入。
- **ROADMAP §9 记录在案（本期不做）**：面板多语言/主题、重置密钥自定义、redisstore 消费风格、
  全局开关隔离、其余 oversize 文件（sse/panel/config.go）拆分、Key-IP 白名单面板 UI。
- **生产版本**：仓库 main 已含上述功能；`D:\yg-gateway` 在跑版本以现场为准，换版走 deploy-prod。
