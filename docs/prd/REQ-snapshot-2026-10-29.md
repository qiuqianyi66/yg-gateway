# REQ: 账号现态每日快照（历史存活曲线数据地基）

> 日期：2026-10-29 · 状态：已落地（合入 main） · 类型：观测基建

## 必要性

时间序列趋势（REQ-trends-2026-10-29）已给出「按天请求/成功率」曲线，但**账号
存活/到期曲线**缺历史数据源：`state.json` 是覆盖式现态文件（每次保存重写），
无法回答「某账号从何时开始沉默 / 积分何时开始逼近到期」。补一个每日落盘的
账号现态快照（03:00，避开全部既有任务时点），跨日文件合成即可回答案类问题。

这是「网关级 benchmark 账号池健康度指标」与「养号效果评估」的共同数据地基，
不依赖上游付费调用（纯本地观测，符合用户约束）。

## Given-When-Then 验收

- **G1（落盘）**：Given 服务运行且 state 文件存在，When 每日 03:00 触发，
  Then `snapshots/YYYY-MM-DD.json` 生成，含 `captured_at` + 全部账号现态
  （Pool.List() 同口径）。
- **G2（幂等）**：Given 当日文件已存在，When 再次触发，Then 原子替换
  （tmp+rename），不产生半截文件。
- **G3（R1）**：Given 快照文件，When 检查内容，Then 不含任何凭证字段
  （access_token / refresh_token），与 /panel/api/status 同口径。
- **G4（目录容错）**：Given 快照目录不存在，When 触发，Then MkdirAll 自动创建后落盘。
- **G5（未配置）**：Given `SnapshotDir` 为空，When 触发，Then 直接返回
  （任务未启用，不落盘不报错）。
- **G6（四态）**：Given 快照文件，Then 内容标注为 observation（池直接观测
  的现态）；「历史存活曲线」为后续消费轮次，本版只落盘不消费（unknown +
  requires 快照积累）。

## 落地记录

- 2026-10-29：实现（scheduler taskSnapshot + snapshot.go + cmd/server 装配），
  4 个专项测试全绿；scheduler + cmd/server 全量测试零回归；pre-commit 11/11。
  提交：main。
