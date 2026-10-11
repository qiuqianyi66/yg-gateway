---
last_updated: 2026-10-11
status: active
owner: YG
---

# 术语表（YG Gateway）

> 领域语言 canonical 定义。术语定了当场写这里，不批量补。
> 只放领域语言，不放实现细节（不是 spec / 草稿纸）。

## 账号域

| 术语 | 定义 | 备注 |
|---|---|---|
| **账号 / Account** | 一个 CodeBuddy 账号的凭证 + 池内状态。文件 `auths/workbuddy-<uid>.json` | 口语"号"即此 |
| **凭证 / Auth** | 账号的 accessToken + refreshToken + 元信息。明文存储 | 🔴 落盘需 0600 + Windows ACL 加固 |
| **UID** | 上游账号唯一标识，UUID 形式。用于拼凭证文件名 | 白名单 `[A-Za-z0-9_-]` ≤64（防路径穿越） |
| **域 / Realm** | `cn`（国内）或 `global`（国际版 workbuddy.ai）。决定上游端点与模型目录 | 与 `enterpriseId` 正交 |
| **企业版账号** | 凭证带非空 `enterpriseId` 的账号。无个人成长体系（签到/旅行/任务全部 403/400） | 仍可派发、保活、查额度 |
| **池 / Pool** | 全部账号的集合 + 选号/冷却/熔断状态机。状态落 `data/state.json` | 单例 |

## 积分域

| 术语 | 定义 | 备注 |
|---|---|---|
| **积分 / Credits** | 上游计费单位。个人号按"资源包"发放，有到期时间 | 企业号按周期配额，语义相反（credit=已用） |
| **资源包 / Package** | 一批带到期时间的积分。上游按 FEFO（先失效先扣）消耗 | 面板"积分构成"页逐包展示 |
| **到期批次** | 同一到期日的积分聚合。到期作废，不累积 | 到期提醒卡片的核心单位 |
| **积分保底 / credit_floor** | 余额低于此值时，实测收费模型不参与选号（防打穿） | 默认 100；`0` = 关闭 |
| **快过期加权 / prefer_expiring** | 窗口内仍有有效批次的账号，选号权重 ×3（软偏好） | 弱于会话粘性与成本分层 |

## 调度域

| 术语 | 定义 | 备注 |
|---|---|---|
| **冷却 / Cooldown** | 账号因错误临时退出选号，带截止时间 | 软冷却（限流，指数退避）/ 硬冷却（402 到次日 04:00）/ 404 固定 60s |
| **熔断 / Breaker** | 连续失败达阈值后的更强隔离，退避更长且指数增长 | 默认阈值 3，基础 30m，封顶 6h |
| **降权 / Degrade** | 未知错误连败达阈值后临时出池（比熔断轻） | 默认阈值 5 |
| **暂停选号 / paused** | 账号级运维态：退出选号但照常保号（签到/保活/刷新） | 与"禁用"区别：暂停是临时态，禁用是判死态 |
| **禁用 / disabled** | 账号因 session 失效被判死，需人工复活或重登 | 默认也跳过保号任务（可用 `include_disabled_in_tasks` 打开） |
| **会话粘性 / Session Sticky** | 同一会话绑定同一账号，TTL 滚动续期 | 键优先 `conversation_id`，无则哈希 `system+首条user` 派生 |

## 上游域

| 术语 | 定义 | 备注 |
|---|---|---|
| **上游 / Upstream** | 腾讯 CodeBuddy 服务端（`copilot.tencent.com` / `www.codebuddy.cn` / `www.workbuddy.cn`） | 非公开接口，逆向所得 |
| **出站改写管线** | 请求发往上游前的统一变换（强制 stream、角色归一、思维链注入、指纹脱敏…） | 集中在 `internal/upstream/payload.go` |
| **指纹** | 出站 UA / `X-IDE-*` / `X-Device-Token` 等头部组合，用于模拟官方客户端 | 上游按 UA 下发不同模型目录 |
| **FEFO** | First-Expire-First-Out：上游按失效时刻升序扣减积分包 | 实测两号口径一致 |
| **DSML** | DeepSeek 系工具调用的原生标记语法（全角竖线分隔） | 仅在请求声明 `tools` 时才被上游解析成结构化 `tool_calls` |

## 面板域

| 术语 | 定义 | 备注 |
|---|---|---|
| **视图 / View** | 面板左侧导航的七个页面：accounts / usage / packages / taskscenter / models / config / logs | - |
| **快照 / Snapshot** | `/overview` 响应里的到期字段（`credits_earliest_expiry`），来自 `state.json`，零上游请求 | 与"逐包明细"（`/packages`，打上游）相对 |
| **热生效** | 面板改配置后不需重启即生效（api_key / cooldown / pool / schedule / 脱敏开关等） | 装配期字段（listen/auth_dir/upstream/upstash）仍需重启 |

## 已废弃 / 易混

| 词 | 说明 |
|---|---|
| "wb2api" | 上游项目名。代码中仅存于**发给上游的报文内容**（conversationId 前缀、哨兵 id），属协议数据不得改名（R8） |
| "WorkBuddy2API" | 上游品牌名。本项目已改名 `yg-gateway`，仅历史文档与 LICENSE 中保留出处 |
