# 同类开源项目对标与可落地学习点（2026-10-25）

> 方法：检索 + 定位对照（不逐行读源码——学的是**功能形态与产品取舍**，不是实现）。
> 结论先行：最同类是 **claude-relay-service（CRS）**（订阅 OAuth 账号池 + 面板 + 原生工具）；
> one-api/new-api 是「API-key 聚合 + 配额分发」生态（与本项目 AGENTS 定位不同）；
> LiteLLM 的价值在路由策略参考。YG Gateway 的差异化（养号友好选号、四态语义、
> 治理棘轮）在同类中无对标——这是 README 已写的定位，学习只取「补短板」，不追大而全。

## 对标表

| 项目 | 形态 | 与本项目的关键差异 |
|---|---|---|
| [wei-shaw/claude-relay-service](https://github.com/wei-shaw/claude-relay-service) | Claude Code 等**订阅 OAuth 账号池**中转，Docker+Redis | 最同类。它有：限流/用量阈值自动换号（本项目=冷却/熔断 ✓）、多账号分组（=Group ✓）、key 级流控（=keyrpm ✓）、**原生 count_tokens**（本项目缺 ❌） |
| [songquanpeng/one-api](https://github.com/songquanpeng/one-api) / [New API](https://docs.newapi.ai) | API-key 聚合 + 令牌配额/兑换码/分组分发 | 令牌过期/额度/IP 白名单（IP 白名单已有 ✓；配额/兑换码=AGENTS 明确不做 ✗）；渠道自动禁用+探测恢复（本项目=熔断+revive ✓） |
| [LiteLLM](https://docs.litellm.ai) | 企业路由网关（100+ provider） | 路由策略可插拔（usage/latency/lowest-cost）——本项目权重组合已可配（idle 两参+ewma 开关+快过期开关），缺口小；count_tokens/Responses API 兼容面广 |

## 学到的（可落地，进 BACKLOG）

1. **`/v1/messages/count_tokens`**（CRS/LiteLLM 都原生支持）：Claude Code 会调它做
   上下文核算；本项目未实现（grep 0 命中），客户端退化为估算——原生体验缺口。
   做法：不引依赖，按字符/词元启发式返回估算 + 标注 derivation；或 501 + 明确信封。
2. **key 级用量告警（只告警不拦截）**：one-api 的令牌配额思想去掉「拦截」保留「可见」
   ——多 Key 派发场景下，某把 key 消耗突增值得 webhook 提醒（数据已有：usage 按
   下游 Key 四维聚合）。与「不做计费」不冲突。
3. **「账号恢复探测」按钮语义对齐 CRS 的自动换号触发器**：本项目 half-open 已自动；
   可学的是把「探测成功率」暴露到面板（数据在 metrics，缺展示）。

## 明确不抄的（定位红线，AGENTS 已裁定）

- 多租户计费 / 兑换码 / 用户体系（one-api 核心）——「不是什么」第 1 条。
- 多 provider 接入（LiteLLM/one-api 核心）——上游就是 CodeBuddy+一个备用源。
- 提示词日志留存（oai-reverse-proxy 的 prompt logging）——R1/R2 脱敏纪律反向禁止。
- ToS 高危形态（oai-reverse-proxy 被标记的账号池滥用面）——本项目所有自动化都走
  正规活动 API + 搭车探索（零新增请求），这是 README 诚实劣势一节的延续。

## 来源

- [CRS README](https://github.com/wei-shaw/claude-relay-service) · [one-api](https://github.com/songquanpeng/one-api) ·
  [LiteLLM docs](https://docs.litellm.ai/docs/proxy/client_setup/overview) ·
  [CLIProxyAPI vs New API vs Sub2API（2026）](https://blog.csdn.net/jqrrgmvtk/article/details/160049085)
