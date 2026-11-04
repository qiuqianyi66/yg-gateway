# ADR-021：/status 代理三态观测——脱敏下沉 logfmt、恒输出键

> 状态：**已实施**（P0-2，`6bef0d2` + 生产部署）。
> 依据：REQ-yg-gateway-proxy-status-2026-11-04 + magpie REA 出口面
> （netproxy.go:71-73 显式代理即 fail-closed；magpie 自身 per-account 观测 unknown）。
> 决策点：三态形态、脱敏下沉层、恒输出键、构造期 vs 连接期判定边界。

## 背景

P0-1（`316c0e7`）落地 fail-closed 后，代理配错 = 该账号出站全失败、请求持续换号。
运维必须能从 /status 看出「代理没生效」，否则把持续换号误判成账号本身的问题。
REA 结论：per-account 代理观测是 yg 自己能补的（magpie 侧也是 unknown，不是现成方案）。

## 候选对比

### D1：观测形态

- 候选 A：只输出 proxy_url 布尔 active。配错时无从区分「配置错误」与「瞬时不可达」。**否决**。
- 候选 B（采用）：**三态** `direct / active / failed`——failed=配置期构造失败（fail-closed，
  出站必失败）；active=构造成功（连接期失败走 dial 错误冒泡，是瞬时不可达不是配置错误）；
  直连账号不出现（accounts[].proxy_url 省略表达 direct，与 AGENTS.md「字段缺失=未知」
  三值语义对齐）。

### D2：脱敏下沉层

- 约束：pool.Status 直出 /status + panel（panel.go 5 处）+ opsagent + bench，原文会泄密
  （R1/R2 红线）；而 pool **不能依赖 upstream**（check-boundaries.ps1 冻结基线）。
- 候选 A：pool/panel 各自写脱敏。多实现漂移、漏一处即泄密。**否决**。
- 候选 B（采用）：**下沉 internal/logfmt**（pool/upstream/auth 三者共同依赖叶子包），
  `logfmt.RedactProxyURL` 单一实现；upstream.redactProxyURL 变薄包装；`auth.Auth.
  RedactedProxyURL()` 视图取值。禁止另起实现（check-boundaries.ps1 冻结）。

### D3：/status 输出键

- 候选 A：零登记时省略键。消费方把缺键读成「无此观测」→ 配错但在冷启动窗口内看不出
  任何信号。**否决**。
- 候选 B（采用）：**顶层恒输出 `proxy_status` 键**（零登记时 `{}`）——键缺失不可读成
  「无此观测」，与四态纪律（missing ≠ absence）一致。

### D4：判定边界

- 构造期（配置错误，fail-closed）→ `failed`，由 newTransportWithProxy 返回
  `proxyEffective` 登记。
- 连接期（代理半死/不可达）→ **不改变三态**，dial 错误冒泡驱动既有换号/日志路径。
  /status 不承诺连接期维度（防把「瞬时不可达」误读成「配置错误」）。**顺带修真 bug**：
  unsupported-scheme 默认分支原误返 active，TestProxyConfigFailClosed 抓出改 failed。

## 合同

- 三态 per-proxyURL（账号共用同 URL 状态共享；同一代理部分账号生效部分失败场景不存在——
  构造失败只取决于 URL 本身）。
- 脱敏入口仅 `logfmt.RedactProxyURL`（userinfo 隐去、解析失败/空串安全）。
- /status 顶层恒输出 proxy_status；accounts[].proxy_url 为脱敏值。

## 副作用与回滚

- 无配置、无持久状态；/status 新增键（消费方新增可读字段，兼容）。
- 回滚：删 handler.status 的 proxy_status 行 / pool.Status.ProxyURL / proxyEff 登记
  （见变更记录 summary，语义级三段回滚）。
