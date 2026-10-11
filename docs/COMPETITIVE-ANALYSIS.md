# 竞品分析（2026-10-11）

> 目标：回答两个问题——(1) 与 GitHub 上其他 WorkBuddy/CodeBuddy 反代网关相比，YG Gateway 处于什么位置；(2) 一个用户为什么要选 YG Gateway 而不是别的。
> 诚实原则：不吹不黑，劣势照实写。本文档是活文档，竞品在动，结论要跟着重估。

---

## 一、竞品图谱

GitHub 上「把腾讯 WorkBuddy / CodeBuddy 账号变成 OpenAI 兼容 API」的网关，按成熟度分三档：

| 项目 | 语言 | 规模 | 定位 | 核心差异 |
|---|---|---|---|---|
| [ithtelab/workbuddy-manager](https://github.com/ithtelab/workbuddy-manager) | Python + Next.js | 486 commits / 83 tags / 2124 测试 | **B 端生产级派发平台** | 多上游级联、密钥分组、IP/模型白名单、SQLite、签名一键更新、五语种 i18n |
| [ardeyouxipianyi/workbuddy2api-hub](https://github.com/ardeyouxipianyi/workbuddy2api-hub) | Python | 526 commits / 54 tags / 119 测试套件 | **功能密度最高的网关** | OpenRouter 费率折算、增量用量聚合、keepalive 池、剩余用量优先调度 |
| [KaiXuanSama/ARP](https://github.com/KaiXuanSama/ARP) | Java (Spring Boot) | 119 commits / 6 tags | 轻量网关 | OpenAI + Anthropic 双协议、扫码登录、GPLv3 |
| [motiful/cc-gateway](https://github.com/motiful/cc-gateway) | Go | — | Anthropic 身份网关（Claude 生态） | 多机共享单身份、OAuth 自动刷新 |
| **YG Gateway（本项目）** | **Go** | 起步 | 单机自用 → 开源 | 见下文 |

## 二、诚实的定位：YG Gateway 现在处于什么位置

**劣势（必须承认）**：

1. **功能广度不如 workbuddy-manager / workbuddy2api-hub**。它们有密钥分组派发、IP/模型白名单、多上游级联、SQLite 落库、OpenRouter 费率折算、增量用量聚合——这些是"给一群用户开 API Key 卖"的 B 端能力。YG Gateway 目前是单 api_key 鉴权，没有多租户派发。
2. **工程成熟度不如它们**。workbuddy-manager 2124 个测试、五语种 i18n、签名校验的一键更新；workbuddy2api-hub 119 个测试套件、OpenWrt 嵌入式适配。这些都是长期打磨出来的。
3. **生态广度不如**。它们支持更多客户端形态（Codex / Claude Code / DSH / Anthropic SDK），YG Gateway 目前聚焦 OpenAI 兼容。

**这不是自我贬低，而是战略判断**：YG Gateway 起步晚，跟它们拼"功能数量"必输。所以必须拼它们**没有的东西**。

## 三、YG Gateway 的真实差异化优势（为什么选你）

对照竞品逐项核验后，YG Gateway 有四个它们**要么没有、要么不如**的硬差异：

### 1. 单二进制自包含（Go + go:embed），部署成本接近零

- **竞品痛点**：workbuddy-manager 是 Python + Next.js，要装 Python 依赖 + Node 构建前端；ARP 是 Java + Maven + Node（首次构建 5-10 分钟）；workbuddy2api-hub 是 Python 单文件但依赖一堆 pip 包。它们要么 Docker、要么一堆环境。
- **YG Gateway**：一个 `yg-gateway.exe`（约 10MB），面板 JS/CSS **go:embed 进二进制**，拷到任意 Windows/Linux/macOS 机器直接跑，**无需 Node、无需 Python、无需 Docker**。GitHub Release 直接下五平台二进制即用。
- **这解决的是"开箱即用"这个最实际的痛点**——大部分用户只是想自己用，不想折腾环境。

### 2. 安全底座是实打实加固过的，不是"能用就行"

对照竞品 commit 历史，YG Gateway 在安全上做了它们普遍没做透的事：

- **出站 transport 加固**：真正禁 HTTP/2（空 `TLSNextProto` 而非仅 `ForceAttemptHTTP2=false`）、TCP 建连 10s、TLS 握手 10s、keepalive 15s、半死连接失败清池——这些是从"上游 WAF 风暴 + 半死连接卡 60s"的实战里提炼的。
- **入站边界加固**（本仓独立完成，竞品大多没有）：请求体上限（默认 64MB 防内存 DoS）、受信任代理判定（防伪造 XFF 污染上游风控）、只读端点限流。
- **凭证 ACL**：Windows 上 Go `0o600` 不剥继承 ACL 的坑，配了 `lock-auths.ps1` 一键加固。
- **构建工具链**：Go 1.27.2（低版本带 9 个 stdlib CVE），CI 里 `govulncheck` 零告警是硬门禁。

### 3. 选号调度是"养号友好"的，不是"流量友好"的

这是最本质的定位差异。竞品的选号大多围绕**成本/吞吐**（OpenRouter 式反平方价格加权、剩余用量优先），而 YG Gateway 的选号围绕**账号存活**：

- 快过期积分优先消耗（×2 软偏好，防止积分过期蒸发）
- 成本分层（免费 > 未知 > 收费，实测台账 + 上游目录倍率双判据）
- 积分保底 `credit_floor`（防止收费模型把余额打穿触发硬冷却）
- 全自动养号（签到/活跃/旅行/保活/夜猫子 + 成长任务 17/18 一键完成）
- 402 硬冷却至次日 04:00（等签到恢复）、429 软冷却指数退避、连续失败熔断

**一句话**：别人帮你"省钱"（成本最低），YG Gateway 帮你"养号"（账号活得久、积分不浪费）。对一个 CodeBuddy 账号是稀缺资源、封了就没的场景，这是更值钱的能力。

### 4. 工程治理是公开透明的（.harness 体系）

YG Gateway 仓库里带一套完整的 `.harness/` 治理体系：十阶段开发流程规范、红线 15 条编码规范、术语表、11 个技能、变更追踪（每次改动都有 summary + review + 根因分析）。这本身是**可审计、可追溯**的信号——开源用户能看到这个项目是怎么做出来的，而不是一堆"能用但不知道靠不靠谱"的黑盒。

---

## 四、结论：目标用户画像

**YG Gateway 不是要打败 workbuddy-manager，而是服务一个它服务不好的群体**：

| 用户 | 该选谁 |
|---|---|
| 我要开 API Key 卖给一群人、要分组计费、要白名单 | workbuddy-manager / workbuddy2api-hub |
| 我要 Java 生态、要 Anthropic 协议 | ARP |
| **我要自己用、几个号、不想折腾环境、怕号被封、想白嫖积分最大化** | **YG Gateway** |

**核心卖点一句话**：*一个二进制、零依赖、养号友好的 CodeBuddy 网关——把"部署省心"和"账号活得久"两件事同时做对。*

---

## 五、要补的短板（后续路线）

诚实地说，YG Gateway 要成为一个真正有竞争力的开源项目，还差这几块（按优先级）：

1. **多 API Key 派发**（对标 workbuddy-manager）—— 这是"从自用到给朋友用"的分水岭，也是星标增长的关键。
2. **OpenRouter 费率折算**（对标 workbuddy2api-hub）—— 用户想知道"我省了多少钱"。
3. **增量用量聚合 + 落库**（对标 workbuddy2api-hub）—— 目前是 JSONL 归档，量大后查询会慢。
4. **更完整的客户端生态**（Claude Code / Codex 直连）—— 目前聚焦 OpenAI 兼容。

这四条是"做出来就能翻星标"的路标，但每一条都是实打实的工程量，需要逐个立项、按十阶段流程推进。
