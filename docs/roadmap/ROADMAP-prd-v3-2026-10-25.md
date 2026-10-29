# ROADMAP PRD v3（2026-10-25）：可审计账号池网关——深化期

> **定位**：接替已全部收口的 `ROADMAP-prd-v2-2026-10-22.md`（7 项全 ✅），把
> 2026-10-24/25 两日四轮深读（架构/算法/治理/安全/运维）+ 竞品三轮研究（magpie
> 文档→源码→实跑）+ 全项目 BACKLOG 的结论，固化为**分期执行计划**。本文件是
> 并行执行的合并基准；所有并行分支自本文件的提交点开出。
>
> **事实源**（按优先级）：
> - 实测代码基线（本文件 §一，全部 `grep`/`go list`/`go test` 实测，2026-10-25）
> - `docs/reference/magpie-deep-read-2026-10-25.md`（对标八维对比 + 源码级公式 + 实跑 §七）
> - `docs/roadmap/BACKLOG-2026-10-25.md`（17 项全量清单，已并入本文件分期）
> - `docs/roadmap/NEW-FRONTIERS-2026-10-25.md`（四块新地，#1 已落地）
> - `docs/prd/REQ-INDEX.md` 待办行（各 REQ 的最新状态权威）
> - `AGENTS.md`（顶层硬约束）、`.harness/rules/*`（实施细则）
>
> **状态**：v1，待执行。执行入口 = §三 各项（REQ 粒度，含 Given-When-Then 验收）。

---

## 一、现状基线（2026-10-25 实测快照）

| 项 | 值 | 口径 |
|---|---|---|
| 测试 | 23 包：21 ok + 2 no test files | `go list`/`go test` 实测 |
| 覆盖率棘轮 | min 73.3（当前最低 74.0，cmd/server） | check-depth |
| 体量 | config.go 1089 顶格 / main.go 999 / app.js 4378 冻结 | 非空行 |
| 上下文预算 | 15822/15822 压线 | check-context-budget |
| 本轮新落地 | pickReason 决策溯源（四路径）、软带 P1、衰减因子 P2（默认关）、opsagent MVP+面板视图、金样基准+T3 注入件 | git log a0568ff/08b5bd6/5ab1dec |
| 在册待执行 | routing P2 校准、growth-split、opsagent 阶段3、structured-output P2 | REQ-INDEX 待办行 |
| 环境债 | 系统 go 损坏（YG_GO_EXE 临时兜底）、提交未推送（等 GitHub 工单）、金样重放 blocked（缺独立执行器） | 本会话实录 |

**本阶段身份主张（README 叙事收拢方向）**：实测成本分层（四态）、健康治理
（EWMA/熔断/half-open）、决策溯源（pickReason）——三个面经八维对比核实为同类
（magpie/CRS/one-api/LiteLLM）全空白，是本项目要打穿的三护城河。

---

## 二、分期总览

| 期 | 目标 | 项 |
|---|---|---|
| **P0 真机周**（本周级，S） | 让已落地的代码吃到真实数据 | V3-1 ~ V3-4 |
| **P1 结构月**（一个月级，M） | 还结构呼吸空间 + 补齐观测与自检 | V3-5 ~ V3-9 |
| **P2 战略季**（季度级，L） | 三护城河写穿 + 生态位落定 | V3-10 ~ V3-14 |

**执行纪律（全程不变）**：研究明白再动工（参数不得拍脑袋——routing 两处拍脑袋被
源码推翻的教训）；每项走十阶段（REQ 必要性 → 实现+测试同提交 → pre-commit 10/10 →
变更目录）；棘轮不回退；单一门禁。

---

## 三、执行项

### P0 真机周

**V3-1 · `/v1/messages/count_tokens`**（REQ 单立）
- 依据：Claude Code 原生调用、grep 0 命中；CRS/LiteLLM/magpie（gateway 层有
  `count_tokens_test.go`）三家原生支持——原生体验最实的缺口。
- 做法：不引依赖。启发式估算（字符/词元量级）+ `evidence.Derive` 标注 derivation；
  响应带 `estimated: true` 类字段（诚实口径，避免伪装成上游精确值）。
- GWT：When 客户端 POST count_tokens，Then 返回 `{input_tokens: N}` 且 N 与同请求
  实测 usage.prompt_tokens 在 ±30% 内（真机校准后收紧）；When 未实现路径被触发
  （理论不可达），Then 明确错误信封而非静默。
- 量级 S。

**V3-2 · key 级用量阈值告警（只告警不拦截）**
- 依据：usage 按下游 Key 四维聚合已有、alerting 通道已有，缺桥接；one-api 令牌
  配额思想去掉「拦截」保留「可见」——与「不做计费」不冲突。
- GWT：When 某 key 周期用量 > `api_keys[].usage_alert_threshold`，Then 经 alerting
  发一次告警（复用去重窗口）；When 阈值未配置，Then 行为零变化。
