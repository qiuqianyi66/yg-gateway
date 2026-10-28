<div align="center">

# YG Gateway

**一个二进制、零依赖、养号友好的 CodeBuddy 网关**
——把"部署省心"和"账号活得久"两件事同时做对。

[![License](https://img.shields.io/github/license/qiuqianyi66/yg-gateway)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/qiuqianyi66/yg-gateway)](go.mod)
[![Stars](https://img.shields.io/github/stars/qiuqianyi66/yg-gateway)](https://github.com/qiuqianyi66/yg-gateway)

自托管的 **OpenAI / Anthropic 兼容反向代理网关**：把腾讯 CodeBuddy 账号池包装成统一的 `/v1` 服务，
附带 Web 管理面板与全自动养号。任何 OpenAI 兼容客户端（Codex / Cursor / OpenWebUI / SDK）与
Claude Code 填两行配置即可接入，零改造。

**[界面速览](#界面速览) · [架构](#架构) · [核心能力](#核心能力) · [快速开始](#快速开始) · [安全基线](#安全基线部署必读) · [开发](#开发)**

</div>

---

## 界面速览

面板是单页九视图（账号池 / 用量 / 积分构成 / 任务中心 / 模型与档位 / API Key / 配置 / 运行日志 / 运维 Agent），
经 `go:embed` 打进二进制，启动后访问 `http://127.0.0.1:7863/panel/` 即可。

> 截图不随仓库分发：画面里会出现真实账号昵称与账号 UUID 片段，属个人信息。

## 为什么选 YG Gateway

同类项目不少（[workbuddy-manager](https://github.com/ithtelab/workbuddy-manager)、
[workbuddy2api-hub](https://github.com/ardeyouxipianyi/workbuddy2api-hub)、
[ARP](https://github.com/KaiXuanSama/ARP)…），它们拼的是**多租户派发、成本优化、功能广度**。
YG Gateway 走的是另一条路——四个它们普遍没做透的点：

<table>
  <tr>
    <td align="center" width="50%">
      <b>📦 单二进制零依赖</b><br/>
      面板 JS/CSS <code>go:embed</code> 进 ~10MB exe，拷到任意机器直接跑，<b>不需要 Node / Python / Docker</b>；Go ≥1.27.2 交叉编译即得五平台二进制
    </td>
    <td align="center" width="50%">
      <b>🔄 养号友好选号</b><br/>
      快过期积分优先、实测成本分层、积分保底、全自动养号（签到 / 活跃 / 旅行 / 保活 / 成长任务）——账号活得久、积分不浪费
    </td>
  </tr>
  <tr>
    <td align="center" width="50%">
      <b>🔒 安全底座加固</b><br/>
      禁 HTTP/2、入站 body 上限、受信任代理防伪造 XFF、Windows 凭证 ACL 加固、常量时间密钥比较、Go 1.27.2 零 stdlib CVE
    </td>
    <td align="center" width="50%">
      <b>📋 工程治理可审计</b><br/>
      <code>.harness/</code> 十阶段流程 + 红线 + 变更追踪，每个改动可追溯；23 个 Go 包（21 个有测试，2 个 CLI 无）全绿，前端 node 断言回归
    </td>
  </tr>
</table>

完整对比（含诚实劣势与路线图）见 [docs/COMPETITIVE-ANALYSIS.md](docs/COMPETITIVE-ANALYSIS.md)。

### 三护城河：同类项目普遍没做透的三件事

上面 2×2 是"能跑起来"的门槛。下面三件是 v3 深化期打穿的差异点——依据
[magpie 源码级深读](docs/reference/magpie-deep-read-2026-10-25.md) §二八维对比
（这是目前唯一逐行核过的同类项目；one-api / LiteLLM / CRS 未深读，状态 unknown，
不做否定断言）：

| 护城河 | 做法 | magpie 现状（唯一核过的对照） |
|---|---|---|
| **💰 实测成本分层（四态）** | 每个账号×每个模型跑一发带 credit 的调用，把「这个号对这个模型到底收不收费、收多少」从目录价/插件声明变成**实测 observation**；样本不足保留 unknown，绝不把"没测过"读成"免费" | ❌ 只有目录价 / 插件声明 / 用户自设，不实测 |
| **🩺 健康治理** | EWMA 成功率/延迟平滑 + 402/429 软硬冷却 + 连续失败熔断 + **half-open 自动试探**（到期限速放行 1 次，避免洪峰二次打挂坏号） | ❌ 无成功率/延迟指标，只手动 "Try now" |
| **🔍 决策溯源（pickReason）** | 每次选号落一个结构化理由（四路径之一），面板 `/status` 透出；配套 `internal/unknown/` registry 把"不知道"当一等公民登记（带 revision，推断不能消解推断） | ❌ 选完就完了，无 pickReason、无 unknown registry |

这三件事不写在 README 会被当成"又一个套壳网关"——它们是本项目养号叙事的证据链。
其他同类项目是否也做了，待深读后补；不补就是 unknown，不是"它们都没有"。

## 架构

```mermaid
flowchart LR
    subgraph 客户端
        C1[Codex / Cursor / OpenWebUI / SDK]
        C2[Claude Code]
    end
    C1 & C2 -->|OpenAI / Anthropic 协议| GW

    subgraph 网关 yg-gateway.exe
        GW[统一 /v1 入口<br/>鉴权 + 限流 + 配额 + 白名单]
        GW --> PICK[选号器<br/>成本分层 · 防惊群 · 粘性会话]
        PICK --> POOL[(CodeBuddy 账号池)]
        POOL --> WB[CodeBuddy 云端]
        GW --> ALERT{异常?}
        ALERT -->|余额告急 / 会话失效 / 被封| WH[Webhook 告警]
        GW -->|账号池全挂| FB[备用 OpenAI 兼容端点]
    end

    GW -.面板 go:embed.-> PANEL[Web 面板<br/>账号池 / 用量 / 任务中心 / Key / 配置]
    PANEL -.热生效.-> GW
```

- **鉴权**：SHA-256 摘要 + 常量时间比较；单 `api_key` 与多 `api_keys` 并存统一索引。
- **选号**：权重 = 积分 + 空闲加成 + 快过期加权，实测成本分层，硬过滤只留最优层；
  粘性会话把同一会话绑到同一账号，失败自动解绑。
- **热生效**：面板改配置 → 校验 → 落盘 → 重建鉴权/选号索引，无需重启。

## 核心能力

### 账号池与可靠性

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
| 运维 agent | 观察-建议闭环（容量告急 / 402-余额口径矛盾自动登记 unknown），面板可视，**默认关**（`opsagent.enabled`） |

### 模型与网关

| 能力 | 说明 |
|---|---|
| 模型映射 | `model_aliases` 客户端短名 → 上游实际名，只改出站 body，观测口径不分裂 |
| 提示词体系 | 网关提示词替换客户端 system，从源头消灭内容审核误报；`passthrough` 遇拦截自动降级重试 |
| 原生工具调用还原 | DeepSeek 系标记文本还原为结构化 `tool_calls` |
| 结构化输出 | `response_format` 原样透传（金样锚定）；2026-10-28 真机一发实测上游认 `json_object` 并返回合法 JSON（模型 deepseek-v4-flash，observation）；其余模型与 `json_schema` 形态未逐一实测（unknown） |
| Responses API | `POST /v1/responses`（Codex 默认协议）：input/instructions/tools 转换复用 chat 管线，非流式 response 对象 + 流式事件（response.created→completed），上游协议零改动；协议转换已测（fake upstream 端到端），真机 Codex 客户端未验证（unknown） |
| 备用上游级联 | CodeBuddy 全挂时自动转发到独立 OpenAI 兼容端点，SSE 透传，用量归因到 `realm=fallback` |
| 延迟观测 | 全局 + 按模型的 P50/P90/P95/P99（自实现固定分桶，零新依赖） |
| 用量归因 | 按域 / 账号 / 模型 / **下游 Key** 四维聚合，含积分扣除与缓存命中率 |
| 导出 | 请求记录 / 用量明细 / 积分扣除一键导出 CSV（带 BOM，Excel 直接打开） |

### 多 API Key 派发（面板可视化管理，热生效）

| 字段 | 说明 |
|---|---|
| `models` | 模型白名单，支持通配符：`glm-*` 放行整个 GLM 族，`*` 放行全部 |
| `realm` | 国内版 / 国际版 / 不限，约束这把 Key 只能用哪个版本的模型 |
| `rpm` | 每分钟最大请求数，超限 429；0 = 不限 |
| `monthly_token_quota` | 当月 token 硬上限（跨重启持久化，跨月自动重置），超限 429；0 = 不限 |
| `group` | 账号池分组：该 Key 只能用同组账号（auth 文件写 `"group": "team-a"`） |

创建 Key 后弹窗直接给出 **OpenAI / Anthropic 两段可复制的接入配置**，或一键下载
`yg-gateway-import.env` 落盘。明文仅创建时展示一次（服务端只存哈希）；万一弹窗误关，
卡片「接入配置」可回看完整配置，或「重置密钥」一键重新生成（旧 key 立即失效）。

## 快速开始

### 环境要求

- Go **≥ 1.27.2**（源码构建；更早版本带 9 个标准库 CVE，覆盖 `net/http` 与 HTTP/2）
- 一个或多个 CodeBuddy 账号（面板 OAuth 登录）
- 可选：Docker

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

首次启动自动生成 `config.json`（含 `crypto/rand` 随机 `api_key`，日志打印一次）。

### Docker

```bash
cp config.example.json config.json
docker compose up -d --build
curl -s http://localhost:7863/healthz
# {"healthy":2,"total":2,"service":"yg-gateway"}
```

### 添加账号

打开 `http://127.0.0.1:7863/panel/` → 右上角「添加账号」→ 浏览器内完成 OAuth →
凭证落盘并**热加载进池，免重启**。新账号会自动开始执行养号排程。

### 接入客户端

| 客户端 | Base URL | API Key |
|---|---|---|
| OpenAI 兼容（Codex / Cursor / OpenWebUI / SDK） | `http://127.0.0.1:7863/v1` | 面板创建的 Key 或 `config.json` 的 `api_key` |
| Anthropic 兼容（Claude Code） | `http://127.0.0.1:7863` | 同上（`ANTHROPIC_AUTH_TOKEN`） |

面板里创建 Key 后可直接复制/下载现成配置，模型列表由客户端自动发现（`GET /v1/models`）。

### 与 Magpie / CC Switch 配合

YG Gateway 把腾讯 CodeBuddy 账号池包装成一个 OpenAI / Anthropic 兼容端点；
**Magpie 和 CC Switch 管的是客户端侧的 provider 切换，两者正交，可叠加**：

- **Magpie**（通用 LLM 路由网关，支持多 provider / 多 agent 编排）：在它的
  provider 配置里加一个自定义 OpenAI 兼容端点，`base_url = http://127.0.0.1:7863/v1`，
  `api_key = 面板 Key`。这样 Magpie 看到的"一个稳定可用的上游"，背后是 YG Gateway
  在替你养 CodeBuddy 号、做选号与健康治理。
- **CC Switch**（Claude Code 配置切换器）：新增一个自定义 provider，
  `ANTHROPIC_BASE_URL = http://127.0.0.1:7863`，`ANTHROPIC_AUTH_TOKEN = 面板 Key`。
  切换到这一档，Claude Code 的请求就落到本网关的 CodeBuddy 池。

两者都只需要填两行（base_url + token），不需要改 YG Gateway 的代码或配置。

## 进阶配置

### 模型映射 `model_aliases`

```json
"model_aliases": {
  "glm": "glm-5.3-flash",
  "ds": "deepseek-v4-flash"
}
```

只影响出站 body；选号 / 白名单 / 用量统计仍按客户端原始名。热生效。

### 异常告警 Webhook

```json
"alerting": {
  "webhook_url": "https://sctapi.ftqq.com/SENDKEY.send",
  "min_credit_threshold": 100,
  "dedup_minutes": 60
}
```

账号余额告急 / 被禁用 / 会话失效 / 触发熔断时 POST 通用 JSON 到 webhook，
Server酱 / TG bot / 飞书自定义机器人直接接。

### 备用上游级联 `fallback`

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

## 调度可靠性（可选）

两个默认关闭/保守的参数，面板「配置」页可改，**热生效**：

- **`pool.global_max_in_flight`**（默认 `0` = 不限）：全网关在途请求总数上限，超限立即
  429 + `Retry-After`（不排队）。防上游慢时请求堆积，推荐 ≈ 账号数 × `max_in_flight` × 1.5。
- **`pool.breaker_probe_gap`**（默认 `"30s"`）：熔断到期后的 half-open 试探间隔，避免
  「到期瞬间洪峰把还是坏的号二次打挂」；设 `"0"` 关闭。

延迟分位数可在 `/status` 的 `latency` 段观察（全局 + 按模型 P50/P90/P95/P99）。

## 安全基线（部署必读）

1. **凭证文件权限（Windows 特有坑）**：`auths/*.json` 存明文 token，Go 的 `0o600` 在
   Windows 上不改写继承 ACL。**每次加完账号跑一次** `powershell -ExecutionPolicy Bypass -File .\lock-auths.ps1`
   （用 `icacls` 而非 `Set-Acl`）。确认当前状态：`(Get-Acl .\auths\*.json).AccessToString`
   应只剩你自己 + `SYSTEM`，出现 `BUILTIN\Users` 说明没加固。
   服务本身已内置自动收紧（F-101，2026-10-21）：Windows 上每次凭证写回后及启动时
   自动执行同语义收紧，脚本仅作手动兜底。
2. **只监听本机**：`listen` 保持 `127.0.0.1:7863`。默认的 `:7863` 等同 `0.0.0.0`，会暴露到局域网。
3. **必设 api_key**：为空 = 完全不鉴权直接放行。
4. **公网部署**：服务本身只提供明文 HTTP，挂公网**必须**前置 HTTPS 反代（Nginx / Caddy）并配访问限制。
5. **构建工具链**：`go version` 确认 ≥1.27.2；定期跑 `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`。

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

## 面板（九视图，go:embed 打进二进制）

| 视图 | 说明 |
|---|---|
| 账号池 | 余额 / 到期提醒 / 成功失败 / 在途 / Token，签到·旅行·活跃·保活·余额刷新一键触发 |
| 用量 | 按域 / 账号 / 模型 / Key 聚合，CSV 导出 |
| 积分构成 | 包内额度拆解与到期批次 |
| 任务中心 | 成长任务一键完成 + 每日自动执行 |
| 模型与档位 | 模型级限流锁池可视化 |
| API Key | 多 Key 增删改 / 启停 / 接入配置 / 重置密钥 |
| 配置 | 全字段热生效编辑 |
| 运行日志 | 环形缓冲实时日志 |
| 运维 Agent | opsagent 规则状态 / 建议列表（默认关，`opsagent.enabled` 开启） |

## 开发

```bash
go build ./...
go vet ./...
go test ./...        # 完整测试套件（23 包：21 有测试 + 2 个 CLI 无测试）
gofmt -l ./cmd ./internal
node --check internal/panel/app.js
```

前端（`internal/panel/app.js`、`index.html`）经 `//go:embed` 打进二进制，**改动后必须重新编译才生效**。
前端有 node + vm 纯函数断言（`internal/panel/frontend_test.go`），后端有鉴权隔离 / 配置合并 / 脱敏回归测试。

### 构建环境注意

- `GOPROXY` 需设为 `https://goproxy.cn,direct`（本机 `proxy.golang.org` 不通）
- 交叉编译用 `GOOS` / `GOARCH`，`CGO_ENABLED=0`

## 路线图

- 重置密钥时支持自定义新密钥（当前固定自动生成）
- ~~更多上游接入（除 CodeBuddy 外的一键对接模板）~~ ✅ 已实现：`fallback.endpoints` 列表按序级联（OpenAI 兼容端点接入模板，见 ADR-015）
- 面板多语言 / 主题完善

（完整对比与演进路线见 [docs/COMPETITIVE-ANALYSIS.md](docs/COMPETITIVE-ANALYSIS.md)。）

## 免责声明

本项目是非官方网关，使用 CodeBuddy 账号作为上游，**仅限本人授权账号、本机或私有环境使用**。
作者不对账号封禁、条款违约或任何使用结果负责。上游 CodeBuddy 属腾讯系商业产品，
请遵守其服务条款与所在地法律。

## License

[MIT](LICENSE)。再分发时请保留原始出处声明。
