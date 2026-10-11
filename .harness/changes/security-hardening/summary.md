# security-hardening 变更记录

> 目录：`.harness/changes/security-hardening/`，与 git 分支 `feature/security-hardening` 同名。
> **无数据库迁移**：状态落 `data/state.json`（文件态），无 SQL 迁移。

## 一、变更概览

| 项 | 内容 |
|---|---|
| 功能名 | security-hardening（入站边界与路由安全加固） |
| 分支 | `feature/security-hardening`（基于 main 9a3349c） |
| 需求来源 | `docs/prd/REQ-yg-gateway-security-hardening-2026-10-11.md`（F-001~F-004，D-001~D-005） |
| 类型 | feat + test |
| 涉及范围 | `internal/server`（handler/trusted_proxy/ratelimit + 测试）、`cmd/server`（config/main）、`config.example.json` |

## 二、需求与方案

### 需求描述

调研了四类同型网关/反代的安全成果（LMDeploy SSRF CVE-2026-63764、LiteLLM SQLi
CVE-2026-42208、ProjectDiscovery「Abusing Reverse Proxies」、Kubeflow 未鉴权 SSRF），
对照本项目现状，确认**出站链路已硬编码、transport 连接层已加固**（禁 h2/TLS 超时/
keepalive/半死池清理），真正的薄弱点在**入站边界**三处：

1. **F-001 请求体无上限**：`io.ReadAll(r.Body)` 无 `MaxBytesReader`，超大 body 可打爆内存。
2. **F-002 伪造 XFF 被无条件采信**：`ExtractClientIP` 取 `X-Forwarded-For` 首段并透传
   上游，直连部署下攻击者可伪造 XFF 污染上游风控/计费。
3. **F-003 只读端点无频控**：`/v1/models`、`/status` 可被刷，放大上游探测成本。

### 验收标准（Given-When-Then）

- **F-001**：Given body 超 `server.max_body_bytes` / When 发送超大 body / Then 413 +
  `request_too_large`，且不触发上游调用（读取阶段即截断）
- **F-002**：Given 伪造 `X-Forwarded-For` / When 来源不在 `security.trusted_proxy` /
  Then 不采信、回落 TCP 对端（RemoteAddr）
- **F-003**：Given 短窗高频请求 `/v1/models` `/status` / When 超 `read_only_rate_limit.limit` /
  Then 429 + `Retry-After`
- **F-004**：Given 注入对应 bug / When 跑回归 / Then FAIL

### 技术方案

**F-001 请求体上限**：`Config.MaxBodyBytes`（`server.max_body_bytes`，缺省 0 = 不设限）。
`chatCompletions` 用 `http.MaxBytesReader` 截断，超限返回 413（`*http.MaxBytesError`
精确识别），半截 JSON 不喂上游。

**F-002 受信任代理**：`TrustedProxyMatcher`（精确 IP + CIDR），`extractClientIP` 闸门：
来源命中受信任集合才采信 XFF，否则回落 `RemoteAddr`。直连部署不配置 = 安全默认。
日志侧 `captureClientInfo` 同步走该闸门，两条口径一致。

**F-003 只读端点限流**：自写固定窗口计数器（`sync.Mutex` + 时间戳，20 行），不引
`golang.org/x/time/rate`（保持单文件自包含 + 最少依赖）。`rateLimited` 中间件挂在
`/v1/models` `/status`，`/healthz` 保持不限流（探活友好），`/v1/chat/completions`
不动（已有软冷却/熔断体系）。

**配置**：三个字段都是装配期固定（改需重启），列入 `restartRequiredFields` 提示面板。

### 刻意不改（红线约束）

- **R8**：出站 base 硬编码、路径常量拼接、`taskCode` 已 `PathEscape`——无 SSRF 面，不动。
- **transport 连接层**：已加固（禁 h2 等），不重做。
- **鉴权**：`httpauth` 常量时间比较已正确，不动。
- **零新依赖**：限流自写，不引 x/time/rate。

## 三、影响分析

- **影响面**：`internal/server`（新增 trusted_proxy.go / ratelimit.go，改 handler.go /
  logging.go）、`cmd/server`（config.go / main.go）、`config.example.json`、测试
- **承重墙**：❌ 不触及选号 / 冷却 / 熔断 / 凭证链路；F-002 只加采信门槛不改报文形态
- **契约变化**：F-001 可能新增 413；F-002 新增 `security.trusted_proxy`（默认关 =
  旧行为）；F-003 新增 429；`logging_test.go` 里 `captureClientInfo` 的 XFF 行为
  **语义变更**（未配置受信任代理时不再采信 XFF），已更新对应测试并新增反证用例
- **回滚路径**：二进制备份 `bin/yg-gateway.exe.v2.0.0-preharden`；配置默认值即旧行为

## 四、自检清单

- [x] `go build ./...` + `go vet ./...` 通过
- [x] `go test ./...` 16 包全绿（含新增 12 个 server 测试 + 2 个 config 测试）
- [x] 牙齿验证：三条加固各注入 bug（去 MaxBytesReader / 无条件信任 XFF / 限流恒放行）
      → 均 FAIL 并精确指出字段；还原 → PASS
- [x] 端到端：限流精确（前 60 放行 200，61-65 返回 429，且 `/status` 与 `/v1/models`
      共享额度被一起限流）；healthz 正常；启动日志无 config 报错
- [x] 部署：二进制备份 `bin/yg-gateway.exe.v2.0.0-preharden` 就位

## 五、Learning

根因一: 「无上限读 body」和「无条件信任 XFF」都不是显而易见的漏洞——它们各有一条
历史注释自我辩解（"对齐上游""反代后拿真实客户端"）。安全审计的价值恰恰在于**重新
质疑这些"一直如此"的决策**，因为部署形态变了（直连 vs 套反代），当年的合理就变成
今天的漏洞。

根因二: 限流/body 上限/代理信任必须**装配期固定**而非热改——热改成 0 等于把防护
静默关掉。把它们放进 `restartRequiredFields` 而不是 livecfg，是刻意的安全取舍。

根因三: 环境侧陷阱——新构建的 exe 在跨命令之间被实时防护删除，只有「构建+swap+启动」
放进同一条命令才能原子完成。排查时先看 Defender 实时防护，别急着怀疑代码。
