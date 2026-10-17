---
name: plan-open-items-2026-10-17
description: yg-gateway 未完成 / 待优化事项统一规划（2026-10-17 实测复核）——逐条给证据、规模、级别，区分真未完成 / 待拍板 / 文档债 / 已决策不做 / 优化项。
status: active
owner: <user>
last_updated: 2026-10-17
---

# yg-gateway 未完成与待优化事项规划（2026-10-17）

> 本文件只谈**本仓库**。每条都附实测命令与现值，不转抄旧文档结论——旧文档里「未做」的
> 结论至少有 3 条已被实测推翻（见 §3）。
> 用途：新会话接手先读这里，再决定要不要翻 `HANDOFF.md`（672 行，含大量已完成历史）。

## 0. 一分钟结论

1. **没有硬阻塞**：所有"未做"都能立刻开工，或只差你一句话拍板。
2. **最划算的不是写功能，是清三件债**：①CI gate 的 R10 覆盖漏洞 ②前端 4607 行 `app.js`
   完全不在任何体量门禁里 ③`HANDOFF.md` 把完成态历史和现行约束混在一层。
3. **有 3 条"遗留"实测已经不成立**，别再重复做（§3）。

---

## 1. 真未完成（可直接开工）

| # | 项 | 实测证据 | 规模 | 级别 |
|---|---|---|---|---|
| **A1** | **CI gate 的 R10 覆盖漏洞**：push 事件用 `HEAD~1` 作 base，多提交 push 只检查最后一个提交的新增行 | `.harness/changes/2026-10-16-ci-gate/summary.md` §四.2；`git rev-parse HEAD~1` 口径 | 2h | L0 |
| **A2** | **CI `gate` job 重复跑一次全量测试**（只为拿覆盖率），Linux 上多约 2 分钟 | 同上 §四.3 | 3h | L1 |
| **A3** | **面板 unknown 端到端观感验证**：本轮只保证 unknown 进入登记链路，没在面板验过（避让并行会话已结束，现在可验） | `.harness/changes/2026-10-16-t7-unknown-admission/summary.md` §七.2；当前 `/metrics` `yg_gateway_unknowns_open 0` → 先验空态，再造一条有值态 | 2h | L1 |
| **A4** | **20 条 per-model `supports_images` 声明未逐条实测**：现有证据只有「目录声明全 true」+「2 次落地实测」，按四态这只是 declaration | `2026-10-16-dsh-default-input/summary.md` §六.3 | 半天（~20 次带图调用，走自己网关，成本可忽略） | L1 |
| **A5** | **`internal/server` 沙箱两份手工同步副本**：`handler_test.go` 与 `main_test.go` 各一份，生成脚本留在 `%TEMP%` 未入库 | `2026-10-16-t5-sandbox/summary.md` §六.3 | 3h | L1 |
| **A6** | **变异核验未穷尽**：panel 只做 5 条、`autotask` 20 分片 / `taskcenter` 其它终态分支未逐条（当前 10 条≠已穷尽） | `2026-10-16-t3-mutation/summary.md` §七.2/3 | 1 天（分批） | L2 |
| **A7** | **`runAutoAll` 与队列路径对「达标未领」处理不一致**：前者跳过不领、后者入队去领 | `2026-10-16-depth-tasks-4-6/summary.md` §四.3 | 2h（需先定语义，见 D3） | L1 |
| **A8** | **`internal/session` 覆盖率口径未标注**：98.8% 含 legacy 派生分支，生产可达语句约 89%——对外不能说成热路径覆盖率 | 同上 §四.4 | 1h（文档+注释） | L0 |
| **A9** | **401 归类与流式错误吞掉**：`cmd/login` 4xx 全归「登录未完成」、`server/anthropic.go` 的 `stream` 类型错被吞 | 同上 §四.5；`2026-10-16-t7-unknown-admission` §七.4 | 3h（需先定语义，见 D2） | L1 |

## 2. 待你拍板（我不会自己动）

