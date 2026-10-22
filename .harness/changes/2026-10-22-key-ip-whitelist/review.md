# 评审记录 — R-108 Key 级来源 IP 白名单（key-ip-whitelist）

- 日期：2026-10-22
- 复审身份：独立只读复审（子代理，L-16）。对象：commit `5f222a2`。
- 分级：L3（数据模型扩展，ADR-013）。**本轮为第 1 轮独立复审。**

## 结论

**PASS-WITH-NOTES**（0 🔴 / 0 🟡 / 1 🟢 / 1 🔵）

> 注：本提交由本复审者所在会话撰写，此处按独立复审身份重核代码证据。

## 逐条 GWT 证据（REQ-key-ip-whitelist / ADR-013）

| GWT | 代码级证据 |
|---|---|
| 有名单 fail-closed（拒不在名单者） | `internal/server/handler.go` withAuth：`key.IPAllowed(extractClientIP(h.cfg.TrustedProxy, r))` 不命中→403 `ip_not_allowed`。`httpauth.Key.IPAllowed`：非空名单逐 CIDR `Contains`，不命中 false。测试 TestIPWhitelist_FailClosed。 |
| 无名单行为不变（空=不限，非「未配置即拒绝」） | IPAllowed：`len(k.AllowedCIDRs)==0 → return true`。normalizeAPIKeys：输入 nil 保持 nil。测试 TestIPWhitelist_EmptyMeansUnrestricted + TestAPIKeyCIDRMissingUnrestricted。 |
| 受信代理取真实 IP（复用 trusted_proxy，不重复实现） | withAuth 调用既有 `extractClientIP(h.cfg.TrustedProxy, r)`，未新写 XFF 解析。测试 TestIPWhitelist_TrustedProxyUsesRealIP（XFF 追加语义取首个非受信段）。 |
| 非法 CIDR fail fast | normalizeAPIKeys 逐条 `net.ParseCIDR`（裸 IP 按 ParseIP 接受 /32），非法报错。测试 TestAPIKeyInvalidCIDRFailsFast。 |
| 四态语义钉死（ADR-013 D-131） | ADR-013 明确「省略=不限，刻意不提供空切片=全拒的中间态」，避免手滑成全员拒绝。 |

## 发现的问题 / 应披露项

| 级别 | 问题 | 证据 / 处置 |
|---|---|---|
| 🟢 | check-boundaries.ps1 在 worktree 环境失败（`.git` 指针文件），主仓合并后复核通过 | diff 新增 import 仅 `net`（标准库），httpauth 仍是纯叶子包、无 internal 反向依赖；server→httpauth 是既有方向。main 合并后 build/test 绿。接受为环境项。 |
| 🔵 | 多层 NAT/移动网络下出口 IP 漂移可能被 fail-closed 误拒 | ADR-013 后果节已披露这是 fail-closed 固有代价，建议管理员用 /32 或收敛网段。已知设计取舍，非缺陷。 |

## 对后续建议

无需返工。后续若做面板可视化编辑 allowed_cidrs，需复用同一 `IPAllowed` 判定与 `extractClientIP`，不要在前端重复实现 CIDR 匹配。
