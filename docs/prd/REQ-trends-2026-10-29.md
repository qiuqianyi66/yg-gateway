# REQ-trends-2026-10-29 — 时间序列证据聚合（池健康度 / 选号 / 路由趋势）

> 状态：✅ 已落地（2026-10-29，main 分支）
> 四态纪律：days 聚合 = reqlog 归档观测（observation）+ 成功率 = derivation；
> accounts 现态 = 当前快照（observation）；历史存活曲线 = unknown（requires
> 后续轮次定期落盘快照，本版只给现态）。

## 必要性（阶段 0）

1. **不做会怎样**：yg-gateway 的选号效果、账号池健康度、路由准确率只有「当前
   快照」（/status）与「最近 1000 条」（request_logs），**没有周/月级趋势证据**。
   用户最初诉求是给网关补「可验证对比分」，时间序列是支撑数据的地基——
   单日/单快照看不出账号是否在缓慢死亡、成功率是否在退化。
2. **替代方案为什么不成立**：
   - request_logs（ReadArchive）截断 1000 条——周/月级聚合按 1000 条截断会
     系统性丢早期数据，成功率是**失真样本**不是趋势；
   - usage 小时桶回答「用量」，不回答「成败/活跃账号/到期」——两套数据互补，
     不重叠；
   - 面板现存的到期分布（renderExpiryDistribution）是逐包快照，不是「按天的
     账号活跃序列」。
3. **为什么现在做**：V3-3 真机回填已证明 reqlog 归档连续（10-22..29 共 7 天
   文件），数据在但无人聚合；48h 观测闭环的「下一轮」需要趋势图来对照
   softband/分口径上线前后的行为变化。

## Given-When-Then 验收

- G1：归档开启且有请求
  - When GET /panel/api/trends
  - Then 200；days 为按天（本地时区）升序数组，含 requests/succeeded/failed/
    success_rate/active_accounts/prompt_tokens/completion_tokens；window=归档事件总数
- G2：归档未接线
  - When GET /panel/api/trends
  - Then 501 "request logger not available"（与 request_metrics 同口径）
- G3：某天 0 请求
  - Then 该天不出现在 days（没有事件就没有天）；不产生 success_rate=0 的假观测
- G4：账号从未成功
  - Then accounts[].last_success_known=false（unknown 显式标记），前端显示
    「从未成功」而非「0 次」
- G5：前端
  - When 打开用量视图
  - Then 趋势区块自动加载：柱=每日请求量、折线=成功率（仅观测天，未知天断线）、
    点=活跃账号数；账号现态表含健康度/到期批次/最近成功

## 落地记录

- 2026-10-29：`internal/reqlog/trends.go`（AggregateDaily 纯函数）、
  `internal/reqlog/reqlog.go`（Recorder.ReadAll / archiveWriter.readAll 全量读取）、
  `internal/panel/panel.go`（GET /panel/api/trends）、
  `internal/panel/index.html` + `app.js`（趋势区块 + 双轴 SVG 图）。
- 测试：`internal/reqlog/trends_test.go`、`internal/reqlog/reqlog_test.go`
  （TestReadAllFullWindow / TestReadAllAggregateDaily）、
  `internal/panel/trends_endpoint_test.go`。
