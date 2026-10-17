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

## 6. 建议顺序

```
今天 1-2h（零决策）: A1 + A2（CI 两条）→ A8（覆盖率口径）→ §3 文档债批量修正
今天 2h（零决策）  : A3 面板 unknown 观感（空态 + 造一条有值态）
本周              : A5 沙箱去重 → A9/D2 401 语义 → A7/D3 达标未领统一 → A4 20 条带图实测
拍板后            : D4 备份瘦身（省 380 MB）→ D5 7864 去留
单开一轮          : O1 前端门禁 + app.js 拆分（最大技术债）→ O5 HANDOFF 瘦身 → O3 超长文件
长期              : A6 变异核验分批 → O2/O4 → O6/O7
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