| # | 决策点 | 现状 | 推荐 | 代价 / 反转条件 |
|---|---|---|---|---|
| **D1** | **upstream 收窄为 interface**（F-807 / deep-opt P0-1） | 两处记录都说"暂缓/未做，需 YG 确认"：`architecture-foundation-evidence/summary.md` §F-807、`deep-optimization-2026-10/summary.md` §一 | **明确记为「不做」** | 零用户可见收益，要动两个 2000+ 行文件的核心类型；反转条件=出现需要替换 upstream 实现的真实需求（多 provider） |
| **D2** | **401 是否走上游错误档**（现在一律归「登录未完成」） | `t7-unknown-admission` §七.4 P4 提示文案已单测覆盖，未真机跑；`login_test` 现有注释标"待确认" | 保持「未完成」 | 改=要区分"账号被拒"与"凭证过期"，牵动 CLI 退出码语义 |
| **D3** | **「达标未领」语义统一**（A7 的前提） | 两条路径行为相反 | 统一为「入队去领」 | 会产生一批后台调用；若积分保底策略要求不领，则统一为「跳过」 |
| **D4** | **生产备份清理**：`D:\yg-gateway` 42 份 `*.exe*.bak`，合计 **431.5 MB**（2026-10-11 起） | 实测 `Get-ChildItem D:\yg-gateway -Filter '*yg-gateway*'` | 清到「最新 1 + 里程碑 1」 | 回滚只需最新一份；删前我会再核一次 hash |
| **D5** | **7864 eval 实例去留**：`D:\yg-gateway-eval\config.json` 还在，进程**没在跑**（`/healthz` 拒连），但 `HANDOFF.md` 仍写"当前活实例 7864" | 实测 `Get-Process yg-gateway` 只有 7863 的 PID 40912 | 退役并删配置（或复活后改 HANDOFF） | 复活=多一个实例要养；退役=丢真机对照环境 |

## 3. 文档债：写着「未做」，实测已经不成立（防重复劳动）

| 文档位置 | 原文说法 | 实测 |
|---|---|---|
| `2026-10-16-t5-sandbox/summary.md` §六.2 | `dynamicModelsCache` 仍是包级全局，建议搬进 Handler | **已完成**：`git grep dynamicModelsCache` 零命中，现为 `Handler.modelsCache` 实例字段（`internal/server/handler.go`） |
| `2026-10-16-depth-tasks-4-6/summary.md` §四.1 | `TestModelsEndpoint` 跨用例全局状态污染（`-count=3` 会挂） | **不复现**：`go test ./internal/server/ -count=3` → `ok 24.455s`（exit 0） |
| `HANDOFF.md` §五 | 「`D:\yg-gateway-eval` 当前活实例 7864」 | **进程不在**：7864 `/healthz` 连接被拒（见 D5） |
| `HANDOFF.md` §五 下一轮候选 2 | 竞品路线图 4 项全未做 | **2 项已有**：Anthropic 基础版（`internal/server/anthropic.go`）、用量落库（`usage.json` 派生路径）；剩余 = OpenRouter 费率细化 + 客户端生态 |
| `docs/adr/ADR-007` §105 | `internal/tools/doccheck` 未落地 | **已决策不做**（避免新增维护面），属记录不是待办 |
| `README.md` L53 | 「20 个 Go 包测试全绿」 | 实际 **23 包**：20 有测试 + 3 个 cmd（`credit`/`signin`/`trial`）无测试 → 措辞宜精确为「20 包有测试」 |

**处置建议**：A8 + 上表一次改完（半天），否则下一个会话会把"搬缓存/修 flake"再做一遍——本仓
`.harness/LESSONS.md` 已记同类教训 6 次。

## 4. 已决策不做（再提先读这里）

| 项 | 理由 | 出处 |
|---|---|---|
| 抽 `UpstreamClient` interface | 零用户可见收益 + 动核心类型 | deep-optimization §一 P0-1；F-807 |
| `internal/tools/doccheck` lint | 避免新增维护面 | ADR-007 §105 |
| `.harness/skills` 11 个技能「下一步 → x」引用机器校验 | 技能名写错会在下次使用时立刻暴露，属指路非契约 | `2026-10-16-doc-drift-fix` §七 |
| 虚拟滚动 | 已被「请求记录 50 条/页分页」取代 | `HANDOFF.md` §五 |
| `gate` 里加 `node --check` / `govulncheck` | 已在 `test` job 跑，分工已写进开发流程规范 | ci-gate §四.1 |

## 5. 待优化（可维护性 / 棘轮，不是功能）

