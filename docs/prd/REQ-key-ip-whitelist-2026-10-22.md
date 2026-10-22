# REQ：Key 级 IP 白名单（R-108，P2）

> **状态：v1（2026-10-22）。config.json 驱动，本期不做面板管理。需要 ADR-013。**
> 落地记录见 `.harness/changes/2026-10-22-key-ip-whitelist/`。

---

## 一、必要性（阶段 0，四个问题）

**Q1 要解决的真实问题是什么？**（实测，非"清单上写着"）

PRD v2 §5 R-108：当前 `httpauth.Key`（index.go:18-31）与 `APIKeyConfig`（config.go:418-430）
只有 Models/Realm/RPM/MonthlyTokenQuota/Group 约束，**没有任何 Key→来源 IP 维度的限制**。
全仓现有 whitelist 都不是这个语义：模型白名单（server/resolve_model.go:45）、受信代理
CIDR（server/trusted_proxy.go:24，是"哪些代理可被采信"而非"哪些客户端可用 Key"）、
国家白名单（upstream/global_register.go:133）。一把泄漏的 Key 在任意网络下都能用。

**Q2 不做会怎样？** 不报错，但 Key 一旦泄漏（贴日志/进前端/被抓包），攻击者拿到即可从
任意 IP 持续调用，直到 Key 轮换。现有约束管不住"从哪来"。

**Q3 现状是不是已经变了？** 真实来源 IP 的**可信提取**已存在（trusted_proxy.go 的
`extractClientIP`，受信代理追加语义防 XFF 伪造）——本需求**复用**它，不重复实现；缺的
只是"把可信 IP 与 Key 白名单比对"这一步。

**Q4 有没有更便宜的等效手段？** 有且本需求即：config 加一个字段 + normalize 校验 +
鉴权链一次比对。不加多租户/面板/计费；受信代理提取复用既有。

**结论：做，L3（数据模型扩展，新增字段跨 config→httpauth→server 三层，需 ADR-013 钉四态语义）。**
明确不做：面板可视化管理、IP 维度计量/计费、多租户 B 端管控。

---

## 二、实测基线（2026-10-22）

| 事实 | 出处 |
|---|---|
| Key 无 IP 字段 | internal/httpauth/index.go:18-31 |
| APIKeyConfig 无 CIDR 字段 | cmd/server/config.go:418-430 |
| 可信 IP 提取已有（防 XFF 伪造） | internal/server/trusted_proxy.go:102 extractClientIP |
| withAuth 鉴权后插入约束的位置 | internal/server/handler.go:350-386 |
| normalize fail fast 语义（realm 校验范式） | cmd/server/config.go:994-996 |

---

## 三、需求项（Given-When-Then 验收）

### F-1 配置态字段贯通

- **Given** config.json `api_keys[].allowed_cidrs` 配 `["10.0.0.0/8","1.2.3.4"]`
- **When** 启动 normalize
- **Then** `APIKeyConfig.AllowedCIDRs` trim 落库；buildAuthSnapshot 转成 `httpauth.Key.AllowedCIDRs []*net.IPNet`；运行时 Key 携带名单

### F-2 有名单 fail-closed（GWT：拒不在名单者）

- **Given** Key 配了 `allowed_cidrs`
- **When** 请求来源真实 IP 不在名单
- **Then** withAuth 返回 403 + 错误码 `ip_not_allowed`，零上游消耗

### F-3 无名单行为不变（GWT：空=不限）

- **Given** Key 省略 `allowed_cidrs`（nil）
- **When** 任意来源请求
- **Then** 恒放行（与改造前完全一致），不是"未配置即拒绝"

### F-4 受信代理取真实 IP（复用 trusted_proxy）

- **Given** 配了受信代理且 RemoteAddr 命中
- **When** XFF 为追加语义 `伪造段, 真实客户端`
- **Then** 取首个非受信段作为真实 IP 做名单比对，客户端伪造的首段不被采信

### F-5 非法 CIDR fail fast

- **Given** `allowed_cidrs` 含 `"not-a-cidr"`
- **When** 启动 normalize
- **Then** 启动报错（对齐 realm 校验），不静默忽略、不降级为不限

---

## 四、边界（明写不做）

- 本期 **config.json 驱动**；面板可视化管理记入 summary「未完成」节。
- 非多租户 B 端管控；不做 IP 维度计费/配额。
- 不改报文字符串（R8）；不改受信代理判定本身。

## 五、验证方式

| 项 | 分级 | 实际验证 |
|---|---|---|
| httpauth.IPAllowed 单测 | L1 | 空=不限 / CIDR 命中拒 / 裸 IP / 端口剥除 / nil 条目跳过 |
| server withAuth 门禁 | L1 | fail-closed 403 / 空放行 / 受信代理真实 IP |
| config normalize | L1 | 非法 CIDR fail fast / 合法 trim 落库 / 省略=nil |
| go test ./... | L1 | 20 ok + 3 no test files 基线口径 |

## 六、未完成 / 待补（诚实清单）

- [ ] 面板可视化编辑 allowed_cidrs（本期 config.json 手填）。
- [ ] 浏览器实机联 requires 可交互会话；本轮到单测+编译级证据。