- 量级 S。

**V3-3 · 生产换版 + 真机校准（所有研究变量的收口）**
- 内容：`deploy-prod.ps1 -Build` 换版启用 opsagent + 软带；采集三类数据回填——
  ①软带命中率与 bandMult 的「20k token/请求」假设；②opsagent 建议
  （capacity/402-contradiction）的真机质量；③count_tokens 估算精度。
- GWT：When 换版完成，Then 冒烟四连过（/healthz、/v1/models、流式/非流式）；
  When 观察满 48h，Then 校准结论落回 REQ（bandMult 保留/调整/撤销）。
- 量级 S（+48h 观察窗口）。依赖：用户在生产机执行（deploy 纪律由脚本承载）。

**V3-4 · 收尾三件**：推送（等 GitHub 工单清 unreachable objects）；管理员重装 Go
≥1.27.3（撤 YG_GO_EXE）；金样重放 T1-T3（人工新会话按 GOLDEN-TASKS 协议）。

### P1 结构月

**V3-5 · config.go 表驱动归一化**（✅ 2026-10-25 落地，GWT 按实测修正）
- 落地形态：normalizeAPIKeys+validKeyID 整段迁 `config_keys.go`（纯机械搬移）+
  normalize() 的 10 个时长解析块、6 个排程默认块表驱动；config.go 1089→**977**
  （净减 112，呼吸空间达成），行为零变化（config 测试断言未改一行）。
- **GWT 修正**（诚实记录）：原定「净减 ≥150 行」是测量前假设——实测 79 处模式
  的大头在 struct 注释（即文档，不可删）与 Default()（不可缩），normalize 内真正
  同构可表的只有时长/排程两类；150 行需靠删注释达成，违背注释文化，故按实测
  重定为「顶格解除 + 同构块表驱动」。剩余结构手段（struct 注释外移）列为不做。
- 量级 M（实际 S）。

**V3-6 · E2E 先行 → app.js 瘦身**（①✅ 2026-10-26）
- 依据：app.js 4378 冻结锁死全部前端演进；10-17 必要性评审裁定「无浏览器级 E2E
  不拆」——本项即解锁动作。
- 步骤：①Playwright 冒烟（无现成脚手架，verify/ 不存在，2026-10-26 复核）：登录→八视图切换→账号卡片→
  opsagent 卡片加载；②E2E 绿后抽公共渲染函数瘦身 ≥100 行；③棘轮收紧。
- GWT：When E2E 套件在本地与 CI 跑，Then 全绿且断言 ≥10 条用户可见行为；
  When 瘦身合并，Then maxFrontendLines 下降且 E2E 仍绿。
- 量级 M-L。**解锁：A3 key RPM 可视化、A5 opsagent 独立导航页。**
- 进展（2026-10-26）：e2e/ 套件 4 测试 34 断言全绿（本地 6.3s，隔离实例 %TEMP%\yg-e2e 端口 7864）；
  CI 接线 `.github/workflows/e2e.yml`（先调共享 gate.yml 再跑浏览器，不复制门禁）；顺带修真 bug：
  `opsagent.js` 请求缺 Authorization 头 → api_key 鉴权下卡片恒 401「加载失败」。②③ 待做：
  抽公共渲染函数瘦身 ≥100 行后收紧 `maxFrontendLines`（基线 4378）。

**V3-7 · opsagent 规则心跳（canary rounds）**（NEW-FRONTIERS #2）
- 依据：词表口径 bug 的本质是「规则在生产永假但无人知道」——单测覆盖编译期，
  不覆盖 pool 词表演进后的运行态漂移。
- GWT：When 注入合成观察（hard 冷却+正余额），Then 对应建议产出且标记 canary；
  When 规则失配，Then observation 落证据 + 告警；When 心跳关闭，Then 零行为。
- 量级 S。风险：canary 建议走 Sink 需抑制（canary Sink 单独注入）。

**V3-8 · reqlog 加 kind 维度**（✅ 2026-10-26 落地）（借鉴 magpie usage.jsonl 的 ep/kind/source）
- 依据：实跑发现它连 model_list 探测都记账；我们无法区分「聊天流量/探测流量/
  养号流量」——agent 流量画像的地基。
- 量级 S。
- 落地注：reqlog 全仓唯一构造点在 chat 路径；探测（FetchModels/canary 合成观察）
  与养号（autotask 直连 upstream）不进 reqlog——probe/farming 为占位词表值，
  现状边界与语义口径见 `docs/prd/REQ-reqlog-kind-2026-10-26.md`。

**V3-9 · harness runner（`cmd/harnessrun`）**（NEW-FRONTIERS #4）
- 依据：金样重放被子 agent 配额阻塞（run-2026-10-24 blocked ×3）——缺不依赖
  会话内子 agent 的执行器。
