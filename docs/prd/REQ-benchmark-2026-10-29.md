# REQ: 网关级 benchmark（三项可验证指标）

> 日期：2026-10-29 · 状态：**立项待实现** · 类型：评测基建
> 前置：REQ-trends-2026-10-29（时间序列聚合）+ REQ-snapshot-2026-10-29（账号
> 现态快照）——本 benchmark 完全复用两者的数据源与四态口径，不新发付费请求。

## 必要性

用户最初诉求：给 yg-gateway 补「公开评测数字」——网关级 benchmark，产出
**可验证对比分**。纠偏（2026-10-29 已对齐）：GAIA 84.2% 是**端到端模型智能**
分数，网关的三项指标是**运维/成本/路由**指标，两者不可硬对比（违反四态纪律，
把「2 个样本的推断」当「观察到的事实」展示，会误导养号决策）。因此 benchmark
的形态是：**对网关自身行为的可验证量化**——三项指标、明确口径、四态标注，
数字随时间/账号池演进可复算，而不是对标某个 LLM 榜单。

## 三项指标（口径）

### M1 选号算法效果（快过期优先消耗是否兑现）

- 数据源：reqlog 归档（`account` + `time` + `credit_known`/`credit`）+ 账号
  现态快照（`credits_expiring` / `credits_earliest_expiry`）。
- 指标：被选账号的**到期压力**——每次请求命中账号的
  `credits_expiring`（窗口内将到期积分）分布 vs 池内全体分布。
  兑现判据：选号样本的 expiring 占比 > 池内基线占比（观察/派生双环）。
- 输出：`expiring_bias`（0~1+，>1 = 快过期优先兑现）+ 样本数 + 四态标注。

### M2 账号池健康度

- 数据源：reqlog 归档（按天 success_rate，derivation）+ 快照
  （disabled/paused/cooling 现态，observation）。
- 指标：7 天窗口成功率均值、健康账号占比（现态）、冷却/禁用趋势。
- 输出：`healthy_ratio` / `success_rate_7d` + 分桶 + 样本数。

### M3 模型路由准确率

- 数据源：reqlog 归档（`model` 请求模型 vs `kind`/`outcome`/`status`）。
- 指标：请求模型（客户端指定）→ 网关实际完成路径的成功率，分模型分桶。
- 输出：每模型 `route_success_rate` + 请求数；样本 <5 的模型标 unknown
  （推断不作数）。

## Given-When-Then 验收

- **G1（离线）**：Given 无任何上游调用，When 运行 benchmark，Then 全部指标
  由 reqlog 归档 + 快照文件计算（零新出站，符合用户付费约束）。
- **G2（四态）**：Given 输出报告，Then 每个数字带 kind
  （observation/derivation/inference/unknown）+ 样本数，缺失数据标 unknown
  而非 0。
- **G3（可复算）**：Given 同一份归档 + 快照输入，When 重跑，Then 输出逐字节
  一致（纯函数，无时钟/随机依赖，或钉住时钟）。
- **G4（面板可见）**：Given 报告生成，Then 面板「时间序列证据」区块旁可见
  三指标摘要（复用 trends.js 自包含模式，app.js 零改动）。
- **G5（增量）**：Given 快照目录多日文件，When 重跑，Then M2 能输出
  disabled/paused/cooling 的**历史趋势**（每日一桶，需 ≥2 天快照）。

## 明确不做

- 不对标 GAIA / 任何 LLM 榜单（不同量纲，硬对比违反四态纪律）。
- 不消耗账号积分做端到端评测（用户约束：不发起付费验证）。
- 不新增面板大块 UI：复用 trends 区块 + 摘要行（app.js 冻结棘轮）。

## 落地记录

- 2026-10-29：立项（本文件）。
- 2026-10-29：实现完成——
  - `internal/bench/bench.go`：Eval 纯函数（零 IO/时钟/随机，可复算），
    Kind 四态 + Samples + Note + Buckets；M1 pickExpiringBias（账号标签
    「昵称(uid8)」↔ 快照 UID 前 8 位匹配，匹配不上 = unknown 不计样本）、
    M2 health（7 天成功率 + 健康占比 + 每日分桶）、M3 routeSuccess
    （minRouteSamples=5，样本不足 = unknown）。
  - `cmd/bench/main.go`：CLI 读 reqlog 归档 + 最新快照，输出 JSON 报告。
  - 6 个测试全绿（含 TestAccountKeyParse 口径解析）。
  - 生产数据实跑（2427 事件 / 7 天窗口）：M2 7 天成功率 0.6（derivation）、
    每日分桶 0.92/0.03/1/1/0.97/1/1；M3 cn:deepseek-v4.1-flash 1.0
    (1398 样本)、cn:glm-5.3-flash 0.97(37)、cn:glm-5.2 0.83(6)；
    M1 为 unknown（快照未部署，生产部署被并行会话脏工作区阻塞，属预期）。
  - 验收：G1/G2/G3 达成；G5 依赖快照部署后 ≥2 天数据。
- 2026-10-29：G4 达成（216a0c4 已推 origin/main，待部署生产）——
  - 端点 `GET /panel/api/benchmark`（internal/panel/bench.go）复用 bench.Eval，
    读 reqlog 归档 + Pool.List() 现态投影，零出站零付费；
  - 前端 bench.js 自包含 IIFE + index.html「网关 benchmark」区块（M1/M2/M3
    摘要，四态徽标 + 样本数，unknown 显示「—」）；
  - bench_endpoint_test.go 2 测试全绿，前端闸门全绿，pre-commit 11/11；
  - 同步两处并行会话语义变更未同步的既有测试断言（context_length→1M、
    legacy effort 测试改用 glm-4.5）。
