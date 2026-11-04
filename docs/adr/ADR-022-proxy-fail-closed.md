# ADR-022：显式代理 fail-closed——哨兵 errProxyConfig + failDial 冒泡

> 状态：**已实施**（P0-1，`316c0e7` + 生产部署）。
> 依据：REQ-yg-gateway-proxy-failclosed-2026-11-04 + magpie REA 出口面
> （netproxy.go:71-73 显式 choice 非空不回退直连，observation）。
> 决策点：fail-open 为何是缺陷、失败表达形态、直连边界。

## 背景

多账号共用网关出口 IP 时，per-account 代理是切断「IP 上 N 号关联」风控的关键机制。
上一轮 4fb5639 的 summary 把「自动回退直连」写成既定行为（第 71 行）——即显式配代理
的账号在代理配错时**静默走共享出口直连**：风控关联重新出现，且成功响应不报错
（最危险形态：不报错的回归）。

## 候选对比

### D1：fail-open 还是 fail-closed

- 候选 A（原实现）：代理构造失败 → WARN + 返回直连 Transport。可用性优先（请求总能发出），
  但把「账号应走的隔离出口」静默替换成「共享出口」，且调用方无感知。**否决**（缺陷即背景所述）。
- 候选 B（采用）：**fail-closed**——显式配置代理的账号，代理不可用 = 该账号出站失败
  （可观测、可排查），绝不回退直连。与 magpie「choice 非空即 fail-closed」同语义。
  直连路径（`proxyURL == ""`）不受影响，仍是**唯一合法直连来源**——用「空 = 直连」
  显式表达，不给任何失败分支留直连口子。

### D2：失败表达形态

- 候选 A：失败分支直接返回 error。构造期无 error 通道（newTransportWithProxy 返回
  (tr, eff)），失败须落到「请求期可见」。**否决**。
- 候选 B（采用）：**哨兵错误 + failDial 替换**——`errProxyConfig`（`proxy config error
  (fail-closed)`）哨兵 + `failDial(cause)` 把 DialContext 替换为必然返回 cause 的
  dialer。请求期 dial 错误冒泡 → chatrun 网络层抖动分支（chatrun.go:683-695）→
  rotateBackoff 换号。错误消息经 redactProxyURL 脱敏（R1/R2：代理 URL 可含 userinfo）。
  顺带修正 4fb5639 遗留：该 commit 把 fail-open 当成既定行为写进 summary，本轮改语义
  并同步文档。

### D3：连接期与构造期边界

- 构造期失败（URL 解析 / SOCKS5 初始化 / 不支持 scheme）→ fail-closed（本 ADR）。
- 连接期失败（代理半死/不可达）→ 本就是 dial 错误冒泡（已 fail-closed），不改；
  与 ADR-021 D4 的「/status 不承诺连接期维度」对齐。

## 合同

- 显式代理三处失败分支（URL 解析 / SOCKS5 初始化 / 不支持 scheme）一律 failDial，
  不再返回直连；WARN 文案标注 fail-closed。
- 直连仅由 `proxyURL == ""` 产生；dial 错误可被既有 rotateBackoff 换号路径消费。
- fail-closed 状态可由 /status proxy_status=failed 观测（ADR-021）。

## 副作用与回滚

- 行为变化：显式代理配错的账号从「静默直连」变为「出站失败」——预期行为
  （配置错误应当可观测）。生产 config 无该形态账号，实际影响为零。
- 回滚：三处失败分支恢复 `WARN + return tr`（git revert 316c0e7 即达）；语义级临时
  放行可把 failDial 换回 return tr。
