# ADR-013：Key 级来源 IP 白名单（allowed_cidrs）

- 状态：已采纳（2026-10-22）
- 对应需求：`docs/prd/REQ-key-ip-whitelist-2026-10-22.md`（R-108，P2）
- 影响包：`internal/httpauth`（Key 新字段 + IPAllowed）、`cmd/server`（APIKeyConfig + normalize + buildAuthSnapshot）、`internal/server`（withAuth 门禁）

## 背景

`httpauth.Key` 既有约束是维度正交的：Models（模型白名单）、Realm（版本域）、RPM、
MonthlyTokenQuota、Group（账号池组）。**唯独没有"来源 IP"这一维**。泄漏的 Key 在任意
网络下可用，现有 whitelist（模型 / 受信代理 / 国家）都不是这个语义。

真实来源 IP 的可信提取已由 `trusted_proxy.go` 解决（受信代理追加语义下从右往左取首个
非受信段，防 XFF 伪造）。本 ADR 只需决定"新字段的四态语义与拒绝语义"。

## 决策

**D-130 字段位置与形态。** `APIKeyConfig.allowed_cidrs []string`（配置态字符串），
`httpauth.Key.AllowedCIDRs []*net.IPNet`（运行时预解析）。运行时存 `*net.IPNet` 而非
字符串，避免每请求重复 ParseCIDR；配置态保持人类可写。

**D-131 四态语义：省略 = 不限，非空 = fail-closed。**
- `allowed_cidrs` 键省略 / nil → **不限来源**（向后兼容，不是"未配置即拒绝"）。
- 非空 → fail-closed：真实来源 IP 不命中任一条 CIDR 即拒。
- 与 `Models` 的三态（nil=不限 / 非nil空=全拒）**刻意不同**：IP 白名单不提供"空切片=全拒"
  这个危险中间态——管理员要么不限制，要么给一份明确名单，没有手滑成"全员拒绝"的机会。
  空切片经 normalize 后等价于 nil（不限）。

**D-132 拒绝语义与错误分类。** 不命中返回 **403 + `ip_not_allowed`**，与 model/realm
约束的 400（realm_mismatch / model_not_allowed）区分：400 是"请求格式/字段错"，403 是
"已鉴权但网络来源被拒"——语义上更接近防火墙拒绝，便于监控与客户端区分处理。

**D-133 真实 IP 来源唯一复用 trusted_proxy。** withAuth 里调用
`extractClientIP(h.cfg.TrustedProxy, r)` 取真实 IP，**不重复实现 XFF 解析**。未配置受信
代理时回落 TCP 对端 RemoteAddr（直连部署安全默认）。

**D-134 非法 CIDR 启动 fail fast。** normalizeAPIKeys 逐条 `net.ParseCIDR`（裸 IP 按
ParseIP 接受为 /32），非法即报错，对齐 realm 校验语义——禁止"配错名单却当成不限放行"。

**D-135 热生效。** allowed_cidrs 经 buildAuthSnapshot 进入 livecfg 的 AuthIndex，
随多 Key 配置热重载，无需重启（不是装配期字段）。

## 后果

正面：泄漏 Key 可被网络位置限制在已知出口；纯新增字段，旧 config 零迁移（省略=不限）。
负面/边界：
- 不做面板可视化（本期 config.json 手填）；不做 IP 维度计费。
- 客户端在多层 NAT/移动网络下出口 IP 漂移可能被误拒——这是 fail-closed 的固有代价，
  文档提示管理员用 /32 单主机或收敛网段。
- httpauth 新增 `net` import（标准库，不新增 internal 跨包方向，仍是纯叶子包）。
