# YG Gateway

自托管的 **OpenAI 兼容反向代理网关**：把腾讯 CodeBuddy 账号池包装成统一的 `/v1/chat/completions` 服务，附带 Web 管理面板与全自动养号。MIT 协议，版权声明见 [LICENSE](LICENSE)。

---

## 为什么选 YG Gateway

同类项目不少（[workbuddy-manager](https://github.com/ithtelab/workbuddy-manager)、[workbuddy2api-hub](https://github.com/ardeyouxipianyi/workbuddy2api-hub)、[ARP](https://github.com/KaiXuanSama/ARP)…），它们拼的是**多租户派发、成本优化、功能广度**。YG Gateway 走的是另一条路，四个它们普遍没做透的点：

1. **单二进制零依赖**：面板 JS/CSS `go:embed` 进一个 ~10MB 的 exe，拷到任意机器直接跑，**不需要 Node / Python / Docker**。五平台二进制直接下。
2. **养号友好型选号**：快过期积分优先消耗、成本分层、积分保底、全自动养号（签到/活跃/旅行/保活/成长任务一键完成）——目标是**账号活得久、积分不浪费**，而不是单纯压成本。
3. **安全底座实打实加固**：禁 HTTP/2、入站 body 上限、受信任代理防伪造 XFF、只读端点限流、Windows 凭证 ACL 加固、Go 1.27.2（零 stdlib CVE）。
4. **公开可审计的工程治理**：仓库带 `.harness/` 治理体系（十阶段流程 + 红线 + 变更追踪），每个改动可追溯。

**一句话定位**：*一个二进制、零依赖、养号友好的 CodeBuddy 网关——把"部署省心"和"账号活得久"两件事同时做对。*

完整对比（含诚实劣势与路线图）见 [docs/COMPETITIVE-ANALYSIS.md](docs/COMPETITIVE-ANALYSIS.md)。

---

## 构建要求

> **Go 版本不是小事**：1.27.0 及更早带 9 个标准库漏洞（GO-2026-6603/6605/6607/6608/6610-6613/6617，覆盖 `net/http`、HTTP/2 实现与 `crypto/tls`），修复版本正是 1.27.2。对一个整天发 HTTPS 出站请求的网关，这处在最要害的位置。**构建必须 ≥1.27.2。**

---

## 快速开始

### 环境要求

- Go **≥ 1.27.2**（源码构建）
- 一个或多个 CodeBuddy 账号（面板 OAuth 登录）
- 可选：Docker（容器化部署）

### 源码构建（推荐本机）

```powershell
# Windows
$env:CGO_ENABLED = '0'
go build -trimpath -ldflags="-s -w" -o yg-gateway.exe ./cmd/server
.\yg-gateway.exe -config config.json
```

```bash
# Linux / macOS
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o yg-gateway ./cmd/server
./yg-gateway -config config.json
```

首次启动会自动生成 `config.json`（含 `crypto/rand` 随机 `api_key`，日志打印一次）。

### Docker

```bash
cp config.example.json config.json
docker compose up -d --build
curl -s http://localhost:7863/healthz
# {"healthy":2,"total":2,"service":"yg-gateway"}
```

### 添加账号

打开 `http://127.0.0.1:7863/panel/` → 右上角「添加账号」→ 浏览器内完成 OAuth → 凭证落盘并**热加载进池，免重启**。

### 接客户端

```
Base URL: http://127.0.0.1:7863/v1
API Key:  config.json 里的 api_key
```

任何 OpenAI 兼容客户端填这两项即可，零改造接入。

---

## 安全基线（本机部署必读）

### 1. 凭证文件权限（Windows 特有坑）

`auths/*.json` 存**明文 accessToken / refreshToken**。Go 的 `0o600` 在 **Windows 上不改写继承 ACL**，新建的凭证默认对 `BUILTIN\Users`（本机所有登录用户）可读。

**每次加完账号都要跑一次**：

```powershell
powershell -ExecutionPolicy Bypass -File .\lock-auths.ps1
```

（用 `icacls` 而非 `Set-Acl`：后者在对象带 SACL 时需要 `SeSecurityPrivilege`，非提升进程会报 `PrivilegeNotHeld`。）

> ⚠️ **这是最容易忘的一步**：面板新加的账号会重新带上继承 ACL，即使之前跑过脚本。
> 用下面这条确认当前状态（应只剩你自己 + `SYSTEM`）：
>
> ```powershell
> (Get-Acl .\auths\*.json).AccessToString
> ```
>
> 若输出里出现 `BUILTIN\Users` 或 `Authenticated Users`，说明没加固——此时本机任意
> 登录用户都能读到明文 token 并冒用账号。

### 2. 只监听本机

`listen` 保持 `127.0.0.1:7863`。默认的 `:7863` 等同 `0.0.0.0`，会暴露到局域网。

### 3. 必设 api_key

为空 = 完全不鉴权直接放行。

### 4. 公网部署

服务本身只提供明文 HTTP。挂公网**必须**前置 HTTPS 反代（Nginx / Caddy）并配访问限制。

### 5. 构建工具链

用 `go version` 确认 ≥1.27.2；定期跑：

```bash
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

---

## 调度可靠性配置（可选）

两个默认关闭/保守的参数，按需开启。都在面板「配置」页可改，**热生效、无需重启**。

### `pool.global_max_in_flight`（默认 `0` = 不限）

全网关在途请求总数上限。超限**立即返回 429 + `Retry-After`**（不排队）。

上游变慢时请求会堆积（每个都占着 goroutine 与连接等超时），这是防雪崩的一道闸。
**默认 0 = 不限**（保持旧行为），推荐值 ≈ `账号数 × max_in_flight × 1.5`：

```json
"pool": { "max_in_flight": 3, "global_max_in_flight": 18 }
```

触顶情况可在 `/status` 的 `global_in_flight` 字段观察。

### `pool.breaker_probe_gap`（默认 `"30s"`）

熔断到期后的 **half-open 试探间隔**。熔断号到期后不再一次性全量放回，而是每该间隔
至多放行 1 次试探：

- 试探**成功** → 全量恢复（清 `retryCount`，不再限速）
- 试探**失败** → 重新熔断，且退避指数 +1（下次禁得更久）

这避免了「到期瞬间洪峰把还是坏的号二次打挂」——二次熔断会让它多禁一个指数周期
（最长 6h）。设 `"0"` 关闭（退化为到期即全量，与旧行为一致）。

### 延迟分位数观测

`/status` 的 `latency` 段给出**全局 + 按模型**的 P50/P90/P95/P99：

```json
"latency": {
  "global": { "count": 128, "p50_ms": 1750, "p95_ms": 2500, "p99_ms": 30000 },
  "by_model": { "glm-5.2": { "p50_ms": 1750, "count": 128 } }
}
```

均值会被大量快请求稀释（一个 60s 超时在 1000 个 50ms 请求里几乎看不见），
分位数才能反映真实体感。自实现固定分桶（`internal/metrics`），零新依赖；
重启归零（是近期健康度，不是历史账本）。

---

## 已加固 / 已审计项

| 项 | 状态 |
|---|---|
| 鉴权 | 未授权访问 `/panel/api/*` 返回 401 |
| 密钥比较 | SHA-256 摘要 + `subtle.ConstantTimeCompare`，常量时间，无计时侧信道 |
| 路径穿越 | UID 白名单 `[A-Za-z0-9_-]`，`..%2F` / `....//` 等均返回 404 |
| 安全响应头 | CSP（`default-src 'none'`）/ nosniff / DENY / no-referrer / COOP / CORP |
| 日志脱敏 | 归档 JSONL 不含提示词、响应正文、Authorization；`state.json` 仅用量统计 |
| XSS | `esc()` 转义 `& < > " '` 五字符；渲染函数实测 4 种 payload 注入均安全 |
| 遥测 / 回连 | **无**——无版本检查、无激活校验、无埋点，唯一外部依赖是可选 Redis |
| 依赖 | 仅 5 个模块（1 直接 + 4 传递，全部来自可选的 go-redis），`govulncheck` 零告警 |

---

## 核心能力

| 能力 | 说明 |
|---|---|
| 多账号池 | 快过期积分加权 ×3、实测成本分层、加权随机选号、防惊群 |
| 熔断与冷却 | 402 硬冷却至次日 04:00、429 软冷却 600s 起指数退避（封顶 2h）、连续失败熔断 + **half-open 试探**（到期后限速放行 1 次，避免洪峰二次打挂）、在途租约限流 |
| 全局准入 | `pool.global_max_in_flight` 在途总数上限，超限立即 429 + `Retry-After`（防上游慢时请求堆积） |
| 会话粘性 | 同一会话绑定同一账号，TTL 滚动续期，失败自动解绑，可镜像 Redis |
| 全自动养号 | 签到 09/21、活跃上报 10、猫猫旅行 09/21、token 保活 22、夜猫子 23，五类独立开关 |
| 连登管家 | 签到末尾自动兑换已解锁档位并抽完抽奖次数 |
| 成长任务 | 17/18 个任务纯 API 一键完成，新号一轮约 +1950 积分 +78 能量 |
| 积分保底 | `pool.credit_floor` 防止收费模型把余额打穿 |
| 提示词体系 | 网关提示词替换客户端 system，从源头消灭内容审核误报；`passthrough` 遇拦截自动降级重试 |
| 原生工具调用还原 | DeepSeek 系标记文本还原为结构化 `tool_calls` |
| 多 API Key | 每把 Key 带 `id` / `models` 白名单 / `realm` 约束；摘要前缀索引 + 常量时间比较；面板可视化管理、热生效 |
| 面板 | 八视图：账号池 / 用量 / 积分构成 / 任务中心 / 模型档位 / API Key / 配置（热生效）/ 运行日志 |
| 用量归因 | 按域 / 账号 / 模型 / **下游 Key** 四个维度聚合，含积分扣除与缓存命中率 |
| 延迟观测 | 全局 + 按模型的 P50/P90/P95/P99（自实现固定分桶，零新依赖）；均值看不见的长尾由此可见 |
| 导出 | 请求记录 / 用量明细 / 积分扣除可一键导出 CSV（带 BOM，Excel 直接打开） |

---

## 进阶配置（小团队自用刚需）

### 模型映射 `model_aliases`

客户端写短名，网关转发前自动替换成上游实际模型名：

```json
"model_aliases": {
  "glm": "glm-5.3-flash",
  "ds": "deepseek-v4-flash"
}
```

只影响出站 body；选号 / 白名单 / 用量统计仍按客户端原始名，观测口径不分裂。热生效。

### Per-Key 精细约束

每把 API Key 可单独配置（面板「API Key」页直接改，热生效）：

| 字段 | 说明 |
|---|---|
| `models` | 模型白名单，支持通配符：`glm-*` 放行整个 GLM 族，`*` 放行全部 |
| `rpm` | 每分钟最大请求数，超限 429；0 = 不限 |
| `monthly_token_quota` | 当月 token 硬上限（跨重启持久化，跨月自动重置），超限 429；0 = 不限 |
| `group` | 账号池分组标签：该 Key 只能用同组账号（在 auth 文件里写 `"group": "team-a"`） |

### 异常告警 Webhook

```json
"alerting": {
  "webhook_url": "https://sctapi.ftqq.com/SENDKEY.send",
  "min_credit_threshold": 100,
  "dedup_minutes": 60
}
```

账号余额告急 / 被禁用 / 会话失效 / 触发熔断时 POST 通用 JSON 到 webhook，Server酱 / TG bot / 飞书自定义机器人直接接。

### 备用上游级联 `fallback`

CodeBuddy 账号池全挂时，自动把请求转发到独立 OpenAI 兼容端点：

```json
"fallback": {
  "enabled": true,
  "base_url": "https://api.deepseek.com/v1",
  "api_key": "sk-xxx",
  "models": ["deepseek-v4-flash"],
  "timeout_seconds": 120
}
```

只对白名单里的模型生效；SSE stream 原样透传。不配 = 完全关闭。

---

## 开发

```bash
go build ./...
go vet ./...
go test ./...        # 完整测试套件（20 包：17 有测试 + 3 个 CLI 无测试）
gofmt -l ./cmd ./internal
node --check internal/panel/app.js
```

前端（`internal/panel/app.js`、`index.html`）经 `//go:embed` 打进二进制，**改动后必须重新编译才生效**。

### 构建环境注意

- `GOPROXY` 需设为 `https://goproxy.cn,direct`（本机 `proxy.golang.org` 不通）
- 交叉编译用 `GOOS` / `GOARCH`，`CGO_ENABLED=0`

---

## 免责声明

本项目是非官方网关，使用 CodeBuddy 账号作为上游，**仅限本人授权账号、本机或私有环境使用**。作者不对账号封禁、条款违约或任何使用结果负责。上游 CodeBuddy 属腾讯系商业产品，请遵守其服务条款与所在地法律。

## License

[MIT](LICENSE)。再分发时请保留原始出处声明。
