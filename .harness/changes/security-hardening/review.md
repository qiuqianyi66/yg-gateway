# security-hardening 代码评审

> 阶段 6 output。评审对象：`feature/security-hardening` 全量变更。
> 六维：正确性 / 性能 / 安全 / 一致性 / 可维护性 / 架构合规。

## 评审结论

**通过**。无 🔴 阻断，🟡 0 项。

## 一、正确性

| 项 | 结论 |
|---|---|
| F-001 MaxBytesReader | ✅ 超限返回 413 且 `*http.MaxBytesError` 精确识别；`err` 变量作用域正确（`var body []byte; var err error` 先声明后分支赋值） |
| F-002 matcher 解析 | ✅ 精确 IP + CIDR 混合、非法条目忽略、全非法回落 nil；`SplitHostPort` 处理 "IP:port" |
| F-002 extractClientIP | ✅ nil matcher → 不采信回落 RemoteAddr；命中 → 采信 XFF；未命中 → 回落。三态清晰 |
| F-003 限流 | ✅ 固定窗口 `allow` 原子计数（mu 锁）；窗口滑过整体重置；空 key 用 `\x00global` 隔离 |
| 限流挂载顺序 | ✅ withAuth 在外、rateLimited 在内——未鉴权者不消耗限流配额 |
| `/healthz` 不限流 | ✅ 有专门测试钉住（探活友好） |

## 二、性能

| 项 | 结论 |
|---|---|
| MaxBytesReader | ✅ 读取时截断，超大 body 不整体进内存（核心收益） |
| 限流 | ✅ 固定窗口 O(1)，map 计数；窗口滑过整体重建 map，无泄漏 |
| 无锁开销 | ✅ 限流器仅命中只读端点，chat 主路径零开销 |

## 三、安全

| 项 | 结论 |
|---|---|
| F-002 伪造 XFF | ✅ 直连部署默认不采信，消除上游风控/计费污染向量 |
| 牙齿验证 | ✅ 三条加固各注入 bug 均被测试精确抓到（含 F-002 注入「无条件信任 XFF」→ `extractClientIP(nil) = 169.254.169.254` 被抓） |
| 错误响应 | ✅ 413/429 均走 `writeOpenAIError`，不泄漏内部实现 |
| 配置 fail-fast | ✅ `limit>0` 但 window 空/非法 → 启动即报错，不静默关防护 |

## 四、一致性

| 项 | 结论 |
|---|---|
| IP 采集口径 | ✅ 日志侧 `captureClientInfo` 与 chat 侧 `extractClientIP` 同走受信任代理闸门，两条口径一致 |
| config 命名 | ✅ `server.max_body_bytes` / `security.trusted_proxy` / `security.read_only_rate_limit` 与既有命名风格一致 |
| 默认值 | ✅ 三个字段缺省均为旧行为（0/空/0），向后兼容 |

## 五、可维护性

| 项 | 结论 |
|---|---|
| 注释 | ✅ 每处改动写了"为什么"（含历史决策的重新质疑），与仓库讲 why 风格一致 |
| 死代码 | 🟢 `clientIPForLog` 生产路径已不再调用，但被测试引用（钉住旧回落语义），注释已标明「新增代码用 extractClientIP」 |
| 单一职责 | ✅ trusted_proxy.go / ratelimit.go 各管一域，边界清晰 |

## 六、架构合规

| 项 | 结论 |
|---|---|
| 依赖方向 | ✅ 未新增包间依赖；`extractClientIP` 复用 `upstream.ExtractClientIP`（单向 server→upstream） |
| 承重墙 | ✅ 未触及选号/冷却/熔断/凭证链路 |
| 红线 | ✅ R8（报文形态不动）、R14（Go 1.27.2）、R15（备份）均遵守 |
| 变更追踪 | ✅ `.harness/changes/security-hardening/` 已建 |

## 待办（不阻塞）

1. 🟢 `clientIPForLog` 是遗留死代码（仅测试引用）。可在后续清理时删除，或让它也接受
   matcher 统一语义——当前保留因为它钉住了「回落 RemoteAddr」的旧行为，删了要同步改测试。
2. 🟢 限流器是固定窗口（边界处允许瞬时 2× 突发）。对「防无脑刷取」够用；若未来要做
   精确配额（计费场景），再换令牌桶/滑窗。

## 七、独立 reviewer 二轮评审（subagent 结果与处置）

独立 reviewer（requesting-code-review 派发）复核 5ee072d，结论「可合入（有条件），
无 🔴」。4 条 🟡 的处置：

| # | reviewer 发现 | 处置 |
|---|---|---|
| 🟡#1 | MaxBodyBytes 默认 0=不限，DoS 对存量用户仍敞开 | **确认成立，已修**：改指针字段 `*int64`，nil（未配置）→ normalize 回落 64MB 安全默认；显式 0 → 不限（逃生门）；负值 fail fast |
| 🟡#2 | 受信任代理用「追加」语义拼 XFF（nginx $proxy_add_x_forwarded_for）时首段仍客户端可控，绕过 F-002 | **确认成立，已修**：`trustedClientIP` 从右往左跳过受信任代理、取第一个非受信任 IP；追加语义下取真实客户端，重写语义下无回归。牙齿验证：注入「取首段」→ 测试报 `extractClientIP(append) = 169.254.169.254` |
| 🟡#3 | rateLimiter.counts map 无逐 key 淘汰，单窗口 distinct IP 可撑大内存 | **部分成立，可接受**：窗口滑过即整体重建 map（`rl.counts = map[string]int{}`），内存随窗口长度有界。个人网关窗口 ≤ 分钟级，风险可忽略。记录不改 |
| 🟡#4 | 漏配 trusted_proxy 时，限流退化为全局单桶，单客户端可 429 所有人 | **评审有误，反驳**：`extractClientIP` 未配置 trusted_proxy 时回落 `RemoteAddr`（每客户端不同的 TCP 对端 IP），限流按真实对端计数，非全局单桶。真正退化为全局单桶的只有 `RemoteAddr` 为空/解析失败的极端兜底（`\x00global` 分支），属防御性极端路径 |

### 附带修正（reviewer 问题②）

`unit-test-ci` 技能硬编码「16 包」，与实况漂移（实际 `go list ./...` = 19 包，其中
15 个有测试 ok + 4 个 no test files）。已改为「包数随演进会变，不写死」。

### 附带补齐（reviewer 问题①：CI 门禁脱节）

`unit-test-ci` 技能承诺的 `go vet` + `govulncheck` + `node --check` 门禁，此前在真实
CI（go-binaries.yml）里只跑了 `go test ./...`——技能说了、CI 没做。已补上：
`go vet ./...` + `node --check internal/panel/app.js` + `govulncheck ./...`。
