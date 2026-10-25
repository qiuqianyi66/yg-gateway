# 同类开源项目对标与可落地学习点（2026-10-25）

> 方法：检索 + 定位对照（不逐行读源码——学的是**功能形态与产品取舍**，不是实现）。
> 结论先行：最同类是 **claude-relay-service（CRS）**（订阅 OAuth 账号池 + 面板 + 原生工具）；
> **magpie（yetone）** 是「本地网关 × 编码 agent × 订阅账号」交汇点上完成度最高的一个
> （Go 单二进制 + 菜单栏/TUI/CLI/Docker，60+ agent 支持，含 WorkBuddy 与 CodeBuddy Code）；
> one-api/new-api 是「API-key 聚合 + 配额分发」生态（与本项目 AGENTS 定位不同）；
> LiteLLM 的价值在路由策略参考。YG Gateway 的差异化（养号友好选号、四态语义、
> 治理棘轮）在同类中无对标——这是 README 已写的定位，学习只取「补短板」，不追大而全。

## 对标表

| 项目 | 形态 | 与本项目的关键差异 |
|---|---|---|
| [yetone/magpie](https://github.com/yetone/magpie) | **本地网关（127.0.0.1:3425）把任意编码 agent 接到任意模型/订阅**；OpenAI Chat/Responses、Anthropic Messages、Gemini 四协议互译（流式/工具/推理）；隐私脱敏、路由组、中间件插件 | 哲学相反处：它做「agent 侧编排 + 插件生态（Bun）」，本项目做「单二进制零依赖 + 治理棘轮」。可直接学的：路由组五模式里的 **smart/pace**（见下）、`/v1/responses`、OTLP 导出、count_tokens |
| [wei-shaw/claude-relay-service](https://github.com/wei-shaw/claude-relay-service) | Claude Code 等**订阅 OAuth 账号池**中转，Docker+Redis | 限流/用量阈值自动换号（=冷却/熔断 ✓）、分组（=Group ✓）、key 流控（=keyrpm ✓）、原生 count_tokens（缺 ❌） |
| [songquanpeng/one-api](https://github.com/songquanpeng/one-api) / [New API](https://docs.newapi.ai) | API-key 聚合 + 令牌配额/兑换码/分组分发 | 令牌过期/额度（配额=AGENTS 不做 ✗）；渠道自动禁用+探测恢复（=熔断+revive ✓） |
| [LiteLLM](https://docs.litellm.ai) | 企业路由网关（100+ provider） | 路由策略可插拔——本项目权重组合已可配，缺口小；兼容面广 |

## magpie 专项：值得记下的三条验证 + 四条学习

**设计被验证（同行独立走到同一结论）**：
1. **「快过期优先」**——magpie 路由组 `smart` 模式 = 「优先用最先重置的订阅，少浪费」，
   与本项目 creditsExpiring 虚拟实例加权同构。
2. **「养号/预热」**——magpie 的 daily warm-up（定时启动 Claude/ChatGPT 的 5 小时窗口）
   是 CRS 和 magpie 都做了的「保活」逻辑；本项目的五类养号任务比它深一个量级（17 任务）。
3. **粘性会话保 prompt cache**——magpie「对话留在答过它的账号，直到供应商缓存不值得」
   与本项目的会话粘性同源。

**可落地学习（进 BACKLOG）**：
1. **`pace` 路由模式**（magpie 独有）：「按重置前每小时剩余额度最多」选号——与本项目
   闲置补偿不同维度（闲置补偿补的是「久未用」，pace 算的是「窗口内燃燒率」）。可作为
   选号权重的一个**可选策略**（config 开关，默认关），对多订阅号池有真实价值。
2. **`/v1/responses`**（magpie 四协议之一）：Codex 客户端正在迁移，本项目 L1 已登记。
3. **usage 的 OTLP 导出**：本项目有 CSV + /metrics（Prometheus 形态）；OTLP 是接入
   自建观测栈的通用语言，S 级。
4. **count_tokens**：magpie 四协议互译里原生处理，再次坐实 BACKLOG A1。

**看过但不抄（写明理由）**：
- **隐私脱敏（请求出机前把密钥/PII 换占位符、回包换回）**：magpie 最有辨识度的功能，
  工程上极其讲究（确定性占位符保 prompt cache、流式与 tool args 都换回、内存不落盘）。
  本项目**暂不引入**：①R8 红线 spirit——出站报文内容保持「客户端说什么就是什么」，
  改写内容层会改变风控画像；②实现复杂度（全协议流式占位符回填）远超当前收益；
  ③本项目的等价物是 R2 日志脱敏 + prompt 体系（网关提示词替换）。留档观察。
- **Bun 插件/中间件生态**（param-override/model-map/word-guard 等，对齐 New API 语义）：
  与「单二进制零依赖、go.mod 个位数」定位直接冲突；且本项目已有 model_aliases、
  提示词体系、prompt_cache_key 注入这些内置等价物。
- **Intent routing**（小模型读每一轮再分流）：每请求多一次上游调用，与「零新增上游
  请求」的 WAF 友好哲学冲突。
- 菜单栏/TUI/会话管理器：本项目是 headless 服务，形态不同。

## 早期对标（保留）

| 项目 | 形态 | 与本项目的关键差异 |
|---|---|---|
| [wei-shaw/claude-relay-service](https://github.com/wei-shaw/claude-relay-service) | 订阅账号池中转 | 见上 |
| [songquanpeng/one-api](https://github.com/songquanpeng/one-api) / [New API](https://docs.newapi.ai) | key 聚合+配额分发 | 见上 |
| [LiteLLM](https://docs.litellm.ai) | 企业路由 | 见上 |

## 学到的（可落地，进 BACKLOG）

1. **`/v1/messages/count_tokens`**（CRS/LiteLLM/magpie 都原生支持）：Claude Code 会调它做
   上下文核算；本项目未实现（grep 0 命中），客户端退化为估算——原生体验缺口。
   做法：不引依赖，按字符/词元启发式返回估算 + 标注 derivation；或 501 + 明确信封。
2. **key 级用量告警（只告警不拦截）**：one-api 的令牌配额思想去掉「拦截」保留「可见」
   ——多 Key 派发场景下，某把 key 消耗突增值得 webhook 提醒（数据已有：usage 按
   下游 Key 四维聚合）。与「不做计费」不冲突。
3. **「账号恢复探测」按钮语义对齐 CRS 的自动换号触发器**：本项目 half-open 已自动；
   可学的是把「探测成功率」暴露到面板（数据在 metrics，缺展示）。
4. **magpie pace 路由模式 + OTLP 导出**（见上节，2026-10-25 增补）。

## 明确不抄的（定位红线，AGENTS 已裁定）

- 多租户计费 / 兑换码 / 用户体系（one-api 核心）——「不是什么」第 1 条。
- 多 provider 接入（LiteLLM/one-api/magpie 核心）——上游就是 CodeBuddy+一个备用源。
- 提示词日志留存（oai-reverse-proxy 的 prompt logging）——R1/R2 脱敏纪律反向禁止。
- ToS 高危形态（oai-reverse-proxy 被标记的账号池滥用面）——本项目所有自动化都走
  正规活动 API + 搭车探索（零新增请求），这是 README 诚实劣势一节的延续。

## 来源

- [magpie README](https://github.com/yetone/magpie) · [CRS README](https://github.com/wei-shaw/claude-relay-service) ·
  [one-api](https://github.com/songquanpeng/one-api) · [LiteLLM docs](https://docs.litellm.ai/docs/proxy/client_setup/overview) ·
  [CLIProxyAPI vs New API vs Sub2API（2026）](https://blog.csdn.net/jqrrgmvtk/article/details/160049085)