| # | 项 | 实测 | 建议 |
|---|---|---|---|
| **O1** | **前端完全不在体量门禁里**：`internal/panel/app.js` **4607 行**，而 `scripts/check-depth.ps1` 只扫生产 `.go`（棘轮 `maxFileLines: 2058` 对前端零约束） | `Get-ChildItem internal -Filter *.js` + 脚本 L54 只匹配 `*.go` | 先把 app.js 纳入基线（阈值先给 4800 不倒退），再按面板域拆多文件（`go:embed` 支持）。**单开一轮**，规模 L |
| **O2** | 测试文件体量：`coverage_autotask_test.go` 4319 / `coverage_handlers_test.go` 2634 / `coverage_parse_test.go` 2526 行 | 按域拆，不改断言。规模 M |
| **O3** | 生产文件超长（棘轮已记 5 个）：`internal/upstream/client.go` 2184 / `internal/server` 与 `pool` 相关若干 | 逐文件拆，每拆一个 `check-depth.ps1 -Update` 收紧一次。规模 M |
| **O4** | 覆盖率棘轮 `minCoverage: 72.3`，但 `panel` 单轮测试 44–60s（autotask 含 0–10s 随机抖动，非可注入） | 先把抖动改成可注入，再谈提覆盖率。规模 M |
| **O5** | `HANDOFF.md` **672 行**：完成态历史 + 现行约束混在一层，是本仓最大的"读文档成本" | 按茶仓 R5 手法（完成态历史移出常驻层 → `.harness/changes/`；现行约束只留一屏），规模 M |
| **O6** | `TestEwmaLatencyPenalizesSlowInPick` 时间敏感偶发（本轮未复现） | 观察计数，第 2 次复现即改机器检查（LESSONS Seen 制）。规模 S |
| **O7** | 3 个 cmd 包零测试（`credit`/`signin`/`trial`） | 待确认是否属"有意不覆盖"；若是，写进本文件 §4 免得反复被提 |

## 6. 建议顺序（2026-10-17 合并两条工作线后重排，见 §9）

```
今天 · 零决策（约 4h）
  A1  CI push 用 HEAD~1 作 base → 改 merge-base        ← 门禁漏洞，最高优先（见 §9.1）  ✅ 已完成 878ed9a
  B7  ERROR-HANDLING 的「留待产品定夺」4 行已过期       ← 20 分钟（并行会话在做，本轮让出）
  §3  文档债批量修正（含 A9 前提已过时、README 包数）    （同上，并行会话在做）
  ~~B6~~  check-depth -List                            ← 实测不成立（已有 -Detail），已关闭

本周 · 零决策
  A2  gate 别再为拿覆盖率重跑一遍全量测试（省 ~2min/push）  ← 并行会话已在改 pre-commit.ps1
  B1  cache_miss_unknown 的产线证据（沙箱 + 假上游）
  A3  面板 unknown 观感（空态已验；与 B1 同一批造有值态）
  A8  session 覆盖率口径标注
  B5  build-image 只在 main/tag 跑多架构（PR 只造 amd64）
  A5  沙箱两份手工同步副本去重

需要你一句话拍板
  D2 401 语义 / D3 达标未领 / D4 备份瘦身（现 42 份 421.7MB）/ D5 7864 去留
  B4-a 夜猫子末次 4s 等待（行为变更）/ B4-d 备份保留策略（脚本是否自动清理）

单开一轮（规模 L，别夹在别的任务里）
  O1 app.js 4607 行纳入门禁 + 按面板域拆文件  → 之后 O3 逐文件拆 + 每拆一个收紧棘轮
  B2 深读的 4 条（②③ 需先立 ADR）

长期分批
  A6 + B3 变异核验（先补 auth/session/scheduler/cmd-server 四包，再加深 panel）
  → A4 20 条带图实测 → O2/O4/O5/O6/O7；A7 依赖 D3
```

## 7. 复现命令（本文件所有结论都能重跑）

```powershell
cd <local>\Desktop\yg-gateway
# 包数 / 有测试的包
go list ./... | Measure-Object -Line                      # 23
# 遗留项是否还成立
go test ./internal/server/ -count=3                        # ok 24.4s → flake 不复现
git grep -n 'dynamicModelsCache' -- '*.go'                 # 零命中 → 已搬进 Handler
# 生产侧
Get-Process yg-gateway | Select-Object Id,Path             # 只有 7863 的 40912
(Get-ChildItem D:\yg-gateway -Filter '*yg-gateway*' | Where-Object { $_.Name -match '\.bak$|\.tmp$' }) |
  Measure-Object Length -Sum                                # 42 份 / 431.5 MB
# 体量
Get-ChildItem internal -Recurse -Filter *.js | ForEach-Object { (Get-Content $_.FullName).Count }
```

