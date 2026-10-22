# 2026-10-22-key-ip-whitelist 变更摘要

需求：`docs/prd/REQ-key-ip-whitelist-2026-10-22.md`（R-108）
决策记录：`docs/adr/ADR-013-key-ip-whitelist.md`
分级：L3（数据模型扩展，新增字段跨 config→httpauth→server 三层）

---

## 一、改了什么

| 文件 | 改动 |
|---|---|
| `internal/httpauth/index.go` | Key 加 `AllowedCIDRs []*net.IPNet`；新增 `IPAllowed(ipStr)` 方法（空=不限，非空 fail-closed，剥端口） |
| `internal/httpauth/ipallow_test.go` | 新单测：空放行/CIDR 命中拒/裸 IP/nil 条目跳过 |
| `cmd/server/config.go` | APIKeyConfig 加 `allowed_cidrs []string`；normalizeAPIKeys 逐条校验 CIDR（非法 fail fast，裸 IP 接受，空串剔除，输入 nil 保留 nil） |
| `cmd/server/main.go` | buildAuthSnapshot 带 `AllowedCIDRs: parseKeyCIDRs(...)`；新增 parseKeyCIDRs/fullMask（string→*net.IPNet，裸 IP 收 /32） |
| `cmd/server/api_keys_config_test.go` | 新增 3 例：非法 CIDR fail fast / 合法 trim 落库 / 省略=nil |
| `internal/server/handler.go` | withAuth 鉴权后加 IP 门禁：`key.IPAllowed(extractClientIP(h.cfg.TrustedProxy, r))` 不命中→403 `ip_not_allowed` |
| `internal/server/ip_whitelist_test.go` | 新测试：空放行 / fail-closed 403 / 受信代理取真实 IP |

**没做的（明写）**：面板可视化编辑 allowed_cidrs；IP 维度计费；不改受信代理判定本身；
不改报文字符串（R8）。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| httpauth 单测 | `go test ./internal/httpauth` | ok |
| server 门禁 | `go test ./internal/server` | ok（fail-closed/空放行/受信代理三例） |
| config normalize | `go test ./cmd/...` | ok（非法 CIDR fail fast） |
| build/vet/gofmt | — | 全绿 |

**GWT 逐条对应**：
- F-2 拒不在名单者 → TestIPWhitelist_FailClosed（8.8.8.8 名单外 403 + ip_not_allowed）
- F-3 无名单行为不变 → TestIPWhitelist_EmptyMeansUnrestricted（任意 remote 200）
- F-4 受信代理取真实 IP → TestIPWhitelist_TrustedProxyUsesRealIP（XFF 追加语义取首非受信段）
- F-5 非法 CIDR fail fast → TestAPIKeyInvalidCIDRFailsFast

## 三、未完成 / 待补（诚实清单）

- [ ] 面板可视化编辑 allowed_cidrs（本期 config.json 手填）。
- [ ] 浏览器实机联 requires 可交互会话；本轮到单测+编译级证据。

## 四、怎么回滚

config.json 删 `allowed_cidrs` 键即回到改造前（省略=不限）；代码回滚 `git checkout HEAD~ -- <本变更文件>`。
无凭证/数据迁移；`auths/` 不参与回滚。字段热生效，回滚代码后需重启进程加载旧二进制。

## 五、Learning

新约束字段的四态语义必须在 ADR 里显式钉死"省略=什么、空切片=什么"，否则三态（Models）
与四态（IP）混用会让运维误配。本需求刻意不给 IP 白名单"空切片=全拒"的中间态，避免手滑
成全员拒绝；真实 IP 提取复用 trusted_proxy，是本需求零新增安全面的关键。
