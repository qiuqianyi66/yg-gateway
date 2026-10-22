# 评审记录 — R-106 OpenRouter 费率 inference 标注（openrouter-savings-display）

- 日期：2026-10-22
- 复审身份：独立只读复审（子代理，L-16）。对象：分支 e269633（panel-features）。
- 分级：前端展示。**本轮为第 1 轮独立复审。**

## 结论

**PASS-WITH-NOTES**（0 🔴 / 0 🟡 / 1 🟢 / 1 🔵）

## 逐条 GWT 证据（REQ-openrouter-savings-display）

| GWT | 代码级证据 |
|---|---|
| 用量区渲染折算金额 + inference 标注 | `internal/panel/savings_note.js`：渲染「本窗口折算 ≈ $X（按 OpenRouter 公开参考价估算 · inference，非上游账单实测）」。 |
| 字段缺失显示「未估算」而非 0（禁止 `!field` 判否） | render() 用 `Object.prototype.hasOwnProperty.call(totals,'usd_value')` 判**键存在**，缺失分支显示「本窗口未估算：无模型定价或窗口内无计费用量（usd_value 字段缺失 = unknown，不按 0 计）」——正确四态，无 `!totals.usd_value` 判否。 |
| 不改后端 wire 形状 | 文件头注释声明不改后端；main 上 `/status`、usage.go 折算一行未动（usage-incremental summary 也确认 bucketUSD 四态折算未改）。 |
| app.js 零增行 | 新逻辑在 savings_note.js；app.js 未动。main build + panel 测试绿。 |

## 发现的问题 / 应披露项

| 级别 | 问题 | 证据 / 处置 |
|---|---|---|
| 🟢 | check-boundaries.ps1 在 worktree 环境失败（`.git` 指针文件），主仓合并后复核通过 | main 合并后 build/test 绿；diff 为纯前端新文件。接受为环境项。 |
| 🔵 | `catch (e) { /* 网络抖动保留静态文案 */ }` 静默吞 fetch 异常 | 这是前端 JS（Go R10 静态检查不扫 .js），且注释明确「保留静态说明文案、不弹窗不报错」是预期行为（401/5xx 也不打扰用户）。语义正确，记录为已知取舍而非缺陷。 |

## 对后续建议

无需返工。后端 `usd_value` 字段名若改，需同步 savings_note.js 的 hasOwnProperty 键名（两处同口径：app.js 明细表 + #savingsLine）。
