# magpie 全盘 REA（2026-11-03）

> 方法：只读逆向 `D:\dev\magpie`（main 最新树），行号以 2026-11-03 源码为准。
> 与 `magpie-deep-read-2026-10-25.md` 的关系：深读已覆盖的选号策略（Smart/Pace/
> LeastUsed/Weighted、90% 三档分层、pace 公式、1h 半衰期 token 记账、Seat 组装、
> quota-history 边界检测）本文只引用不重新论证；深读之后的增量证据由本系列 9 份章节
> 补齐。两者冲突时以**当前源码实跑 grep** 为准（已消解三处，见 §2 开头）。
>
> **四态纪律**：每条结论标 `[observation]`（源码直读）/ `[derivation]`（从观察值计算）/
> `[inference]`（推测带置信度）/ `[unknown requires: ...]`（需要什么证据才能消解）。
> 否定断言穷尽证据后才写，做不到穷尽就写 unknown。
>
> **对照对象 yg-gateway 三条 ban-risk 清单**：#11 代理 fail-open 直连（🟡-2）、
> #12 11102 三值判定盲区、#13 三条代码问题（🔵-1 会话编造 / 🟢-2 转发多路径头处理分散 /
> 🟢-4 模型快照复制）。

---

## ① 总览

### magpie 是什么

**一句话**：magpie（yetone/magpie，MIT）是一个本机自托管的 OpenAI 兼容网关 + 桌面应用，
把「多个编码 agent（Claude Code / Codex / Gemini CLI / OpenCode……）× 多个 provider
（API key / 本地模型 / Claude·ChatGPT·Copilot 订阅）× 多个账号」收敛到一个本地入口，
某账号额度打满时自动静默切到下一个。
> 证据：`README.md:7-11` "Every agent's model. One place. … when a quota runs out,
> it quietly moves on to your next account." `[observation]`

**定位**：agent 侧编排层。它读本机各 agent 的登录态文件变账号池，改写 agent 自己配置
里的 base URL/key，agent 无感知；不做多租户计费、不追求接全部 provider 生态之外的东西。

### 一页架构图（分层）

```
┌─ 集成面：CLI / TUI / Web GUI / daemon / browserext / MCP stdio   ← §2.8 (internal/cli, internal/tui, internal/gui)
│    Web 面板 127.0.0.1:3430，URL key + HttpOnly cookie + 常量时间比较
├─ Agent 识别与发现：70+ agent 静态声明，内置只真读 claude/codex 两个登录态 ← §2.9 (internal/agent, internal/provider/logins.go)
├─ Gateway 核心转发：routes() → serve candidate 循环 → attempt(passthrough|translate)
│    → forward 包装 → forwardOnce 唯一发起点（p.Do 全仓一处）        ← §2.1 (internal/gateway)
├─ 决策层：选号策略（深读已述）+ failure() 八分类 + restAfter 分档退避
│    + RPM/并发硬闸 + sink 耻辱排序 + session 粘性                   ← §2.4 (internal/routes, internal/gateway/{routing,fallback,rpm,concurrency,sink,affinity}.go)
├─ 记账层：usage.jsonl 一行 Record（TTFT/队列/重试分段计时）
│    + budget 网关 key 额度 + quota-history 45 天                    ← §2.5 (internal/usage, internal/budget)
├─ Provider 适配层：四协议抽象（Chat/Responses/Anthropic/Gemini）
│    + AuthHeaders 硬编码表 + UA 指纹按上游伪装 + PassHeaders 白/黑名单
│    + 插件 provider（Bun 子进程）                                    ← §2.2 (internal/provider, internal/plugin, internal/gwmw, internal/middleware)
├─ 账号/凭据层：logins.json 明文 0600（macOS Keychain 兜 Claude）
│    + savedTokenMu 三层锁保 refresh token 单持有 + keepalive 8 天错峰续期
│    + claudelock 模仿 Claude Code mkdir 锁                          ← §2.3 (internal/provider/{logins,claude_dirs,keepalive}, internal/access, internal/oidc, internal/mcpauth, internal/claudeseal, internal/claudelock)
├─ 出口隔离层：per-account 代理 ctx 注入 + transport 按 {choice,h1,unchecked} 分桶
│    + 显式代理 fail-closed + 私网直连绕代理                         ← §2.7 (internal/netproxy, internal/netguard)
└─ 出站脱敏层：redact 规则引擎 + 本地 ML（可选）+ 确定性 HMAC 占位符     ← §2.6 (internal/redact, internal/pii, internal/masking, internal/maskrec)
```

### 与 yg-gateway 的定位差异（引用深读战略结论）

- 两者**正交**：magpie 做「agent 侧编排 + 多 provider 多账号池」，yg-gateway 做
  「CodeBuddy 深耕 + 账号池治理 + 全自动养号」。`[derivation 依据: deep-read §六]`
- magpie 强在额度记账（5h/7d 双窗 + 历史 + 边界检测）、路由策略、出站节奏克制；
  yg 强在实测成本分层、EWMA 健康跟踪、决策可审计（pickReason + 四态 + unknown registry）、
  五类养号（magpie 对 CodeBuddy 类上游的养号深度 = 仅每日签到，`cli/accounts_cli.go:712`）。
  `[observation 依据: deep-read §二/§七.6]`
- 最优关系是互补：把 yg-gateway 的 `/v1` 填为 magpie 的自定义 OpenAI 兼容端点。
  `[derivation 依据: deep-read §六]`

---

## ② 子系统章节

