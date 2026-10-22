# REQ：多上游接入模板（R-107，P2）

> 状态：v1（2026-10-22）。已落地，见 `.harness/changes/2026-10-22-multiupstream-template/summary.md`。
> ADR：`docs/adr/ADR-015-fallback-cascade.md`。
> 本文定位：需求 + 验收。边界纪律——**只做 OpenAI 兼容族接入模板，不做「接所有 provider」**。

---

## 一、必要性（阶段 0，四个问题）

**Q1 要解决的真实问题是什么？**

现有 fallback 是**单端点硬编码**：`config.fallback` 只接受一个 base_url/api_key，
`handler.go` 的 `fallbackForward` 写死单端点透传。想加第二个备用源（比如主备用
DeepSeek、次备 OpenRouter）只能改代码。README 路线图挂着「更多上游接入（除 CodeBuddy
外的一键对接模板）」未做。

**Q2 不做会怎样？** 单备用源挂了就是挂了，没有二级兜底；加备用源要发版。

**Q3 现状是不是已经变了？** fallback 已有单端点透传（SSE flush 透传 + usage 归因
realm=fallback + R10 半截流判失败），骨架在；缺的是**列表化配置 + 按序级联 +
接入模板抽象**。

**Q4 有没有更便宜的等效手段？** 有边界：不做「接所有 provider」（AGENTS.md Product
Direction 明确不是通用 AI 网关）。所以模板只覆盖 OpenAI 兼容端点这一族——BaseURL +
APIKey + 模型白名单即可接入，URL 拼装/鉴权头/Accept 透传收敛进一个模板函数，
避免每端点手写漂移。

**结论：做。配置列表化（向后兼容旧单端点字段）+ handler 级联循环 + OpenAI 兼容
模板新文件。**

---

## 二、验收（Given-When-Then）

| # | GWT |
|---|---|
| 1 | Given config.fallback.endpoints 配 N 个端点，When 选号全失败，Then 按配置顺序逐个尝试，第一个覆盖该模型且转发成功的端点终结请求 |
| 2 | Given 端点 1 连接失败、端点 2 正常，When 触发 fallback，Then 落到端点 2（级联，不因子端点失败而整体放弃） |
| 3 | Given 某端点白名单不含该模型，When 级联到它，Then 跳过该端点继续下一个 |
| 4 | Given 旧配置只写 fallback.base_url/api_key（无 endpoints），Then 自动合成单端点列表，旧配置零回归 |
| 5 | Given 任何 fallback 改动，Then 主聊天链路不回归（go test ./... 全绿）；SSE 逐块 flush、半截流判失败、usage 归因 realm=fallback 三条既有行为不变 |

---

## 三、明确不做（反膨胀锚点）

- 不接非 OpenAI 兼容 provider（Anthropic Messages 等另立适配文件，不膨胀本模板）。
- 不做 fallback 负载均衡/权重（按序级联即够，顺序即优先级）。
- 不改 `internal/panel/*`（面板 fallback 展示另议）。
