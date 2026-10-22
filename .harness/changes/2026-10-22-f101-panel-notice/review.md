# 评审记录 — R-101 F-101 面板 ACL 显式提示（f101-panel-notice）

- 日期：2026-10-22
- 复审身份：独立只读复审（子代理，L-16）。对象：分支 e269633（panel-features）。
- 分级：前端展示。**本轮为第 1 轮独立复审。**

## 结论

**PASS-WITH-NOTES**（0 🔴 / 0 🟡 / 1 🟢 / 1 🔵）

## 逐条 GWT 证据（REQ-f101-panel-acl-notice）

| GWT | 代码级证据 |
|---|---|
| 凭证管理区展示「已自动收紧 ACL」说明文案 | `internal/panel/acl_notice.js`：NOTICE 文案 = 「凭证已落盘：写回后服务会自动收紧 ACL（仅当前用户 + SYSTEM）。收紧失败仅记日志（WARN），可手动运行 lock-auths.ps1 复核」。挂到 #addDone / #importDone 成功提示后。 |
| 文案严谨不夸大（不承诺「保证已收紧」） | 文案明确「服务会**自动**收紧…**失败仅记日志（WARN）**，可手动运行 lock-auths.ps1 复核」——如实描述 fail-warn 语义，无「保证/已加固」过度承诺。文件头注释写明「收紧结果不落盘、面板不可观测，因此只描述机制与失败兜底路径」。 |
| 纯只读提示，不做收紧操作 | acl_notice.js 只 createElement/appendChild/textContent；无写盘/无 API POST。MutationObserver 幂等（dataset.aclNotice 标记）。 |
| app.js 零增行（前端棘轮 maxFrontendLines=4378） | 新逻辑全部落在新文件 acl_notice.js；app.js 未动。main 上 build + panel 测试绿。 |

## 发现的问题 / 应披露项

| 级别 | 问题 | 证据 / 处置 |
|---|---|---|
| 🟢 | check-boundaries.ps1 在 worktree 环境失败（`.git` 指针文件），已由主仓合并后复核通过 | main 合并后 `go build ./...` + `go test ./internal/panel` 绿；diff 为纯前端新文件，无跨包方向变化。接受为环境项。 |
| 🔵 | 无 handler/DOM 文本断言测试（summary 称有浏览器截图更好、不强求） | 面板提示靠运行时渲染，本变更到「文案存在 + 挂载点正确」级证据；实机截图未附。**接受为已知局限**，建议后续补一个 serve index.html 断言文案存在的轻量测试。 |

## 对后续建议

无需返工。若未来后端 ACL 收紧从 WARN 升级为阻断，需同步改 acl_notice.js 文案（当前文案假设 fail-warn）。