> **三处章节间冲突已消解（以当前源码实跑为准）**：
> 1. **X-Magpie-Conversation 仍然存在**：`internal/provider/plugins.go:36` 常量定义、
>    `gateway.go:5388` plugin 请求 Set、`plugins.go:728/734` plugin host 读取、
>    `plugins.go:976` `req.Header.Del(ConversationHeader)`——删除点在 **provider 包的
>    plugin transport**（转发到 vendor 之前剥掉），不在 gateway 包。**禁止写"已删除"**。
>    `[observation]`
> 2. **steady / beat / awake 与账号节奏无关**：steady = 文件 rename 重试助手
>    （`steady.go:1-10`），beat = 时钟对齐 ticker 合并器省电（`beat.go:1-6`），
>    awake = 阻止 OS 休眠（`awake.go:1-18`）。真正的节奏克制在 gateway 的
>    RPM/并发/fallback 退避 + provider keepalive（见 §2.5）。`[observation]`
> 3. **claudeseal 不是加密密封**：它是把 Claude `thinking block` 包成
>    `"magpie-claude1:" + base64(json)` 塞进 Responses 协议 `encrypted_content` 字段的
>    协议适配（`claudeseal.go:23-29`），OpenAI 上游前剥掉（`claudeseal.go:10-12`）；
>    **claudelock 是模仿 Claude Code 的 mkdir 锁协议**（`claudelock.go:1-7`）。
>    两者都与凭据加密无关。`[observation]`

### 02.1 网关转发链路（internal/gateway）

**链路全貌（每跳文件:行号）**：
1. 路由注册 `gateway.go:906` → handle 解析 body `gateway.go:1910` →
   中间件/hook 包装 `middleware.go:23` → `serve` 主循环 `gateway.go:2100` →
   candidate 循环 `gateway.go:3600` → `attempt` 分叉 `gateway.go:4808`：
   同协议透传 `:4963` / 跨协议翻译 `:5004` → `forwardTranslated` 建 body `:6799` →
   `forward` 包装重发 `:5075`（refitOverCap / mantleReroute / retryTokenField /
   refuseThinkingBack / 400/403 形状自愈，最多 3 轮 `:5107,:5130`）→
   `forwardOnce` 构造 req `:5149`、签名、发请求 `:5439`。`[observation]`
- **单 sink**：全仓 `p.Do(s.client, req)` 仅 `forwardOnce:5439` 一处发起上游请求
  （codex 自家 socket 通道除外）。`[observation]`
- **换号在 candidate 循环层，不在 forwardOnce 内部**：`hw.failed()/hw.code()/hw.errBody()`
  判定分支——安全过滤拒绝（`gateway.go:3931-3987`）不 rest 当前号；流式超时首次不 rest、
  连续慢才 `restSlow`（`:3767-3786`）；推理 loop 同候选 `i--` 重发（`:3787-3806`）；
  模型不支持图像标 blindTo 带描述重发（`:3876-3902`）；已到最后候选 `failTo` 写客户端
  （`:3638`）。`[observation]`
- **Header 是新建的不是透传**：`gateway.go:5270` `http.NewRequestWithContext` 返回空
  Header，只 Set 固定 Content-Type/Accept/User-Agent（`:5277-5283`）+ anthropic-version
  （`:5284-5306`）；再按客户端身份白名单拷贝：Claude Code（`claudeCodeHeader`
  `:5291-5300`，谓词 `:5501`）、Codex（`:5307-5329`）、bili（`:5330-5343`）、
  OpenCode/Cline/Kilo/内置 Codex 账号（`:5344-5386`）。`[observation]`
- **会话从不编造**：`sessionOf`（`gateway.go:8619`）按顺序读 `X-Magpie-Session` 再读
  原生会话头列表（`gateway.go:8586-8590`：x-opencode-session / x-session-affinity /
  x-session-id / session_id / session-id / x-claude-code-session-id / x-conversation-id /
  x-deepseek-harness-session-id），每值 trim 截 128 字符，**全空返回 "" 不生成新 ID**；
  写回客户端 `withGatewaySession`（`conversations.go:24-28`）仅当客户端自带 session 才
  Set，注释明写 "magpie never makes one up (#1355)"；OpenCode 优先级
  `opencode_session.go:29`。`[observation]`

**与 yg 的对照**：单 sink 收敛（发请求只一个函数）直接回答 yg 🟢-2——magpie 没有第二份
签名/头处理路径。会话不编造直接回答 yg 🔵-1：网关不要为无会话请求伪造 UUID。
头处理「按客户端身份白名单 + 全局前缀黑名单（`requestheaders.go:32`
`Content-, Proxy-, Sec-, X-Magpie-`）+ per-provider 显式 opt-in」三层模式可抄。

### 02.2 Provider 协议适配（internal/provider 等）

- **Provider 抽象**：`provider.go:50` 结构体含 base URL 四槽位（Chat/Responses/Anthropic/
  Gemini，`provider.go:39-47`）；`Speaks()`（`:1892`）按 base URL 非空判定原生协议；
  `APIs(model)`（`account.go:136`）受 models.dev 目录收窄；`Native(model)`（`:2025-2030`）
  订阅类（claude/devin account）返回空串表示每次都要翻译。`[observation]`
- **AuthHeaders 是硬编码表**（`test.go:588-616`）：Vertex 不发卡；Azure 非 Anthropic 用
  `api-key`；Gemini 用 `x-goog-api-key`；anthropic.com/Bedrock 只发 `x-api-key`，第三方
  Anthropic 兼容默认两个都发、可用 `AuthHeader` 字段收窄；其余只发
  `Authorization: Bearer`。401 后 `RetryOneAuth`（`authheader.go:40-74`）自动试单头并
  `LearnAuthHeader` 持久化写回 providers.json。`[observation]`
- **UA 指纹按上游伪装，不是一个统一 UA**：`user_agent.go:9-19` 四类——透传 /
  OwnClient（OpenCode/Cline/Kilo/remote magpie 伪装成对应 CLI）/ 订阅账号由 sign 闭包自写；
  Codex 三段式 `codex_cli_rs/<ver> (os; arch) <terminal>`（`codex_request.go:63-66`）；
  OpenCode `opencode/latest/<ver>/cli`，ver 每 6h 后台拉 npm 最新、硬下限 `2.2.5`
  （`opencode_client.go:35,41-43,49`）——Zen 免费档对旧 UA 一律 429。`[observation]`
