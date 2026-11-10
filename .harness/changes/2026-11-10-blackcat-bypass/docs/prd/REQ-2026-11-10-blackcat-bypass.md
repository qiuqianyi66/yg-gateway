# REQ — 面板 runBlackCat 绕过排程互斥的修补（P3）

## 必要性

深度复审（`review-deep.md`，2026-11-10）发现 P2-5 的同族互斥只覆盖 scheduler 内部入口：
面板自动任务 `runBlackCat`（`internal/panel/autotask_scripts.go`）直接调 `Upstream.RunNightChats`，
不经过 `taskBlackcat` 锁。23:00 排程正在逐账号补夜间对话时，面板手动触发会先调
`BlackcatNeed` 读到**排程填补前的陈旧差额**，对同一账号多发真实 glm-5.2 对话（1–3 条），
超过当日计数上限。面板的 `tryLockAccount` 只串行面板动作，不认识排程；HTTP 层
`concurrency_per_account=4` 只压并行、压不住串行重复。

## 需求

- scheduler 暴露「blackcat 排程是否正在执行」的只读查询，面板在 `BlackcatNeed` 之前先查；
  排程在跑就直接返回提示，不发任何上游调用。
- 查询是**建议性只读**：不抢锁、不阻塞面板，也不改变 P2-5 的 TryLock 语义。
- 不同族、面板其他自动任务不受影响。

## 验收（GWT）

- Given 排程 blackcat 已取锁、卡在某账号请求里 → When 面板调 `runBlackCat` →
  Then 返回「排程正在自动补足夜猫子，跳过本次手动触发」，且面板侧上游调用数为 0。
- Given 排程未运行 → When 面板调 `runBlackCat` → Then 原有窗口/差额/补足逻辑不变。
- Given 取锁与释放 → Then `BlackcatBusy()` 随门控严格翻转（变异：不置忙标记 → 用例报红）。

## 不做

- 不把面板自动任务改成走 scheduler 入口（跨模块重排，超出本批）。
- 不给其他自动任务（buddy/skill/library 等）加同族检查：排程没有对应族，无重复面。
- 不部署：本批只提交推送，部署等用户明确指示。
