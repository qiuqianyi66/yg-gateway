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
- G4（面板摘要行，复用 trends 区块 + 零 app.js 改动）暂缓——trends 线
  完成后排期。
- G5（M2 历史趋势分桶）依赖快照部署后 ≥2 天数据。

## 怎么回滚

- 纯新增（internal/bench、cmd/bench、bench_test.go、REQ、summary），无既有
  行为改动；回滚 = revert 提交并删除产物文件 bench-report.json（工作区产物，
  未入库）。