- **出向回复头默认只透 Request-Id / X-Request-Id / X-Oneapi-Request-Id**
  （`replyheaders.go:17`），黑名单挡 Set-Cookie/CORS/X-Magpie-*/Content-*（`:25-38`）。
  `[observation]`
- **"用完切换"基于额度观测而非 429**：`codex_switch.go:116-127` `SpentShare = 98`
  切出、`backShare = 90` 切回（避免边缘抖动）；`usedPast`（`:219-226`）要求窗口
  Used ≥ share 且 reset 未到；观测来自 `/wham/usage`、Claude `/usage` 周期轮询
  （`loginSwitchEvery = 5min`、`codexNearOutEvery = 1min`）；切换动作 = 把凭据写回
  agent 自己的登录文件（`switchLoginBy` `:467`）。`[observation]`
- **三个桥**：codexrelay = 退出后端口保持子进程（`relay.go:76-90`）；claudebridge =
  Claude Code 的 stdio MCP 子进程（`mcp.go:144-199`）；claudetui = PTY 跑交互式
  Claude Code 伪装 stream-json API——动机是 Anthropic 按 entrypoint 计费，`cli` 比
  `sdk-cli` 便宜一半（`tui.go:12-16`）。`[observation]`

**与 yg 的对照**：magpie 多 UA 指纹抽象不可抄（yg R8 钉死三段式 UA），但「UA 版本号
随上游 CLI 自动更新 + 硬下限」模式在 CodeBuddy 风控收紧时可借鉴。`codex_switch` 的
98/90 双阈值 + reset 后强制重评估，对 yg 是「观测到用量接近上限就软冷却」的前瞻层参考；
但改写本机 agent 登录文件对 yg 不适用。

### 02.3 账号 / 凭据 / 登录体系

**凭据存储全景**（明文 JSON + 0600，跨进程 filelock）：

| 文件 | 内容 | 权限 | 锁 |
|---|---|---|---|
| logins.json | 各 agent 订阅账号凭据 blob | 0600（Windows no-op） | logins.json.lock |
| caller-keys.json | 网关自身 API key | 0600（`access.go:723-733`） | caller-keys.json.lock |
| mcp-signins.json | MCP server OAuth | 0600（`mcpauth.go:168-177`） | .renew.lock |
| providers.json | provider Key/AK/SK | 0600 | filelock |
| 备份 bundle | 上述全部 | **AES-256-GCM**（`backup.go:363`） | 用户口令 |

`[observation]`——核心观察：magpie 自己的凭据在磁盘上**全部明文**，Unix 0o600 在
Windows 上是 no-op，没有补 Windows ACL；加密只在用户主动导出的备份 bundle。

- **refresh token 单持有（三层锁）**：`logins.go:7-9` 包注释明写 "two holders of the
  same one would sign each other out"；`savedTokenMu` 是跨进程文件锁
  （`logins_on.go:26` `loginsPath() + ".renew.lock"`）；调用点
  `claude_dirs.go:209`（读回写）、`:250`（写独立 config dir）、`logins.go:1276`
  （切号前）；同进程 `claudeDirRun` 是 `sync.RWMutex`（`claude_dirs.go:114-132`），
  跨进程另有 `.run-*.lock`（`:152-163`），切换前 `waitClaudeRuns` 等释放（`:170-187`，
  #4532 踩坑后补的）。`[observation]`
- **网关 key 体系（access）**：List() 绝不返回 secret，只填 Masked（`access.go:215-241`）；
  常量时间比较 5 处（`access.go:104/295/299/631`、`issue.go:235`）；IssueKey 子 key
  TTL 1min~30 天、父 key 死子 key 立即失效（`issue.go:26-29`）。`[observation]`
- **mcpauth**：过期前 2 分钟提前刷（`mcpauth.go:62`），`renewing sync.Map` 按 server 互斥
  + `.renew.lock` 跨进程（`:311`）；refresh 被拒不删记录只置 `Dead=true` 软死亡标记
  （`:357-381`）。`[observation]`
- **gh token**：不落盘、每次现读 settings/env、401 后内存标记拒绝并降级匿名重试
  （`ghtoken.go:64-67,125-147`），host 严格匹配 api.github.com（`:163-174`）。`[observation]`
- **claudelock**：`os.Mkdir(path, 0o755)` 拿锁、holder 每 2s touch、stale（refresh 锁 60s/
  config 锁 10s）后 takeover、等待上限 20s（`claudelock.go:21-27,33-36,58-100`）——
  模仿 Claude Code 的锁协议才能安全动它的文件。`[observation]`

**与 yg 的对照**：yg 在 Windows 上有凭证 ACL 加固，magpie 没有（Windows 上
`%APPDATA%/magpie/logins.json` 本机任何进程可读）——这是 yg 更扎实的地方。
值得抄：① `.renew.lock` 跨进程锁范式（yg 未来多实例/CLI 时直接用）；② List() 脱敏原则；
③ Dead 软死亡标记（比硬拉黑温和）。

### 02.4 路由 / 重试 / 换号

> 选号策略（Smart/Pace/LeastUsed/Weighted、90% 三档字典序分层、pace=周剩余份额÷距
> reset 小时数、半衰期 1h token 记账、Seat 组装）深读已覆盖，不重复论证。

- **failure() 八分类**（`routing.go:486-552`）：502+proxyDown → failProxy；401/403+
  Verification → failVerify；402 本身或 credit 词 → failCredit；402+usedUpWords → failQuota；
  429+resetsWhen → failQuota、429+minuteLimit/rateWords → failRate；5xx+quotaCodes →
  failQuota、其余 → failOther。词表在 `routing.go:273-354`（creditWords/usedUpWords/
  rateWords/minuteWords/longWords/resetsWhen）。`[observation]`
