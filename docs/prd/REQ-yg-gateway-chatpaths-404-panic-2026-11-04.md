# REQ — yg-gateway chatStream 全路径 404/405 撞 panic 修复

## 必要性

chatStream 对 404/405 无条件换路径，上游把**全部**候选路径都回 404/405 时循环耗尽，
撞上 `panic("unreachable: chatPaths is never empty")`——一次上游路径变更就能让整个
网关进程崩，所有在途请求同时失败。该项已由 e2e 登记为「观察未修」，属可复现的稳定性
缺陷，必须在发版前闭合。

日期：2026-11-04
来源：P0-3 e2e 登记"观察未修"项 + 本轮闭合
基线：`5ba554a`

## 背景

`internal/upstream/client.go` chatStream 对 404/405 无条件 `continue` 换路径；
上游对**全部**候选路径回 404/405 时循环耗尽，撞
`panic("unreachable: chatPaths is never empty")`。chatPaths 恒非空只保证至少一条
路径，不保证路径不全 404/405（global 双路径、cn 单路径均为真实可达形态）。

## 需求

Given 上游对全部候选 chat 路径返回 404（或 405）
When ChatStream 发起请求
Then 进程不 panic，正常返回 ≥400 错误（带 Kind 信封，行为与单路径 ≥400 一致）

Given global 账号（两候选路径）
When 两条路径均返回 404
Then 各请求一次（换路径重试语义不变），最终返回 404 错误

Given cn 账号（单候选路径）
When 返回 404
Then 只请求一次，正常返回 404 错误

Given 首路径 404、次路径 200（换路径成功场景，既有语义）
When 请求
Then 行为不变（换路径继续，不落 ≥400 返回）

## 非目标

- 429 分类细化、PII 占位符、min_request_interval 默认值、warm-up 错峰、三层锁：
  均为闭合评估结论（不实施），见 summary.md。