## 8. 边界（跨仓纪律）

- 本文件只覆盖 `yg-gateway`。`tea`（一盏茶）是**另一个项目**，其未完成事项由该仓自己的
  `docs/plans/PLAN-open-items-2026-10-17.md` 负责，本仓不碰。
- 生产实例：`D:\yg-gateway`（7863，PID 40912）。替换二进制前必须备份（R15）。

---

## 9. 补充与修正（同日，来自 CI / 部署那条工作线）

> 本节由另一条工作线（N1–N4 + 两条发布线合并 + 生产实弹，提交 `ebbe018..cb971b4`）收口后追加。
> §1–§5 的 A/D/O 我**逐条核对过**，没发现错误（A1/A2/O1 都实测成立，§3 的 4 条也复核一致）；
> 这里只补它们**没有覆盖**的，以及需要修正口径的地方。§6 的排序已按本节重排。

### 9.1 修正（三处口径）

| 位置 | 原文 | 实测 |
|---|---|---|
| **§1 A9** | 「`cmd/login` 4xx 全归「登录未完成」、`anthropic.go` 的 `stream` 类型错被吞」 | **两条都已实现**（T7-P4/P3）：`cmd/login/main.go:237-253` 的 `pollFailureMessage` 是四档（0/5xx、401/403、404、其它 4xx）；`server/anthropic.go:26-40` 的 `anthropicStreamIntent` 对类型非法**返回诊断 note** 而不是吞掉。P1 也已收紧（`handler.go:1021-1035` 的 `usageCreditTotal` 带 ok 标志）。**A9 剩下的只是真机证据**，规模从 3h 降到 ~1h；D2 问的「401 是否走上游错误档」是另一个更细的语义问题，与 A9 不重叠 |
| **§2 D4** | 「42 份 `*.exe*.bak`，合计 431.5 MB」 | 按 `*.bak` 统计为 **421.7 MB**（差在 filter：`*yg-gateway*` 还含 `.tmp` 与其它产物）。份数 42 一致。注意其中 **3 份是本次实弹新增**（`pre-n1-livefire{,-2,-3}-20261016.bak`；第一份是 T7，另两份是新构建的副本） |
| **§3 第 4 行** | 「竞品路线图 4 项全未做 → 2 项已有」 | 结论保留，但两项的语义来源不同（Anthropic 是 wire 兼容；用量落库是派生物），别读成「路线图完成了一半」 |

### 9.2 B 系列：上面没有覆盖的未完成项