- **restAfter 分档**（`routing.go:172-221` 常量 + `:1221+` 实现）：
  failCredit `creditRest=30min`（Codex 5h）且**唯一爬梯** `creditLadder =
  [30min,2h,8h,24h,3d,7d]`（`:232`，`:249-271` 触发）；failQuota `quotaRest=15min`
  或按 reset/Retry-After、封顶 `longestQuota=8d`；failRate `rateRest` 指数翻倍、封顶
  `longestRateRest=30min`、`rateForget=30min` 内再犯计数累计（`:1737-1773`）；
  failOther `min(fallbackCooldown << min(n-1,10), RestCapOr)` 即 1min 起翻倍封顶 10min
  （`:1485`）；failRestricted `=2h`（`:215`）；failOffline/failProxy/policy refusal
  不 rest（`:1234-1270`）；overloaded 529/503 `=30s` 按 Retry-After 封顶 1min（`:1435-1448`）。
  `fallbackCooldown = 1min`（`fallback.go:43`）。`[observation]`
- **"有观测才切换"严格实现**：`Seat.Resting/Spent` 只由 `restingUntil.m[id]` 写入——
  写入点全部来自 `restAfter`（上游响应分类）/ `loadRests`（读 rests.json）/ 手动 Unrest，
  无任何基于 EWMA 或目录为空的预判写路径（`weighted.go:43-52` `resting(c)` 只读 restOf）。
  `[derivation 公式: grep "restingUntil.m[" 写入点 = restAfter + loadRests + Unrest 三处]`
- **sink 耻辱排序**（`sink.go:42-48,94-117`）：只在 failRate 时按沉底序号稳定沉底，
  纯内存重启清空，无自动时间解封，靠"其他号也被 429 → 序号超过它 → 自然浮上来"轮换；
  本来就是轮询的 routing 不参与（`:87-89`）。`[observation]`
- **probeOne 减损**（`fallback.go:1277-1308`）：多个 resting 候选时只保留 rest 结束最早
  的那一个参与本轮，避免每个请求扫一遍死号。`[observation]`
- **Session 粘性**（`affinity.go:570-654`）：Off/死绑/会话内/单轮 四模式；解绑条件按
  gone/strict/reserve/resting/credits/busy/main/order/back/spent/no-cache/cold 十一条
  分支；持久化 affinity.json（`:283`）。`[observation]`
- **budget 面向网关 key 而非上游账号**（`budget.go:1-31`）：used+reserved ≥ cap 硬拦
  （`:396-401`），多 limit 叠加任一耗尽即拒（`:24-27`），在途 reservation 估 body/4+
  窗口均输出（`:419-422`），包注释自承并行会超约一次调用量（`:20-23`）。`[observation]`

**与 yg 的对照**：magpie 不预判换号，与 yg credit_floor 硬拦 + 90% 软带的养号哲学
**相反，不要照抄否定 yg 预判**；可借鉴：① failure() 429 五分类词表（比 yg 粗分类精确）；
② probeOne 只探最早解封；③ sink 按沉底序号稳定排序的轻量风控规避；④ 在途 reservation
估算（body/4 + 平均输出）。

### 02.5 额度记账 / 节奏克制

- **usage.jsonl 一行 Record**（`usage.go:30-254`）：核心列 `t/agent/provider/host/
  providerAccount/model/in/out/cache_read/cache_write/ms/status/ep/kind/source` 全部命中；
  额外有 `ttft_ms/first_text_ms/queued_ms/retried_ms/flow_ms` 分段计时、`tries/handed_on`、
  `credits/cost`；写盘 `Append`（`:287`）注释 "Errors are swallowed: accounting must never
  break a call"；读路径有块索引 + log-v1.bin 磁盘快照 + SHA-256 前缀校验
  （`log_disk.go:24,46,137`），避免 750k 行全量重扫。`[observation]`
- **出站 RPM 硬闸**（`rpm.go:16-41`）：每 key/account 一个 60s+1s 滑动槽位队列
  （`rpmWindow=1min` `:44`、`rpmSlack=1s` `:52`、`rpmLongest=2min` `:59`），
  每个出站请求（含 count_tokens、decide、embeddings、drawing）都过 `rpmTransport.RoundTrip`
  （`:276-294`），满了 FIFO 预约下一出发时刻。`[observation]`
- **并发车道**（`concurrency.go:15-27`）：包注释 "a Codex account is risk-controlled
  past five or six requests at once"；FIFO 排队 + `laneRecheck=5s` 兄弟车道让位（`:127,334`）。
  `[observation]`
- **后台复活探测**（`rest_probe.go:1-17`）：对 rest 中的 key 在 1/2/5/10 分钟阶梯后
  打 `/v1/models`（**零成本**），列表应答即抬起 rest；明确**不探订阅号、不探上游已命名
  结束时间的 rest**；刚抬起又被打回则 30min 不再探（`restProbeMiss=30m` `:39`）。
  `[observation]`
- **refresh token 错峰续期**（`keepalive.go:1-26,42-56`）：8 天周期、每轮只续 1 个号、
  每小时醒一次——包注释明写反对"一天内 N 个号一起刷"（owl on Discord）；失败号 6h 后
  再试不堵队；Claude 号真跑一次 Claude Code `/usage` 让它自己刷（`:225-276`）。`[observation]`
- **每日 warm-up**（`claude_warmup.go:40-67`）：Haiku 档 stdin 就一个词 "hi"，无工具无
  MCP；`codex_warmup.go:565-613` 调度——启动 2 分钟首跑、墙钟每分钟对齐、窗口未启动时
  kick（单号节流 10min）。`[observation]`；`[inference 置信度: 高]` 动机 = 最低成本让号保持
  活跃，避免"一周没用突然用"被当异常。
