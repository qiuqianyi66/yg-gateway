# REQ-2026-11-04-panel-standalone-auth.md — 面板独立前端文件族鉴权补漏（P1-3 机制化闭环）

## 必要性（四问）

- **真实问题**：生产开 api_key 鉴权后，用量页「时间序列证据·趋势」「流量画像·Agent构成」
  两个模块显示「读取趋势失败（未授权）/ 读取流量画像失败（未授权）」——trends.js /
  agent_profile.js / bench.js 三处**自包含 fetch 不带 Authorization 头**，被面板鉴权
  中间件 401 拦截。
- **根因**：P1-3（b544037）「api() 强制鉴权机制化」只覆盖 app.js 主路径的 api()；
  独立嵌入文件族（trends/agent_profile/bench/opsagent/savings_note）各自带独立 fetch，
  其中 opsagent/savings_note 已带 Bearer（正确先例），三处漏网——机制化当时没有
  「凡 /panel/api fetch 必须带鉴权」的机器检查，漏网当场不红。
- **不做会怎样**：用量页三个模块（趋势/流量画像/benchmark）在鉴权环境全部 401 不可用；
  未来新独立文件大概率再漏（无检查）。
- **更便宜手段**：三处照 opsagent 同款补 Bearer（各自读 localStorage，保持自包含设计，
  不引入共享依赖）；加逐文件源码断言测试钉住「有 fetch 必须含 Authorization」。
- **现状是否已变**：P1-3 机制已存在，缺的是覆盖范围与检查。

## Given-When-Then 验收

1. Given 生产 api_key 鉴权开启；When 打开面板用量视图（#usage）；
   Then 趋势 / 流量画像 / benchmark 三模块数据正常加载（HTTP 200），无「未授权」。
2. Given 任意前端脚本文件包含 `fetch('/panel/api`；When 跑 go test ./internal/panel；
   Then 该文件必须含 Authorization 注入，否则测试 FAIL（TestAppJSStandaloneFetchBearer）。
3. Given 无密钥（localStorage 无 yg-gateway.key）；When 独立文件请求；
   Then 不带 Authorization（与未填密钥语义一致，不注入空头）。

## 范围

- 动：`internal/panel/trends.js`、`internal/panel/agent_profile.js`、`internal/panel/bench.js`
  （api() 注入 Bearer，与 opsagent.js 同款）；`internal/panel/frontend_ux_test.go`
  （机制化检查 TestAppJSStandaloneFetchBearer）。
- 不动：app.js 主 api()（已正确）；opsagent.js / savings_note.js（已正确）；
  后端鉴权中间件（行为正确，401 是前端漏头所致）；CSP 与嵌入方式。
- 未决：密钥门（openKey）弹窗由 app.js 401 分支承担，独立文件 401 仍显示错误+重试
  （用户先填密钥即可，行为与 opsagent.js 一致）。

## 不做 / 留档

- 不引入共享 fetch helper（maxFrontendLines 棘轮 + 自包含设计先例；
  逐文件 Bearer 是现有正确先例 opsagent 的既定模式）。机制化由测试承担。