| # | 项 | 实测证据 | 规模 | 级别 |
|---|---|---|---|---|
| **B1a** ✅ | **`cache_miss_unknown` 的归档链路端到端证据**——已补：真实 handler 请求 → 真实 Recorder → 真实 `*.jsonl`，4 个子用例双向断言；按 L-2 做两次变异（还原旧实现→「未知」用例红；无条件标记→「已观测」用例红，哈希闭环无残留） | `.harness/changes/2026-10-17-cache-miss-unknown-evidence/summary.md`。**仍然未知**：产线从未实际出现该标记（6916 行 0 次；口径：6 个文件只读到 5 个，当日文件被占用 → 当日为 unknown）——消解需要一次真实上游返回「只有 hit、无 miss、无 prompt」，不受我们控制 | 已完成 | L1 |
| **B2** | **N6 在交接文档里被转述失真**：`HANDOFF-2026-10-16-ci-closed.md` §3 N6 列的 4 条（退避与额度解耦 / 快过期积分纯函数 / 流式 error 门禁 / 写回前回读）**全仓搜不到**，也不是它引用那份 wiki 的内容 | 全仓 grep 四条短语 → 0 命中。wiki 自己「top 3 照做」= **①剪枝前先回填派生值**（`model_costs` tier 是 inference，而 reqlog 归档 7 天到期后永久失去纠成本机会）**②持久化版本闸门 + 迁移前备份 + 版本过新拒启动**（`usage.json` 有 `Version` 但 `load()` 从不校验；`state.json` 连版本字段都没有）**③跨文件写前意图 + 启动前滚**（`auths/*.json` 与 `state.json` 无对账）。第 4 条「该改（小）」= 明细清理挪到启动期+周期 | 数小时~1天 | L2（②③ 需先立 ADR） |
| **B3** | **N7 的真实缺口：`auth` / `session` / `scheduler` / `cmd/server` 四包从未做过独立变异核验**（A6 说的是 panel/autotask 的**深度**不够，不是这个缺口） | `2026-10-16-t3-mutation/summary.md` 只对 `upstream`/`panel` 做了独立复验，其余四个包是**自述**。配方在 T3 里（`%TEMP%` 副本 + `go env GOMOD` 断言 + 改前改后哈希闭环 + 每包 5 条语义变异） | 数小时 | L2 |
| **B4** | **部署脚本落地后新开的口子**（`scripts/deploy-prod.ps1`，见 9.3） | — | 0.5~1h 除 B4-a | L1 |
| **B5** | **`build-image` 是 CI 的墙钟瓶颈**：PR 上也跑 QEMU 双架构 | `gh run view 38908276493`：build-image 13:31:19→13:38:26（**7 分钟**）；其余 job（test/race/gate/5×build）并行跑完约 2 分钟，整轮墙钟 8.6 分钟 ≈ gate + build-image 的关键路径 | 0.5h | L1 |
| ~~**B6**~~ | ~~`check-depth.ps1` 只报计数、不报明细~~ → **实测不成立，已关闭**：脚本早有 `-Detail` 开关，打印 5 个超长文件 + 7 个超长函数（**7/7 全列，不截断**） | `scripts/check-depth.ps1:208-216`；实跑见 §9.4 | — | 已关闭 |
| **B7** | **`.harness/rules/ERROR-HANDLING.md` 的「留待产品定夺」4 行已过期**：那 4 行正是 REQ 的 P1–P4，而该 PRD 标「P1–P4 全实施」，代码也已核实 | 交叉对照 `docs/prd/REQ-usage-unknown-admission-2026-10-16.md` §实施状态 + §9.1 | 20 分钟 | L0 |

### 9.3 B4 明细（部署脚本的四个遗留）

| 项 | 现状 | 建议 |
|---|---|---|
| **B4-a** 夜猫子末次 4s 等待 | `RunNightChats` 每成功一次等 4s，**含最后一次**（N4 刻意没顺手改，属行为变更） | 拍板后改成 `need-1` 次；生产每次夜猫子任务省 4s。需要 PRD 一句话 |
| **B4-b** 失败启动残留 `cmd.exe` | 启动失败时 launcher 会残留；`Replace-Binary` 的改名替换使其**无害**（只握着一个已摘链的 inode），但会累积 | 换版失败时按「命令行含目标 exe 且父进程已死」清理；判据要写清（避免误杀无关 shell） |
| **B4-c** `AGENTS.md` 部署纪律 | 仍写「构建+复制+启动同一条命令」等手工步骤，**未指向 `deploy-prod.ps1`** | 改成指向脚本（一条命令 + 退出码表：1=已确认旧版在跑 / 2=已回滚 / 3=冒烟失败 / 4=生产可能不可用） |
| **B4-d** 备份无保留策略 | 42 份 / 421.7MB，脚本每次换版新增一份且永不清理（= D4） | 脚本加 `-KeepBackups N`（默认不删，显式才清），或交给 D4 的人工清理 |

### 9.4 本轮已经「顺手做掉」/ 实测不成立的，别再立项