- **节奏克制五层叠加**（穷尽 grep 三路：MaxRPM/MaxConcurrency 17 文件、warm/heartbeat
  全指向已知文件、ticker 24 处逐个看过——SSE ping 与上游无关）：出站硬闸 → 指数退避 →
  零成本探测复活 → token 错峰续期 → 每日 warm-up。`[observation]`

**与 yg 的对照**：最高优先级可抄——① upstream 包加一层 rpmTransport（按 providerAccount
限 RPM/并发），这是防"短时间把一个号打热"最直接手段；② warm-up 走 CLI 指纹打最低成本
请求（yg 必须走 WorkBuddy 正常路径，不能裸 HTTP）；③ 累计翻倍退避 + 复活探测只打
零成本端点。退避封顶 30min 对 CodeBuddy 周窗口太短，封顶要按上游重置周期重设。

### 02.6 PII 出站脱敏（internal/redact + pii + masking + maskrec）

- **出站前替换、响应流式还原**：`gateway.go:2394` `redactedHits(maskedFor(...), ...)`
  返回的 b 就是脱敏后请求体，随后发 vendor、随后 `logBody` 记录（`:2421`）——日志里的
  body 必然来自 `redactedHits` 或 `maskedCopy`，无"先记原文再打码"路径（grep logBody 3 处穷尽）。
  `[observation]`
- **fail-closed**：开了脱敏但 masker 不可用/出错，请求直接 503 不发未脱敏数据
  （`gateway/redact.go:24-25`）。`[observation]`
- **确定性占位符可还原**：`{{KIND_<base32(hmac-sha256(key, kind\0value))[:8]>}}`
  （`redact.go:776-779`）——同一值每次占位符相同（vendor 缓存 prefix 不失效）；
  值存内存 map 上限 200k LRU（`:714,726`），HMAC key 可落盘跨重启稳定；`restore()`
  （`:1657-1698`）流式把占位符换回原值，跨 SSE event 边界 hold 住。`[observation]`
- **识别范围**：规则引擎（PRIVATE_KEY/API_KEY/TOKEN/PASSWORD/EMAIL/ID_CARD/PHONE/
  BANK_CARD，`redact.go:161-222`，含 CJK 命名密码）；可选本地 ML（39MB ONNX 默认 /
  1.6GB GGUF，`pii.go:6-18`）补自由文本人名/地址；默认关 home 路径、公网 IP、云存储桶名
  （`more.go:21-24`：system prompt 里每个 agent 都有 home 路径，打码会破坏）。`[observation]`
- **maskrec 审计**：只存 placeholder/kind/by/head(首字符)/len/n，**绝不存原值**
  （`maskrec.go:5-10`）；每日 JSONL 0700/0600，保留 30 天/100MB（`:222-226,35-36`）。
  `[observation]`

**与 yg 的对照**：ML 模型和完整规则引擎不抄（破单二进制零依赖红线）；可借鉴——
① HMAC 确定性占位符保缓存前缀的设计；② maskrec"只存 hint 不存值"的审计模式；
③ fail-closed 思想。

### 02.7 插件 / 中间件 / 出口隔离

- **gwmw 两类挂载，失败策略相反**（`gwmw.go:1-17`）：Agent（JS 中间件）fail-open——
  钩子超时（请求 250ms/事件 50ms/加载 1s）或抛错，body 原样继续；Masker（脱敏）
  **唯一 fail-closed**（`:288-307`）。`[observation]`
- **JS 中间件跑在进程内 moejs（Go 写的 JS 引擎）**，不是 Bun（`middleware.go:7-13`）；
  外部插件跑在独立 Bun 子进程（`host.go:447-449`），Bun 二进制首次按需下载 + SHA-256
  版本锁定（`bun.go:36-40`）。`[observation]`
- **per-account 代理 ctx 注入**：`proxy.go:34-51` 优先级 账号级 > provider 级 > 全局 >
  env > 系统；`Via(ctx)`（`:19-25`）在主路径 wrap（`gateway.go:4834`）；
  **transport 按 `{choice,h1,unchecked}` 分桶 clone**，连接池不串味（#4523，
  `netproxy.go:165-198,227-259`）；全局代理变了 `CloseIdleConnections`（`:189-198,212-225`）。
  `[observation]`
- **显式代理即 fail-closed**（`netproxy.go:71-73`）：ctx 里 choice 非空就只返回这个代理，
  函数后续全局/env/system 回退分支根本不进；代理 dial 失败冒泡 `proxyconnect tcp: …` 错误，
  **没有 fallback to direct 分支**；Bun 侧同样（`host.js:504-509` catch 只包装不重试
  直连，`host.js:512-563` `proxyRefuses()` 区分"代理死了"和"目标站死了"）。`[observation]`
- **唯一 fail-open 窗口**：开机时全局/system 代理 app 还没起来——15s/2s 缓存 + 主动重探
  （`netproxy.go:506-518,560-569`，注释 #1518）。`[observation]`
- **私网直连绕代理**（`lan.go:49-88`）：RFC1918 + CGNAT 100.64/10 + .local 一律 `nil`
  代理（`netproxy.go:74-78`）；Bun 侧 JS 复刻同规则（`host.js:452-474`，#1572）。
  `[observation]`
- **/status 只能看全局代理**：`Describe()` 返回 (proxy, source)（`netproxy.go:393-410`），
  GUI 状态 `api.go:766`；per-account 实际走了哪条路**仓库里没有观测字段**，
  `[unknown requires: 读 internal/probelog/ 全文确认 proxy 字段]`。

