# 变更摘要：网关级 benchmark（三项可验证指标）

> 日期：2026-10-29 · 关联 REQ：REQ-benchmark-2026-10-29.md
> 前置依赖：REQ-trends（时间序列聚合，b208fa7）、REQ-snapshot（现态快照，c9c7650）

## 改了什么

- 新增 `internal/bench/bench.go`：网关级 benchmark 纯函数包。
  - `Eval(events, accounts, generatedAt) Report`——零 IO/时钟/随机，同一输入
    重跑逐字节一致（G3 可复算）。
  - 四态纪律（AGENTS.md）：每个 Metric 带 Kind
    （observation/derivation/inference/unknown）+ Samples + Note；样本不足
    输出 unknown（Value=0）而非硬编码 0（AGENTS.md L-22）。
  - M1 pickExpiringBias：被选账号平均 expiring / 池内平均，>1 = 快过期优先
    兑现。账号匹配口径 = 事件标签「昵称(uid8)」（reqlog R2 脱敏形态）取括号
    uid8 ↔ 快照完整 UID 前 8 位；匹配不上 = unknown 不计样本（缺失 = 未知）。
  - M2 health：7 天窗口成功率（derivation）+ 健康账号占比（observation，
    非禁用/暂停/冷却）+ 每日分桶。
  - M3 routeSuccess：按请求模型分桶成功率，minRouteSamples=5，样本不足 =
    unknown（推断不作数）。
- 新增 `cmd/bench/main.go`：CLI——`bench -archive <reqlog目录> -snapshots
  <快照目录> [-out <json>]`，读归档全量 + 最新快照，输出 JSON 报告。
  零出站（不发起任何上游调用，用户付费约束）。
- 新增 `internal/bench/bench_test.go`：6 测试
  （M1 标签口径、M2/M3、空输入、样本不足 unknown、确定性、accountKey/uidKey
  解析）。

## 怎么验证的

- `go test ./internal/bench/ -count=1`：6/6 PASS。
- `go build ./cmd/bench/ ./internal/bench/`：通过。
- `gofmt -l ./cmd ./internal`：无输出（前置段已清漂移，新文件零容忍）。
- 生产数据实跑（D:/yg-gateway/data/request-logs，2427 事件 / 7 天窗口）：
  - M2 7 天成功率 0.6（derivation，2427 样本）；每日分桶
    0.92(10-22)/0.03(10-23)/1(10-24)/1(10-26)/0.97(10-27)/1(10-28)/1(10-29)。
  - M3：cn:deepseek-v4.1-flash 1.0（1398 样本，derivation）、
    cn:glm-5.3-flash 0.97（37）、cn:glm-5.2 0.83（6）、cn:auto 1.0（7）；
    其余样本 <5 = unknown（含带倍率后缀的别名模型）。
  - M1：unknown（快照目录未部署——生产部署被并行会话 agent_profile 脏文件
    阻塞，快照次日 03:00 首次触发，等并行会话提交后补部署）。

## 未做 / 后续项（明确边界）

- 不硬对标 GAIA / LLM 榜单（不同量纲，违反四态纪律，已纠偏）。
- 不发起付费评测调用（用户约束）。
- G5（M2 历史趋势分桶）依赖快照部署后 ≥2 天数据。

## G4 面板摘要行（2026-10-29 追加，已随 216a0c4 合入）

- 端点 `GET /panel/api/benchmark`（internal/panel/bench.go）：复用 bench.Eval
  纯函数，读 reqlog 归档 + Pool.List() 现态投影，零出站零付费（R1 无凭证）。
- 前端 `internal/panel/bench.js`：自包含 IIFE（对齐 trends.js 模式，esc()
  转义、无内联事件、CSP 合规）；index.html 用量视图挂「网关 benchmark」
  区块（#benchBox/#benchNote/#btnBench/#benchHost）：M1 到期偏置 / M2
  成功率+健康占比 / M3 路由成功率表，四态徽标 + 样本数，unknown 显示「—」
  不做 0 美化。
- 测试 `bench_endpoint_test.go` 2 个：全链路 JSON 字段断言 + 未接线归档回
  501（与 trends 同口径）；前端闸门（TestAppJSSyntax/TestIndexHTMLNoInlineScript
  /TestAppJSTopLevelSmoke）全绿。
- 边界白名单收录 panel → bench（只读调用评测层）。
- 同步两处并行会话语义变更未同步的既有测试断言（消除 CI 红）：
  coverage_handlers_test.go context_length 200000→1000000（f8d8460 floor
  语义）；client_test.go legacy effort 测试改用非别名模型 glm-4.5
  （487dc4e 起 auto 路由别名被过滤）。
- 验证：bench/panel/upstream 全绿；pre-commit 全量 11/11 通过；已推
  origin/main（216a0c4）。

## 怎么回滚

- 纯新增（internal/bench、cmd/bench、bench_test.go、REQ、summary），无既有
  行为改动；回滚 = revert 提交并删除产物文件 bench-report.json（工作区产物，
  未入库）。G4 追加项同纯新增（internal/panel/bench.go/bench.js/
  bench_endpoint_test.go + 端点接线）；回滚 = revert 216a0c4（其中两处既有
  测试断言同步随本提交一并 revert 亦可，语义见各自注释）。
