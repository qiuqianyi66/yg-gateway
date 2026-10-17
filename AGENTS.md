# AGENTS.md — YG Gateway 仓库协作约束

> 新会话**第一件事读本文件**。它定义本项目是什么、红线在哪、以及对外暴露
> 事实时必须遵守的语义纪律。与 `.harness/rules/` 三份规范配合使用：
> 本文件是**顶层硬约束**，那三份是**实施细则**。冲突时以本文件为准。

---

## Product Direction（本项目是什么 / 不是什么）

**YG Gateway** 是一个自托管的 OpenAI 兼容反向代理网关：把腾讯 CodeBuddy
账号池包装成统一的 `/v1/chat/completions` 服务，附 Web 管理面板与全自动养号。

**四条差异化定位（写进 README，任何改动不得背离）**：

1. **单二进制零依赖**：面板 JS/CSS `go:embed` 进一个 ~10MB 的 exe，拷到任意
   机器直接跑，不需要 Node / Python / Docker。`go.mod` 直接依赖必须保持在个位数量级。
2. **养号友好型选号**：快过期积分优先消耗、实测成本分层、积分保底、全自动养号
   ——目标是**账号活得久、积分不浪费**，而不是单纯压成本。
3. **安全底座实打实加固**：禁 HTTP/2、入站 body 上限、受信任代理防伪造 XFF、
   只读端点限流、Windows 凭证 ACL 加固、常量时间密钥比较、Go ≥1.27.2。
4. **可审计治理**：`.harness/` 下 rules / wiki / changes / skills，每次改动
   都有 PRD + ADR + 变更追踪。

**明确不是什么**：
- 不是多租户计费 / 配额派发平台（不做充值、兑换码、OIDC、多语言）。
- 不是通用 AI 网关（不追求接所有 provider；上游就是 CodeBuddy + 一个备用源）。
- 不是一次性分析工具（是 7×24 常驻服务，稳定性优先于功能广度）。

---

## Boundary Contracts（硬红线，不可降级）

以下约束**上提为顶层硬约束**，评审时红线问题禁止降级合入。

### R8 — 上游协议数据不得改名

`wb2api-<ms>` 形式的 conversationId、`chatcmpl-wb2api` 哨兵、三段式 UA 指纹
（`WorkBuddy/<ver> ... CLI/<cliVer>`）属于**报文内容**，改名有风控风险。
任何"优化"这些字符串的需求一律拒绝并说明原因。

> 影响面：`internal/upstream/headers.go`、`internal/upstream/client.go`、
> `internal/upstream/useragent*`。

### 依赖方向必须向内流动

```
domain/contracts  ←  provider adapters  ←  application  ←  CLI/HTTP adapters
```

机器校验：`powershell -File scripts/check-boundaries.ps1`（退出码非 0 即违规）。
白名单是**实测生成**的，新增方向必须说明理由并更新脚本。

当前实测方向（冻结基线）：以 `scripts/check-boundaries.ps1` 的 `allow` 表为**唯一真相**
（18 个源包条目，由 `-Fix` 实测生成）。本文件**不复制**该表——手抄副本必然会漂移。

**禁止**：`pool → server`、`upstream → panel`、任何 internal 包反向依赖 `server`（`cmd` 除外）。

### 其他不可违反的红线（摘要，详见 `.harness/rules/编码规范.md`）

- **R1** 凭证不出 `auths/`；**R2** 日志脱敏（UID 只取前 8 位）
- **R5** 密钥比较走 `internal/httpauth`，禁 `==`
- **R6** 出站请求集中 `internal/upstream`，禁其他包直连上游
- **R9** goroutine 必须有回收路径；**R10** 错误不静默吞（禁 `_ =` 吞业务错误）
- **R14** 构建工具链 ≥ Go 1.27.2；**R15** 二进制替换前备份

---

## 事实语义纪律（四态分离）— 本项目的核心纪律

对外暴露的每个事实必须标注它是**观察**、**派生**、**推断**还是**未知**。
rea 的原则，本项目采纳：

> **Missing coverage is unknown, not absence.**
> 没覆盖 = 未知，不是"没有"。

### 四态定义

| 态 | 含义 | 例 |
|---|---|---|
| `observation` | 直接观测到的 | 上游返回 402、账号被禁用 |
| `derivation` | 从观察值算出来的（无损或已知误差） | EWMA 平滑值、均值、分桶插值分位数 |
| `inference` | 基于样本的推测（有置信度问题） | 成本 tier=收费（凭 N 条 credit 样本外推） |
| `unknown` | 不知道，且说明需要什么才能知道 | 该号该模型成本未知（需一次带 credit 的调用） |