**与 yg 的对照（#11）**：magpie 显式代理不回退直连正是 yg #11 想要的语义——先做语义修复
（显式代理 dial 失败直接报错让账号失败/降级），再做三态观测；"/status 能看出代理未生效"
只是 yg 的一半方向，magpie 自己 Describe() 也只暴露全局代理，per-account 是 unknown。
transport 分桶 clone、proxyMoved 主动断池、`proxyconnect` 前缀驱动换号都可抄。

### 02.8 CLI / TUI / Web / 守护进程 / 更新

- **CLI 全家桶入口**：裸 switch 分发（`cli/main.go:457`），serve/daemon/accounts/
  providers/backup/webdav/s3/github/usage/heap/cpu/goroutines 全在一个二进制。`[observation]`
- **Web 面板鉴权**（`gui/web.go:181-273`）：启动生成 16 字节随机 key（32 字符 hex），
  `?k=` 首次访问种 `HttpOnly; SameSite=Lax` cookie（寿命 400 天），cookie 常量时间比较
  （`:207`）；默认绑 `127.0.0.1:3430`（`web_cli.go:19`），`--lan` 才翻 0.0.0.0 且命令行
  主动警告（`web_cli.go:50-53,156-161`）；跨机必须 TLS（`web.go:90`）。`[observation]`
- **onePort 反伪造**：页面端口上的 `/v1/*` 一律按跨机请求判、必须带 gateway key
  （`web.go:431-444`）——挡反向代理/X-Forwarded 把 loopback 伪装成本地 agent。
  IP allowlist **不信 XFF** 只看 TCP RemoteAddr（`allow.go:20-28`），settings 读不出时
  **fail-closed 只放本机**（`allow.go:55-75`）。`[observation]`
- **CORS/DNS rebinding/BREACH 防御**（`cors.go:17-50`、`web.go:370-386`）：非空 Origin
  必须在白名单 + 带 key；Origin: null、localhost 子域、rebound Host 都拒；gzip 加 32 字节
  随机 padding 防 BREACH。`[observation]`
- **自动更新**（`update.go`）：HTTPS feed（usemagpie.ai）拿 version + SHA256，二进制
  从 GitHub Releases 下（镜像可替换 URL 但哈希仍以主站为准，`mirror.go:16-21`）；
  校验不过不落地（`:1239-1245`）；macOS 加 codesign 同团队校验，**Windows/Linux 裸 SHA256
  无代码签名**（`:5-9,302-313`）。`[observation]`
- **daemon**：用户级服务（Linux systemd --user / macOS launchd），Windows 明确不支持
  （`daemon.go:62-74`）；服务文件 0600（`:407-420`）；卸载顺序先 disconnect agent →
  再拆系统注册 → 最后删数据，Library 在用则保留，不删 exe 本身（`uninstall.go:275-283`）。
  `[observation]`

**与 yg 的对照**：值得抄——onePort 页面端口上的 `/v1/*` 必须带 key、allowlist 读不出时
fail-closed、cookie HttpOnly+常量时间+BREACH padding 组合；不抄 URL query 塞 key（yg
面板在可信内网）；Windows 自更新必须补 Authenticode（magpie 裸 SHA256 换 Windows exe 是
短板，别学）。

### 02.9 Agent 集成与识别

- **Agent 抽象**：`agent.go:118-136` 结构体声明 Bin/Dir/Path/UA 前缀等静态属性；
  内置 70+ agent 声明（`agents.go:228-303`），但**真读登录态的 loginAgents 只有
  claude 和 codex 两个**（`logins.go:161`）——grok/copilot/kiro/devin/qoder/zed 等的
  list 字段为 nil，由社区插件提供（`logins.go:1006-1015`）。`[observation]`；
  `[derivation 依据: nil list 字段 + 插件迁移注释]`
- **Detected() 多重证据**（`agent.go:402-429`）：自定义探针 / 配置文件存在且属主正确 /
  目录非空（排除只剩 logs/debug/cache 的残留，`leftover` map `:475`）/ sign 标记 / Bin 在
  PATH——从不凭 UA 前缀单独判定。`[observation]`
- **总开关**：`MAGPIE_NO_ACCOUNTS=1` 或 Settings→Privacy 关掉后，`discoveryOff()` 为真，
  所有 `readAccountFile()` 直接拒绝（`discovery.go:1-18,36-41`）——一个 gate 函数挡所有
  读取点。`[observation]`
- **凭证 blob 复制进 logins.json**（`logins.go:95-97,187`）：Codex auth.json 含 id_token
  （JWT 带邮箱/plan）、access/refresh token；Claude `.credentials.json` 含 OAuth 全套；
  独立账号跑在自己的 `CLAUDE_CONFIG_DIR`（`claude_dirs.go:33-40`，sha256(user)[:16] 目录名）。
  `[observation]`

**与 yg 的对照**：magpie 主动读 agent 登录态不抄（yg 定位是 CodeBuddy 账号池、R1 凭证
不出 auths/）；可借鉴——① agent 识别要"文件存在 + 可执行文件存在"双重证据，UA 可伪造；
② 自动发现类功能要有总开关 + 所有读取点过同一个 gate 函数。

---

## ③ 安全 / 风控专题：magpie 为什么不容易封号

### 面 1 · 身份面：会话从不编造，原生指纹保留

