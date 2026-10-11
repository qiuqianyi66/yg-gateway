---
last_updated: 2026-10-11
status: 待确认
owner: YG
modification_level: L2（业务逻辑 / 入站边界）
complexity: B 模块级
---

# YG Gateway 入站边界与路由安全加固 — 需求分析（PRD 级）

> 十阶段流水线阶段 1 output。基线：2.0.0，出站链路已硬编码、transport 连接层已加固，
> 本次聚焦**入站路由边界**。
> 调研来源：LMDeploy SSRF（CVE-2026-63764）、LiteLLM SQLi（CVE-2026-42208）、
> ProjectDiscovery「Abusing Reverse Proxies」、Kubeflow 未鉴权 SSRF（GHSA-gqww-5pj5-8fq7）。

---

## 一、范围边界

### 做什么

1. 入站请求体加**大小上限**（F-001）：`/v1/chat/completions` 的 body 目前 `io.ReadAll`
   无 `MaxBytesReader`，超大请求体可打爆内存（DoS）。
2. 出站透传的客户端 IP 加**受信任代理门槛**（F-002）：`ExtractClientIP` 无条件取
   `X-Forwarded-For` 首段并透传上游，若网关前面没有剥离伪造头的反代，攻击者
   可伪造 `X-Forwarded-For` 污染上游风控/计费（ProjectDiscovery 反代滥用向量）。
3. 只读端点加**轻量限流**（F-003）：`/v1/models`、`/status` 等 GET 端点无任何频控，
   可被刷，每次 `/v1/models` 失效都可能放大为上游探测。
4. 建立入站边界的安全回归测试，钉住上述三条（F-004）。

### 不做什么（明确排除）

1. **不动出站 base / 路径**：base 已硬编码（`copilot.tencent.com`），路径全常量拼接，
   唯一用户可控片段 `taskCode` 已 `PathEscape`——无 SSRF 面，不改。
2. **不动 transport 连接层**：禁 h2 / TLS 超时 / keepalive / 半死池清理已到位，不重做。
3. **不动鉴权**：`api_key` 走 `httpauth` 常量时间比较，已正确。
4. **不做 WAF / 全量反代**：本项目是个人网关，不引入额外中间件依赖。
5. **不改上游协议报文**（R8 红线）：任何加固都不得改变出站报文的既有形态。

---

## 二、需求（F-编号）

| F-编号 | 需求 | 优先级 |
|---|---|---|
| F-001 | `/v1/chat/completions` 入站 body 加 `http.MaxBytesReader`，超限返回明确错误 | P0 |
| F-002 | 客户端 IP 透传增加「受信任代理」判定：默认不信任 XFF，仅当显式配置受信任时才信任 | P0 |
| F-003 | `/v1/models`、`/status` 等只读端点加轻量请求频控（令牌桶/滑窗，进程内） | P1 |
| F-004 | 三条加固各配一个有牙齿的回归测试 | P0 |

### 验收标准（Given-When-Then）

- **F-001**：Given body 超过上限 / When 发送超大请求体 / Then 返回 413（或明确 4xx），
  且**不**把完整 body 读进内存（`MaxBytesReader` 在读取时截断）
- **F-002**：Given 攻击者伪造 `X-Forwarded-For: 169.254.169.254` / When 网关未配置
  受信任代理 / Then 该伪造 IP **不被采信也不被透传**上游（回落 TCP 对端或忽略）
- **F-003**：Given 短窗口内高频请求只读端点 / When 超过阈值 / Then 返回 429，
  且不影响 `/v1/chat/completions` 正常流量
- **F-004**：Given 注入对应 bug / When 跑回归测试 / Then 必须 FAIL

### Spec 门禁

1. 单切片 ≤100 行。
2. 每条 F 编号映射 ≥1 测试（L2 级强制）。
3. 改动不改上游报文形态（R8），需在测试里断言出站头/body 与改动前一致（F-002 之外的部分）。

---

## 三、非功能约束

- **性能**：MaxBytesReader 与限流均为 O(1) 级开销，不引入额外分配；限流状态进程内、重启清零
- **安全**：限流不泄漏账号信息；错误响应不暴露内部实现细节
- **兼容**：F-002 需默认**不改变**现有直连部署的行为——默认不信任 XFF 会改变
  「直连时 XFF 本就没有」的场景为无害；「套反代」场景需新增配置项开启信任
- **成本**：零新依赖（用 stdlib `net/http` 的 `MaxBytesReader` + 进程内 `sync.Mutex`
  + 时间戳滑窗自写限流器；不引 `golang.org/x/time/rate`——项目现仅 redis 一个直接
  依赖，保持单文件自包含哲学，个人网关的限流语义极简，自写 20 行足够且可测试）

---

## 四、影响分析

- **影响面**：`internal/server/handler.go`（F-001、F-003）、`internal/upstream/headers.go`
  （F-002）、`internal/server/config.go` 或 livecfg（F-002/F-003 的配置项）、测试
- **承重墙**：❌ 不触及选号 / 冷却 / 熔断 / 凭证链路；F-002 触及出站头注入但仅加
  门槛不改报文形态（R8 安全）
- **契约变化**：F-001 可能新增 413 响应语义（客户端需能识别）；F-002 新增配置项
  `upstream.trusted_proxy` / `passthrough_ip_trusted`（默认关）；F-003 新增 429
- **回滚路径**：二进制备份 + git 回退；配置项默认值即旧行为

---

## 五、决策点（D-编号）

| D-编号 | 决策 | 选项 | 建议 | 状态 |
|---|---|---|---|---|
| D-001 | body 上限取值 | A 固定 64MB / B 可配置（默认 64MB） | **B**：模型上下文增长快，硬编码会反复改；默认 64MB 足够大不误伤 | 待拍板 |
| D-002 | 超限响应码 | A 413 Payload Too Large / B 400 | **A**：413 语义精确，客户端可据此调整 | 待拍板 |
| D-003 | XFF 信任默认值 | A 默认不信任（需显式开）/ B 默认信任（现状） | **A**：安全默认（secure by default），直连部署无影响，套反代者显式开 | 待拍板 |
| D-004 | 限流粒度 | A 按来源 IP / B 按 api_key / C 全局 | **C + 可选 A**：个人网关全局令牌桶最简单；按 IP 需先解决 XFF 信任（依赖 F-002） | 待拍板 |
| D-005 | 限流实现 | A 自写 stdlib 令牌桶/滑窗 / B 引 golang.org/x/time/rate | **A**：个人网关语义极简（全局 + 按 IP 两档），自写 20 行可测、零依赖；x/time/rate 引入新模块违背「单文件自包含」 | 待拍板 |
