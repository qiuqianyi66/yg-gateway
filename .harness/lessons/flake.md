# 已知 flake（待修缺陷，不是流程教训）

> 由 `.harness/LESSONS.md` §二 外置而来。


| flake | 现象 | 现状 |
|---|---|---|
| `internal/server` `TestModelsEndpoint` | `go test ./internal/server/ -count=3` 因**测试间全局状态污染**失败（`-count=1` 全绿） | **已修（T5，2026-10-16）**：真因是**进程内包级全局**动态模型缓存（其它用例写入的 5min 负缓存）——沙箱隔离不了，T5 的修法是「用例首行手动清缓存」；顺带去掉了对**活上游**的依赖（改假上游固定目录）。**已结构性根治（N3，2026-10-16）**：缓存搬出包级全局、成为 `Handler` 实例字段（`modelsCache`），隔离由 `NewHandler` 的构造保证，不再依赖「记得清」这条纪律；证据见 `.harness/changes/2026-10-16-t5-sandbox/summary.md` 与 `.harness/changes/2026-10-16-models-cache-instance/summary.md` |
| `TestEwmaLatencyPenalizesSlowInPick` | 时间敏感，约 1/8 概率偶发 | **已修（T5，2026-10-16）**：真因是 Windows `time.Now()` 约 1ms 精度 + pick 会刷新中选号 lastUsed → 第 2 轮起并列被打破。改为**可注入时钟**（`Pool.SetClock`，生产默认仍是 `time.Now()`），测试冻结时间；`-count=20` 稳定全绿 |
| panel 包单轮 44–60s | mp 上报路径含 0–10s 随机反作弊抖动且不可注入 | 预期行为；写新测试尽量走「达标/已领」路径（0 抖动） |