- 会话空就是空：`gateway.go:8619` `sessionOf` 只读客户端 `X-Magpie-Session` 或原生
  会话头列表，全空返回 `""`；`conversations.go:24-28` 注释 "magpie never makes one up
  (#1355)"。`[observation]`
- 原生会话头保留：`nativeSessionOf`（`gateway.go:8652`）只读原生会话头、不受
  X-Magpie-Session 覆盖，用于 usage 日志区分；OpenCode 优先级
  `opencode_session.go:29`，再 fallback 到 `prompt_cache_key` 或首条 user message 派生。
  `[observation]`
- UA 指纹按上游伪装且版本号随官方 CLI 更新（OpenCode 每 6h 拉 npm、硬下限 2.2.5，
  `opencode_client.go:35,41-49`）——不发一个"自己发明的 UA"。`[observation]`

**对 yg 的含义**：网关不为无会话请求伪造 UUID（直接对应 yg 🔵-1）；R8 三段式 UA 已是
正确做法，magpie 的"版本号自动跟踪上游 CLI"是风控收紧时的备选模式。

### 面 2 · 节奏面：出站硬闸 + 分档退避 + 零成本复活

- 出站 RPM/并发硬闸：`rpm.go:44,52` 60s+1s 滑动窗，每个出站请求都在
  `rpmTransport.RoundTrip`（`:276-294`）前等槽位；`concurrency.go:15-27` 车道 FIFO。
  `[observation]`
- 失败指数退避：`fallback.go:43` 1min 起步，`routing.go:1754` 翻倍左移封顶
  `longestRateRest=30min`（`:196`），`rateForget=30min` 内再犯计数累计（`:199`）。
  `[observation]`
- 后台 `/v1/models` 零成本探测复活：`rest_probe.go:32` 阶梯 1/2/5/10 分钟，
  `:74-85` `probeable()` 只探 5xx/超时的 key、明确不探订阅号。`[observation]`
- refresh token 8 天错峰续期：`keepalive.go:45,50` 8 天周期 + 每轮 1 个号、每小时醒一次；
  包注释明写反对一天内 N 个号一起刷（`:42-44`）。`[observation]`
- 每日 Haiku "hi" warm-up：`claude_warmup.go:67` stdin 就一个词，最低成本保持号活跃。
  `[observation]`
- rest 按失败原因分档：quota/rate/credit/backoff/overloaded 各有常数（`routing.go:172-221`），
  不把分钟限流 rest 成小时。`[observation]`

**对 yg 的含义**：这是 magpie 相对 yg 最大的缺口——yg 目前无出站前强制槽位。
在 `internal/upstream/client.go` 包一层 rpmTransport 是防"短时间把一个号打热"最直接的手段；
warm-up 必须走 WorkBuddy CLI 指纹。

### 面 3 · 凭据面：单持有 + 三层锁（如实写明短板）

- savedTokenMu 三层锁：进程内 RWMutex（`claude_dirs.go:114-132`）+ 跨进程 `.renew.lock`
  （`logins_on.go:26`）+ 跨进程 `.run-*.lock`（`claude_dirs.go:152-163`，切换前
  `waitClaudeRuns` 等释放）——refresh token 同一时刻磁盘上只有一个 holder。`[observation]`
- refresh 单持有的注释证据：`logins.go:7-9` "two holders of the same one would sign each
  other out"；`logins.go:1292-1294` "whichever refreshed first would sign the other out"。
  `[observation]`
- **短板如实写**：凭据明文 JSON 0600（`access.go:723-733`、`mcpauth.go:168-177`），
  Windows 上 0600 是 no-op、无 ACL 加固；全仓 `aes.NewCipher` 只在
  `backup/backup.go:363`（用户主动导出的备份 bundle）——magpie 日常凭据在 Windows 上是
  裸明文。`[observation]`

**对 yg 的含义**：yg 的 Windows 凭证 ACL 加固是比 magpie 扎实的地方，保持；
`.renew.lock` + 锁内重读 + 版本号对比再写的范式，yg 未来做 CLI 切号或多实例时直接复用。

### 面 4 · 出口面：per-account 代理 + 显式 fail-closed

- per-account 代理 ctx 注入：`proxy.go:34-51` 账号级 > provider 级 > 全局 > env > 系统；
  `gateway.go:4834` 主路径 `p.Via(ctx)`。`[observation]`
- transport 分桶不串味：`netproxy.go:227-259` 按 `{choice,h1,unchecked}` clone，
  "Connections are never carried to another proxy"（#4523）。`[observation]`
- 显式 choice 即 fail-closed：`netproxy.go:71-73` choice 非空就只走这个代理，
  无回退直连分支；代理错误带 `proxyconnect tcp: …` 前缀（`host.js:504-509`）驱动网关
  换下一个号（`host.js:515-517`）。`[observation]`
- fail-open 仅限开机窗口：`netproxy.go:506-518` system 代理未起的 15s/2s 缓存窗口，
  这是唯一明确的直连放行路径。`[observation]`

**对 yg 的含义**：这正是 yg #11 的修复范式——显式配了代理就不允许静默直连，
代理挂了报错让换号，绝不"配错了反而用共享 IP 出去"。

### 面 5 · 平台隔离面：订阅读读移出主仓库 + 总开关

- AGENTS.md 明写读取 Claude/Copilot/Devin 订阅登录的实现在社区插件层；内置真读的
  loginAgents 只有 `claude` 和 `codex` 两个（`logins.go:161`），其余 list 为 nil
  （`logins.go:1006-1015`）。`[observation]`；`[derivation 依据: nil list 字段]`
- `MAGPIE_NO_ACCOUNTS=1` 总开关：关掉后 `discoveryOff()` 挡死所有 `readAccountFile()`
  （`discovery.go:36-41`），一个 gate 函数而非散落的 if。`[observation]`
- 所有 `X-Magpie-*` 内部头被 `requestheaders.go:32` 前缀黑名单挡在 vendor 之外——
  vendor 永远看不到 magpie 的会话/选号/限流内部语义。`[observation]`

**对 yg 的含义**：yg 的 R1（凭证不出 auths/）+ R8（报文内容不改）是同构纪律；
"一个 gate 函数挡所有读取点"是 yg 未来加自动发现时的正确形态。

---

## ④ yg-gateway 可借鉴清单（按优先级）

### 对三条 ban-risk 逐条：magpie 怎么做 / 我们怎么抄

**#11 代理 fail-open 直连（🟡-2）**
- magpie 怎么做：显式代理即 fail-closed——`netproxy.go:71-73` choice 非空就只走该代理，
  代理 dial 失败冒泡 `proxyconnect tcp: …` 错误、不回退直连；唯一 fail-open 是开机时
  system 代理未起的 15s/2s 窗口（`netproxy.go:506-518`）。`[observation]`
- 我们怎么抄（方向）：**先做语义修复，再做三态观测**。第一步（magpie 也做了）：显式代理
  dial 失败时不回退直连，直接报错让该账号失败/降级——这是语义修复不是观测修复。
  第二步（magpie 也只做了一半）："/status 能看出代理未生效"只是 yg 的一半方向——magpie
  自己 `Describe()`（`netproxy.go:393-410`）也只暴露全局代理，per-account 实际走了哪条
  是 unknown。所以 yg 的目标应是两步都做，但不要把"magpie 做到了 per-account 观测"当
  标杆——它没做到。

**#12 11102 三值判定**
- magpie 怎么做：`Seat.Resting/Spent` 只由 `restAfter` 写入（`weighted.go:43-52`、
  `restingUntil.m` 写入点 = restAfter + loadRests + Unrest）——"有观测才切换"的严格实现。
  `[derivation 公式: 写入点穷尽]`
- 我们怎么抄：**不建议照抄否定 yg 预判**——magpie 是单网关转发、上游挂了再切不影响正确性，
   yg 是养号导向（号要活得久、积分不浪费），需要 credit_floor 硬拦 + 90% 软带的前瞻撤号。
  可借鉴的是：① `failure()` 429 五分类词表（`routing.go:273-354`，把 429 拆成频率类/
  额度类/月度类/单模型类）；② `probeOne()`（`fallback.go:1277-1308`）只探最早解封的那个，
  避免每个请求扫一遍死号。

**#13 三条代码问题**
- 🔵-1 会话不编造：magpie `sessionOf` 空返回 ""、`conversations.go:24` 注释 #1355。
  抄法——网关不要为无会话请求伪造 UUID，会话亲和性依赖客户端自带头。`[observation]`
- 🟢-2 转发单 sink：magpie 上游 HTTP 只在 `forwardOnce:5439` 一处发出，头处理集中在
  `forwardOnce`（按客户端身份白名单）+ `requestheaders.go:21-32` 全局黑名单 + per-provider
  显式 opt-in。抄法——把"发请求"收敛到一个函数，其他函数只准备 body/header 并调用它；
  头处理用"白名单 + 前缀黑名单"模式。`[observation]`
- 🟢-4 模型快照复制：结合 §2.4 的 `creditLadder`（`routing.go:232`）/rest 常量轻量方案
  方向——退避梯子的常量表是集中式数据结构而非散落逻辑，yg 整理模型快照/退避常量时
  参照这种"一张分档常量表 + 一个分类函数"的形态。`[observation]`

### 其他按优先级

1. **出站 RPM 硬闸**（P0）：`rpm.go` 60s+1s 滑动窗 + RoundTripper 卡槽位。
   为什么值得抄：yg 现在无出站前强制槽位，"短时间把一个号打热"是养号场景最直接的封号
   路径。落点方向：`internal/upstream/client.go` 包一层 rpmTransport，按 providerAccount
   维度限 RPM/并发。`[observation]`
2. **warm-up 错峰范式**（P0）：`claude_warmup.go:67` Haiku 档一个 "hi" +
   `codex_warmup.go:565-613` 墙钟对齐 + 窗口未启动 kick。为什么值得抄：yg 完全没有
   主动养号动作。落点方向：走 WorkBuddy 正常客户端路径（R8 指纹），不能裸 HTTP。
   `[observation]`
3. **Web 面板鉴权组合拳**（P1）：随机 key + HttpOnly SameSite=Lax cookie + 常量时间
   比较（`web.go:207`）+ gzip 32 字节随机 padding 防 BREACH（`web.go:370-386`）+
   allowlist 读不出时 fail-closed（`allow.go:55-75`）+ 不信 XFF。为什么值得抄：yg 面板
   目前鉴权纪律弱于这套。落点方向：照抄组合，不抄 URL query 塞 key 模型。`[observation]`
4. **PII 出站脱敏管线**（P2）：HMAC 确定性占位符（`redact.go:776-779`）——同一值每次
   占位符相同保 vendor 缓存 prefix；maskrec 只存 hint 不存值（`maskrec.go:5-10`）。
   为什么值得抄：yg 目前只有日志脱敏（UID 前 8 位），没有"请求归并/缓存 key 稳定性"
   设计。落点方向：借鉴确定性占位符思想用于多账号路由的请求归并，不引入 ML 模型
   （破单二进制红线）。`[observation]`
5. **三层锁范式**（P2）：RWMutex + `.renew.lock` 跨进程文件锁 + `.run-*.lock`（
   `claude_dirs.go:114-187`）。为什么值得抄：yg 目前单实例常驻无此问题，但
   `deploy-prod.ps1` 之外若做 CLI 切号或多实例，直接复用。`[observation]`
6. **update SHA256 + 签名纪律**（P3）：feed 拿 SHA256、镜像可换 URL 不可换哈希
   （`mirror.go:16-21`）。为什么值得抄：yg 若做自更新，Windows 端必须补 Authenticode
   签名——magpie 裸 SHA256 换 Windows exe 是反面教材，不要学。`[observation]`

---

## 来源

- magpie 仓库：`D:\dev\magpie`（main 最新，2026-11-03 只读逆向）
- 本系列 9 份章节（gateway / provider / credentials / routes-budget / usage-rhythm /
  pii / plugins-mw / integration / agent）
- `docs/reference/magpie-deep-read-2026-10-25.md`（选号策略、quota 记账、战略结论，
  本文引用不重复论证）