| 项 | 结论 |
|---|---|
| **A1 gate 的 base 只看最后一个提交** | ✅ **已修**（`878ed9a`）：push 用事件 `before`、tag 用上一 tag、不可用时回落 + `::warning::`。PRD `REQ-ci-gate-base-2026-10-17`，牙齿探针证明旧口径会「检查了 0 个生产文件」还整轮绿 |
| **B6 check-depth 不报明细** | ❌ **实测不成立**：`-Detail` 早就打印 5 个超长文件 + 7 个超长函数（7/7 全列）。这条是**我写了假的否定断言**（只读定义处与计数处，漏了第三个引用点 `:208`），按 Seen 制已升级为 `AGENTS.md` 的「否定性断言必须穷尽证据源」+ LESSONS L-15 |
| `dynamicModelsCache` 搬进 Handler | ✅ 已做（`ac5e5ea`），§3 第 1 行已确认 |
| 夜猫子 4s sleep 不可注入 | ✅ 已做（`b761187`）：`internal/upstream` 单轮 11.05s → 2.88s |
| 两条发布线各跑一次 gate | ✅ 已合并成 `release.yml`（`9c68947`），CI 实证一次 gate |
| 换版脚本「回滚对停服后失败不可达」 | ✅ 已修（`bf7ab84`），排练场景 D 钉住 |
| 「部署的二进制能追到哪个提交」 | ✅ 已修（`cb971b4`）：报告记 `git_head`/`git_dirty`，`-Build` 遇脏工作区在构建前拒绝 |
| `HANDOFF-2026-10-16-ci-closed.md` 本身 | ⚠️ **部分过时**：N1–N4/N8 已完成，N5 拆成 A3+B1，N6 转述失真（见 B2），N7 见 B3。建议下一轮交接直接指向本文件 + 各 `.harness/changes/*/summary.md`，不要再从那份读任务 |

**同日另一条工作线的收尾（2026-10-17，见 `.harness/changes/2026-10-17-precommit-single-test-and-doc-debt/`）**：

| 项 | 结论 |
|---|---|
| **A2 全量测试跑两遍（本地）** | ✅ **已去重**：检查 6 的 `go test ./... -cover` 输出直接喂给检查 7。A/B 实测 `pre-commit.ps1` 全量 **128.4s → 62.0s（−66.4s，−52%）**。CI 侧 `test`/`gate` 的并行重复**保留**（决策 D-2：串行化后 wall clock ~2.2→~2.8min，只省 runner 分钟数） |
| **check-depth 对测试失败宽容** | ✅ **已收紧**：自跑模式测试红即 `exit 2`（旧实现「输出里有 coverage: 就继续」会让棘轮 PASS）。牙齿探针：临时塞必失败测试 → 全量 `exit=1` 且 check-depth 不给棘轮结论 |
| **A8 session 覆盖率口径** | ✅ **已实测更正**：425/430 = 98.8%；剔除测试专用 `deriveKey`（20 语句）后 405/410 = 98.8%。**「生产可达约 89%」复现不出来**，代码注释与两份变更记录均已更正 |
| **A3 面板 unknown 展示** | 🟡 **部分**：徽章/提示行抽成纯函数 `unkBadge()`/`unkNoteText()` 并新增 JS 级测试（3/3 反向变异变红）。**有值态的真机观感仍归 B1**（需真实未消解 unknown），空态由 `/metrics` 的 `unknowns_open 0` 佐证 |
| 文档债 4 条 | ✅ 已就地更正：`dynamicModelsCache` 已不存在、`-count=3` 污染不复现、7864 实例没在跑、Go 包数 23（20 有测试 + 3 无） |



---

## 10. 当前状态盘点（2026-10-17 晚，实测复核后）

> 本表**取代**上面 §1/§9.2 的"未完成"标记：那两张表是 10-17 白天的判断，今天收口了 8 项。
> 每条都带**卡点**——`无` 表示随时可开工，`你` 表示等你一句话。

### 10.1 已收口（别再立项）

| 项 | 落点 |
|---|---|
| A1 gate 的 push 差分基准 | `878ed9a` + 复审修正 `094768d`（tag 路径排除 `-ci`、空 ref 护栏） |
| A2 全量测试跑两遍（本地） | `d09dd9f`（A/B −66.4s）；CI 侧重复保留 = 决策 D-2 |
| A3 面板 unknown（JS 层） | `d09dd9f`：`unkBadge`/`unkNoteText` + `TestAppJSUnknownBadgeAndNote`（3/3 反向变异红）。**有值态观感仍归 B1** |
| A5 沙箱副本人工同步 | `5717e20`：`TestSandboxFilesInSync`（抽公共包仍被设计拒绝） |
| A8 session 覆盖率口径 | `d09dd9f`：425/430 与 405/410 均 98.8%，「约 89%」不复现 |
| A9 401 / stream 吞错 | **判定不做**：`cmd/login` 已带状态码 + 有测试约束；`anthropic.go` 的 note 已修（见 `PLAN-necessity-review`） |
| O1 前端体量无门禁 | `5717e20` + `3286f07`：`maxFrontendLines 4378` + `oversizeFrontendFiles 2`（含 `index.html` 1755） |
| B6「check-depth 不报明细」 | 实测不成立（`-Detail` 早就在打）→ L-15 |
| B7 ERROR-HANDLING「留待定夺」4 行 | `a514e40` 已回填 |
| 阶段 0 必要性分析 | `5717e20` 入规范 + 检查 8/8；裁定见 `PLAN-necessity-review-2026-10-17.md` |