- GWT：When runner 以任务清单运行，Then 每任务独立进程+超时+评分点外置，产出
  `run-*.md` 记录；When 单任务失败，Then 不影响后续任务且失败原因可见。
- 量级 M。复用 mutation 沙箱模式；对外开源候选（awesome-harness-engineering
  分类缺 Go 实现）。

### P2 战略季

**V3-10 · agent 流量画像**（NEW-FRONTIERS #3）
- 依据：编码 agent 的流量模式（长对话+高频工具循环+cache 复用）与人类完全不同，
  用量视图不区分——「养号友好」叙事的下一代证据（agent 流量对账号的伤害模式
  第一次可见）。
- 做法：UA 自识别 + 会话键 → 按「agent 会话」聚合轮数/每轮 token/cache 命中；
  先出 /status JSON，面板展示依赖 V3-6。量级 M。

**V3-11 · growth 子包拆分**（REQ-growth-package-split 在册）
- 前置已研究透（方法绑定+未导出 helper 耦合事实、候选 A/B）；等一个顺路的业务
  改动一起做，或 R8 金样扩容后独立做。量级 L。

**V3-12 · structured-output P2**（REQ-structured-output）
- P1 锚定已落地；剩真机一发（上游认不认 response_format）→ 分支决策（文档化 /
  网关兜底 / 诚实声明不支持）。依赖 V3-3 的换版窗口。量级 XS+决策。

**V3-13 · README 三护城河叙事 + 生态配合章节**
- 内容：能力表前置三护城河（实测成本/健康治理/决策溯源，各一行「同类空白」）；新增
  「与 Magpie/CC Switch 配合」一节（把本网关填为自定义 OpenAI 兼容端点的做法）。
- 依据：magpie 抖音推广（两支，2026-11）验证赛道拉力——市场教育由它完成，我们接
  深耕人群。量级 S。

**V3-14 · harness 产品化评估**
- 依据：治理体系（四态/棘轮/Seen 计数/判据文化）在同类中无对标，且经本会话实战
  自证（拦下 4 类真问题）；harness runner 是对外形态。
- 动作：只评估（写一篇可行性），不立项——主线仍是网关。量级 S。

---

## 四、明确不做（重申定位红线）

多租户计费/兑换码/用户体系；多 provider 接入；提示词日志留存；面板多语言/主题；
账号自动发现（读本机 agent 登录态，与 R1 冲突）；Bun 插件运行时（单二进制红线）；
intent routing（与零新增上游请求冲突）。

## 五、依赖与风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| 真机换版依赖用户在生产机执行 | V3-3/校准阻塞 | deploy-prod 脚本承载全部纪律；P0 其余项不阻塞 |
| E2E 环境本机不可用 | V3-6 阻塞 | 无现成 E2E 脚手架（verify/ 不存在，2026-10-26 复核）；CI ubuntu 可跑 |
| 子 agent 配额不稳 | 独立复审/金样重放 | 串行协议+2 败即弃；V3-9 是根治 |
| 上下文预算压线（余 0） | 常驻层任何新增要先压降 | 每次顺手等价压缩（本会话已 3 次实践） |
| 参数假设未校准 | 软带带宽/估算精度 | V3-3 数据回填是显式步骤，REQ 已注明 |

## 六、验收总闸

每项合入即跑：pre-commit 10/10（含 R10/棘轮/预算）+ 本项 GWT + 变更目录三件套。
P0 结束做一次「V3 中期对账」：三分项 ✅/🟡/⛔ 回填本文件（ROADMAP v2 的对账惯例）。

## 七、V3 中期对账（2026-10-29，git log 实证）

> 全部断言以 `git log` 为证（L-15/L-22：REQ-INDEX 没行 ≠ 没做）。
> 完整状态表见 `docs/PLAN-v3-remaining-2026-10-29.md` §一。

| V3 项 | 状态 | 落点 |
|---|---|---|
| V3-1 count_tokens | ✅ | `a07727c`（无独立 REQ，执行期债） |
| V3-2 key 级用量告警 | ✅ | `a07727c`（同上） |
| V3-3 换版+真机校准 | ✅ | `b5ffee2`（48h 回填：bandMult 被双峰推翻，决策见 PLAN §四） |
| V3-4 收尾三件 | 🟡 | 金样 ✅（`fcf5a42`）；推送 / Go ≥1.27.3 重装待人工（R4） |
| V3-5 / V3-6 / V3-8 / V3-9 | ✅ | 已落地（见 §三） |
| V3-7 规则心跳 | ✅ | `4931e4b`（无独立 REQ） |
| V3-10 agent 流量画像 | ✅（UI 未做） | `f3385f4`；面板展示 = 可选增强（PLAN R5） |
| V3-11 growth 子包拆分 | 🟡 | REQ 在册；M3 测试基线 → M4 ADR+执行 |
| V3-12 / V3-13 / V3-14 | ✅ | `502ede3` / `cbf1399`+`bde24a4` / `2d39e9c` |
