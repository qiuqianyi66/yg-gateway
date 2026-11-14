# REQ-cache-key-session-fallback-2026-10-29 — 无会话标识时缓存键回退内容派生

## 必要性

生产实测（`requests-2026-10-29.jsonl` 267 条）已确认：deepseek-harness 不带任何会话标识，
而生产配置 `session_sticky.derive=false` 关闭内容派生，`buildCacheKey(uid, "")` 退化成
每账号一个定值键——同账号的多个子会话共用一个缓存键、互相覆盖，表现为同账号连续 3 条
大请求（前缀仅差 2-4k token）全部 miss、恢复后 99%+ 命中。这是真实在跑的缓存命中率塌陷，
直接体现为上游 token 消耗翻倍，不是理论风险。

## 背景（生产实测，2026-10-29）

`D:\yg-gateway\data\request-logs\requests-2026-10-29.jsonl`（267 条）显示：
- deepseek-harness 请求不带任何会话标识（`conversation_id` / `conversationId` /
  `prompt_cache_key` 全缺；日志 `session_fp` 缺失佐证），且生产配置
  `session_sticky.derive=false` 关闭内容派生（anti-monopoly 防误钉）。
- 网关注入 `prompt_cache_key` 的会话段取 `chatMeta.ConversationID`，客户端不带时
  为空 → `buildCacheKey(uid, "")` 退化为**每个账号一个定值键**（`wb2a-<uid8>-<定值>`）。
- 同一账号内 harness 多个子会话共用该定值键 → 互相覆盖缓存 → 间歇性整段 miss：
  同账号连续 3 条大请求（前缀仅差 2-4k token）全 miss，恢复后 99%+ 命中，
  再被下一个新会话覆盖。当日 glm-5.3 45 条请求中 15 条失效请求吃掉 906/1499.7
  credit（60.4%）。
- 2026-10-15 计划 `docs/superpowers/plans/2026-10-15-upstream-sync-and-session-fix.md`
  Task 3 已识别同 bug 并给出修法（无 conversation_id 时回退 sessKey），**从未落地**
  （Step 1-6 未勾选、git 无对应 commit、`.harness/changes/` 无记录）。

## 需求

无显式会话标识的请求，出站 `prompt_cache_key` 的会话段回退**内容派生键**
（SHA-256(system + 首条 user)），且**不受 `session_sticky.derive=false` 影响**
（缓存键不参与账号选择，关闭派生只应打散路由，不应牺牲缓存复用）。

## Given-When-Then 验收

| # | Given | When | Then |
|---|---|---|---|
| G1 | 请求不带 conversation_id / conversationId / prompt_cache_key | 出站 | `prompt_cache_key` 会话段 = 内容派生键（非空、非账号定值）；同账号不同会话（首条 user 不同）键**不同** |
| G2 | 同一会话（system + 首条 user 恒定）重复请求 | 出站 | `prompt_cache_key` 相同（多轮前缀可递增复用） |
| G3 | 请求带 conversation_id / conversationId | 出站 | 会话段 = 该显式值（行为与修复前逐位一致） |
| G4 | 请求带 prompt_cache_key | 出站 | 原值保留（`promptCacheKeyFrom` 优先级 1 不变） |
| G5 | 无 user 消息 / body 不可解析 | 出站 | 回退为空串（保持原空会话语义，退化为账号定值键） |
| G6 | 路由粘性配置 derive=false | — | 路由键（ExtractKeyOptView）行为不变：无显式标识仍不粘性，加权轮换打散不变 |

## 权衡（写进决策，未来读者困惑点）

1. **缓存键与路由键解耦**：缓存键不受 `DisableSessionDerive` 影响，路由键受。
   理由：缓存键不参与账号选择，只决定上游前缀缓存槽位；把两者绑死会让
   anti-monopoly 的派生开关连带杀死缓存复用。
2. **内容派生键碰撞**（固定 system + 短开场的无关对话算出同键）：后果是共享
   缓存槽、互相覆盖，**不涉及账号错误钉定**（路由不受影响）；且上游缓存按内容
   前缀匹配，键相同内容不同不会错命中。风险低于路由误钉，可接受。
3. **范围**：只改 `chatflow.prepareOutbound` 一处取值（chatMeta.ConversationID
   的回退）+ session 包新增一个导出函数。`promptCacheKeyFrom` / `buildCacheKey`
   不改（回退值经 conversationID 参数流入，天然一致）。

## 约束

- R8 及协议数据不改名红线不涉及（仅缓存键会话段取值）。
- 带显式会话标识的客户端（spring-ai、Codex、Cherry Studio 等）行为逐位不变。
- 依赖方向：session 包新增导出函数，server 依赖 session（既有方向），无新越界。
