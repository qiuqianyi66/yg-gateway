# YG Gateway

自托管的 **OpenAI 兼容反向代理网关**：把腾讯 CodeBuddy 账号池包装成统一的 `/v1/chat/completions` 服务，附带 Web 管理面板与全自动养号。

本项目从 [linguo2625469/workbuddy2api-panel](https://github.com/linguo2625469/workbuddy2api-panel) v1.13.0 独立而来，此后自行演进、不再跟随上游。原始项目链：`Sliverkiss/workbuddy2api`（已删库）→ `linguo2625469/workbuddy2api-panel` → 本仓库。MIT 协议，版权声明见 [LICENSE](LICENSE)。

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

## 与上游的关系

| | 说明 |
|---|---|
| **继承了什么** | 全部底层能力：账号池调度、错误分类、提示词体系、出站指纹、成长任务逆向、面板本体 |
| **改了什么** | module 名、产品身份标识、版本号起点、构建工具链、部署命名 |
| **没改什么** | 上游协议数据（`wb2api-<ms>` 形式的 conversationId、`chatcmpl-wb2api` 哨兵）——它们是发给上游的报文内容，改名无收益且有风控风险 |
| **是否同步上游** | 不再自动同步。上游有价值的修复会手工挑选合入 |

### 独立化改动清单

| 类别 | 改前 | 改后 |
|---|---|---|
| Go module | `github.com/linguo2625469/workbuddy2api-panel` | `yg-gateway` |
| `/healthz` service 字段 · `X-Service` 头 | `workbuddy2api` | `yg-gateway` |
| 启动日志前缀 | `workbuddy2api listening on ...` | `yg-gateway listening on ...` |
| 面板标题 / 品牌 | `WorkBuddy2API · 控制台` | `YG Gateway · 控制台` |
| 版本号起点 | `1.13.0-panel` | `2.0.0` |
| 二进制名 | `wb2api[.exe]` | `yg-gateway[.exe]` |
| 容器名 | `workbuddy2api` | `yg-gateway` |
| Redis key 前缀 | `wb2api:` | `yg-gateway:` |
| 面板 localStorage key | `wb2api.key` / `wb2api.theme` | `yg-gateway.key` / `yg-gateway.theme` |
| Go 构建版本 | 1.22.5（含 9 个标准库漏洞） | **1.27.2**（`govulncheck` 零告警） |
| 到期提醒卡片 | 初始 `hidden` + 仅点「检查」才渲染 → 死锁不可达 | 默认可见 + overview 快照即时渲染（零上游请求） |

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
| 依赖 | 仅 12 个模块，`govulncheck` 零告警 |

---

## 核心能力

| 能力 | 说明 |
|---|---|
| 多账号池 | 快过期积分加权 ×3、实测成本分层、加权随机选号、防惊群 |
| 熔断与冷却 | 402 硬冷却至次日 04:00、429 软冷却 600s 起指数退避（封顶 2h）、连续失败熔断、在途租约限流 |
| 会话粘性 | 同一会话绑定同一账号，TTL 滚动续期，失败自动解绑，可镜像 Redis |
| 全自动养号 | 签到 09/21、活跃上报 10、猫猫旅行 09/21、token 保活 22、夜猫子 23，五类独立开关 |
| 连登管家 | 签到末尾自动兑换已解锁档位并抽完抽奖次数 |
| 成长任务 | 17/18 个任务纯 API 一键完成，新号一轮约 +1950 积分 +78 能量 |
| 积分保底 | `pool.credit_floor` 防止收费模型把余额打穿 |
| 提示词体系 | 网关提示词替换客户端 system，从源头消灭内容审核误报；`passthrough` 遇拦截自动降级重试 |
| 原生工具调用还原 | DeepSeek 系标记文本还原为结构化 `tool_calls`（上游 PR #123） |
| 面板 | 七视图：账号池 / 用量 / 积分构成 / 任务中心 / 模型档位 / 配置（热生效）/ 运行日志 |

---

## 开发

```bash
go build ./...
go vet ./...
go test ./...        # 完整测试套件（19 包）
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
