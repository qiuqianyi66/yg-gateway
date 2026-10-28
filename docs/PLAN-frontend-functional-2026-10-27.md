# 前端功能性优化计划表（2026-10-27）

> 来源：今日学习（Linear/PostHog/Supabase 组件渲染 + 同类项目对标笔记）+
> ROADMAP v3 剩余项对齐。执行入口 = 本文件 §三 优先级。
> 状态权威：各任务完成后回填本文 + 变更目录 summary。

## 一、今日学习结论（已吸收，不重复调研）

- **三家 SaaS 后台共性**：卡片无阴影靠 1px 边框、hover 边框变品牌色、focus-ring 统一、
  输入框 hover→focus 两态、color-mix 派生交互色（原生 CSS 不需构建链）。
- **同类项目缺口**：探测成功率面板化（数据在 metrics）、pace 路由模式、/v1/responses。
- **已纠正**：count_tokens（V3-1）、key 用量告警（V3-2）已落地，不在本计划。

## 二、任务清单

| # | 任务 | 量级 | 依赖 | 验收（GWT 简述） |
|---|---|---|---|---|
| **F1** | **app.js 瘦身 ≥100 行**（V3-6 ②③） | M | 无 | 抽公共渲染函数；maxFrontendLines 4378→≤4278；E2E 仍全绿 |
| **F2** | 探测成功率上账号卡片 | S | 无（可并行 F1） | 账号卡片显示最近 half-open 探测成功率（metrics 已有数据） |
| **F3** | 提交锁防连点 | S | 无 | 保存/添加 Key 按钮 in-flight 文案 + disabled 防重复提交 |
| **F4** | 行内开关乐观更新+失败回滚 | S | 无 | toggleKeyEnabled 乐观切换 UI；失败回滚 + toast 报错 |
| **F5** | 空态/取数失败全视图区分 | S | 无 | 各视图"暂无数据"vs"加载失败，请重试"文案与操作区分 |
| **F6** | key RPM 可视化（A3） | M | **F1** | 账号卡片或 KPI 区显示 key RPM 曲线/数值 |
| **F7** | opsagent 独立导航页（A5） | M | **F1** | 面板独立页展示 opsagent 规则状态 / 建议列表 |
| **F8** | `/v1/responses` 端点 | M | 无 | Codex 客户端经本网关可用（四协议互译） |
| **F9** | pace 选号模式 | M | 无（config 开关默认关） | config 可配 pace 权重，按"重置前每小时剩余额度"选号 |
| **F10** | 金样重放 T1–T3 | M | V3-9 harnessrun 已就绪 | ✅ **已完成（2026-10-28）**：`harnessrun check T1/T2` 实跑通过；T3 漂移狩猎 3/3（AGENTS/README 共 5 处修正）；报告 `docs/agent-eval/run-2026-10-28.md` |
| **F11** | V3-12 structured-output 真机验证 | XS | ~~V3-3 窗口（10-28 21:41 满）~~ | ✅ **已完成（2026-10-28）**：真机一发，上游认 `json_object` 返回合法 JSON（observation），决策=文档化（见 REQ 与 `.harness/changes/2026-10-28-structured-output-p2/`） |
| **F12** | V3-14 harness 产品化评估 | S | 无 | ✅ **已完成（2026-10-27，2d39e9c）**：`docs/agent-eval/V3-14-harness-product-eval-2026-10-27.md` |

## 三、优先级与依赖图

```
F1（瘦身）──┬──> F6（key RPM）
            └──> F7（opsagent 独立页）

F2/F3/F4/F5（纯前端展示/交互）── 可立即并行，不依赖 F1

F8（/v1/responses）── 后端协议，独立
F9（pace 模式）──── 路由策略，独立
F10（金样重放）──── V3-9 已就绪，随时可跑

F11（structured-output 真机）── ✅ 2026-10-28 已执行（生产实例 + 可用账号，一发）
F12（harness 评估）────── 独立，写文档
```

**建议执行顺序**：
1. **F1 瘦身**（关键路径，解锁 F6/F7）
2. **F2+F3+F4+F5**（并行，纯前端，立即见效）
3. **F6/F7**（F1 解锁后）
4. ~~F10 金样重放~~ ✅ 已完成（2026-10-28，见 run-2026-10-28.md）
5. **F8/F9**（后端类，择机）
6. ~~F11~~ ✅ 已完成（2026-10-28 真机一发：上游认 json_object，决策=文档化）
7. ~~F12~~ ✅ 已完成（2026-10-27，2d39e9c）

## 四、红线提醒

- F1–F7 全在 `internal/panel/`（index.html + app.js）：R3 esc() 转义不动、R8 协议字符串不动、
  D-002 不引入前端构建链、CSP 无行内 onclick、改完必须 go build 重编译（R13）。
- F1 收紧棘轮后，maxFrontendLines 只许下调不许上调。
- F8/F9 动选号/协议：R6 出站请求集中 internal/upstream；config 改动走表驱动（V3-5 已归一）。
- 所有任务走十阶段：REQ 必要性 → 实现+测试同提交 → pre-commit 10/10 → 变更目录三件套。