### 已知的推断/未知清单（改动这些地方必须带 provenance）

| 位置 | 现状语义 | 必须标注 |
|---|---|---|
| `accounts[].model_costs` tier | **推断**（少量 credit 样本） | kind=inference + samples |
| `accounts[].ewma_success` / `ewma_latency_ms` | **派生**（EWMA 平滑） | kind=derivation；样本不足时 kind=**unknown**（不输出数字） |
| usage 的 USD 折算价 | **推断**（OpenRouter 公开价折算） | kind=inference |
| `/status` latency 分位数 | **派生**（固定分桶**插值**估算，非精确值） | kind=derivation + limitations 说明 |
| `/v1/models` `supported_efforts` 为空 | **未知**（精简目录未返回），**不是**"不支持" | kind=unknown |
| `/v1/models` `supports_images` 缺失 | **未知**，不是"不支持"也不是"支持" | kind=unknown |
| 「该号不可用」 | **混合**（硬观察 / 推断判定） | 必须拆开 |

**实现口径（别把"缺失"读成"否"）**：`/v1/models` 的未知以**字段省略**表达——不额外输出
provenance 对象，避免改变 wire 形状。因此任何消费方（面板 / 客户端 / 统计）都必须按三值处理：
`true` / `false` / **字段缺失 = 未知**。**禁止**用 `!field` 判否。面板已按此修正：模型筛选
只排除显式 `false`，能力档位缺失显示「档位未知」而非「不支持」。

### 为什么这条是硬要求

养号决策直接依赖这些值。把"2 个样本的推断"当成"观察到的事实"展示，会导致
**基于噪声把一个健康号排除出选号**——这比功能缺失更危险，因为它不报错。

---

## Unknown 消解纪律

Unknown 是**一等公民实体**，不是面板上的一个展示区：

- 登记时必须写明 `requires`（需要什么才能消解）
- 带 `revision`：用过期的 revision 调 Resolve **必须报错**，调用方须重读后重试，
  禁止盲重试
- **消解只能由合格的观察证据完成**（推断不能消解推断）

实现：`internal/unknown/`（带 revision 的 registry）；`internal/evidence/`（四态标记）。

---

## Development

- Go 工具链：`C:\Program Files\Go\bin\go.exe`（≥1.27.2），
  `GOPROXY=https://goproxy.cn,direct`，`CGO_ENABLED=0`
- 构建：`go build -trimpath -ldflags="-s -w" -o yg-gateway.exe ./cmd/server`
- 门禁（五项，本地与 CI 同源）：
  - `go test ./...`（**23 包：20 ok + 3 no test files**）
  - `go vet ./...`
  - `gofmt -l ./cmd ./internal`（**必须无输出**；漂移已清零，新文件零容忍）
  - `powershell -NoProfile -File scripts/check-boundaries.ps1`（依赖方向，退出码非 0 即违规）
  - `powershell -NoProfile -File scripts/check-depth.ps1`（体量/覆盖率棘轮，只许收紧；`-Update` 抬棘轮）
- 提交前总闸：`powershell -NoProfile -File scripts/pre-commit.ps1`（全量；`-Fast` 跳过 `go test`）
- **动手前先读 `.harness/LESSONS.md`**（会伪造结论的坑清单 + Seen 计数制：同一类坑第 2 次出现
  即升级为规则或机器检查）
- 本机无 gcc，`-race` 不可用（**由 CI 独占**）；并发正确性靠设计 + 单测保证

## 变更追踪

每次改动建 `.harness/changes/{feat-name}/`，配：
- `docs/prd/REQ-*.md`（需求 + Given-When-Then 验收）
- `docs/adr/ADR-NNN-*.md`（仅当满足：难逆转 / 未来读者会困惑 / 有真实权衡）
- `summary.md`（改了什么、怎么验证的、怎么回滚）

## 部署纪律

- 生产实例在 `D:\yg-gateway`，端口 7863，配置 `D:\yg-gateway\config.json`
- **替换二进制前必须备份**（R15）
- 构建 + 复制 + 启动放同一条命令（跨命令空档是实时防护下手时机）
- 部署后必须冒烟：`/healthz`、`/v1/models`、流式与非流式各一发