### 10.2 仍未做（按"现在能不能动"排）

| # | 项 | 工作量 | 卡点 |
|---|---|---|---|
| ~~**B2a**~~ ✅ | **N6 转述失真已加更正**：两份交接文档保留原文 + 追加「⚠ 更正」块（4 个短语全仓 0 命中、wiki 真正的四件事、改指向本文件 B2 实质） | `.harness/changes/2026-10-17-n6-distortion-correction/` | 已完成 | L0 |
| ~~**B4c**~~ ✅ | **`AGENTS.md` 部署纪律已指向脚本**：保留四条原则 + 三条用法命令 + 退出码表（`1` vs `4` 的区别）+ `-Build` 脏工作区拒绝 + 先跑排练 | `.harness/changes/2026-10-17-deploy-discipline-pointer/` | 已完成 | L0 |
| **B1b** | **A3 的剩余部分：造一条 unknown 验「有值态」的面板观感**（B1 已被拆成两半：归档链路 ✅ 见 §9.2 的 B1a；这里只剩面板观感） | 半天 | **无** |
| **B4b** | 启动失败时 launcher 可能残留 `cmd.exe`（无害但累积）；需写清「命令行含目标 exe 且父进程已死」的清理判据 | 2h | **无** |
| ~~**B5**~~ ✅ | **`build-image` 的墙钟瓶颈已消**：根因不是双架构，而是构建阶段没走 `BUILDPLATFORM`（arm64 整段 Go 编译在 QEMU 下模拟）。改成构建机架构交叉编译后，**实测 239s → 108s → 96s（−60%）**，两架构验证强度不变 | `bf5a4aa`；CI run `39014207601`；PRD `REQ-image-buildplatform-2026-10-17` | 已完成 | L2 |
| ~~**B3**~~ ✅ | **四包补了独立变异核验**：22 个语义变异全部咬住（auth 6 / session 5 / scheduler 4 / cmd/server 7，0 存活 0 无效变异）；驱动脚本与清单已入库 `scripts/mutation/`。**但复审自选的 3 个更难变异 3/3 存活**：B3-a 已修（`SaveAtomic` rename 失败，新增常驻测试）· **B3-b 仍开**（session 闲置号优先无测试）· **B3-c 仍开**（多 Key 带空格同值绕过检测，且 `Load()` 的双重 normalize 会掩盖） | `.harness/changes/2026-10-17-mutation-auth-session-scheduler-cmdserver/` | 主体完成，B3-b/c 待补 | L1 |
| **B2 实质** | wiki 真实的 3 条高价值建议（剪枝前回填派生值 / 持久化版本闸门 / 跨文件写前意图），②③ 需先立 ADR | 数小时~1天（L2） | 需立项 |
| **B4a** | 夜猫子末次 4s 等待（`blackcat.go` 循环末次也 `sleepNightChat()`），改了每次任务省 4s | 1h | **你**（行为变更，PRD 一句话） |
| **A7 / D3** | 「达标未领」两条路径语义相反 | 2h | **你**（D3 语义） |
| **D2** | 401 是否走上游错误档 | 3h | **你** |
| **D1** | upstream 收窄为 interface | — | **你**（建议明确记「不做」） |
| **D4/D5** | 备份清理 / 7864 实例 | — | **你**（我按必要性分析建议都不做，理由见 `PLAN-necessity-review` §D4/D5） |
| **A4** | 20 条 `supports_images` 全量实测 | 半天 | **你**（我建议不做全量，转 unknown + 按需） |
| **A6** | 变异核验穷尽度 | — | 等条件（改到哪条分支补哪条） |
| **O2/O3/O4/O6/O7** | 超长文件拆分 / 覆盖率抖动 / flake 观察 / 3 个 cmd 包无测试 | — | 不做（棘轮已冻结；见必要性分析） |
| **生产换版** | 生产二进制落后源码（纯重构，行为等价） | 20s | 等条件（下一个行为变更批次一起上） |