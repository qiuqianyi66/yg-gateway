# V3-3 真机 48h 数据回填（2026-10-28 21:53，窗口 10-26 21:41 起 48h 满）

> 纯文档回填，零产品代码改动。数据来源：生产实例 /status 快照
> （D:\yg-gateway，PID 26588，exe 10-28 17:44 换版）+ reqlog 归档
> （10-27 + 10-28，604 行）。四态纪律全程标注。

## 回填结论

### ① count_tokens 估算精度 —— 无样本（unknown）

- **observation**：48h 内 reqlog 归档 `path` 直方图 = `/v1/chat/completions` 604 行，
  count_tokens **0 次调用**（生产客户端未走 Anthropic 协议或未触发该端点）。
- 结论：精度无法回填。估算精度「±30% 内」需一次带对照的真机调用
  （requires: 客户端真实 count_tokens 请求 + 同请求 usage.prompt_tokens 对照）。
  现状不推翻、不确认——保持「estimated:true 诚实标注 + 单测数值锚定（derivation）」。
- 已排除成本：不发起付费真机验证（用户约束，避免无谓消耗）。

### ② opsagent 建议质量 —— 未启用，无样本（observation）

- **observation**：生产 config.json `opsagent` 段 = null（默认 off，从未开启）。
- 结论：V3-3 换版后 opsagent 未启用，建议质量无法回填。解锁动作：
  生产 config 开 `opsagent.enabled` 后观察一轮（REQ-opsagent 解锁动作 ③）。
- 影响：capacity / 402-contradiction 两类规则的运行态漂移检测仍处于空窗。

### ③ 软带命中率 + bandMult 20k/请求假设 —— 0 命中；20k 假设被双峰分布推翻（observation + derivation）

- **observation**：48h 内 5 账号 credits 全在 floor 之上很远
  （2098/2162/350/2641/4103，floor=100，floor×1.5=150 预筛门槛），
  软带 **0 命中**，pick_reason 无一条含 softband。
- **derivation（关键校准）**：reqlog 成功请求 596 行的 total_tokens 分布为**双峰**：
  - 聊天小请求：p50 = **762** token（332 行 ≤1k + 51 行 1-5k）
  - 评测大请求：**215 行 × 200-500k** + 3 行 >500k，全部 UA =
    `deepseek-harness/0.2.1-rc.3`（实测单次 prompt 30-32 万 token，credit 4.4/次）
- **结论**：`softBandTypicalTokens = 20_000` 假设**既不贴近聊天（762）也不贴近评测
  （30 万）**——按「2 次典型请求定带宽」语义，评测流量会让带内一次请求就吃掉
  按 20k 估算的 15 倍带宽。**建议 bandMult 按流量类分口径**：
  - 保持默认 20k 只能覆盖聊天场景；评测流量应走 cost_explore 实测口径
    （该号近期 P50 扣费，代码已有 `softBandUpper` 的中位数路径，但评测大请求
    会把 P50 拉高）。
  - 决策待用户：是「带宽按模型 P50 实测」还是「评测流量单独评估」。
- 备注：`softBandUpper` 有 cap（floor×0.5），带内上界 ≤150，现状无号进带，
  该假设的实弹影响尚未触发，属校准优先而非紧急修复。

### ④ recent_usage_factor —— 默认关，无样本（observation）

- 生产 config `pool` 段无 `recent_usage_factor`（默认 false）。半衰期 1h / scale 200k
  参数无真机样本，维持 REQ 论证（1h 适配 CodeBuddy 12h 签到节拍）不推翻。

## 其他顺带观察（一并记录，不展开）

- 池健康度：5/5 healthy，disabled=0、cooling=0（/status 顶层计数）；
  global latency P50=3104ms / P90=6375 / P99=9750，count=309。
- 成功率：604 行中 594 success + 10 http_error（98.3%），attempts 全为 1。
- credit 记账：596/604 行 credit_known=true（10 条 http_error 为 false）。

## 改了什么（仅文档）

- `docs/prd/REQ-routing-improvements-2026-10-25.md`：追加「真机回填（2026-10-28）」节。
- `docs/prd/REQ-opsagent-2026-10-24.md`：追加「真机回填（2026-10-28）」节。
- 本 summary。

## 怎么验证的

- 数据全部来自生产实机：/status 实时快照（落盘 status-snapshot.json，含 provenance
  字段）+ reqlog 归档逐行解析；分位数/分桶由本次采集脚本现算。
- 无代码改动，无需跑门禁；文档格式走既有 REQ 体例。

## 怎么回滚

单提交 revert（纯文档）。

## 遗留（不阻塞，记录）

- 生产 exe 10-28 17:44 换版早于本回填；工作区有 3 个未提交文件
  （models_handler.go / resolve_model.go / handler_test.go，model-id 倍率后缀半成品），
  按 L-21 未碰、未提交，待用户确认归属后另行处理。
